package ddb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"engine/internal/batch"
	"engine/internal/domain"
	"engine/internal/evaluate"
)

// One table holds both ports, batch.Items and evaluate.DecisionStore:
//
//	DECISION#<decision_id> / RESULT          a single evaluation
//	BATCH#<batch_id>       / ITEM#<item_id>  one batch item: input, status, attempts, result
//
// A batch is its ITEM# rows; there is no batch row. Item IDs are version 7
// UUIDs, so a Query on the partition returns items in submission order.
// A transition is one conditional UpdateItem on its own ITEM# row, so the item
// status rules are enforced here, atomically, and concurrent workers on one
// batch never write the same row.
const (
	writeChunk    = 25 // BatchWriteItem limit
	writeInFlight = 8
	writeTries    = 6
	backoffBase   = 25 * time.Millisecond
	retryInFlight = 8
)

type Store struct {
	client *dynamodb.Client
	table  string
}

func New(cfg aws.Config, table string) *Store {
	return &Store{client: dynamodb.NewFromConfig(cfg), table: table}
}

func sAttr(v string) *types.AttributeValueMemberS { return &types.AttributeValueMemberS{Value: v} }
func n64Attr(v int64) *types.AttributeValueMemberN {
	return &types.AttributeValueMemberN{Value: strconv.FormatInt(v, 10)}
}

func nAttr(v int) *types.AttributeValueMemberN {
	return &types.AttributeValueMemberN{Value: strconv.Itoa(v)}
}

func decisionKey(id string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"pk": sAttr("DECISION#" + id), "sk": sAttr("RESULT")}
}

func itemKey(batchID, itemID string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"pk": sAttr("BATCH#" + batchID), "sk": sAttr("ITEM#" + itemID)}
}

func (st *Store) Save(ctx context.Context, decisionID string, c domain.Customer, r domain.Result) error {
	customer, err := json.Marshal(c)
	if err != nil {
		return err
	}
	result, err := json.Marshal(r)
	if err != nil {
		return err
	}
	item := decisionKey(decisionID)
	item["customer"] = sAttr(string(customer))
	item["result"] = sAttr(string(result))
	_, err = st.client.PutItem(ctx, &dynamodb.PutItemInput{TableName: new(st.table), Item: item})
	return err
}

func (st *Store) Get(ctx context.Context, decisionID string) (domain.Result, error) {
	out, err := st.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      new(st.table),
		Key:            decisionKey(decisionID),
		ConsistentRead: new(true),
	})
	if err != nil {
		return domain.Result{}, err
	}
	if out.Item == nil {
		return domain.Result{}, evaluate.ErrNotFound
	}
	var r domain.Result
	return r, unmarshalAttr(out.Item, "result", &r)
}

// Create writes every ITEM# row with BatchWriteItem, several chunks in flight
// at once. A failed write deletes every row for the batch.
func (st *Store) Create(ctx context.Context, batchID string, items []batch.Item) error {
	now := time.Now().UnixMilli()
	puts := make([]types.WriteRequest, 0, len(items))
	for _, it := range items {
		raw, err := json.Marshal(it.Customer)
		if err != nil {
			return err
		}
		item := itemKey(batchID, it.ID)
		item["item_id"] = sAttr(it.ID)
		item["customer"] = sAttr(string(raw))
		item["status"] = sAttr(string(batch.Queued))
		item["attempts"] = nAttr(1)
		item["queued_at"] = n64Attr(now)
		puts = append(puts, types.WriteRequest{PutRequest: &types.PutRequest{Item: item}})
	}

	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	sem := make(chan struct{}, writeInFlight)
	for start := 0; start < len(puts); start += writeChunk {
		chunk := puts[start:min(start+writeChunk, len(puts))]
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			if err := st.batchWrite(ctx, chunk); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		if delErr := st.deleteBatch(ctx, batchID); delErr != nil {
			return errors.Join(err, delErr)
		}
		return err
	}
	return nil
}

