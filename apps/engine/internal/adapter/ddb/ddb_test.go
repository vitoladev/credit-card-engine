package ddb_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"

	"engine/internal/adapter/ddb"
	"engine/internal/adapter/sqspub"
	"engine/internal/batch"
	"engine/internal/domain"
	"engine/internal/evaluate"
	"engine/internal/flocitest"
)

type itemsTable struct {
	*ddb.Items
	faults *flocitest.Faults
	cfg    aws.Config
	tables flocitest.Tables
}

func newItemsTable(t *testing.T) itemsTable {
	t.Helper()
	cfg, faults := flocitest.Config(t)
	tables := flocitest.CreateTables(t, cfg)
	return itemsTable{Items: ddb.NewItems(cfg, tables.Items), faults: faults, cfg: cfg, tables: tables}
}

func newDecisions(t *testing.T) *ddb.Decisions {
	t.Helper()
	cfg, _ := flocitest.Config(t)
	return ddb.NewDecisions(cfg, flocitest.CreateTables(t, cfg).Decisions)
}

// newItems gives each customer a version 7 item ID, as batch.Submit does.
func newItems(customers []domain.Customer) []batch.Item {
	items := make([]batch.Item, len(customers))
	for i, c := range customers {
		items[i] = batch.Item{ID: uuid.NewV7().String(), Customer: c}
	}
	return items
}

func create(t *testing.T, st itemsTable, batchID string, customers []domain.Customer) []string {
	t.Helper()
	items := newItems(customers)
	if err := st.Create(t.Context(), batchID, items); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	return ids
}

type counters struct{ queued, approved, denied, failed, cancelled int }

// all reads every item of the batch page by page and tallies them by status.
func all(t *testing.T, st itemsTable, batchID string, q batch.PageQuery) ([]batch.Item, counters) {
	t.Helper()
	var items []batch.Item
	for {
		p, err := st.Page(t.Context(), batchID, q)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Items) > q.Limit {
			t.Fatalf("page of %d items over limit %d", len(p.Items), q.Limit)
		}
		items = append(items, p.Items...)
		if !p.More {
			break
		}
		q.After = p.Items[len(p.Items)-1].ID
	}
	var c counters
	for _, it := range items {
		switch it.Status {
		case batch.Queued:
			c.queued++
		case batch.Approved:
			c.approved++
		case batch.Denied:
			c.denied++
		case batch.Failed:
			c.failed++
		case batch.Cancelled:
			c.cancelled++
		}
	}
	return items, c
}

func every(t *testing.T, st itemsTable, batchID string) ([]batch.Item, counters) {
	t.Helper()
	return all(t, st, batchID, batch.PageQuery{Limit: batch.MaxPageLimit})
}

