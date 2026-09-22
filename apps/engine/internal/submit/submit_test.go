package submit_test

import (
	"errors"
	"testing"

	"engine/internal/domain"
	"engine/internal/queue"
	"engine/internal/store"
	"engine/internal/submit"
)

func TestExecuteStoresThenEnqueuesOneJobPerCustomer(t *testing.T) {
	q := queue.NewMemory()
	mem := store.NewMemory()
	got, err := submit.New(mem, q, submit.DefaultBatchSize).Execute(t.Context(), []domain.Customer{
		{Name: "Ana", CPF: "39053344705", CreditScore: 720},
		{Name: "Bruno", CPF: "12345678909", CreditScore: 400},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Queued != 2 || got.BatchID == "" {
		t.Fatalf("%+v", got)
	}
	if len(q.Jobs) != 2 {
		t.Fatalf("jobs=%d", len(q.Jobs))
	}
	if j := q.Jobs[1]; j.BatchID != got.BatchID || j.Index != 1 || j.Attempt != 1 || j.Customer.Name != "Bruno" {
		t.Fatalf("%+v", q.Jobs)
	}
	b, err := mem.Report(t.Context(), got.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if b.Counters != (store.Counters{Queued: 2}) || len(b.Items) != 2 {
		t.Fatalf("%+v", b)
	}
	for i, it := range b.Items {
		if it.Index != i || it.Status != store.Queued || it.Attempts != 1 {
			t.Fatalf("item %d = %+v", i, it)
		}
	}
	if b.Items[1].Customer.CPF != "12345678909" {
		t.Fatal("the stored item lost the full CPF")
	}
}

func TestExecuteRejectsABatchOverTheLimit(t *testing.T) {
	q := queue.NewMemory()
	mem := store.NewMemory()
	_, err := submit.New(mem, q, 5).Execute(t.Context(), make([]domain.Customer, 6))
	if !errors.Is(err, submit.ErrBatchTooLarge) {
		t.Fatalf("err=%v", err)
	}
	if !mem.Empty() || len(q.Jobs) != 0 {
		t.Fatal("stored or queued a rejected batch")
	}
}

func TestParseBatchSize(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"", 100},
		{"1", 1},
		{"1000", 1000},
	} {
		got, err := submit.ParseBatchSize(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("ParseBatchSize(%q) = %d, %v; want %d", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{"0", "1001", "-5", "abc", "10.5"} {
		if got, err := submit.ParseBatchSize(in); err == nil {
			t.Fatalf("ParseBatchSize(%q) = %d, want an error", in, got)
		}
	}
}
