package sqs_test

import (
	"encoding/json"
	"strings"
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
	resp, err := h.Handle(t.Context(), events.SQSEvent{
		Records: []events.SQSMessage{{MessageId: "m1", Body: string(body)}},
	})
	if err != nil || len(resp.BatchItemFailures) != 0 {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	got, err := mem.Report(t.Context(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Items[0].Status != store.Decided || got.Items[0].Result.Decision != domain.Approved {
		t.Fatalf("%+v", got.Items)
	}
}

func TestHandleReportsOnlyTheFailedRecords(t *testing.T) {
	c := domain.Customer{Name: "Ana", CPF: "39053344705", CreditScore: 780, CreditLimitCents: 500_000, MonthlySpendCents: []int64{80_000}}
	mem := store.NewMemory()
	if err := mem.Create(t.Context(), "b1", []domain.Customer{c, c}); err != nil {
		t.Fatal(err)
	}
	h := sqs.New(processjob.New(evaluate.New(rules.NewPolicy()), mem))
	var recs []events.SQSMessage
	for i, job := range []queue.Job{
		{BatchID: "b1", Index: 1, Attempt: 1, Customer: c}, // the store fails
		{BatchID: "b1", Index: 0, Attempt: 1, Customer: c}, // decided
		{BatchID: "b1", Index: 0, Attempt: 1, Customer: c}, // redelivery: ErrInvalidTransition
		{BatchID: "nope", Index: 0, Attempt: 1, Customer: c},
	} {
		body, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, events.SQSMessage{MessageId: string(rune('a' + i)), Body: string(body)})
	}
	recs = append(recs, events.SQSMessage{MessageId: "e", Body: "not json"})

	mem.FailNextWrites(1)
	resp, err := h.Handle(t.Context(), events.SQSEvent{Records: recs})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range resp.BatchItemFailures {
		got = append(got, f.ItemIdentifier)
	}
	if strings.Join(got, ",") != "a,d,e" {
		t.Fatalf("failures=%v", got)
	}
	b, err := mem.Report(t.Context(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if b.Counters != (store.Counters{Queued: 1, Decided: 1}) {
		t.Fatalf("%+v", b.Counters)
	}
}
