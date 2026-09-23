package ddb

import (
	"context"
	"encoding/json"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"engine/internal/domain"
	"engine/internal/evaluate"
)

// Decisions is the Decisions table: one row per single evaluation, keyed by
// decision_id (ADR 0006).
type Decisions struct {
	client *dynamodb.Client
	table  string
}

func NewDecisions(cfg aws.Config, table string) *Decisions {
	return &Decisions{client: dynamodb.NewFromConfig(cfg), table: table}
}

func decisionKey(id string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"decision_id": sAttr(id)}
}

func (st *Decisions) Save(ctx context.Context, decisionID string, c domain.Customer, r domain.Result) error {
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

func (st *Decisions) Get(ctx context.Context, decisionID string) (domain.Result, error) {
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
