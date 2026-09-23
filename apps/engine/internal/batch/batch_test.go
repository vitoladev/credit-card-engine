package batch_test

import (
	"encoding/json"
	"errors"
	"reflect"
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
	"engine/internal/flocitest"
	"engine/internal/rules"
)

// Ana and Carla are approved (250_000 and 800_000), Bruno is denied.
var threeCustomers = []domain.Customer{
	{Name: "Ana", CPF: "39053344705", CreditScore: 780, CurrentInvoiceCents: 50_000, CreditLimitCents: 500_000, MonthlySpendCents: []int64{80_000, 90_000, 70_000}},
	{Name: "Bruno", CPF: "12345678909", CreditScore: 520, CurrentInvoiceCents: 200_000, CreditLimitCents: 400_000, MonthlySpendCents: []int64{80_000}},
	{Name: "Carla", CPF: "98765432100", CreditScore: 820, CurrentInvoiceCents: 100_000, CreditLimitCents: 1_000_000, MonthlySpendCents: []int64{100_000}},
}

type itemEvent struct {
	batchID     string
	itemID      string
	attempt     int
	result      domain.Result
	sinceQueued time.Duration
}

// recorder is the test Emitter.
type recorder struct {
	mu      sync.Mutex
	decided []itemEvent
	failed  []itemEvent
}

func (r *recorder) ItemDecided(batchID, itemID string, res domain.Result, _, sinceQueued time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decided = append(r.decided, itemEvent{batchID: batchID, itemID: itemID, result: res, sinceQueued: sinceQueued})
}

func (r *recorder) ItemFailed(batchID, itemID string, attempt int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed = append(r.failed, itemEvent{batchID: batchID, itemID: itemID, attempt: attempt})
}

// maxReceiveCount is the queue's redrive setting in the stack (ADR 0001).
const maxReceiveCount = 5

// harness is the batch module over DynamoDB, its stream, and SQS on Floci.
// Tests move the stream into the relay and the queue into Process or
// DeadLetter themselves, the way the event source mappings would.
type harness struct {
	t      *testing.T
	b      batch.Module
	cfg    aws.Config
	faults *flocitest.Faults
	table  string
	queue  string
	stream *flocitest.Stream
	emit   *recorder
}

func newHarness(t *testing.T) *harness {
	return newHarnessWithBatchSize(t, batch.DefaultBatchSize)
}

func newHarnessWithBatchSize(t *testing.T, size int) *harness {
	t.Helper()
	cfg, faults := flocitest.Config(t)
	h := &harness{t: t, cfg: cfg, faults: faults, table: flocitest.Table(t, cfg), queue: flocitest.Queue(t, cfg), emit: &recorder{}}
	h.stream = flocitest.TableStream(t, cfg, h.table)
	h.b = batch.New(batch.Deps{
		Items:        ddb.New(cfg, h.table),
		Publisher:    sqspub.New(cfg, h.queue),
		Policy:       rules.NewPolicy(),
		Emitter:      h.emit,
		MaxCustomers: size,
	})
	return h
}

func (h *harness) submit(customers []domain.Customer) batch.Accepted {
	h.t.Helper()
	acc, err := h.b.Submit(h.t.Context(), customers)
	if err != nil {
		h.t.Fatal(err)
	}
	return acc
}

// relay hands the stream records written since the last call to the relay,
// as its event source mapping would, and returns its response.
func (h *harness) relay() (events.DynamoDBEvent, events.DynamoDBEventResponse) {
	h.t.Helper()
	ev := h.stream.Next()
	resp, err := ddb.NewRelay(h.b).Handle(h.t.Context(), ev)
	if err != nil {
		h.t.Fatal(err)
	}
	return ev, resp
}

// take relays the stream and receives every queued event, in submission
// order: item IDs are version 7 UUIDs, which sort in the order they were made.
func (h *harness) take() []batch.ItemEvent {
	h.t.Helper()
	if _, resp := h.relay(); len(resp.BatchItemFailures) != 0 {
		h.t.Fatalf("relay failures=%+v", resp.BatchItemFailures)
	}
	return h.receive()
}

