package processjob

import (
	"context"
	"encoding/json"
	"errors"

	"engine/internal/domain"
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

// Outcome is the decision the adapter logs. Recorded is true only when
// Decide succeeded, so a redelivered message is not counted twice.
type Outcome struct {
	Recorded bool
	Result   domain.Result
	BatchID  string
	Index    int
}

func (u UseCase) Execute(ctx context.Context, body []byte) (Outcome, error) {
	var job queue.Job
	if err := json.Unmarshal(body, &job); err != nil {
		return Outcome{}, err
	}
	if job.BatchID == "" {
		return Outcome{}, errors.New("missing batch_id")
	}
	r := u.evaluate.Execute(ctx, job.Customer)
	err := u.batches.Decide(ctx, job.BatchID, job.Index, job.Attempt, r)
	if errors.Is(err, store.ErrInvalidTransition) {
		// A redelivered message for an item already decided on this attempt.
		return Outcome{}, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Recorded: true, Result: r, BatchID: job.BatchID, Index: job.Index}, nil
}
