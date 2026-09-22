package dlq

import (
	"context"

	"github.com/aws/aws-lambda-go/events"

	"engine/internal/markfailed"
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
		if err := h.markFailed.Execute(ctx, []byte(rec.Body)); err != nil {
			resp.BatchItemFailures = append(resp.BatchItemFailures, events.SQSBatchItemFailure{ItemIdentifier: rec.MessageId})
		}
	}
	return resp, nil
}