func (h *harness) receive() []batch.ItemEvent {
	h.t.Helper()
	msgs := flocitest.Receive(h.t, h.cfg, h.queue)
	for _, m := range msgs {
		for _, c := range threeCustomers {
			if strings.Contains(m.Body, c.CPF) || strings.Contains(m.Body, c.Name) {
				h.t.Fatalf("queue message carries customer data: %s", m.Body)
			}
		}
	}
	got := flocitest.Decode[batch.ItemEvent](h.t, msgs)
	slices.SortFunc(got, func(x, y batch.ItemEvent) int { return strings.Compare(x.ItemID, y.ItemID) })
	return got
}

func (h *harness) process(itemEvents ...batch.ItemEvent) {
	h.t.Helper()
	for _, e := range itemEvents {
		if err := h.b.Process(h.t.Context(), e); err != nil {
			h.t.Fatalf("process %+v: %v", e, err)
		}
	}
}

// deadLetter fails the items of the events, as the DLQ consumer would once each
// exhausted its deliveries.
func (h *harness) deadLetter(itemEvents ...batch.ItemEvent) {
	h.t.Helper()
	for _, e := range itemEvents {
		if err := h.b.DeadLetter(h.t.Context(), e); err != nil {
			h.t.Fatal(err)
		}
	}
}

func (h *harness) drain() { h.t.Helper(); h.process(h.take()...) }

// failThroughDLQ makes the store fail on every one of the maxReceiveCount=5
// deliveries of the event, then dead-letters it, as SQS would.
func (h *harness) failThroughDLQ(a batch.ItemEvent) {
	h.t.Helper()
	h.faults.FailCalls("UpdateItem", maxReceiveCount)
	for range maxReceiveCount {
		if err := h.b.Process(h.t.Context(), a); !errors.Is(err, flocitest.ErrInjected) {
			h.t.Fatalf("process on a store failure: err=%v", err)
		}
	}
	if err := h.b.DeadLetter(h.t.Context(), a); err != nil {
		h.t.Fatal(err)
	}
}

// failedBatch submits threeCustomers, fails the first item through the DLQ,
// and decides the other two.
func (h *harness) failedBatch() (batch.Accepted, batch.ItemEvent) {
	h.t.Helper()
	acc := h.submit(threeCustomers)
	attempts := h.take()
	h.failThroughDLQ(attempts[0])
	h.process(attempts[1:]...)
	return acc, attempts[0]
}

// list reads every item of the batch, following the cursor across pages of
// limit items.
func (h *harness) list(batchID string, q batch.ListQuery) []batch.Entry {
	h.t.Helper()
	var all []batch.Entry
	for {
		page, err := h.b.ListItems(h.t.Context(), batchID, q)
		if err != nil {
			h.t.Fatal(err)
		}
		if page.BatchID != batchID {
			h.t.Fatalf("batch_id=%q", page.BatchID)
		}
		all = append(all, page.Items...)
		if page.NextCursor == "" {
			return all
		}
		q.Cursor = page.NextCursor
	}
}

func (h *harness) items(batchID string) []batch.Entry {
	h.t.Helper()
	return h.list(batchID, batch.ListQuery{Limit: batch.DefaultPageLimit})
}

func statuses(entries []batch.Entry) []batch.ItemStatus {
	out := make([]batch.ItemStatus, len(entries))
	for i, e := range entries {
		out[i] = e.Status
	}
	return out
}

func wantStatuses(t *testing.T, entries []batch.Entry, want ...batch.ItemStatus) {
	t.Helper()
	if got := statuses(entries); !slices.Equal(got, want) {
		t.Fatalf("statuses=%v want %v", got, want)
	}
}

func assertNoFullCPF(t *testing.T, v any) {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range threeCustomers {
		if strings.Contains(string(body), c.CPF) {
			t.Fatalf("report leaked full CPF %s: %s", c.CPF, body)
		}
	}
}