// batchWrite sends up to writeChunk requests and retries UnprocessedItems with
// backoff.
func (st *Store) batchWrite(ctx context.Context, reqs []types.WriteRequest) error {
	for try := range writeTries {
		if try > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoffBase << (try - 1)):
			}
		}
		out, err := st.client.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{
			RequestItems: map[string][]types.WriteRequest{st.table: reqs},
		})
		if err != nil {
			return fmt.Errorf("batch write item: %w", err)
		}
		reqs = out.UnprocessedItems[st.table]
		if len(reqs) == 0 {
			return nil
		}
	}
	return fmt.Errorf("batch write item: %d requests still unprocessed after %d tries", len(reqs), writeTries)
}

// Decide moves the item to APPROVED or DENIED, conditional on it being QUEUED
// on this attempt.
func (st *Store) Decide(ctx context.Context, batchID, itemID string, attempt int, r domain.Result) error {
	result, err := json.Marshal(r)
	if err != nil {
		return err
	}
	err = st.move(ctx, batchID, itemID, batch.DecidedAs(r), batch.Queued, itemUpdate{
		update: "SET #status = :to, #result = :result",
		cond:   "#status = :from AND #attempts = :attempt",
		names:  map[string]string{"#result": "result", "#attempts": "attempts"},
		values: map[string]types.AttributeValue{":attempt": nAttr(attempt), ":result": sAttr(string(result))},
	})
	return transitionErr(err)
}

// Fail moves the item from QUEUED to FAILED, conditional on this attempt.
func (st *Store) Fail(ctx context.Context, batchID, itemID string, attempt int) error {
	err := st.move(ctx, batchID, itemID, batch.Failed, batch.Queued, itemUpdate{
		update: "SET #status = :to",
		cond:   "#status = :from AND #attempts = :attempt",
		names:  map[string]string{"#attempts": "attempts"},
		values: map[string]types.AttributeValue{":attempt": nAttr(attempt)},
	})
	return transitionErr(err)
}

// Retry moves the item from FAILED on this attempt to QUEUED on the next one.
// The attempt cap is part of the condition, so a concurrent retry cannot
// exceed it.
func (st *Store) Retry(ctx context.Context, batchID, itemID string, attempt int) error {
	err := st.move(ctx, batchID, itemID, batch.Queued, batch.Failed, itemUpdate{
		update: "SET #status = :to, #attempts = #attempts + :one, #queued_at = :now",
		cond:   "#status = :from AND #attempts = :attempt AND #attempts < :max",
		names:  map[string]string{"#attempts": "attempts", "#queued_at": "queued_at"},
		values: map[string]types.AttributeValue{
			":attempt": nAttr(attempt), ":max": nAttr(batch.MaxAttempts), ":one": nAttr(1), ":now": n64Attr(time.Now().UnixMilli()),
		},
	})
	old, ok := conditionFailed(err)
	if !ok || len(old) == 0 {
		return transitionErr(err)
	}
	if n, err := readN(old, "attempts"); err == nil && n >= batch.MaxAttempts && isStatus(old, batch.Failed) {
		return batch.ErrMaxAttempts
	}
	return batch.ErrInvalidTransition
}

