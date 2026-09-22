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
	got, err := submit.New(mem, q).Execute(t.Context(), []domain.Customer{
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
	_, err := submit.New(mem, q).Execute(t.Context(), make([]domain.Customer, submit.MaxCustomers+1))
	if !errors.Is(err, submit.ErrBatchTooLarge) {
		t.Fatalf("err=%v", err)
	}
	if !mem.Empty() || len(q.Jobs) != 0 {
		t.Fatal("stored or queued a rejected batch")
	}
}
