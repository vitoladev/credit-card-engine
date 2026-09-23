package sqs_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

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

func (discard) ItemDecided(string, string, domain.Result, time.Duration, time.Duration) {}
func (discard) ItemFailed(string, string, int)                                          {}

// newBatch stores batch b1 with two items on Floci and returns their IDs. The
// queue consumers never publish, so the module has no Publisher.
func newBatch(t *testing.T) (batch.Module, *flocitest.Faults, [2]string) {
	t.Helper()
	cfg, faults := flocitest.Config(t)
	st := ddb.New(cfg, flocitest.Table(t, cfg))
	ids := [2]string{uuid.NewV7().String(), uuid.NewV7().String()}
	if err := st.Create(t.Context(), "b1", []batch.Item{{ID: ids[0], Customer: ana}, {ID: ids[1], Customer: ana}}); err != nil {
		t.Fatal(err)
	}
	return batch.New(batch.Deps{Items: st, Policy: rules.NewPolicy(), Emitter: discard{}}), faults, ids
}

func statuses(t *testing.T, b batch.Module) []batch.ItemStatus {
	t.Helper()
	list, err := b.ListItems(t.Context(), "b1", batch.ListQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]batch.ItemStatus, len(list.Items))
	for i, e := range list.Items {
		out[i] = e.Status
	}
	return out
}

func record(t *testing.T, id string, a batch.ItemEvent) events.SQSMessage {
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

func handle(t *testing.T, c sqs.Consumer, records ...events.SQSMessage) string {
	t.Helper()
	resp, err := c.Handle(t.Context(), events.SQSEvent{Records: records})
	if err != nil {
		t.Fatal(err)
	}
	return failures(resp)
}

// Records run concurrently, so an injected store failure is armed for a
// batch of one record: it cannot land on another record.
func TestWorkerReportsOnlyTheFailedRecords(t *testing.T) {
	b, faults, id := newBatch(t)
	w := sqs.Worker(b)

	faults.FailCalls("UpdateItem", 1)
	if got := handle(t, w, record(t, "a", batch.NewItemEvent("b1", id[1], 1, batch.Queued))); got != "a" {
		t.Fatalf("store failure: failures=%s", got)
	}
	got := handle(t, w,
		record(t, "b", batch.NewItemEvent("b1", id[0], 1, batch.Queued)), // decided
		record(t, "c", batch.NewItemEvent("b1", id[0], 1, batch.Queued)), // redelivered at the same time
		record(t, "d", batch.NewItemEvent("nope", id[0], 1, batch.Queued)),
		events.SQSMessage{MessageId: "e", Body: "not json"},
	)
	if got != "d,e" {
		t.Fatalf("failures=%s, want them in arrival order", got)
	}
	if got := statuses(t, b); !slices.Equal(got, []batch.ItemStatus{batch.Approved, batch.Queued}) {
		t.Fatalf("statuses=%v", got)
	}
}

func TestDeadLettersFailTheItemAndAcknowledgePoison(t *testing.T) {
	b, faults, id := newBatch(t)
	dl := sqs.DeadLetters(b)

	faults.FailCalls("UpdateItem", 1)
	if got := handle(t, dl, record(t, "a", batch.NewItemEvent("b1", id[0], 1, batch.Queued))); got != "a" {
		t.Fatalf("store failure: failures=%s", got)
	}
	got := handle(t, dl,
		record(t, "b", batch.NewItemEvent("b1", id[1], 1, batch.Queued)), // failed
		record(t, "c", batch.NewItemEvent("nope", id[0], 1, batch.Queued)),
		events.SQSMessage{MessageId: "d", Body: "not json"},
	)
	if got != "" {
		t.Fatalf("failures=%s", got)
	}
	if got := statuses(t, b); !slices.Equal(got, []batch.ItemStatus{batch.Queued, batch.Failed}) {
		t.Fatalf("statuses=%v", got)
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
			b, faults, id := newBatch(t)
			faults.FailCalls("UpdateItem", 1)
			resp, err := tc.consumer(b).Handle(t.Context(), events.SQSEvent{Records: []events.SQSMessage{
				record(t, "m1", batch.NewItemEvent("b1", id[0], 1, batch.Queued)),
			}})
			if err != nil || failures(resp) != "m1" {
				t.Fatalf("resp=%+v err=%v", resp, err)
			}
			logs := buf.String()
			if !strings.Contains(logs, `"msg":"record_failed"`) || !strings.Contains(logs, `"message_id":"m1"`) {
				t.Fatalf("missing record_failed:\n%s", logs)
			}
			if !strings.Contains(logs, `"batch_id":"b1"`) || !strings.Contains(logs, `"item_id":"`+id[0]+`"`) {
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
	b, _, _ := newBatch(t)
	body := `{"batch_id":"b1","customer":{"name":"Ana","cpf":"39053344705"`
	resp, err := sqs.Worker(b).Handle(t.Context(), events.SQSEvent{Records: []events.SQSMessage{{MessageId: "m1", Body: body}}})
	if err != nil || failures(resp) != "m1" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if logs := buf.String(); strings.Contains(logs, "Ana") || strings.Contains(logs, "39053344705") {
		t.Fatalf("leaked customer data:\n%s", logs)
	}
}
