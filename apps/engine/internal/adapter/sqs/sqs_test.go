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
	mem := store.NewMemory()
	h := sqs.New(processjob.New(evaluate.New(rules.NewChain(), mem)))
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
	if err := h.Handle(t.Context(), events.SQSEvent{
		Records: []events.SQSMessage{{Body: string(body)}},
	}); err != nil {
		t.Fatal(err)
	}
	got := mem.All()
	if len(got) != 1 || got[0].Decision != domain.Approved {
		t.Fatalf("%+v", got)
	}
}
