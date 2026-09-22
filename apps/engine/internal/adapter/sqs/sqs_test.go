package sqs_test

import (
	"encoding/json"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	"engine/internal/adapter/sqs"
	"engine/internal/domain"
	"engine/internal/evaluate"
	"engine/internal/processjob"
	"engine/internal/queue"
	"engine/internal/rules"
	"engine/internal/store"
)

func TestHandleEvaluatesEachRecord(t *testing.T) {
	ana := domain.Customer{
		Name: "Ana", CPF: "39053344705", CreditScore: 780,
		CurrentInvoiceCents: 50_000, CreditLimitCents: 500_000,
		MonthlySpendCents: []int64{80_000, 90_000, 70_000},
	}
	mem := store.NewMemory()
	if err := mem.Create(t.Context(), "b1", []domain.Customer{ana}); err != nil {
		t.Fatal(err)
	}
	h := sqs.New(processjob.New(evaluate.New(rules.NewPolicy()), mem))
	body, err := json.Marshal(queue.Job{BatchID: "b1", Index: 0, Attempt: 1, Customer: ana})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Handle(t.Context(), events.SQSEvent{
		Records: []events.SQSMessage{{Body: string(body)}},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := mem.Report(t.Context(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Items[0].Status != store.Decided || got.Items[0].Result.Decision != domain.Approved {
		t.Fatalf("%+v", got.Items)
	}
}