func ids(items []batch.Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

func TestDecisionRoundTrip(t *testing.T) {
	st := newDecisions(t)
	r := domain.Result{Name: "Ana", CPFMasked: "390.***.***-05", Decision: domain.Approved, RevolvingAmountCents: 250_000, Reasons: []string{"eligible"}}
	if err := st.Save(t.Context(), "d1", domain.Customer{Name: "Ana", CPF: "39053344705"}, r); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(t.Context(), "d1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Decision != r.Decision || got.RevolvingAmountCents != r.RevolvingAmountCents || got.CPFMasked != r.CPFMasked {
		t.Fatalf("%+v", got)
	}
	if _, err := st.Get(t.Context(), "missing"); !errors.Is(err, evaluate.ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestBatchTransitions(t *testing.T) {
	st := newItemsTable(t)
	// 1000 items of ~2 KB push each Query past one 1 MB response, and the
	// write past one BatchWriteItem chunk.
	customers := make([]domain.Customer, 1000)
	for i := range customers {
		customers[i] = domain.Customer{Name: strings.Repeat("n", 2000), CPF: "39053344705"}
	}
	created := create(t, st, "b1", customers)
	items, c := every(t, st, "b1")
	if c != (counters{queued: 1000}) || !slices.Equal(ids(items), created) {
		t.Fatalf("counters=%+v items=%d, in submission order: %v", c, len(items), slices.Equal(ids(items), created))
	}
	for i, it := range items {
		if it.Status != batch.Queued || it.Attempts != 1 || it.Customer.CPF != "39053344705" {
			t.Fatalf("item %d: status=%s attempts=%d", i, it.Status, it.Attempts)
		}
	}

	approve := domain.Result{Decision: domain.Approved, RevolvingAmountCents: 100, Reasons: []string{"eligible"}}
	if err := st.Decide(t.Context(), "b1", created[7], 1, approve); err != nil {
		t.Fatal(err)
	}
	if err := st.Decide(t.Context(), "b1", created[7], 1, approve); !errors.Is(err, batch.ErrInvalidTransition) {
		t.Fatalf("repeat decide err=%v", err)
	}
	if err := st.Decide(t.Context(), "b1", created[8], 2, approve); !errors.Is(err, batch.ErrInvalidTransition) {
		t.Fatalf("wrong attempt err=%v", err)
	}
	if err := st.Decide(t.Context(), "b1", uuid.NewV7().String(), 1, approve); !errors.Is(err, batch.ErrNotFound) {
		t.Fatalf("missing item err=%v", err)
	}
	deny := domain.Result{Decision: domain.Denied, Reasons: []string{"score_below_600"}}
	if err := st.Decide(t.Context(), "b1", created[8], 1, deny); err != nil {
		t.Fatal(err)
	}

	items, c = every(t, st, "b1")
	if c != (counters{queued: 998, approved: 1, denied: 1}) {
		t.Fatalf("counters=%+v", c)
	}
	if it := items[7]; it.Status != batch.Approved || it.Result.RevolvingAmountCents != 100 {
		t.Fatalf("%+v", it.Status)
	}
	// A filtered read over 1 MB responses still finds the one match.
	if got, _ := all(t, st, "b1", batch.PageQuery{Status: batch.Denied, Limit: 10}); len(got) != 1 || got[0].ID != created[8] {
		t.Fatalf("denied=%+v", ids(got))
	}
	if _, err := st.Page(t.Context(), "missing", batch.PageQuery{Limit: 10}); !errors.Is(err, batch.ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestFailRetryCancelTransitions(t *testing.T) {
	st := newItemsTable(t)
	ctx := t.Context()
	id := create(t, st, "b1", []domain.Customer{{Name: "Ana", CPF: "39053344705"}, {Name: "Bruno", CPF: "12345678909"}, {Name: "Carla", CPF: "98765432100"}})
	missing := uuid.NewV7().String()
	wantErr := func(name string, err, want error) {
		t.Helper()
		if !errors.Is(err, want) || (want == nil && err != nil) {
			t.Fatalf("%s: err=%v want %v", name, err, want)
		}
	}
	wantCounters := func(want counters) {
		t.Helper()
		if _, c := every(t, st, "b1"); c != want {
			t.Fatalf("counters=%+v want %+v", c, want)
		}
	}

	wantErr("fail wrong attempt", st.Fail(ctx, "b1", id[0], 2), batch.ErrInvalidTransition)
	wantErr("fail missing", st.Fail(ctx, "b1", missing, 1), batch.ErrNotFound)
	wantErr("fail 0", st.Fail(ctx, "b1", id[0], 1), nil)
	wantErr("fail 0 again", st.Fail(ctx, "b1", id[0], 1), batch.ErrInvalidTransition)
	wantErr("fail 1", st.Fail(ctx, "b1", id[1], 1), nil)
	wantErr("decide failed item", st.Decide(ctx, "b1", id[0], 1, domain.Result{Decision: domain.Approved}), batch.ErrInvalidTransition)
	wantCounters(counters{queued: 1, failed: 2})

	failed, _ := all(t, st, "b1", batch.PageQuery{Status: batch.Failed, Limit: 10})
	if !slices.Equal(ids(failed), id[:2]) || failed[1].Customer.CPF != "12345678909" {
		t.Fatalf("failed=%+v", failed)
	}

	before, err := st.Item(ctx, "b1", id[0])
	if err != nil || before.QueuedAt.IsZero() || time.Since(before.QueuedAt) > time.Minute {
		t.Fatalf("queued_at at create: item=%+v err=%v", before, err)
	}
	time.Sleep(5 * time.Millisecond)

	// Two operators retry the same attempt: only one wins.
	wantErr("retry 0", st.Retry(ctx, "b1", id[0], 1), nil)
	wantErr("retry 0 again", st.Retry(ctx, "b1", id[0], 1), batch.ErrInvalidTransition)
	wantErr("retry queued", st.Retry(ctx, "b1", id[2], 1), batch.ErrInvalidTransition)
	wantErr("retry missing", st.Retry(ctx, "b1", missing, 1), batch.ErrNotFound)
	it, err := st.Item(ctx, "b1", id[0])
	if err != nil || it.ID != id[0] || it.Status != batch.Queued || it.Attempts != 2 || it.Customer.Name != "Ana" {
		t.Fatalf("item=%+v err=%v", it, err)
	}
	if !it.QueuedAt.After(before.QueuedAt) {
		t.Fatalf("retry kept queued_at %v, was %v", it.QueuedAt, before.QueuedAt)
	}
	if _, err := st.Item(ctx, "b1", missing); !errors.Is(err, batch.ErrNotFound) {
		t.Fatalf("item missing: err=%v", err)
	}
	wantCounters(counters{queued: 2, failed: 1})

	for attempt := 2; attempt < batch.MaxAttempts; attempt++ {
		wantErr("fail", st.Fail(ctx, "b1", id[0], attempt), nil)
		wantErr("retry", st.Retry(ctx, "b1", id[0], attempt), nil)
	}
	wantErr("fail at max", st.Fail(ctx, "b1", id[0], batch.MaxAttempts), nil)
	wantErr("retry at max", st.Retry(ctx, "b1", id[0], batch.MaxAttempts), batch.ErrMaxAttempts)

	wantErr("cancel 1", st.Cancel(ctx, "b1", id[1]), nil)
	wantErr("cancel 1 again", st.Cancel(ctx, "b1", id[1]), nil)
	wantErr("cancel queued", st.Cancel(ctx, "b1", id[2]), batch.ErrInvalidTransition)
	wantErr("cancel missing", st.Cancel(ctx, "b1", missing), batch.ErrNotFound)
	wantErr("retry cancelled", st.Retry(ctx, "b1", id[1], 1), batch.ErrInvalidTransition)
	wantCounters(counters{queued: 1, failed: 1, cancelled: 1})
}

func TestRetryManyMovesFailedItems(t *testing.T) {
	st := newItemsTable(t)
	ctx := t.Context()
	id := create(t, st, "b1", []domain.Customer{{Name: "Ana", CPF: "39053344705"}, {Name: "Bruno", CPF: "12345678909"}, {Name: "Carla", CPF: "98765432100"}})
	for _, i := range id[:2] {
		if err := st.Fail(ctx, "b1", i, 1); err != nil {
			t.Fatal(err)
		}
	}
	failed, _ := all(t, st, "b1", batch.PageQuery{Status: batch.Failed, Limit: 10})
	queued, err := st.RetryMany(ctx, "b1", append(failed, batch.Item{ID: id[2], Attempts: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids(queued), id[:2]) {
		t.Fatalf("queued=%+v", queued)
	}
	items, c := every(t, st, "b1")
	if c != (counters{queued: 3}) {
		t.Fatalf("counters=%+v", c)
	}
	if items[0].Attempts != 2 || items[1].Attempts != 2 || items[2].Attempts != 1 {
		t.Fatalf("attempts=%d,%d,%d", items[0].Attempts, items[1].Attempts, items[2].Attempts)
	}
}

func TestRetryManyMovesManyItems(t *testing.T) {
	st := newItemsTable(t)
	ctx := t.Context()
	n := 51
	customers := make([]domain.Customer, n)
	for i := range customers {
		customers[i] = domain.Customer{Name: "Ana", CPF: "39053344705"}
	}
	for _, i := range create(t, st, "b1", customers) {
		if err := st.Fail(ctx, "b1", i, 1); err != nil {
			t.Fatal(err)
		}
	}
	failed, _ := all(t, st, "b1", batch.PageQuery{Status: batch.Failed, Limit: batch.MaxPageLimit})
	queued, err := st.RetryMany(ctx, "b1", failed)
	if err != nil || len(queued) != n {
		t.Fatalf("queued=%d err=%v", len(queued), err)
	}
	if _, c := every(t, st, "b1"); c != (counters{queued: n}) {
		t.Fatalf("counters=%+v", c)
	}
}

// Workers decide items of one batch in parallel. Each transition writes only
// its own item, so none of them conflicts with another.
func TestConcurrentDecidesOnOneBatch(t *testing.T) {
	st := newItemsTable(t)
	ctx := t.Context()
	n := 200
	customers := make([]domain.Customer, n)
	for i := range customers {
		customers[i] = domain.Customer{Name: "Ana", CPF: "39053344705"}
	}
	id := create(t, st, "b1", customers)
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() { errs[i] = st.Decide(ctx, "b1", id[i], 1, domain.Result{Decision: domain.Approved}) })
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	if _, c := every(t, st, "b1"); c != (counters{approved: n}) {
		t.Fatalf("counters=%+v", c)
	}
}

// Ten items, even ones failed: pages of 3 over all items and pages of 2 over
// the failed ones both come back full and in submission order.
func TestPageFollowsTheCursorAndTheFilter(t *testing.T) {
	st := newItemsTable(t)
	ctx := t.Context()
	customers := make([]domain.Customer, 10)
	for i := range customers {
		customers[i] = domain.Customer{Name: "Ana", CPF: "39053344705"}
	}
	id := create(t, st, "b1", customers)
	var even []string
	for i := 0; i < len(id); i += 2 {
		if err := st.Fail(ctx, "b1", id[i], 1); err != nil {
			t.Fatal(err)
		}
		even = append(even, id[i])
	}

	first, err := st.Page(ctx, "b1", batch.PageQuery{Limit: 3})
	if err != nil || len(first.Items) != 3 || !first.More {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	if got, _ := all(t, st, "b1", batch.PageQuery{Limit: 3}); !slices.Equal(ids(got), id) {
		t.Fatalf("all=%v", ids(got))
	}
	byStatus, err := st.Page(ctx, "b1", batch.PageQuery{Status: batch.Failed, Limit: 2})
	if err != nil || !slices.Equal(ids(byStatus.Items), even[:2]) || !byStatus.More {
		t.Fatalf("failed page=%+v err=%v", byStatus, err)
	}
	if got, _ := all(t, st, "b1", batch.PageQuery{Status: batch.Failed, Limit: 2}); !slices.Equal(ids(got), even) {
		t.Fatalf("failed=%v want %v", ids(got), even)
	}
	// A page that ends on the last match has no cursor, even when unmatched
	// rows follow it.
	for name, q := range map[string]batch.PageQuery{
		"whole batch":         {Limit: 10},
		"last three":          {After: id[6], Limit: 3},
		"last two failed":     {Status: batch.Failed, After: id[4], Limit: 2},
		"unmatched rows left": {Status: batch.Failed, After: id[6], Limit: 1},
	} {
		p, err := st.Page(ctx, "b1", q)
		if err != nil || len(p.Items) != q.Limit || p.More {
			t.Fatalf("%s: page=%d more=%v err=%v", name, len(p.Items), p.More, err)
		}
	}
	last, err := st.Page(ctx, "b1", batch.PageQuery{After: id[9], Limit: 3})
	if err != nil || len(last.Items) != 0 || last.More {
		t.Fatalf("after the last item=%+v err=%v", last, err)
	}
}

// A batch always has items, so no rows scanned means an unknown batch, and
// rows scanned with none matching is an empty page.
func TestPageTellsAnUnknownBatchFromNoMatch(t *testing.T) {
	st := newItemsTable(t)
	create(t, st, "b1", []domain.Customer{{Name: "Ana", CPF: "39053344705"}})
	p, err := st.Page(t.Context(), "b1", batch.PageQuery{Status: batch.Cancelled, Limit: 10})
	if err != nil || len(p.Items) != 0 || p.More {
		t.Fatalf("page=%+v err=%v", p, err)
	}
	for _, q := range []batch.PageQuery{{Limit: 10}, {Status: batch.Cancelled, Limit: 10}} {
		if _, err := st.Page(t.Context(), "missing", q); !errors.Is(err, batch.ErrNotFound) {
			t.Fatalf("%+v: err=%v", q, err)
		}
	}
}

// A filtered page takes several Query calls. A failure on a later one fails
// the page instead of returning a short one.
func TestPageFailsOnAFailedLaterQuery(t *testing.T) {
	st := newItemsTable(t)
	customers := make([]domain.Customer, 6)
	for i := range customers {
		customers[i] = domain.Customer{Name: "Ana", CPF: "39053344705"}
	}
	id := create(t, st, "b1", customers)
	if err := st.Fail(t.Context(), "b1", id[5], 1); err != nil {
		t.Fatal(err)
	}
	st.faults.FailCallsAfter("Query", 1, 1)
	if _, err := st.Page(t.Context(), "b1", batch.PageQuery{Status: batch.Failed, Limit: 1}); !errors.Is(err, flocitest.ErrInjected) {
		t.Fatalf("err=%v", err)
	}
}

// A rollback that fails too leaves rows behind: the error says so and the log
// names the batch, so an operator can find it.
func TestCreateLogsTheBatchWhenTheRollbackFails(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	st := newItemsTable(t)
	st.faults.FailCalls("BatchWriteItem", 1)
	st.faults.FailCalls("Query", 1)
	customers := make([]domain.Customer, 30)
	for i := range customers {
		customers[i] = domain.Customer{Name: "Ana", CPF: "39053344705"}
	}
	err := st.Create(t.Context(), "b1", newItems(customers))
	if !errors.Is(err, flocitest.ErrInjected) {
		t.Fatalf("err=%v", err)
	}
	logs := buf.String()
	if !strings.Contains(logs, `"msg":"batch_rollback_failed"`) || !strings.Contains(logs, `"batch_id":"b1"`) {
		t.Fatalf("logs=%s", logs)
	}
	if strings.Contains(logs, "39053344705") || strings.Contains(logs, "Ana") {
		t.Fatalf("leaked customer data: %s", logs)
	}
}

// The rollback runs on its own deadline: a request that already timed out
// still removes the rows it wrote.
func TestCreateRollsBackAfterTheRequestDeadline(t *testing.T) {
	st := newItemsTable(t)
	st.faults.FailCalls("BatchWriteItem", 1)
	ctx, cancel := context.WithCancel(t.Context())
	customers := make([]domain.Customer, 30)
	for i := range customers {
		customers[i] = domain.Customer{Name: "Ana", CPF: "39053344705"}
	}
	items := newItems(customers)
	// Cancel once the writes are done, before the rollback: the injected
	// failure makes Create roll back on a cancelled request context.
	st.faults.OnCall("Query", cancel)
	if err := st.Create(ctx, "b1", items); err == nil {
		t.Fatal("expected write failure")
	}
	if n := flocitest.Rows(t, st.cfg, st.tables.All()...); n != 0 {
		t.Fatalf("rows=%d", n)
	}
}

// A Lambda is killed at its deadline, so Create stops writing a second before
// it and still has time to delete what it wrote. Before, a batch written by a
// timed-out invocation stayed behind and was evaluated.
func TestCreateStopsWritingInTimeToRollBack(t *testing.T) {
	st := newItemsTable(t)
	ctx, cancel := context.WithTimeout(t.Context(), 1500*time.Millisecond)
	defer cancel()
	customers := make([]domain.Customer, 30)
	for i := range customers {
		customers[i] = domain.Customer{Name: "Ana", CPF: "39053344705"}
	}
	// One chunk's write waits past the write deadline (500 ms before
	// the request's), as a slow store would.
	st.faults.OnCall("BatchWriteItem", func() { time.Sleep(700 * time.Millisecond) })
	start := time.Now()
	err := st.Create(ctx, "b1", newItems(customers))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("Create returned after the request's deadline (%v)", time.Since(start))
	}
	if n := flocitest.Rows(t, st.cfg, st.tables.All()...); n != 0 {
		t.Fatalf("rows=%d", n)
	}
}

func TestCreateRemovesPartialRowsOnWriteFailure(t *testing.T) {
	st := newItemsTable(t)
	st.faults.FailCalls("BatchWriteItem", 1)
	customers := make([]domain.Customer, 30)
	for i := range customers {
		customers[i] = domain.Customer{Name: "Ana", CPF: "39053344705"}
	}
	if err := st.Create(t.Context(), "b1", newItems(customers)); err == nil {
		t.Fatal("expected write failure")
	}
	if _, err := st.Page(t.Context(), "b1", batch.PageQuery{Limit: 10}); !errors.Is(err, batch.ErrNotFound) {
		t.Fatalf("leftover batch: err=%v", err)
	}
	if n := flocitest.Rows(t, st.cfg, st.tables.All()...); n != 0 {
		t.Fatalf("rows=%d", n)
	}
}

// The BatchItems stream carries every item change. A removal is not an item
// event.
func TestStreamRecordsBecomeItemEvents(t *testing.T) {
	st := newItemsTable(t)
	ctx := t.Context()
	id := create(t, st, "b1", []domain.Customer{{Name: "Ana", CPF: "39053344705"}})
	if err := st.Decide(ctx, "b1", id[0], 1, domain.Result{Decision: domain.Approved}); err != nil {
		t.Fatal(err)
	}

	var got []batch.ItemEvent
	for _, rec := range flocitest.TableStream(t, st.cfg, st.tables.Items).Next().Records {
		e, ok, err := ddb.ItemEventFrom(rec)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			got = append(got, e)
		}
	}
	want := []batch.ItemEvent{
		batch.NewItemEvent("b1", id[0], 1, batch.Queued),
		batch.NewItemEvent("b1", id[0], 1, batch.Approved),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("events=%+v", got)
	}

	if _, ok, err := ddb.ItemEventFrom(events.DynamoDBEventRecord{EventName: "REMOVE"}); ok || err != nil {
		t.Fatalf("remove: ok=%v err=%v", ok, err)
	}
	broken := events.DynamoDBEventRecord{EventName: "INSERT", Change: events.DynamoDBStreamRecord{NewImage: map[string]events.DynamoDBAttributeValue{
		"batch_id": events.NewStringAttribute("b1"), "item_id": events.NewStringAttribute(id[0]),
	}}}
	if _, _, err := ddb.ItemEventFrom(broken); err == nil {
		t.Fatal("decoded a record with no status")
	}
}

// ADR 0007: a list by status reads the StatusIndex, so every transition must
// move the item's batch_status with its status.
func TestTheStatusIndexFollowsEveryTransition(t *testing.T) {
	st := newItemsTable(t)
	ctx := t.Context()
	customers := make([]domain.Customer, 4)
	for i := range customers {
		customers[i] = domain.Customer{Name: "Ana", CPF: "39053344705"}
	}
	id := create(t, st, "b1", customers)
	approve := domain.Result{Decision: domain.Approved, Reasons: []string{"eligible"}}
	steps := []error{
		st.Decide(ctx, "b1", id[0], 1, approve),
		st.Fail(ctx, "b1", id[1], 1),
		st.Fail(ctx, "b1", id[2], 1),
		st.Retry(ctx, "b1", id[2], 1),
		st.Fail(ctx, "b1", id[3], 1),
		st.Cancel(ctx, "b1", id[3]),
	}
	if err := errors.Join(steps...); err != nil {
		t.Fatal(err)
	}
	for status, want := range map[batch.ItemStatus][]string{
		batch.Approved:  {id[0]},
		batch.Failed:    {id[1]},
		batch.Queued:    {id[2]},
		batch.Cancelled: {id[3]},
		batch.Denied:    nil,
	} {
		p, err := st.Page(ctx, "b1", batch.PageQuery{Status: status, Limit: 10})
		if err != nil || !slices.Equal(ids(p.Items), want) || p.More {
			t.Fatalf("%s: page=%v more=%v err=%v, want %v", status, ids(p.Items), p.More, err, want)
		}
	}
}

// Every batch.Items method works against the BatchItems table the worker and
// the DLQ consumer receive (ADR 0006).
func TestItemsNeedOnlyTheBatchItemsTable(t *testing.T) {
	cfg, _ := flocitest.Config(t)
	tables := flocitest.CreateTables(t, cfg)
	st := ddb.NewItems(cfg, tables.Items)
	ctx := t.Context()
	items := newItems([]domain.Customer{{Name: "Ana", CPF: "39053344705"}, {Name: "Bruno", CPF: "12345678909"}})
	if err := st.Create(ctx, "b1", items); err != nil {
		t.Fatal(err)
	}
	approve := domain.Result{Decision: domain.Approved, Reasons: []string{"eligible"}}
	steps := []error{
		st.Decide(ctx, "b1", items[0].ID, 1, approve),
		st.Fail(ctx, "b1", items[1].ID, 1),
		st.Retry(ctx, "b1", items[1].ID, 1),
		st.Fail(ctx, "b1", items[1].ID, 2),
		st.Cancel(ctx, "b1", items[1].ID),
	}
	if err := errors.Join(steps...); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Item(ctx, "b1", items[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RetryMany(ctx, "b1", nil); err != nil {
		t.Fatal(err)
	}
	for _, q := range []batch.PageQuery{{Limit: 10}, {Status: batch.Cancelled, Limit: 10}} {
		if p, err := st.Page(ctx, "b1", q); err != nil || len(p.Items) == 0 {
			t.Fatalf("%+v: page=%+v err=%v", q, p, err)
		}
	}
}

func TestRelayHandlePublishesQueuedItems(t *testing.T) {
	st := newItemsTable(t)
	create(t, st, "b1", []domain.Customer{{Name: "Ana", CPF: "39053344705"}})
	queue := flocitest.Queue(t, st.cfg)
	resp, err := ddb.NewRelay(batch.New(batch.Deps{Publisher: sqspub.New(st.cfg, queue)})).Handle(
		t.Context(), flocitest.TableStream(t, st.cfg, st.tables.Items).Next())
	if err != nil || len(resp.BatchItemFailures) != 0 {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}

func TestRelayHandleReportsAFailedPublish(t *testing.T) {
	st := newItemsTable(t)
	create(t, st, "b1", []domain.Customer{{Name: "Ana", CPF: "39053344705"}})
	queue := flocitest.Queue(t, st.cfg)
	st.faults.FailCalls("SendMessageBatch", 1)
	ev := flocitest.TableStream(t, st.cfg, st.tables.Items).Next()
	resp, err := ddb.NewRelay(batch.New(batch.Deps{Publisher: sqspub.New(st.cfg, queue)})).Handle(t.Context(), ev)
	if err != nil || len(resp.BatchItemFailures) != 1 {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if resp.BatchItemFailures[0].ItemIdentifier != ev.Records[0].Change.SequenceNumber {
		t.Fatalf("failure=%q want sequence %q", resp.BatchItemFailures[0].ItemIdentifier, ev.Records[0].Change.SequenceNumber)
	}
}

func TestRelayHandleSkipsRemovalsAndUnreadableRecords(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	resp, err := ddb.NewRelay(batch.New(batch.Deps{})).Handle(t.Context(), events.DynamoDBEvent{
		Records: []events.DynamoDBEventRecord{
			{EventID: "r1", EventName: "REMOVE"},
			{EventID: "r2", EventName: "INSERT", Change: events.DynamoDBStreamRecord{
				NewImage: map[string]events.DynamoDBAttributeValue{"batch_id": events.NewStringAttribute("b1")},
			}},
		},
	})
	if err != nil || len(resp.BatchItemFailures) != 0 {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	logs := buf.String()
	if !strings.Contains(logs, `"msg":"stream_record_skipped"`) || !strings.Contains(logs, `"event_id":"r2"`) {
		t.Fatalf("logs=%s", logs)
	}
}
