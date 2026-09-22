package sqs

import (
	"context"
	"log/slog"
	"time"

	"github.com/aws/aws-lambda-go/events"

	"engine/internal/adapter/telemetry"
	"engine/internal/evaluate"
	"engine/internal/processjob"
	"engine/internal/queue"
	"engine/internal/rules"
	"engine/internal/store"
)

type Handler struct {
	jobs processjob.UseCase
}

func New(jobs processjob.UseCase) Handler {
	return Handler{jobs: jobs}
}

func Default() Handler {
	return New(processjob.New(evaluate.New(rules.NewPolicy()), store.NewMemory()))
}

// Handle lists only the records that failed, so SQS redelivers those and
// keeps the rest. A record that keeps failing, including one whose item does
// not exist, reaches the DLQ after maxReceiveCount receives.
func (h Handler) Handle(ctx context.Context, ev events.SQSEvent) (events.SQSEventResponse, error) {
	var resp events.SQSEventResponse
	for _, rec := range ev.Records {
		job, err := queue.ParseJob(rec.Body)
		if err != nil {
			failRecord(rec, err, job)
			resp.BatchItemFailures = append(resp.BatchItemFailures, events.SQSBatchItemFailure{ItemIdentifier: rec.MessageId})
			continue
		}
		start := time.Now()
		out, err := h.jobs.Execute(ctx, job)
		if err != nil {
			failRecord(rec, err, job)
			resp.BatchItemFailures = append(resp.BatchItemFailures, events.SQSBatchItemFailure{ItemIdentifier: rec.MessageId})
			continue
		}
		if out.Recorded {
			telemetry.Decision(out.Result, time.Since(start), telemetry.IDs{BatchID: out.BatchID, Index: out.Index})
		}
	}
	return resp, nil
}

func failRecord(rec events.SQSMessage, err error, job queue.Job) {
	attrs := []any{slog.String("message_id", rec.MessageId), slog.String("error", err.Error())}
	if job.BatchID != "" {
		attrs = append(attrs, slog.String("batch_id", job.BatchID), slog.Int("index", job.Index))
	}
	slog.Error("record_failed", attrs...)
}
