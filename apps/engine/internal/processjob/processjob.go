package processjob

import (
	"context"
	"encoding/json"
	"errors"

	"engine/internal/evaluate"
	"engine/internal/queue"
	"engine/internal/store"
)

type UseCase struct {
	evaluate evaluate.UseCase
	batches  store.BatchStore
}

func New(ev evaluate.UseCase, batches store.BatchStore) UseCase {
	return UseCase{evaluate: ev, batches: batches}
}

func (u UseCase) Execute(ctx context.Context, body []byte) error {
	var job queue.Job
	if err := json.Unmarshal(body, &job); err != nil {
		return err
	}
	if job.BatchID == "" {
		return errors.New("missing batch_id")
	}
	r := u.evaluate.Execute(ctx, job.Customer)
	err := u.batches.Decide(ctx, job.BatchID, job.Index, job.Attempt, r)
	if errors.Is(err, store.ErrInvalidTransition) {
		// A redelivered message for an item already decided on this attempt.
		return nil
	}
	return err
}
