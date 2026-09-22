package batch_test

import (
	"cmp"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
	batchID string
	index   int
	attempt int
	result  domain.Result
}

// recorder is the test Emitter.
type recorder struct {
	mu      sync.Mutex
	decided []itemEvent
	failed  []itemEvent
}

func (r *recorder) ItemDecided(batchID string, index int, res domain.Result, _ time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decided = append(r.decided, itemEvent{batchID: batchID, index: index, result: res})
}

func (r *recorder) ItemFailed(batchID string, index, attempt int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed = append(r.failed, itemEvent{batchID: batchID, index: index, attempt: attempt})
}

// harness is the batch module over DynamoDB and SQS on Floci. Tests read the
// queue themselves and hand each attempt to Process or DeadLetter, the way the
// worker and DLQ consumer would.
type harness struct {
	t      *testing.T
	b      batch.Module
	cfg    aws.Config
	faults *flocitest.Faults
	table  string
	queue  string
	emit   *recorder
}

func newHarness(t *testing.T) *harness {
	return newHarnessWithBatchSize(t, batch.DefaultBatchSize)
}

func newHarnessWithBatchSize(t *testing.T, size int) *harness {
	t.Helper()
	cfg, faults := flocitest.Config(t)
	h := &harness{t: t, cfg: cfg, faults: faults, table: flocitest.Table(t, cfg), queue: flocitest.Queue(t, cfg), emit: &recorder{}}
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

// take receives every published attempt, ordered by index.
func (h *harness) take() []batch.Attempt {
	h.t.Helper()
	attempts := flocitest.Decode[batch.Attempt](h.t, flocitest.Receive(h.t, h.cfg, h.queue))
	slices.SortFunc(attempts, func(x, y batch.Attempt) int { return cmp.Compare(x.Index, y.Index) })
	return attempts
}

func (h *harness) process(attempts ...batch.Attempt) {
	h.t.Helper()
	for _, a := range attempts {
		if err := h.b.Process(h.t.Context(), a); err != nil {
			h.t.Fatalf("process %+v: %v", a, err)
		}
	}
}

func (h *harness) drain() { h.t.Helper(); h.process(h.take()...) }

// failThroughDLQ makes the store fail on every one of the maxReceiveCount=3
// deliveries of the attempt, then dead-letters it, as SQS would.
func (h *harness) failThroughDLQ(a batch.Attempt) {
	h.t.Helper()
	h.faults.FailCalls("UpdateItem", 3)
	for range 3 {
		if err := h.b.Process(h.t.Context(), a); !errors.Is(err, flocitest.ErrInjected) {
			h.t.Fatalf("process on a store failure: err=%v", err)
		}
	}
	if err := h.b.DeadLetter(h.t.Context(), a); err != nil {
		h.t.Fatal(err)
	}
}

// failedBatch submits threeCustomers, fails item 0 through the DLQ, and
// decides the other two.
func (h *harness) failedBatch() (string, batch.Attempt) {
	h.t.Helper()
	acc := h.submit(threeCustomers)
	attempts := h.take()
	h.failThroughDLQ(attempts[0])
	h.process(attempts[1:]...)
	return acc.BatchID, attempts[0]
}

func (h *harness) report(batchID string) batch.Report {
	h.t.Helper()
	r, err := h.b.Report(h.t.Context(), batchID)
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

func assertNoFullCPF(t *testing.T, r batch.Report) {
	t.Helper()
	body, err := json.Marshal(r)
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
	if acc.Queued != 3 {
		t.Fatalf("%+v", acc)
	}
	attempts := h.take()
	if len(attempts) != 3 || attempts[0].Number != 1 || attempts[0].BatchID != acc.BatchID || attempts[0].Customer.CPF != "39053344705" {
		t.Fatalf("%+v", attempts)
	}
	h.process(attempts...)

	got := h.report(acc.BatchID)
	if got.BatchID != acc.BatchID || got.Status != batch.Completed || got.Counters != (batch.Counters{Decided: 3}) {
		t.Fatalf("%+v", got)
	}
	if len(got.Approved) != 2 || len(got.Denied) != 1 || len(got.Failed) != 0 || len(got.Cancelled) != 0 || got.TotalRevolvingAmountCents != 1_050_000 {
		t.Fatalf("%+v", got)
	}
	want := batch.Entry{Index: 0, Name: "Ana", CPFMasked: "***05", Decision: domain.Approved, Reasons: []string{"eligible"}, RevolvingAmountCents: 250_000, Attempts: 1}
	if !reflect.DeepEqual(got.Approved[0], want) {
		t.Fatalf("approved[0]=%+v", got.Approved[0])
	}
	if d := got.Denied[0]; d.Index != 1 || d.Reasons[0] != "score_below_600" || d.RevolvingAmountCents != 0 {
		t.Fatalf("denied[0]=%+v", d)
	}
	assertNoFullCPF(t, got)
	if len(h.emit.decided) != 3 || len(h.emit.failed) != 0 {
		t.Fatalf("emitted %+v", h.emit)
	}
}

func TestReportBeforeProcessingIsProcessing(t *testing.T) {
	h := newHarness(t)
	acc := h.submit(threeCustomers)
	got := h.report(acc.BatchID)
	if got.Status != batch.Processing || got.Counters != (batch.Counters{Queued: 3}) || got.TotalRevolvingAmountCents != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestRedeliveryLeavesReportUnchangedAndEmitsOnce(t *testing.T) {
	h := newHarness(t)
	acc := h.submit(threeCustomers)
	attempts := h.take()
	h.process(attempts...)
	first := h.report(acc.BatchID)

	h.process(attempts...)
	if second := h.report(acc.BatchID); !reflect.DeepEqual(first, second) {
		t.Fatalf("report changed on redelivery:\n%+v\n%+v", first, second)
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
	id, _ := h.failedBatch()

	got := h.report(id)
	if got.Status != batch.NeedsAttention || got.Counters != (batch.Counters{Decided: 2, Failed: 1}) {
		t.Fatalf("%+v", got)
	}
	if f := got.Failed[0]; f.Index != 0 || f.CPFMasked != "***05" || f.Attempts != 1 || f.Decision != "" {
		t.Fatalf("failed[0]=%+v", f)
	}
	if len(h.emit.failed) != 1 || !reflect.DeepEqual(h.emit.failed[0], itemEvent{batchID: id, index: 0, attempt: 1}) {
		t.Fatalf("failed events=%+v", h.emit.failed)
	}

	attempt, err := h.b.Retry(t.Context(), id, 0)
	if err != nil || attempt != 2 {
		t.Fatalf("attempt=%d err=%v", attempt, err)
	}
	h.drain()

	got = h.report(id)
	if got.Status != batch.Completed || got.Counters != (batch.Counters{Decided: 3}) || got.TotalRevolvingAmountCents != 1_050_000 {
		t.Fatalf("%+v", got)
	}
	if a := got.Approved[0]; a.Index != 0 || a.Attempts != 2 {
		t.Fatalf("approved[0]=%+v", a)
	}
	assertNoFullCPF(t, got)
}

func TestRetryAtMaxAttemptsIsRefused(t *testing.T) {
	h := newHarness(t)
	id, first := h.failedBatch()
	for attempt := 2; attempt <= batch.MaxAttempts; attempt++ {
		if _, err := h.b.Retry(t.Context(), id, 0); err != nil {
			t.Fatal(err)
		}
		attempts := h.take()
		if len(attempts) != 1 || attempts[0].Number != attempt || attempts[0].Customer.CPF != first.Customer.CPF {
			t.Fatalf("attempt %d: %+v", attempt, attempts)
		}
		h.failThroughDLQ(attempts[0])
	}

	if got := h.report(id); got.Status != batch.NeedsAttention || got.Failed[0].Attempts != batch.MaxAttempts {
		t.Fatalf("%+v", got)
	}
	if _, err := h.b.Retry(t.Context(), id, 0); !errors.Is(err, batch.ErrMaxAttempts) {
		t.Fatalf("err=%v", err)
	}
	if n, err := h.b.RetryFailed(t.Context(), id); n != 0 || err != nil {
		t.Fatalf("requeued=%d err=%v", n, err)
	}
	if got := h.take(); len(got) != 0 {
		t.Fatalf("published %+v", got)
	}
}

func TestCancelFailedItemIsIdempotentAndCompletesTheBatch(t *testing.T) {
	h := newHarness(t)
	id, _ := h.failedBatch()
	for range 2 {
		if err := h.b.Cancel(t.Context(), id, 0); err != nil {
			t.Fatal(err)
		}
	}
	got := h.report(id)
	if got.Status != batch.Completed || got.Counters != (batch.Counters{Decided: 2, Cancelled: 1}) {
		t.Fatalf("%+v", got)
	}
	if len(got.Cancelled) != 1 || got.Cancelled[0].Index != 0 || got.Cancelled[0].CPFMasked != "***05" {
		t.Fatalf("cancelled=%+v", got.Cancelled)
	}
	if _, err := h.b.Retry(t.Context(), id, 0); !errors.Is(err, batch.ErrInvalidTransition) {
		t.Fatalf("retry cancelled: err=%v", err)
	}
}

func TestRetryOrCancelDecidedOrQueuedItemConflicts(t *testing.T) {
	h := newHarness(t)
	id, _ := h.failedBatch()
	if _, err := h.b.Retry(t.Context(), id, 1); !errors.Is(err, batch.ErrInvalidTransition) {
		t.Fatalf("retry decided: err=%v", err)
	}
	if err := h.b.Cancel(t.Context(), id, 1); !errors.Is(err, batch.ErrInvalidTransition) {
		t.Fatalf("cancel decided: err=%v", err)
	}

	// A second operator's retry of the same failed item loses the race.
	if _, err := h.b.Retry(t.Context(), id, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := h.b.Retry(t.Context(), id, 0); !errors.Is(err, batch.ErrInvalidTransition) {
		t.Fatalf("second retry: err=%v", err)
	}
	if err := h.b.Cancel(t.Context(), id, 0); !errors.Is(err, batch.ErrInvalidTransition) {
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
	for name, err := range map[string]error{
		"retry unknown batch":        second(h.b.Retry(ctx, "nope", 0)),
		"cancel unknown batch":       h.b.Cancel(ctx, "nope", 0),
		"retry unknown index":        second(h.b.Retry(ctx, id, 3)),
		"cancel unknown index":       h.b.Cancel(ctx, id, 3),
		"retry-failed unknown batch": second(h.b.RetryFailed(ctx, "nope")),
		"report unknown batch":       second(h.b.Report(ctx, "nope")),
	} {
		if !errors.Is(err, batch.ErrNotFound) {
			t.Errorf("%s: err=%v", name, err)
		}
	}
}

func second[T any](_ T, err error) error { return err }

func TestPublishFailuresOnSubmitAreFailedThenRetriedTogether(t *testing.T) {
	h := newHarness(t)
	h.faults.DropEntries(2)
	acc := h.submit(threeCustomers)
	if acc.Queued != 1 {
		t.Fatalf("%+v", acc)
	}
	if got := h.report(acc.BatchID); got.Counters != (batch.Counters{Queued: 1, Failed: 2}) {
		t.Fatalf("every queued item must have a message: %+v", got.Counters)
	}
	if len(h.emit.failed) != 2 || h.emit.failed[0].attempt != 1 {
		t.Fatalf("failed events=%+v", h.emit.failed)
	}
	h.drain()
	got := h.report(acc.BatchID)
	if got.Status != batch.NeedsAttention || len(got.Failed) != 2 || got.Failed[0].Index != 0 || got.Failed[1].Index != 1 {
		t.Fatalf("%+v", got)
	}

	if n, err := h.b.RetryFailed(t.Context(), acc.BatchID); n != 2 || err != nil {
		t.Fatalf("requeued=%d err=%v", n, err)
	}
	h.drain()
	got = h.report(acc.BatchID)
	if got.Status != batch.Completed || got.Counters != (batch.Counters{Decided: 3}) || got.Approved[0].Attempts != 2 {
		t.Fatalf("%+v", got)
	}
}

func TestRetryFailedCountsOnlyPublishedItems(t *testing.T) {
	h := newHarness(t)
	h.faults.DropEntries(3)
	acc := h.submit(threeCustomers)
	if acc.Queued != 0 {
		t.Fatalf("%+v", acc)
	}

	h.faults.DropEntries(1)
	if n, err := h.b.RetryFailed(t.Context(), acc.BatchID); n != 2 || err != nil {
		t.Fatalf("requeued=%d err=%v", n, err)
	}
	got := h.report(acc.BatchID)
	if got.Counters != (batch.Counters{Queued: 2, Failed: 1}) || len(h.take()) != 2 {
		t.Fatalf("%+v", got.Counters)
	}
	if f := got.Failed[0]; f.Index != 0 || f.Attempts != 2 {
		t.Fatalf("failed=%+v", got.Failed)
	}
}

func TestRetryFailedReportsAStoreErrorAndPublishesWhatMoved(t *testing.T) {
	h := newHarness(t)
	h.faults.DropEntries(3)
	acc := h.submit(threeCustomers)

	h.faults.FailCalls("UpdateItem", 1)
	n, err := h.b.RetryFailed(t.Context(), acc.BatchID)
	if !errors.Is(err, flocitest.ErrInjected) {
		t.Fatalf("err=%v", err)
	}
	got := h.report(acc.BatchID)
	if published := len(h.take()); got.Counters.Queued != n || published != n || n > 2 {
		t.Fatalf("requeued=%d published=%d counters=%+v", n, published, got.Counters)
	}
}

func TestRetryPublishFailureFailsTheItemAgain(t *testing.T) {
	h := newHarness(t)
	id, _ := h.failedBatch()

	h.faults.DropEntries(1)
	if _, err := h.b.Retry(t.Context(), id, 0); !errors.Is(err, batch.ErrEnqueueFailed) {
		t.Fatalf("err=%v", err)
	}
	got := h.report(id)
	if got.Status != batch.NeedsAttention || got.Counters != (batch.Counters{Decided: 2, Failed: 1}) || got.Failed[0].Attempts != 2 {
		t.Fatalf("%+v", got)
	}
	if len(h.take()) != 0 {
		t.Fatal("published a retry that failed")
	}
	if len(h.emit.failed) != 2 || h.emit.failed[1].attempt != 2 {
		t.Fatalf("failed events=%+v", h.emit.failed)
	}
}

// An attempt whose item does not exist can never succeed. Process keeps
// failing it so it reaches the DLQ, and DeadLetter acknowledges it instead of
// failing it forever.
func TestAttemptForAMissingItemIsDroppedByDeadLetter(t *testing.T) {
	h := newHarness(t)
	id := h.submit(threeCustomers).BatchID
	attempts := h.take()
	for _, a := range []batch.Attempt{
		{BatchID: "nope", Index: 0, Number: 1, Customer: attempts[0].Customer},
		{BatchID: id, Index: 99, Number: 1, Customer: attempts[0].Customer},
	} {
		if err := h.b.Process(t.Context(), a); !errors.Is(err, batch.ErrNotFound) {
			t.Fatalf("process %+v: err=%v", a, err)
		}
		if err := h.b.DeadLetter(t.Context(), a); err != nil {
			t.Fatalf("dead letter %+v: err=%v", a, err)
		}
	}
	if err := h.b.Process(t.Context(), batch.Attempt{}); err == nil {
		t.Fatal("processed an attempt with no batch")
	}
	if err := h.b.DeadLetter(t.Context(), batch.Attempt{}); err != nil {
		t.Fatalf("dead letter with no batch: err=%v", err)
	}
	if got := h.report(id); got.Counters != (batch.Counters{Queued: 3}) || len(h.emit.failed) != 0 {
		t.Fatalf("%+v failed=%+v", got.Counters, h.emit.failed)
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
	if got := h.report(id); got.Counters != (batch.Counters{Queued: 1, Decided: 1, Failed: 1}) {
		t.Fatalf("%+v", got.Counters)
	}
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
