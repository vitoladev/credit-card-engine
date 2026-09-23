package ddb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

// Each port has its own table (ADR 0006):
//
//	Decisions        decision_id               a single evaluation (evaluate.DecisionStore)
//	BatchItems       batch_id, item_id         one batch item: input, status, attempts, result (batch.Items)
//	IdempotencyKeys  idempotency_key           one Idempotency-Key (idempotency.Store, see Keys)
//
// A batch is its items; there is no batch row. Item IDs are version 7 UUIDs,
// so a Query on the batch returns items in submission order. A transition is
// one conditional UpdateItem on the item's own row, so the item status rules
// are enforced here, atomically, and concurrent workers on one batch never
// write the same row. Only BatchItems has a stream: it feeds the relay.
//
// StatusIndex, a global secondary index on BatchItems, keys each item by
// batch_status (<batch_id>#<status>) and item_id, so a list by status reads
// only the items in that status (ADR 0007). Every write that sets status also
// sets batch_status, in the same request.
const (
	writeChunk    = 25 // BatchWriteItem limit
	writeInFlight = 8
	writeTries    = 6
	backoffBase   = 25 * time.Millisecond
	retryInFlight = 8
	// rollbackTimeout bounds the delete of a batch whose Create failed. It
	// runs past the request's deadline, inside the Lambda's own timeout.
	rollbackTimeout = 2 * time.Second
)

// StatusIndex is the BatchItems index that lists a batch's items by status.
// The stack declares the same name.
const StatusIndex = "by-status"

func batchStatus(batchID string, s batch.ItemStatus) string {
	return batchID + "#" + string(s)
}

// Tables names the engine's tables. A Lambda that uses only some ports leaves
// the others empty.
type Tables struct {
	Decisions string
	Items     string
	Keys      string
}

type Store struct {
	client *dynamodb.Client
	tables Tables
}

func New(cfg aws.Config, tables Tables) *Store {
	return &Store{client: dynamodb.NewFromConfig(cfg), tables: tables}
}

func sAttr(v string) *types.AttributeValueMemberS { return &types.AttributeValueMemberS{Value: v} }
func n64Attr(v int64) *types.AttributeValueMemberN {
	return &types.AttributeValueMemberN{Value: strconv.FormatInt(v, 10)}
}

func nAttr(v int) *types.AttributeValueMemberN {
	return &types.AttributeValueMemberN{Value: strconv.Itoa(v)}
}

func decisionKey(id string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"decision_id": sAttr(id)}
}

func itemKey(batchID, itemID string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"batch_id": sAttr(batchID), "item_id": sAttr(itemID)}
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
	_, err = st.client.PutItem(ctx, &dynamodb.PutItemInput{TableName: new(st.tables.Decisions), Item: item})
	return err
}