func TestSubmittedBatchCompletes(t *testing.T) {
	h := newHarness(t)
	acc := h.submit(threeCustomers)
	if acc.Queued != 3 || len(acc.ItemIDs) != 3 || !slices.IsSorted(acc.ItemIDs) {
		t.Fatalf("%+v", acc)
	}
	attempts := h.take()
	if len(attempts) != 3 || attempts[0] != batch.NewItemEvent(acc.BatchID, acc.ItemIDs[0], 1, batch.Queued) {
		t.Fatalf("%+v", attempts)
	}
	if want := acc.BatchID + ":" + acc.ItemIDs[0] + ":1:QUEUED"; attempts[0].ID != want {
		t.Fatalf("event_id=%s want %s", attempts[0].ID, want)
	}
	for i, a := range attempts {
		if a.ItemID != acc.ItemIDs[i] {
			t.Fatalf("attempt %d item_id=%s want %s", i, a.ItemID, acc.ItemIDs[i])
		}
	}
	h.process(attempts...)

	got := h.items(acc.BatchID)
	wantStatuses(t, got, batch.Approved, batch.Denied, batch.Approved)
	want := batch.Entry{ItemID: acc.ItemIDs[0], Name: "Ana", CPFMasked: "***05", Status: batch.Approved, Reasons: []string{"eligible"}, RevolvingAmountCents: 250_000, Attempts: 1}
	if !reflect.DeepEqual(got[0], want) {
		t.Fatalf("items[0]=%+v", got[0])
	}
	if d := got[1]; d.ItemID != acc.ItemIDs[1] || d.Reasons[0] != "score_below_600" || d.RevolvingAmountCents != 0 {
		t.Fatalf("items[1]=%+v", d)
	}
	if got[2].RevolvingAmountCents != 800_000 {
		t.Fatalf("items[2]=%+v", got[2])
	}
	assertNoFullCPF(t, got)
	if len(h.emit.decided) != 3 || len(h.emit.failed) != 0 {
		t.Fatalf("emitted %+v", h.emit)
	}
	// Queued to decided spans the submit, the relay, and the worker; a whole
	// minute would mean queued_at was not read.
	for _, d := range h.emit.decided {
		if d.sinceQueued <= 0 || d.sinceQueued > time.Minute {
			t.Fatalf("since queued=%v", d.sinceQueued)
		}
	}
}

func TestItemsBeforeProcessingAreQueued(t *testing.T) {
	h := newHarness(t)
	acc := h.submit(threeCustomers)
	got := h.items(acc.BatchID)
	wantStatuses(t, got, batch.Queued, batch.Queued, batch.Queued)
	if got[0].Reasons == nil || len(got[0].Reasons) != 0 || got[0].RevolvingAmountCents != 0 {
		t.Fatalf("items[0]=%+v", got[0])
	}
}

func TestRedeliveryLeavesItemsUnchangedAndEmitsOnce(t *testing.T) {
	h := newHarness(t)
	acc := h.submit(threeCustomers)
	attempts := h.take()
	h.process(attempts...)
	first := h.items(acc.BatchID)

	h.process(attempts...)
	if second := h.items(acc.BatchID); !reflect.DeepEqual(first, second) {
		t.Fatalf("items changed on redelivery:\n%+v\n%+v", first, second)
	}
	if len(h.emit.decided) != 3 {
		t.Fatalf("decision events=%d", len(h.emit.decided))
	}
}

func TestSubmitOverTheLimitStoresAndPublishesNothing(t *testing.T) {
	h := newHarnessWithBatchSize(t, 2)
	if _, err := h.b.Submit(t.Context(), threeCustomers); !errors.Is(err, batch.ErrTooLarge) {
		t.Fatalf("err=%v", err)
	}
	if n := flocitest.Rows(t, h.cfg, h.table); n != 0 || len(h.take()) != 0 {
		t.Fatalf("rows=%d", n)
	}
	if acc := h.submit(threeCustomers[:2]); acc.Queued != 2 {
		t.Fatalf("%+v", acc)
	}
}

func TestSubmitStoreFailurePublishesNothing(t *testing.T) {
	h := newHarness(t)
	h.faults.FailCalls("BatchWriteItem", 1)
	if _, err := h.b.Submit(t.Context(), threeCustomers); !errors.Is(err, batch.ErrNotRecorded) {
		t.Fatalf("err=%v", err)
	}
	if n := flocitest.Rows(t, h.cfg, h.table); n != 0 || len(h.take()) != 0 {
		t.Fatalf("stored or published a batch that was not recorded: rows=%d", n)
	}
}

