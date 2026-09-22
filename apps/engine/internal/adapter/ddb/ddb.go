package ddb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
//	BATCH#<batch_id>       / META       marks the batch as existing; written once
//	BATCH#<batch_id>       / ITEM#<i>   one batch item: input, status, attempts, result
//
// A transition writes only its own ITEM# row. Counters are derived from the
// items on read, so concurrent workers on one batch never write the same row.
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
		return domain.Result{}, store.ErrNotFound
	}
	var r domain.Result
	return r, unmarshalAttr(out.Item, "result", &r)
}

// Create writes META and every ITEM# with BatchWriteItem, several chunks in
// flight at once. A failed write deletes every row for the batch.
func (st *Store) Create(ctx context.Context, batchID string, customers []domain.Customer) error {
	meta := metaKey(batchID)
	meta["size"] = nAttr(len(customers))
	puts := []types.WriteRequest{{PutRequest: &types.PutRequest{Item: meta}}}
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

// Decide moves the item to DECIDED, conditional on it being QUEUED on this
// attempt.
func (st *Store) Decide(ctx context.Context, batchID string, index, attempt int, r domain.Result) error {
	result, err := json.Marshal(r)
	if err != nil {
		return err
	}
	err = st.move(ctx, batchID, index, store.Decided, store.Queued, itemUpdate{
		update: "SET #status = :to, #result = :result",
		cond:   "#status = :from AND #attempts = :attempt",
		names:  map[string]string{"#result": "result", "#attempts": "attempts"},
		values: map[string]types.AttributeValue{":attempt": nAttr(attempt), ":result": sAttr(string(result))},
	})
	return transitionErr(err)
}

// Fail moves the item from QUEUED to FAILED, conditional on this attempt.
func (st *Store) Fail(ctx context.Context, batchID string, index, attempt int) error {
	err := st.move(ctx, batchID, index, store.Failed, store.Queued, itemUpdate{
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
func (st *Store) Retry(ctx context.Context, batchID string, index, attempt int) error {
	err := st.move(ctx, batchID, index, store.Queued, store.Failed, itemUpdate{
		update: "SET #status = :to, #attempts = #attempts + :one",
		cond:   "#status = :from AND #attempts = :attempt AND #attempts < :max",
		names:  map[string]string{"#attempts": "attempts"},
		values: map[string]types.AttributeValue{":attempt": nAttr(attempt), ":max": nAttr(store.MaxAttempts), ":one": nAttr(1)},
	})
	old, ok := conditionFailed(err)
	if !ok || len(old) == 0 {
		return transitionErr(err)
	}
	if n, err := readN(old, "attempts"); err == nil && n >= store.MaxAttempts && isStatus(old, store.Failed) {
		return store.ErrMaxAttempts
	}
	return store.ErrInvalidTransition
}

// RetryMany retries each item with its own conditional update, several in
// flight at once. Items that cannot transition are skipped. The first store
// error stops the items not yet started.
func (st *Store) RetryMany(ctx context.Context, batchID string, items []store.Item) ([]store.Item, error) {
	moved := make([]bool, len(items))
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	sem := make(chan struct{}, retryInFlight)
	for i, it := range items {
		if it.Attempts >= store.MaxAttempts {
			continue
		}
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
			err := st.Retry(ctx, batchID, it.Index, it.Attempts)
			switch {
			case err == nil:
				moved[i] = true
			case errors.Is(err, store.ErrInvalidTransition), errors.Is(err, store.ErrMaxAttempts):
			default:
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	var queued []store.Item
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
func (st *Store) Cancel(ctx context.Context, batchID string, index int) error {
	err := st.move(ctx, batchID, index, store.Cancelled, store.Failed, itemUpdate{
		update: "SET #status = :to",
		cond:   "#status = :from",
	})
	if old, ok := conditionFailed(err); ok && isStatus(old, store.Cancelled) {
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
func (st *Store) move(ctx context.Context, batchID string, index int, to, from store.ItemStatus, u itemUpdate) error {
	names := map[string]string{"#status": "status"}
	maps.Copy(names, u.names)
	vals := map[string]types.AttributeValue{":from": sAttr(string(from)), ":to": sAttr(string(to))}
	maps.Copy(vals, u.values)
	_, err := st.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                           new(st.table),
		Key:                                 itemKey(batchID, index),
		UpdateExpression:                    new(u.update),
		ConditionExpression:                 new(u.cond),
		ExpressionAttributeNames:            names,
		ExpressionAttributeValues:           vals,
		ReturnValuesOnConditionCheckFailure: types.ReturnValuesOnConditionCheckFailureAllOld,
	})
	return err
}

func isStatus(row map[string]types.AttributeValue, s store.ItemStatus) bool {
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
		return store.ErrNotFound
	}
	return store.ErrInvalidTransition
}

func (st *Store) Item(ctx context.Context, batchID string, index int) (store.Item, error) {
	out, err := st.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      new(st.table),
		Key:            itemKey(batchID, index),
		ConsistentRead: new(true),
	})
	if err != nil {
		return store.Item{}, err
	}
	if out.Item == nil {
		return store.Item{}, store.ErrNotFound
	}
	return batchItem(out.Item)
}

// Failed reads the batch's failed items with one paginated Query, filtered to
// META (to tell an unknown batch apart) and FAILED items.
func (st *Store) Failed(ctx context.Context, batchID string) ([]store.Item, error) {
	b, err := st.query(ctx, batchID, &dynamodb.QueryInput{
		FilterExpression:         new("attribute_exists(#size) OR #status = :failed"),
		ExpressionAttributeNames: map[string]string{"#size": "size", "#status": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":failed": sAttr(string(store.Failed)),
		},
	})
	return b.Items, err
}

// Report reads the whole batch with one paginated Query on pk and counts the
// items by status.
func (st *Store) Report(ctx context.Context, batchID string) (store.Batch, error) {
	return st.query(ctx, batchID, &dynamodb.QueryInput{})
}

// query runs one paginated Query on pk with in's filter, if any.
func (st *Store) query(ctx context.Context, batchID string, in *dynamodb.QueryInput) (store.Batch, error) {
	b := store.Batch{ID: batchID}
	found := false
	in.TableName = new(st.table)
	in.KeyConditionExpression = new("pk = :pk")
	if in.ExpressionAttributeValues == nil {
		in.ExpressionAttributeValues = map[string]types.AttributeValue{}
	}
	in.ExpressionAttributeValues[":pk"] = sAttr("BATCH#" + batchID)
	in.ConsistentRead = new(true)
	p := dynamodb.NewQueryPaginator(st.client, in)
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
	b.Counters = store.Count(b.Items)
	return b, nil
}

func addRow(b *store.Batch, row map[string]types.AttributeValue) (meta bool, err error) {
	sk, err := readS(row, "sk")
	if err != nil {
		return false, err
	}
	if sk == "META" {
		return true, nil
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
