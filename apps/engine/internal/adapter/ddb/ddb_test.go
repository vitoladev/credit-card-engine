package ddb_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"engine/internal/adapter/ddb"
	"engine/internal/batch"
	"engine/internal/domain"
	"engine/internal/evaluate"
	"engine/internal/flocitest"
)

func newStore(t *testing.T) (*ddb.Store, *flocitest.Faults) {
	t.Helper()
	cfg, faults := flocitest.Config(t)
	return ddb.New(cfg, flocitest.Table(t, cfg)), faults
}

type counters struct{ queued, decided, failed, cancelled int }

// all reads every item of the batch and tallies them by status.
func all(t *testing.T, st *ddb.Store, batchID string) ([]batch.Item, counters) {
	t.Helper()
	items, err := st.All(t.Context(), batchID)
	if err != nil {
		t.Fatal(err)
	}
	var c counters
	for _, it := range items {
		switch it.Status {
		case batch.Queued:
			c.queued++
		case batch.Decided:
			c.decided++
		case batch.Failed:
			c.failed++
		case batch.Cancelled:
			c.cancelled++
		}
	}
	return items, c
}

func TestDecisionRoundTrip(t *testing.T) {
	st, _ := newStore(t)
	r := domain.Result{Name: "Ana", CPFMasked: "***05", Decision: domain.Approved, RevolvingAmountCents: 250_000, Reasons: []string{"eligible"}}
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
	st, _ := newStore(t)
	// 1000 items of ~2 KB push the Query past one 1 MB page, and the write
	// past one BatchWriteItem chunk.
	customers := make([]domain.Customer, 1000)
	for i := range customers {
		customers[i] = domain.Customer{Name: strings.Repeat("n", 2000), CPF: "39053344705"}
	}
	if err := st.Create(t.Context(), "b1", customers); err != nil {
		t.Fatal(err)
	}
	items, c := all(t, st, "b1")
	if c != (counters{queued: 1000}) || len(items) != 1000 {
		t.Fatalf("counters=%+v items=%d", c, len(items))
	}
	for i, it := range items {
		if it.Index != i || it.Status != batch.Queued || it.Attempts != 1 || it.Customer.CPF != "39053344705" {
			t.Fatalf("item %d: index=%d status=%s attempts=%d", i, it.Index, it.Status, it.Attempts)
		}
	}

	r := domain.Result{Decision: domain.Approved, RevolvingAmountCents: 100, Reasons: []string{"eligible"}}
	if err := st.Decide(t.Context(), "b1", 7, 1, r); err != nil {
		t.Fatal(err)
	}
	if err := st.Decide(t.Context(), "b1", 7, 1, r); !errors.Is(err, batch.ErrInvalidTransition) {
		t.Fatalf("repeat decide err=%v", err)
	}
	if err := st.Decide(t.Context(), "b1", 8, 2, r); !errors.Is(err, batch.ErrInvalidTransition) {
		t.Fatalf("wrong attempt err=%v", err)
	}
	if err := st.Decide(t.Context(), "b1", 5000, 1, r); !errors.Is(err, batch.ErrNotFound) {
		t.Fatalf("missing item err=%v", err)
	}

	items, c = all(t, st, "b1")
	if c != (counters{queued: 999, decided: 1}) {
		t.Fatalf("counters=%+v", c)
	}
	if it := items[7]; it.Status != batch.Decided || it.Result.RevolvingAmountCents != 100 {
		t.Fatalf("%+v", it.Status)
	}
	if _, err := st.All(t.Context(), "missing"); !errors.Is(err, batch.ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestFailRetryCancelTransitions(t *testing.T) {
	st, _ := newStore(t)
	ctx := t.Context()
	customers := []domain.Customer{{Name: "Ana", CPF: "39053344705"}, {Name: "Bruno", CPF: "12345678909"}, {Name: "Carla", CPF: "98765432100"}}
	if err := st.Create(ctx, "b1", customers); err != nil {
		t.Fatal(err)
	}
	wantErr := func(name string, err, want error) {
		t.Helper()
		if !errors.Is(err, want) || (want == nil && err != nil) {
			t.Fatalf("%s: err=%v want %v", name, err, want)
		}
	}
	wantCounters := func(want counters) {
		t.Helper()
		if _, c := all(t, st, "b1"); c != want {
			t.Fatalf("counters=%+v want %+v", c, want)
		}
	}

	wantErr("fail wrong attempt", st.Fail(ctx, "b1", 0, 2), batch.ErrInvalidTransition)
	wantErr("fail missing", st.Fail(ctx, "b1", 9, 1), batch.ErrNotFound)
	wantErr("fail 0", st.Fail(ctx, "b1", 0, 1), nil)
	wantErr("fail 0 again", st.Fail(ctx, "b1", 0, 1), batch.ErrInvalidTransition)
	wantErr("fail 1", st.Fail(ctx, "b1", 1, 1), nil)
	wantErr("decide failed item", st.Decide(ctx, "b1", 0, 1, domain.Result{}), batch.ErrInvalidTransition)
	wantCounters(counters{queued: 1, failed: 2})

	failed, err := st.Failed(ctx, "b1")
	if err != nil || len(failed) != 2 || failed[0].Index != 0 || failed[1].Index != 1 || failed[1].Customer.CPF != "12345678909" {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	if _, err := st.Failed(ctx, "missing"); !errors.Is(err, batch.ErrNotFound) {
		t.Fatalf("failed of missing batch: err=%v", err)
	}

	// Two operators retry the same attempt: only one wins.
	wantErr("retry 0", st.Retry(ctx, "b1", 0, 1), nil)
	wantErr("retry 0 again", st.Retry(ctx, "b1", 0, 1), batch.ErrInvalidTransition)
	wantErr("retry queued", st.Retry(ctx, "b1", 2, 1), batch.ErrInvalidTransition)
	wantErr("retry missing", st.Retry(ctx, "b1", 9, 1), batch.ErrNotFound)
	it, err := st.Item(ctx, "b1", 0)
	if err != nil || it.Status != batch.Queued || it.Attempts != 2 || it.Customer.Name != "Ana" {
		t.Fatalf("item=%+v err=%v", it, err)
	}
	if _, err := st.Item(ctx, "b1", 9); !errors.Is(err, batch.ErrNotFound) {
		t.Fatalf("item missing: err=%v", err)
	}
	wantCounters(counters{queued: 2, failed: 1})

	for attempt := 2; attempt < batch.MaxAttempts; attempt++ {
		wantErr("fail", st.Fail(ctx, "b1", 0, attempt), nil)
		wantErr("retry", st.Retry(ctx, "b1", 0, attempt), nil)
	}
	wantErr("fail at max", st.Fail(ctx, "b1", 0, batch.MaxAttempts), nil)
	wantErr("retry at max", st.Retry(ctx, "b1", 0, batch.MaxAttempts), batch.ErrMaxAttempts)

	wantErr("cancel 1", st.Cancel(ctx, "b1", 1), nil)
	wantErr("cancel 1 again", st.Cancel(ctx, "b1", 1), nil)
	wantErr("cancel queued", st.Cancel(ctx, "b1", 2), batch.ErrInvalidTransition)
	wantErr("cancel missing", st.Cancel(ctx, "b1", 9), batch.ErrNotFound)
	wantErr("retry cancelled", st.Retry(ctx, "b1", 1, 1), batch.ErrInvalidTransition)
	wantCounters(counters{queued: 1, failed: 1, cancelled: 1})
}

func TestRetryManyMovesFailedItems(t *testing.T) {
	st, _ := newStore(t)
	ctx := t.Context()
	customers := []domain.Customer{{Name: "Ana", CPF: "39053344705"}, {Name: "Bruno", CPF: "12345678909"}, {Name: "Carla", CPF: "98765432100"}}
	if err := st.Create(ctx, "b1", customers); err != nil {
		t.Fatal(err)
	}
	if err := st.Fail(ctx, "b1", 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.Fail(ctx, "b1", 1, 1); err != nil {
		t.Fatal(err)
	}
	failed, err := st.Failed(ctx, "b1")
	if err != nil || len(failed) != 2 {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	queued, err := st.RetryMany(ctx, "b1", append(failed, batch.Item{Index: 2, Attempts: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 2 || queued[0].Index != 0 || queued[1].Index != 1 {
		t.Fatalf("queued=%+v", queued)
	}
	items, c := all(t, st, "b1")
	if c != (counters{queued: 3}) {
		t.Fatalf("counters=%+v", c)
	}
	if items[0].Attempts != 2 || items[1].Attempts != 2 || items[2].Attempts != 1 {
		t.Fatalf("attempts=%d,%d,%d", items[0].Attempts, items[1].Attempts, items[2].Attempts)
	}
}

func TestRetryManyMovesManyItems(t *testing.T) {
	st, _ := newStore(t)
	ctx := t.Context()
	n := 51
	customers := make([]domain.Customer, n)
	for i := range customers {
		customers[i] = domain.Customer{Name: "Ana", CPF: "39053344705"}
	}
	if err := st.Create(ctx, "b1", customers); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		if err := st.Fail(ctx, "b1", i, 1); err != nil {
			t.Fatal(err)
		}
	}
	failed, err := st.Failed(ctx, "b1")
	if err != nil || len(failed) != n {
		t.Fatalf("failed=%d err=%v", len(failed), err)
	}
	queued, err := st.RetryMany(ctx, "b1", failed)
	if err != nil || len(queued) != n {
		t.Fatalf("queued=%d err=%v", len(queued), err)
	}
	if _, c := all(t, st, "b1"); c != (counters{queued: n}) {
		t.Fatalf("counters=%+v", c)
	}
}

// Workers decide items of one batch in parallel. Each transition writes only
// its own item, so none of them conflicts with another.
func TestConcurrentDecidesOnOneBatch(t *testing.T) {
	st, _ := newStore(t)
	ctx := t.Context()
	n := 200
	customers := make([]domain.Customer, n)
	for i := range customers {
		customers[i] = domain.Customer{Name: "Ana", CPF: "39053344705"}
	}
	if err := st.Create(ctx, "b1", customers); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() { errs[i] = st.Decide(ctx, "b1", i, 1, domain.Result{Decision: domain.Approved}) })
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	if _, c := all(t, st, "b1"); c != (counters{decided: n}) {
		t.Fatalf("counters=%+v", c)
	}
}

func TestCreateRemovesPartialRowsOnWriteFailure(t *testing.T) {
	st, faults := newStore(t)
	faults.FailCalls("BatchWriteItem", 1)
	customers := make([]domain.Customer, 30)
	for i := range customers {
		customers[i] = domain.Customer{Name: "Ana", CPF: "39053344705"}
	}
	if err := st.Create(t.Context(), "b1", customers); err == nil {
		t.Fatal("expected write failure")
	}
	if _, err := st.All(t.Context(), "b1"); !errors.Is(err, batch.ErrNotFound) {
		t.Fatalf("leftover batch: err=%v", err)
	}
}
