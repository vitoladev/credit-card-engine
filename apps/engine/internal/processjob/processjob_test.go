package processjob_test

import (
	"encoding/json"
	"testing"

	"engine/internal/domain"
	"engine/internal/evaluate"
	"engine/internal/processjob"
	"engine/internal/queue"
	"engine/internal/rules"
	"engine/internal/store"
)

var ana = domain.Customer{
	Name: "Ana", CPF: "39053344705", CreditScore: 780,
	CurrentInvoiceCents: 50_000, CreditLimitCents: 500_000,
	MonthlySpendCents: []int64{80_000, 90_000, 70_000},
}

func TestExecuteDecidesTheItemOnce(t *testing.T) {
	mem := store.NewMemory()
	if err := mem.Create(t.Context(), "b1", []domain.Customer{ana}); err != nil {
		t.Fatal(err)
	}
	uc := processjob.New(evaluate.New(rules.NewPolicy()), mem)
	body, err := json.Marshal(queue.Job{BatchID: "b1", Index: 0, Attempt: 1, Customer: ana})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := uc.Execute(t.Context(), body); err != nil {
			t.Fatal(err)
		}
	}
	got, err := mem.Report(t.Context(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Counters != (store.Counters{Decided: 1}) {
		t.Fatalf("%+v", got.Counters)
	}
	if got.Items[0].Status != store.Decided || got.Items[0].Result.Decision != domain.Approved {
		t.Fatalf("%+v", got.Items[0])
	}
}

func TestExecuteFailsForAnUnknownItem(t *testing.T) {
	uc := processjob.New(evaluate.New(rules.NewPolicy()), store.NewMemory())
	body, err := json.Marshal(queue.Job{BatchID: "missing", Index: 0, Attempt: 1, Customer: ana})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uc.Execute(t.Context(), body); err == nil {
		t.Fatal("expected error")
	}
}

func TestExecuteRejectsInvalidJSON(t *testing.T) {
	uc := processjob.New(evaluate.New(rules.NewPolicy()), store.NewMemory())
	if _, err := uc.Execute(t.Context(), []byte("{")); err == nil {
		t.Fatal("expected error")
	}
}
