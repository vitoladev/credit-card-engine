package ddb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"engine/internal/domain"
	"engine/internal/store"
)

// One table holds both ports:
//
//	DECISION#<decision_id> / RESULT     a single evaluation
//	BATCH#<batch_id>       / META       the batch counters
//	BATCH#<batch_id>       / ITEM#<i>   one batch item: input, status, attempts, result
const (
	writeChunk    = 25 // BatchWriteItem limit
	writeInFlight = 8
	writeTries    = 6
	backoffBase   = 25 * time.Millisecond
)

type Store struct {
	client *dynamodb.Client
	table  string
}

func New(cfg aws.Config, table string) *Store {
	return &Store{client: dynamodb.NewFromConfig(cfg), table: table}
}

func sAttr(v string) *types.AttributeValueMemberS { return &types.AttributeValueMemberS{Value: v} }
func nAttr(v int) *types.AttributeValueMemberN {
	return &types.AttributeValueMemberN{Value: strconv.Itoa(v)}
}

func decisionKey(id string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"pk": sAttr("DECISION#" + id), "sk": sAttr("RESULT")}
}

func itemKey(batchID string, index int) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"pk": sAttr("BATCH#" + batchID), "sk": sAttr("ITEM#" + strconv.Itoa(index))}
}

func metaKey(batchID string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"pk": sAttr("BATCH#" + batchID), "sk": sAttr("META")}
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
	_, err = st.client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(st.table), Item: item})
	return err
}

func (st *Store) Get(ctx context.Context, decisionID string) (domain.Result, error) {
	out, err := st.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(st.table),
		Key:            decisionKey(decisionID),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return domain.Result{}, err
	}
	if out.Item == nil {
		return domain.Result{}, store.ErrNotFound
	}
	var r domain.Result
	return r, unmarshalAttr(out.Item, "result", &r)
}

// Create writes META and every ITEM# with BatchWriteItem, several chunks in
// flight at once.
func (st *Store) Create(ctx context.Context, batchID string, customers []domain.Customer) error {
	meta := metaKey(batchID)
	meta["queued"] = nAttr(len(customers))
	meta["decided"] = nAttr(0)
	meta["failed"] = nAttr(0)
	meta["cancelled"] = nAttr(0)
	puts := []map[string]types.AttributeValue{meta}
	for i, c := range customers {
		raw, err := json.Marshal(c)
		if err != nil {
			return err
		}
		item := itemKey(batchID, i)
		item["index"] = nAttr(i)
		item["customer"] = sAttr(string(raw))
		item["status"] = sAttr(string(store.Queued))
		item["attempts"] = nAttr(1)
		puts = append(puts, item)
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
			if err := st.writeChunk(ctx, chunk); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (st *Store) writeChunk(ctx context.Context, items []map[string]types.AttributeValue) error {
	reqs := make([]types.WriteRequest, len(items))
	for i, item := range items {
		reqs[i] = types.WriteRequest{PutRequest: &types.PutRequest{Item: item}}
	}
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
	return fmt.Errorf("batch write item: %d items still unprocessed after %d tries", len(reqs), writeTries)
}

// Decide moves the item to DECIDED and updates META in one transaction,
// conditional on the item being QUEUED on this attempt.
func (st *Store) Decide(ctx context.Context, batchID string, index, attempt int, r domain.Result) error {
	result, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = st.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{Update: &types.Update{
				TableName:           aws.String(st.table),
				Key:                 itemKey(batchID, index),
				UpdateExpression:    aws.String("SET #status = :decided, #result = :result"),
				ConditionExpression: aws.String("#status = :queued AND #attempts = :attempt"),
				ExpressionAttributeNames: map[string]string{
					"#status":   "status",
					"#result":   "result",
					"#attempts": "attempts",
				},
				ExpressionAttributeValues: map[string]types.AttributeValue{
					":decided": sAttr(string(store.Decided)),
					":queued":  sAttr(string(store.Queued)),
					":attempt": nAttr(attempt),
					":result":  sAttr(string(result)),
				},
				ReturnValuesOnConditionCheckFailure: types.ReturnValuesOnConditionCheckFailureAllOld,
			}},
			{Update: &types.Update{
				TableName:        aws.String(st.table),
				Key:              metaKey(batchID),
				UpdateExpression: aws.String("ADD #queued :minus, #decided :one"),
				ExpressionAttributeNames: map[string]string{
					"#queued":  "queued",
					"#decided": "decided",
				},
				ExpressionAttributeValues: map[string]types.AttributeValue{
					":minus": nAttr(-1),
					":one":   nAttr(1),
				},
			}},
		},
	})
	return transitionErr(err)
}

// transitionErr maps a failed item condition to ErrNotFound when the item does
// not exist and to ErrInvalidTransition when it is in another state.
func transitionErr(err error) error {
	var canceled *types.TransactionCanceledException
	if !errors.As(err, &canceled) || len(canceled.CancellationReasons) == 0 {
		return err
	}
	item := canceled.CancellationReasons[0]
	if aws.ToString(item.Code) != "ConditionalCheckFailed" {
		return err
	}
	if len(item.Item) == 0 {
		return store.ErrNotFound
	}
	return store.ErrInvalidTransition
}

// Report reads the whole batch with one paginated Query on pk.
func (st *Store) Report(ctx context.Context, batchID string) (store.Batch, error) {
	b := store.Batch{ID: batchID}
	found := false
	p := dynamodb.NewQueryPaginator(st.client, &dynamodb.QueryInput{
		TableName:                 aws.String(st.table),
		KeyConditionExpression:    aws.String("pk = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": sAttr("BATCH#" + batchID)},
		ConsistentRead:            aws.Bool(true),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return store.Batch{}, err
		}
		for _, row := range page.Items {
			meta, err := addRow(&b, row)
			if err != nil {
				return store.Batch{}, err
			}
			found = found || meta
		}
	}
	if !found {
		return store.Batch{}, store.ErrNotFound
	}
	slices.SortFunc(b.Items, func(x, y store.Item) int { return x.Index - y.Index })
	return b, nil
}

func addRow(b *store.Batch, row map[string]types.AttributeValue) (meta bool, err error) {
	sk, err := readS(row, "sk")
	if err != nil {
		return false, err
	}
	if sk == "META" {
		b.Counters, err = counters(row)
		return true, err
	}
	if !strings.HasPrefix(sk, "ITEM#") {
		return false, nil
	}
	it, err := batchItem(row)
	if err != nil {
		return false, err
	}
	b.Items = append(b.Items, it)
	return false, nil
}

func counters(row map[string]types.AttributeValue) (store.Counters, error) {
	var c store.Counters
	var err error
	for name, dst := range map[string]*int{"queued": &c.Queued, "decided": &c.Decided, "failed": &c.Failed, "cancelled": &c.Cancelled} {
		if *dst, err = readN(row, name); err != nil {
			return store.Counters{}, err
		}
	}
	return c, nil
}

func batchItem(row map[string]types.AttributeValue) (store.Item, error) {
	var it store.Item
	var err error
	if it.Index, err = readN(row, "index"); err != nil {
		return store.Item{}, err
	}
	if it.Attempts, err = readN(row, "attempts"); err != nil {
		return store.Item{}, err
	}
	status, err := readS(row, "status")
	if err != nil {
		return store.Item{}, err
	}
	it.Status = store.ItemStatus(status)
	if err := unmarshalAttr(row, "customer", &it.Customer); err != nil {
		return store.Item{}, err
	}
	if _, ok := row["result"]; ok {
		if err := unmarshalAttr(row, "result", &it.Result); err != nil {
			return store.Item{}, err
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