func TestFailedItemIsRetriedToCompletion(t *testing.T) {
	h := newHarness(t)
	acc, first := h.failedBatch()
	id := acc.BatchID

	got := h.items(id)
	wantStatuses(t, got, batch.Failed, batch.Denied, batch.Approved)
	if f := got[0]; f.ItemID != first.ItemID || f.CPFMasked != "***05" || f.Attempts != 1 || len(f.Reasons) != 0 {
		t.Fatalf("items[0]=%+v", f)
	}
	if len(h.emit.failed) != 1 || !reflect.DeepEqual(h.emit.failed[0], itemEvent{batchID: id, itemID: first.ItemID, attempt: 1}) {
		t.Fatalf("failed events=%+v", h.emit.failed)
	}

	attempt, err := h.b.Retry(t.Context(), id, first.ItemID)
	if err != nil || attempt != 2 {
		t.Fatalf("attempt=%d err=%v", attempt, err)
	}
	h.drain()

	got = h.items(id)
	wantStatuses(t, got, batch.Approved, batch.Denied, batch.Approved)
	if a := got[0]; a.ItemID != first.ItemID || a.Attempts != 2 || a.RevolvingAmountCents != 250_000 {
		t.Fatalf("items[0]=%+v", a)
	}
	assertNoFullCPF(t, got)
}

func TestRetryAtMaxAttemptsIsRefused(t *testing.T) {
	h := newHarness(t)
	acc, first := h.failedBatch()
	id := acc.BatchID
	for attempt := 2; attempt <= batch.MaxAttempts; attempt++ {
		if _, err := h.b.Retry(t.Context(), id, first.ItemID); err != nil {
			t.Fatal(err)
		}
		attempts := h.take()
		if len(attempts) != 1 || attempts[0].Attempt != attempt || attempts[0].ItemID != first.ItemID {
			t.Fatalf("attempt %d: %+v", attempt, attempts)
		}
		h.failThroughDLQ(attempts[0])
	}

	if got := h.items(id); got[0].Status != batch.Failed || got[0].Attempts != batch.MaxAttempts {
		t.Fatalf("%+v", got)
	}
	if _, err := h.b.Retry(t.Context(), id, first.ItemID); !errors.Is(err, batch.ErrMaxAttempts) {
		t.Fatalf("err=%v", err)
	}
	if n, err := h.b.RetryFailed(t.Context(), id); n != 0 || err != nil {
		t.Fatalf("requeued=%d err=%v", n, err)
	}
	if got := h.take(); len(got) != 0 {
		t.Fatalf("published %+v", got)
	}
}

func TestCancelFailedItemIsIdempotent(t *testing.T) {
	h := newHarness(t)
	acc, first := h.failedBatch()
	id := acc.BatchID
	for range 2 {
		if err := h.b.Cancel(t.Context(), id, first.ItemID); err != nil {
			t.Fatal(err)
		}
	}
	got := h.items(id)
	wantStatuses(t, got, batch.Cancelled, batch.Denied, batch.Approved)
	if got[0].ItemID != first.ItemID || got[0].CPFMasked != "***05" {
		t.Fatalf("items[0]=%+v", got[0])
	}
	if _, err := h.b.Retry(t.Context(), id, first.ItemID); !errors.Is(err, batch.ErrInvalidTransition) {
		t.Fatalf("retry cancelled: err=%v", err)
	}
}

