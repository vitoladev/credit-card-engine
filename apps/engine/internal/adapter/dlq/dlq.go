package dlq

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/aws/aws-lambda-go/events"

	"engine/internal/markfailed"
	"engine/internal/queue"
)

// Handler consumes the EvaluationJobs DLQ and marks each record's item failed.
type Handler struct {
	markFailed markfailed.UseCase
}

func New(markFailed markfailed.UseCase) Handler {
	return Handler{markFailed: markFailed}
}

func (h Handler) Handle(ctx context.Context, ev events.SQSEvent) (events.SQSEventResponse, error) {
	var resp events.SQSEventResponse
	for _, rec := range ev.Records {
		job, err := parseJob(rec.Body)
		if err != nil {
			// Unparseable poison is acknowledged so it does not loop in the DLQ.
			continue
		}
		if err := h.markFailed.Execute(ctx, job); err != nil {
			attrs := []any{slog.String("message_id", rec.MessageId), slog.String("error", err.Error())}
			if job.BatchID != "" {
				attrs = append(attrs, slog.String("batch_id", job.BatchID), slog.Int("index", job.Index))
			}
			slog.Error("record_failed", attrs...)
			resp.BatchItemFailures = append(resp.BatchItemFailures, events.SQSBatchItemFailure{ItemIdentifier: rec.MessageId})
		}
	}
	return resp, nil
}

func parseJob(body string) (queue.Job, error) {
	var job queue.Job
	if err := json.Unmarshal([]byte(body), &job); err != nil {
		return queue.Job{}, err
	}
	return job, nil
}