func (st *Store) Get(ctx context.Context, decisionID string) (domain.Result, error) {
	out, err := st.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      new(st.tables.Decisions),
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

// Create writes every item with BatchWriteItem, several chunks in flight
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
		item["customer"] = sAttr(string(raw))
		item["status"] = sAttr(string(batch.Queued))
		item["batch_status"] = sAttr(batchStatus(batchID, batch.Queued))
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
		// The rollback runs even when the request's deadline caused the
		// failure: rows left behind would be relayed and evaluated for a batch
		// the client was told was not recorded.
		rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		if delErr := st.deleteBatch(rbCtx, batchID); delErr != nil {
			slog.Error("batch_rollback_failed", slog.String("batch_id", batchID), slog.String("error", delErr.Error()))
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
			RequestItems: map[string][]types.WriteRequest{st.tables.Items: reqs},
		})
		if err != nil {
			return fmt.Errorf("batch write item: %w", err)
		}
		reqs = out.UnprocessedItems[st.tables.Items]
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
		TableName:              new(st.tables.Items),
		KeyConditionExpression: new("batch_id = :b"),
		ProjectionExpression:   new("batch_id, item_id"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":b": sAttr(batchID),
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
			keys = append(keys, map[string]types.AttributeValue{"batch_id": row["batch_id"], "item_id": row["item_id"]})
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

// itemUpdate is a transition's update: a SET expression and its condition.
// They may also use #status, :from, and :to, which move sets. move also sets
// batch_status to match the new status.
type itemUpdate struct {
	update, cond string
	names        map[string]string
	values       map[string]types.AttributeValue
}

// move changes one item's status with a single conditional UpdateItem.
func (st *Store) move(ctx context.Context, batchID, itemID string, to, from batch.ItemStatus, u itemUpdate) error {
	names := map[string]string{"#status": "status", "#batch_status": "batch_status"}
	maps.Copy(names, u.names)
	vals := map[string]types.AttributeValue{
		":from": sAttr(string(from)), ":to": sAttr(string(to)),
		":batch_status": sAttr(batchStatus(batchID, to)),
	}
	maps.Copy(vals, u.values)
	_, err := st.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                           new(st.tables.Items),
		Key:                                 itemKey(batchID, itemID),
		UpdateExpression:                    new(u.update + ", #batch_status = :batch_status"),
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
		TableName:      new(st.tables.Items),
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

// Page reads one page of items with Query: the batch's items, or with a
// status the StatusIndex, which holds only the items in that status. A Query
// stops early at 1 MB, so Page repeats it until the page holds q.Limit items
// or the items end. An empty first page is an unknown batch unless the batch
// has items in another status.
func (st *Store) Page(ctx context.Context, batchID string, q batch.PageQuery) (batch.Page, error) {
	p, err := st.read(ctx, st.pageInput(batchID, q), q.Limit)
	if err != nil {
		return batch.Page{}, err
	}
	if q.After == "" && len(p.Items) == 0 {
		if err := st.mustExist(ctx, batchID); err != nil {
			return batch.Page{}, err
		}
	}
	return p, nil
}

// read runs the Query until it has limit items or the items end, and sets
// More when an item follows the page.
func (st *Store) read(ctx context.Context, in *dynamodb.QueryInput, limit int) (batch.Page, error) {
	var p batch.Page
	for {
		in.Limit = new(int32(limit - len(p.Items)))
		out, err := st.client.Query(ctx, in)
		if err != nil {
			return batch.Page{}, err
		}
		if p.Items, err = appendItems(p.Items, out.Items); err != nil {
			return batch.Page{}, err
		}
		if out.LastEvaluatedKey == nil {
			return p, nil
		}
		if len(p.Items) == limit {
			p.More, err = st.more(ctx, in, out.LastEvaluatedKey)
			return p, err
		}
		in.ExclusiveStartKey = out.LastEvaluatedKey
	}
}

// mustExist returns ErrNotFound for a batch with no items. A batch always has
// at least one, so a batch_id with none is unknown.
func (st *Store) mustExist(ctx context.Context, batchID string) error {
	out, err := st.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 new(st.tables.Items),
		KeyConditionExpression:    new("batch_id = :b"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":b": sAttr(batchID)},
		Select:                    types.SelectCount,
		Limit:                     new(int32(1)),
		ConsistentRead:            new(true),
	})
	if err != nil {
		return err
	}
	if out.Count == 0 {
		return batch.ErrNotFound
	}
	return nil
}

// more reports whether an item q selects follows start. DynamoDB returns a
// LastEvaluatedKey whenever a Query stops at its Limit, even on the last item,
// so a full page checks before it hands out a cursor to an empty page. Every
// item the Query reads matches, so reading one is enough.
func (st *Store) more(ctx context.Context, in *dynamodb.QueryInput, start map[string]types.AttributeValue) (bool, error) {
	probe := *in
	probe.Select = types.SelectCount
	probe.ExclusiveStartKey = start
	probe.Limit = new(int32(1))
	out, err := st.client.Query(ctx, &probe)
	if err != nil {
		return false, err
	}
	return out.Count > 0, nil
}

// pageInput is the Query that q selects. With a status it reads the
// StatusIndex, which is eventually consistent: an item that just changed
// status can show under its old status for a moment. Without one it reads the
// batch's items with a consistent Query.
func (st *Store) pageInput(batchID string, q batch.PageQuery) *dynamodb.QueryInput {
	if q.Status == "" {
		in := &dynamodb.QueryInput{
			TableName:                 new(st.tables.Items),
			KeyConditionExpression:    new("batch_id = :b"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":b": sAttr(batchID)},
			ConsistentRead:            new(true),
		}
		if q.After != "" {
			in.ExclusiveStartKey = itemKey(batchID, q.After)
		}
		return in
	}
	bs := batchStatus(batchID, q.Status)
	in := &dynamodb.QueryInput{
		TableName:                 new(st.tables.Items),
		IndexName:                 new(StatusIndex),
		KeyConditionExpression:    new("batch_status = :bs"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":bs": sAttr(bs)},
	}
	if q.After != "" {
		start := itemKey(batchID, q.After)
		start["batch_status"] = sAttr(bs)
		in.ExclusiveStartKey = start
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