func TestRetryOrCancelDecidedOrQueuedItemConflicts(t *testing.T) {
	h := newHarness(t)
	acc, first := h.failedBatch()
	id, decided := acc.BatchID, acc.ItemIDs[1]
	if _, err := h.b.Retry(t.Context(), id, decided); !errors.Is(err, batch.ErrInvalidTransition) {
		t.Fatalf("retry decided: err=%v", err)
	}
	if err := h.b.Cancel(t.Context(), id, decided); !errors.Is(err, batch.ErrInvalidTransition) {
		t.Fatalf("cancel decided: err=%v", err)
	}

	// A second operator's retry of the same failed item loses the race.
	if _, err := h.b.Retry(t.Context(), id, first.ItemID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.b.Retry(t.Context(), id, first.ItemID); !errors.Is(err, batch.ErrInvalidTransition) {
		t.Fatalf("second retry: err=%v", err)
	}
	if err := h.b.Cancel(t.Context(), id, first.ItemID); !errors.Is(err, batch.ErrInvalidTransition) {
		t.Fatalf("cancel queued: err=%v", err)
	}
	if got := h.take(); len(got) != 1 {
		t.Fatalf("published %+v", got)
	}
}

func TestUnknownTargetsAreNotFound(t *testing.T) {
	h := newHarness(t)
	id := h.submit(threeCustomers).BatchID
	ctx := t.Context()
	unknown := uuid.NewV7().String()
	for name, err := range map[string]error{
		"retry unknown batch":            second(h.b.Retry(ctx, "nope", unknown)),
		"cancel unknown batch":           h.b.Cancel(ctx, "nope", unknown),
		"retry unknown item":             second(h.b.Retry(ctx, id, unknown)),
		"cancel unknown item":            h.b.Cancel(ctx, id, unknown),
		"retry-failed unknown batch":     second(h.b.RetryFailed(ctx, "nope")),
		"list unknown batch":             second(h.b.ListItems(ctx, "nope", batch.ListQuery{Limit: 10})),
		"list unknown batch by a status": second(h.b.ListItems(ctx, "nope", batch.ListQuery{Status: batch.Failed, Limit: 10})),
	} {
		if !errors.Is(err, batch.ErrNotFound) {
			t.Errorf("%s: err=%v", name, err)
		}
	}
}

func second[T any](_ T, err error) error { return err }

// A publish that fails in the relay leaves the item queued: the stream
// delivers the record again, and nothing is failed for a transient error.
func TestRelayPublishFailureIsDeliveredAgain(t *testing.T) {
	h := newHarness(t)
	acc := h.submit(threeCustomers)

	h.faults.DropEntries(2)
	ev, resp := h.relay()
	if len(resp.BatchItemFailures) != 2 {
		t.Fatalf("failures=%+v", resp.BatchItemFailures)
	}
	wantStatuses(t, h.items(acc.BatchID), batch.Queued, batch.Queued, batch.Queued)
	if len(h.emit.failed) != 0 || len(h.receive()) != 1 {
		t.Fatalf("failed events=%+v", h.emit.failed)
	}

	// The event source mapping delivers the batch again from the failed records.
	resp, err := ddb.NewRelay(h.b).Handle(t.Context(), ev)
	if err != nil || len(resp.BatchItemFailures) != 0 {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	h.process(h.receive()...)
	wantStatuses(t, h.items(acc.BatchID), batch.Approved, batch.Denied, batch.Approved)
}

// Only a move to QUEUED is work. Decisions, failures, and cancels reach the
// relay too and publish nothing.
func TestRelayPublishesOnlyQueuedItems(t *testing.T) {
	h := newHarness(t)
	acc, first := h.failedBatch()
	if got := h.take(); len(got) != 0 {
		t.Fatalf("published %+v", got)
	}
	if err := h.b.Cancel(t.Context(), acc.BatchID, acc.ItemIDs[1]); !errors.Is(err, batch.ErrInvalidTransition) {
		t.Fatal(err)
	}
	if _, err := h.b.Retry(t.Context(), acc.BatchID, first.ItemID); err != nil {
		t.Fatal(err)
	}
	got := h.take()
	if len(got) != 1 || got[0] != batch.NewItemEvent(acc.BatchID, first.ItemID, 2, batch.Queued) {
		t.Fatalf("published %+v", got)
	}
}

// A late event for an older attempt finds the item queued on a newer one and
// is acknowledged without deciding it.
func TestStaleEventDoesNotDecide(t *testing.T) {
	h := newHarness(t)
	acc, first := h.failedBatch()
	if _, err := h.b.Retry(t.Context(), acc.BatchID, first.ItemID); err != nil {
		t.Fatal(err)
	}
	h.process(first)
	got := h.items(acc.BatchID)
	if got[0].Status != batch.Queued || got[0].Attempts != 2 {
		t.Fatalf("items[0]=%+v", got[0])
	}
	h.drain()
	if got := h.items(acc.BatchID); got[0].Status != batch.Approved || got[0].Attempts != 2 {
		t.Fatalf("items[0]=%+v", got[0])
	}
}

func TestRetryFailedMovesEveryFailedItem(t *testing.T) {
	h := newHarness(t)
	acc := h.submit(threeCustomers)
	h.deadLetter(h.take()...)
	wantStatuses(t, h.items(acc.BatchID), batch.Failed, batch.Failed, batch.Failed)

	if n, err := h.b.RetryFailed(t.Context(), acc.BatchID); n != 3 || err != nil {
		t.Fatalf("requeued=%d err=%v", n, err)
	}
	got := h.take()
	if len(got) != 3 || got[0].Attempt != 2 {
		t.Fatalf("published %+v", got)
	}
	h.process(got...)
	entries := h.items(acc.BatchID)
	wantStatuses(t, entries, batch.Approved, batch.Denied, batch.Approved)
	if entries[0].Attempts != 2 {
		t.Fatalf("%+v", entries)
	}
}

func TestRetryFailedReportsAStoreErrorAndCountsWhatMoved(t *testing.T) {
	h := newHarness(t)
	acc := h.submit(threeCustomers)
	h.deadLetter(h.take()...)

	h.faults.FailCalls("UpdateItem", 1)
	n, err := h.b.RetryFailed(t.Context(), acc.BatchID)
	if !errors.Is(err, flocitest.ErrInjected) {
		t.Fatalf("err=%v", err)
	}
	queued := 0
	for _, s := range statuses(h.items(acc.BatchID)) {
		if s == batch.Queued {
			queued++
		}
	}
	if published := len(h.take()); queued != n || published != n || n > 2 {
		t.Fatalf("requeued=%d published=%d queued=%d", n, published, queued)
	}
}

// An attempt whose item does not exist can never succeed. Process keeps
// failing it so it reaches the DLQ, and DeadLetter acknowledges it instead of
// failing it forever.
func TestAttemptForAMissingItemIsDroppedByDeadLetter(t *testing.T) {
	h := newHarness(t)
	id := h.submit(threeCustomers).BatchID
	attempts := h.take()
	for _, a := range []batch.ItemEvent{
		batch.NewItemEvent("nope", attempts[0].ItemID, 1, batch.Queued),
		batch.NewItemEvent(id, uuid.NewV7().String(), 1, batch.Queued),
	} {
		if err := h.b.Process(t.Context(), a); !errors.Is(err, batch.ErrNotFound) {
			t.Fatalf("process %+v: err=%v", a, err)
		}
		if err := h.b.DeadLetter(t.Context(), a); err != nil {
			t.Fatalf("dead letter %+v: err=%v", a, err)
		}
	}
	if err := h.b.Process(t.Context(), batch.ItemEvent{}); err == nil {
		t.Fatal("processed an attempt with no batch")
	}
	if err := h.b.DeadLetter(t.Context(), batch.ItemEvent{}); err != nil {
		t.Fatalf("dead letter with no batch: err=%v", err)
	}
	wantStatuses(t, h.items(id), batch.Queued, batch.Queued, batch.Queued)
	if len(h.emit.failed) != 0 {
		t.Fatalf("failed=%+v", h.emit.failed)
	}
}

func TestDeadLetterReportsAStoreFailureThenAcknowledges(t *testing.T) {
	h := newHarness(t)
	id := h.submit(threeCustomers).BatchID
	attempts := h.take()

	h.faults.FailCalls("UpdateItem", 1)
	if err := h.b.DeadLetter(t.Context(), attempts[0]); !errors.Is(err, flocitest.ErrInjected) {
		t.Fatalf("err=%v", err)
	}
	if err := h.b.DeadLetter(t.Context(), attempts[0]); err != nil {
		t.Fatalf("second try: err=%v", err)
	}
	// A late DLQ record for an item already failed, or decided, is
	// acknowledged and emits nothing.
	if err := h.b.DeadLetter(t.Context(), attempts[0]); err != nil {
		t.Fatalf("already failed: err=%v", err)
	}
	h.process(attempts[1])
	if err := h.b.DeadLetter(t.Context(), attempts[1]); err != nil {
		t.Fatalf("decided: err=%v", err)
	}
	wantStatuses(t, h.items(id), batch.Failed, batch.Denied, batch.Queued)
	if len(h.emit.failed) != 1 {
		t.Fatalf("failed events=%+v", h.emit.failed)
	}
}

func TestParseBatchSize(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
		ok   bool
	}{
		{"", batch.DefaultBatchSize, true},
		{"1", 1, true},
		{"1000", 1000, true},
		{"0", 0, false},
		{"1001", 0, false},
		{"ten", 0, false},
	} {
		got, err := batch.ParseBatchSize(tc.in)
		if got != tc.want || (err == nil) != tc.ok {
			t.Errorf("ParseBatchSize(%q) = %d, %v", tc.in, got, err)
		}
	}
}

// tenCustomers alternates Ana (approved) and Bruno (denied).
func tenCustomers() []domain.Customer {
	out := make([]domain.Customer, 10)
	for i := range out {
		out[i] = threeCustomers[i%2]
	}
	return out
}

func TestListItemsPagesInSubmissionOrder(t *testing.T) {
	h := newHarness(t)
	acc := h.submit(tenCustomers())
	h.drain()

	var ids []string
	q := batch.ListQuery{Limit: 3}
	for pages := 1; ; pages++ {
		page, err := h.b.ListItems(t.Context(), acc.BatchID, q)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 3 {
			t.Fatalf("page %d has %d items", pages, len(page.Items))
		}
		for _, e := range page.Items {
			ids = append(ids, e.ItemID)
		}
		if page.NextCursor == "" {
			if pages != 4 {
				t.Fatalf("pages=%d", pages)
			}
			break
		}
		q.Cursor = page.NextCursor
	}
	if !slices.Equal(ids, acc.ItemIDs) {
		t.Fatalf("ids=%v\nwant %v", ids, acc.ItemIDs)
	}
}

// Query's Limit counts rows before the status filter. A filtered page is still
// full as long as enough items match.
func TestListItemsByStatusFillsEachPage(t *testing.T) {
	h := newHarness(t)
	acc := h.submit(tenCustomers())
	h.drain()

	page, err := h.b.ListItems(t.Context(), acc.BatchID, batch.ListQuery{Status: batch.Denied, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("%+v", page)
	}
	denied := h.list(acc.BatchID, batch.ListQuery{Status: batch.Denied, Limit: 2})
	var want []string
	for i, id := range acc.ItemIDs {
		if i%2 == 1 {
			want = append(want, id)
		}
	}
	var got []string
	for _, e := range denied {
		if e.Status != batch.Denied {
			t.Fatalf("%+v", e)
		}
		got = append(got, e.ItemID)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("denied=%v\nwant %v", got, want)
	}
}

// A batch with no item in a status lists nothing; it is not an unknown batch.
func TestListItemsWithNoMatchIsEmptyNotNotFound(t *testing.T) {
	h := newHarness(t)
	acc := h.submit(threeCustomers)
	page, err := h.b.ListItems(t.Context(), acc.BatchID, batch.ListQuery{Status: batch.Cancelled, Limit: 10})
	if err != nil || len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	body, err := json.Marshal(page)
	if err != nil || !strings.Contains(string(body), `"items":[]`) {
		t.Fatalf("body=%s err=%v", body, err)
	}
}

func TestListItemsRejectsACursorItDidNotIssue(t *testing.T) {
	h := newHarness(t)
	acc := h.submit(threeCustomers)
	for _, cursor := range []string{"not base64!", "bm90LWEtdXVpZA"} {
		if _, err := h.b.ListItems(t.Context(), acc.BatchID, batch.ListQuery{Cursor: cursor, Limit: 10}); !errors.Is(err, batch.ErrInvalidCursor) {
			t.Fatalf("cursor %q: err=%v", cursor, err)
		}
	}
}

// A store failure while reading the failed items stops RetryFailed before
// any item moves.
func TestRetryFailedStopsOnAFailedRead(t *testing.T) {
	h := newHarness(t)
	acc := h.submit(threeCustomers)
	h.deadLetter(h.take()...)

	h.faults.FailCalls("Query", 1)
	if _, err := h.b.RetryFailed(t.Context(), acc.BatchID); !errors.Is(err, flocitest.ErrInjected) {
		t.Fatalf("err=%v", err)
	}
	wantStatuses(t, h.items(acc.BatchID), batch.Failed, batch.Failed, batch.Failed)
	if len(h.take()) != 0 {
		t.Fatal("published after a failed read")
	}
}
