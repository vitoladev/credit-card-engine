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
	ev := evaluate.New(rules.NewChain(), store.NewMemory())
	return New(processjob.New(ev))
}

func (h Handler) Handle(ctx context.Context, ev events.SQSEvent) error {
	for _, rec := range ev.Records {
		if err := h.jobs.Execute(ctx, []byte(rec.Body)); err != nil {
			return err
		}
	}
	return nil
}
