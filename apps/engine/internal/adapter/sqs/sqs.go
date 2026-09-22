// Package sqs consumes the batch queues: the EvaluationJobs queue and its DLQ.
package sqs

import (
	"context"
	"log/slog"

	"github.com/aws/aws-lambda-go/events"

	"engine/internal/batch"
)

// Consumer hands each record's attempt to handle and lists only the records
// that failed, so SQS redelivers those and keeps the rest.
type Consumer struct {
	handle func(context.Context, batch.Attempt) error
	// ackMalformed acknowledges a record that is not an attempt instead of
	// reporting it.
	ackMalformed bool
}

// Worker evaluates attempts. A record that keeps failing, including one that
// is not an attempt or names no item, reaches the DLQ after maxReceiveCount
// receives, which is the signal the DLQ alarm watches.
func Worker(b batch.Module) Consumer {
	return Consumer{handle: b.Process}
}

// DeadLetters fails the item of each dead-lettered attempt. A record that is
// not an attempt is acknowledged, so it does not loop in the DLQ.
func DeadLetters(b batch.Module) Consumer {
	return Consumer{handle: b.DeadLetter, ackMalformed: true}
}

func (c Consumer) Handle(ctx context.Context, ev events.SQSEvent) (events.SQSEventResponse, error) {
	var resp events.SQSEventResponse
	for _, rec := range ev.Records {
		a, err := batch.ParseAttempt(rec.Body)
		if err != nil && c.ackMalformed {
			continue
		}
		if err == nil {
			err = c.handle(ctx, a)
		}
		if err != nil {
			failRecord(rec, err, a)
			resp.BatchItemFailures = append(resp.BatchItemFailures, events.SQSBatchItemFailure{ItemIdentifier: rec.MessageId})
		}
	}
	return resp, nil
}

// failRecord logs the ids of a failed record, never its body: the body holds
// the full CPF and the name.
func failRecord(rec events.SQSMessage, err error, a batch.Attempt) {
	attrs := []any{slog.String("message_id", rec.MessageId), slog.String("error", err.Error())}
	if a.BatchID != "" {
		attrs = append(attrs, slog.String("batch_id", a.BatchID), slog.String("item_id", a.ItemID))
	}
	slog.Error("record_failed", attrs...)
}
