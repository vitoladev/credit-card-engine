package ddb

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"engine/internal/idempotency"
)

// Keys implements idempotency.Store on the IdempotencyKeys table, one item per
// key (idempotency_key): fingerprint, owner, state, lease_until, expires_at,
// and once DONE the replayed status_code and body.
//
// expires_at is the table's TTL attribute, in epoch seconds. DynamoDB deletes
// an expired item within days, not on time, so every condition also treats an
// expired item as absent. The table has no stream.
type Keys struct {
	client *dynamodb.Client
	table  string
}

func NewKeys(cfg aws.Config, table string) Keys {
	return Keys{client: dynamodb.NewFromConfig(cfg), table: table}
}

const (
	keyPending = "PENDING"
	keyDone    = "DONE"
)

func idempotencyKey(key string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"idempotency_key": sAttr(key)}
}

// Claim is one conditional PutItem: it wins when the key is absent, expired,
// or pending past its lease. When it loses, the condition failure returns the
// live row, so no second read is needed.
func (k Keys) Claim(ctx context.Context, key string, c idempotency.Claim) (idempotency.Record, bool, error) {
	row := idempotencyKey(key)
	row["fingerprint"] = sAttr(c.Fingerprint)
	row["owner"] = sAttr(c.Owner)
	row["state"] = sAttr(keyPending)
	row["lease_until"] = n64Attr(c.LeaseUntil.UnixMilli())
	row["expires_at"] = n64Attr(c.ExpiresAt.Unix())
	_, err := k.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: new(k.table),
		Item:      row,
		ConditionExpression: new("attribute_not_exists(idempotency_key) OR expires_at < :now_s OR " +
			"(#state = :pending AND lease_until < :now_ms)"),
		ExpressionAttributeNames: map[string]string{"#state": "state"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":now_s":   n64Attr(c.Now.Unix()),
			":now_ms":  n64Attr(c.Now.UnixMilli()),
			":pending": sAttr(keyPending),
		},
		ReturnValuesOnConditionCheckFailure: types.ReturnValuesOnConditionCheckFailureAllOld,
	})
	old, failed := conditionFailed(err)
	if !failed {
		return idempotency.Record{}, err == nil, err
	}
	rec, err := keyRecord(old)
	return rec, false, err
}

// Complete stores the response on the claim owner still holds.
func (k Keys) Complete(ctx context.Context, key, owner string, r idempotency.Response) error {
	_, err := k.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                new(k.table),
		Key:                      idempotencyKey(key),
		UpdateExpression:         new("SET #state = :done, status_code = :code, #body = :body REMOVE lease_until"),
		ConditionExpression:      new("#owner = :owner AND #state = :pending"),
		ExpressionAttributeNames: map[string]string{"#state": "state", "#owner": "owner", "#body": "body"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":done":    sAttr(keyDone),
			":pending": sAttr(keyPending),
			":owner":   sAttr(owner),
			":code":    nAttr(r.Status),
			":body":    sAttr(r.Body),
		},
	})
	return err
}

// Release deletes the pending claim owner still holds.
func (k Keys) Release(ctx context.Context, key, owner string) error {
	_, err := k.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:                new(k.table),
		Key:                      idempotencyKey(key),
		ConditionExpression:      new("#owner = :owner AND #state = :pending"),
		ExpressionAttributeNames: map[string]string{"#state": "state", "#owner": "owner"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pending": sAttr(keyPending),
			":owner":   sAttr(owner),
		},
	})
	return err
}

func keyRecord(row map[string]types.AttributeValue) (idempotency.Record, error) {
	fp, err := readS(row, "fingerprint")
	if err != nil {
		return idempotency.Record{}, err
	}
	state, err := readS(row, "state")
	if err != nil {
		return idempotency.Record{}, err
	}
	rec := idempotency.Record{Fingerprint: fp, Done: state == keyDone}
	if !rec.Done {
		return rec, nil
	}
	if rec.Response.Status, err = readN(row, "status_code"); err != nil {
		return idempotency.Record{}, err
	}
	if rec.Response.Body, err = readS(row, "body"); err != nil {
		return idempotency.Record{}, err
	}
	return rec, nil
}