// RetryMany retries each item with its own conditional update, several in
// flight at once. Items that cannot transition are skipped. The first store
// error stops the items not yet started.
func (st *Store) RetryMany(ctx context.Context, batchID string, items []batch.Item) ([]batch.Item, error) {
	moved := make([]bool, len(items))
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	sem := make(chan struct{}, retryInFlight)
	for i, it := range items {
		sem <- struct{}{}
		mu.Lock()
		stop := len(errs) > 0
		mu.Unlock()
		if stop {
			<-sem
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			err := st.Retry(ctx, batchID, it.ID, it.Attempts)
			switch {
			case err == nil:
				moved[i] = true
			case errors.Is(err, batch.ErrInvalidTransition), errors.Is(err, batch.ErrMaxAttempts):
			default:
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	var queued []batch.Item
	for i, ok := range moved {
		if ok {
			queued = append(queued, items[i])
		}
	}
	return queued, errors.Join(errs...)
}

func (st *Store) deleteBatch(ctx context.Context, batchID string) error {
	keys, err := st.keysForBatch(ctx, batchID)
	if err != nil {
		return err
	}
	dels := make([]types.WriteRequest, len(keys))
	for i, key := range keys {
		dels[i] = types.WriteRequest{DeleteRequest: &types.DeleteRequest{Key: key}}
	}
	var errs []error
	for start := 0; start < len(dels); start += writeChunk {
		if err := st.batchWrite(ctx, dels[start:min(start+writeChunk, len(dels))]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (st *Store) keysForBatch(ctx context.Context, batchID string) ([]map[string]types.AttributeValue, error) {
	in := &dynamodb.QueryInput{
		TableName:              new(st.table),
		KeyConditionExpression: new("pk = :pk"),
		ProjectionExpression:   new("pk, sk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": sAttr("BATCH#" + batchID),
		},
		ConsistentRead: new(true),
	}
	var keys []map[string]types.AttributeValue
	p := dynamodb.NewQueryPaginator(st.client, in)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range page.Items {
			keys = append(keys, map[string]types.AttributeValue{"pk": row["pk"], "sk": row["sk"]})
		}
	}
	return keys, nil
}

// Cancel moves the item from FAILED to CANCELLED. An item already CANCELLED
// is left as it is and the call succeeds.
func (st *Store) Cancel(ctx context.Context, batchID, itemID string) error {
	err := st.move(ctx, batchID, itemID, batch.Cancelled, batch.Failed, itemUpdate{
		update: "SET #status = :to",
		cond:   "#status = :from",
	})
	if old, ok := conditionFailed(err); ok && isStatus(old, batch.Cancelled) {
		return nil
	}
	return transitionErr(err)
}

// itemUpdate is a transition's update. Its expressions may also use #status,
// :from, and :to, which move sets.
type itemUpdate struct {
	update, cond string
	names        map[string]string
	values       map[string]types.AttributeValue
}

// move changes one item's status with a single conditional UpdateItem.
func (st *Store) move(ctx context.Context, batchID, itemID string, to, from batch.ItemStatus, u itemUpdate) error {
	names := map[string]string{"#status": "status"}
	maps.Copy(names, u.names)
	vals := map[string]types.AttributeValue{":from": sAttr(string(from)), ":to": sAttr(string(to))}
	maps.Copy(vals, u.values)
	_, err := st.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                           new(st.table),
		Key:                                 itemKey(batchID, itemID),
		UpdateExpression:                    new(u.update),
		ConditionExpression:                 new(u.cond),
		ExpressionAttributeNames:            names,
		ExpressionAttributeValues:           vals,
		ReturnValuesOnConditionCheckFailure: types.ReturnValuesOnConditionCheckFailureAllOld,
	})
	return err
}

func isStatus(row map[string]types.AttributeValue, s batch.ItemStatus) bool {
	v, err := readS(row, "status")
	return err == nil && v == string(s)
}

// conditionFailed returns the item as it was when its condition failed. An
// empty item means the item does not exist.
func conditionFailed(err error) (map[string]types.AttributeValue, bool) {
	failed, ok := errors.AsType[*types.ConditionalCheckFailedException](err)
	if !ok {
		return nil, false
	}
	return failed.Item, true
}

// transitionErr maps a failed item condition to ErrNotFound when the item does
// not exist and to ErrInvalidTransition when it is in another state.
func transitionErr(err error) error {
	old, ok := conditionFailed(err)
	if !ok {
		return err
	}
	if len(old) == 0 {
		return batch.ErrNotFound
	}
	return batch.ErrInvalidTransition
}

func (st *Store) Item(ctx context.Context, batchID, itemID string) (batch.Item, error) {
	out, err := st.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      new(st.table),
		Key:            itemKey(batchID, itemID),
		ConsistentRead: new(true),
	})
	if err != nil {
		return batch.Item{}, err
	}
	if out.Item == nil {
		return batch.Item{}, batch.ErrNotFound
	}
	return batchItem(out.Item)
}

// Page reads one page of items with Query, repeating it until the page holds
// q.Limit items or the partition ends. Query's Limit counts rows before the
// status filter, so one call can return fewer matches than asked for, or none.
// The first page tells an unknown batch apart by ScannedCount: a batch always
// has rows, so a partition with none scanned does not exist.
func (st *Store) Page(ctx context.Context, batchID string, q batch.PageQuery) (batch.Page, error) {
	in := st.pageInput(batchID, q)
	var (
		p       batch.Page
		scanned int32
	)
	for {
		in.Limit = new(int32(q.Limit - len(p.Items)))
		out, err := st.client.Query(ctx, in)
		if err != nil {
			return batch.Page{}, err
		}
		scanned += out.ScannedCount
		if p.Items, err = appendItems(p.Items, out.Items); err != nil {
			return batch.Page{}, err
		}
		if out.LastEvaluatedKey == nil {
			break
		}
		if len(p.Items) == q.Limit {
			p.More = true
			break
		}
		in.ExclusiveStartKey = out.LastEvaluatedKey
	}
	if q.After == "" && scanned == 0 {
		return batch.Page{}, batch.ErrNotFound
	}
	return p, nil
}

// pageInput is the Query over the batch's ITEM# rows that q selects.
func (st *Store) pageInput(batchID string, q batch.PageQuery) *dynamodb.QueryInput {
	in := &dynamodb.QueryInput{
		TableName:              new(st.table),
		KeyConditionExpression: new("pk = :pk AND begins_with(sk, :item)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":   sAttr("BATCH#" + batchID),
			":item": sAttr("ITEM#"),
		},
		ConsistentRead: new(true),
	}
	if q.Status != "" {
		in.FilterExpression = new("#status = :status")
		in.ExpressionAttributeNames = map[string]string{"#status": "status"}
		in.ExpressionAttributeValues[":status"] = sAttr(string(q.Status))
	}
	if q.After != "" {
		in.ExclusiveStartKey = itemKey(batchID, q.After)
	}
	return in
}

func appendItems(items []batch.Item, rows []map[string]types.AttributeValue) ([]batch.Item, error) {
	for _, row := range rows {
		it, err := batchItem(row)
		if err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, nil
}

func batchItem(row map[string]types.AttributeValue) (batch.Item, error) {
	var it batch.Item
	var err error
	if it.ID, err = readS(row, "item_id"); err != nil {
		return batch.Item{}, err
	}
	if it.Attempts, err = readN(row, "attempts"); err != nil {
		return batch.Item{}, err
	}
	status, err := readS(row, "status")
	if err != nil {
		return batch.Item{}, err
	}
	it.Status = batch.ItemStatus(status)
	queuedAt, err := readN(row, "queued_at")
	if err != nil {
		return batch.Item{}, err
	}
	it.QueuedAt = time.UnixMilli(int64(queuedAt))
	if err := unmarshalAttr(row, "customer", &it.Customer); err != nil {
		return batch.Item{}, err
	}
	if _, ok := row["result"]; ok {
		if err := unmarshalAttr(row, "result", &it.Result); err != nil {
			return batch.Item{}, err
		}
	}
	return it, nil
}

func readS(row map[string]types.AttributeValue, name string) (string, error) {
	v, ok := row[name].(*types.AttributeValueMemberS)
	if !ok {
		return "", fmt.Errorf("attribute %s missing or not a string", name)
	}
	return v.Value, nil
}

func readN(row map[string]types.AttributeValue, name string) (int, error) {
	v, ok := row[name].(*types.AttributeValueMemberN)
	if !ok {
		return 0, fmt.Errorf("attribute %s missing or not a number", name)
	}
	return strconv.Atoi(v.Value)
}

func unmarshalAttr(row map[string]types.AttributeValue, name string, dst any) error {
	raw, err := readS(row, name)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), dst)
}
