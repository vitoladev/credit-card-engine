package ddb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"engine/internal/domain"
)

type Store struct {
	client *dynamodb.Client
	table  string
}

func New(cfg aws.Config, table string) *Store {
	return &Store{client: dynamodb.NewFromConfig(cfg), table: table}
}

func (s *Store) Save(ctx context.Context, reportID string, r domain.Result) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.table),
		Item: map[string]types.AttributeValue{
			"report_id": &types.AttributeValueMemberS{Value: reportID},
			"sk":        &types.AttributeValueMemberS{Value: "RES#" + newID()},
			"payload":   &types.AttributeValueMemberS{Value: string(raw)},
		},
	})
	return err
}

func (s *Store) List(ctx context.Context, reportID string) ([]domain.Result, error) {
	out, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(s.table),
		KeyConditionExpression: aws.String("report_id = :pk AND begins_with(sk, :pref)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":   &types.AttributeValueMemberS{Value: reportID},
			":pref": &types.AttributeValueMemberS{Value: "RES#"},
		},
	})
	if err != nil {
		return nil, err
	}
	items := make([]domain.Result, 0, len(out.Items))
	for _, item := range out.Items {
		payload, ok := item["payload"].(*types.AttributeValueMemberS)
		if !ok {
			return nil, fmt.Errorf("payload missing")
		}
		var r domain.Result
		if err := json.Unmarshal([]byte(payload.Value), &r); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, nil
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
