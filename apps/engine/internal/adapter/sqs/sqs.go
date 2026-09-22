package sqs

import (
	"context"

	"github.com/aws/aws-lambda-go/events"

	"engine/internal/evaluate"
	"engine/internal/processjob"
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
		if err := h.jobs.Execute(ctx, []byte(rec.Body)); err != nil {
			resp.BatchItemFailures = append(resp.BatchItemFailures, events.SQSBatchItemFailure{ItemIdentifier: rec.MessageId})
		}
	}
	return resp, nil
}
