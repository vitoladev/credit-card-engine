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

func TestExecutePersistsApproved(t *testing.T) {
	mem := store.NewMemory()
	uc := processjob.New(evaluate.New(rules.NewChain(), mem))
	body, err := json.Marshal(queue.Job{
		ReportID: "r1",
		Queued:   1,
		Customer: domain.Customer{
			Name: "Ana", CPF: "39053344705", CreditScore: 780,
			CurrentInvoiceCents: 50_000, AvailableLimitCents: 500_000,
			MonthlySpendCents: []int64{80_000, 90_000, 70_000},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := uc.Execute(t.Context(), body); err != nil {
		t.Fatal(err)
	}
	got, err := mem.List(t.Context(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Decision != domain.Approved {
		t.Fatalf("%+v", got)
	}
}

func TestExecuteRejectsInvalidJSON(t *testing.T) {
	uc := processjob.New(evaluate.New(rules.NewChain(), store.NewMemory()))
	if err := uc.Execute(t.Context(), []byte("{")); err == nil {
		t.Fatal("expected error")
	}
}
