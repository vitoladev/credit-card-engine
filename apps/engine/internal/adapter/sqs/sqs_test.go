package sqs_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"

	"engine/internal/adapter/ddb"
	"engine/internal/adapter/sqs"
	"engine/internal/batch"
	"engine/internal/domain"
	"engine/internal/flocitest"
	"engine/internal/rules"
)

var ana = domain.Customer{Name: "Ana", CPF: "39053344705", CreditScore: 780, CreditLimitCents: 500_000, MonthlySpendCents: []int64{80_000}}

type discard struct{}

func (discard) ItemDecided(string, int, domain.Result, time.Duration) {}
func (discard) ItemFailed(string, int, int)                           {}

// newBatch stores batch b1 with two items on Floci. The queue consumers never
// publish, so the module has no Publisher.
func newBatch(t *testing.T) (batch.Module, *flocitest.Faults) {
	t.Helper()
	cfg, faults := flocitest.Config(t)
	st := ddb.New(cfg, flocitest.Table(t, cfg))
	if err := st.Create(t.Context(), "b1", []domain.Customer{ana, ana}); err != nil {
		t.Fatal(err)
	}
	return batch.New(batch.Deps{Items: st, Policy: rules.NewPolicy(), Emitter: discard{}}), faults
}

func record(t *testing.T, id string, a batch.Attempt) events.SQSMessage {
	t.Helper()
	body, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	return events.SQSMessage{MessageId: id, Body: string(body)}
}

func failures(resp events.SQSEventResponse) string {
	var ids []string
	for _, f := range resp.BatchItemFailures {
		ids = append(ids, f.ItemIdentifier)
	}
	return strings.Join(ids, ",")
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestWorkerReportsOnlyTheFailedRecords(t *testing.T) {
	b, faults := newBatch(t)
	ev := events.SQSEvent{Records: []events.SQSMessage{
		record(t, "a", batch.Attempt{BatchID: "b1", Index: 1, Number: 1, Customer: ana}), // the store fails
		record(t, "b", batch.Attempt{BatchID: "b1", Index: 0, Number: 1, Customer: ana}), // decided
		record(t, "c", batch.Attempt{BatchID: "b1", Index: 0, Number: 1, Customer: ana}), // redelivery
		record(t, "d", batch.Attempt{BatchID: "nope", Index: 0, Number: 1, Customer: ana}),
		{MessageId: "e", Body: "not json"},
	}}
	faults.FailCalls("UpdateItem", 1)
	resp, err := sqs.Worker(b).Handle(t.Context(), ev)
	if err != nil {
		t.Fatal(err)
	}
	if got := failures(resp); got != "a,d,e" {
		t.Fatalf("failures=%s", got)
	}
	r, err := b.Report(t.Context(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if r.Counters != (batch.Counters{Queued: 1, Decided: 1}) {
		t.Fatalf("%+v", r.Counters)
	}
}

func TestDeadLettersFailTheItemAndAcknowledgePoison(t *testing.T) {
	b, faults := newBatch(t)
	ev := events.SQSEvent{Records: []events.SQSMessage{
		record(t, "a", batch.Attempt{BatchID: "b1", Index: 0, Number: 1, Customer: ana}), // the store fails
		record(t, "b", batch.Attempt{BatchID: "b1", Index: 1, Number: 1, Customer: ana}), // failed
		record(t, "c", batch.Attempt{BatchID: "nope", Index: 0, Number: 1, Customer: ana}),
		{MessageId: "d", Body: "not json"},
	}}
	faults.FailCalls("UpdateItem", 1)
	resp, err := sqs.DeadLetters(b).Handle(t.Context(), ev)
	if err != nil {
		t.Fatal(err)
	}
	if got := failures(resp); got != "a" {
		t.Fatalf("failures=%s", got)
	}
	r, err := b.Report(t.Context(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if r.Counters != (batch.Counters{Queued: 1, Failed: 1}) {
		t.Fatalf("%+v", r.Counters)
	}
}

func TestFailedRecordsAreLoggedWithoutCustomerData(t *testing.T) {
	for _, tc := range []struct {
		name     string
		consumer func(batch.Module) sqs.Consumer
	}{
		{"worker", sqs.Worker},
		{"dead letters", sqs.DeadLetters},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureLogs(t)
			b, faults := newBatch(t)
			faults.FailCalls("UpdateItem", 1)
			resp, err := tc.consumer(b).Handle(t.Context(), events.SQSEvent{Records: []events.SQSMessage{
				record(t, "m1", batch.Attempt{BatchID: "b1", Index: 0, Number: 1, Customer: ana}),
			}})
			if err != nil || failures(resp) != "m1" {
				t.Fatalf("resp=%+v err=%v", resp, err)
			}
			logs := buf.String()
			if !strings.Contains(logs, `"msg":"record_failed"`) || !strings.Contains(logs, `"message_id":"m1"`) {
				t.Fatalf("missing record_failed:\n%s", logs)
			}
			if !strings.Contains(logs, `"batch_id":"b1"`) || !strings.Contains(logs, `"index":0`) {
				t.Fatalf("missing ids:\n%s", logs)
			}
			if strings.Contains(logs, "Ana") || strings.Contains(logs, "39053344705") {
				t.Fatalf("leaked customer data:\n%s", logs)
			}
		})
	}
}

func TestWorkerLogsAMalformedRecordWithoutItsBody(t *testing.T) {
	buf := captureLogs(t)
	b, _ := newBatch(t)
	body := `{"batch_id":"b1","customer":{"name":"Ana","cpf":"39053344705"`
	resp, err := sqs.Worker(b).Handle(t.Context(), events.SQSEvent{Records: []events.SQSMessage{{MessageId: "m1", Body: body}}})
	if err != nil || failures(resp) != "m1" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if logs := buf.String(); strings.Contains(logs, "Ana") || strings.Contains(logs, "39053344705") {
		t.Fatalf("leaked customer data:\n%s", logs)
	}
}
