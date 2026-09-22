package markfailed

import (
	"context"
	"encoding/json"
	"errors"

	"engine/internal/queue"
	"engine/internal/store"
)

// UseCase marks the batch item of a dead-lettered message failed.
type UseCase struct {
	batches store.BatchStore
}

func New(batches store.BatchStore) UseCase {
	return UseCase{batches: batches}
}

// Execute returns an error only when the store may succeed on a later try.
// A message that names no item (unparseable, or an item that does not exist)
// and a message for an item that already moved on (decided, or retried on a
// newer attempt) are acknowledged: failing them again would only loop in the
// DLQ until its retention drops them.
func (u UseCase) Execute(ctx context.Context, body []byte) error {
	var job queue.Job
	if err := json.Unmarshal(body, &job); err != nil || job.BatchID == "" {
		return nil //nolint:nilerr // a poison message is acknowledged, see above
	}
	err := u.batches.Fail(ctx, job.BatchID, job.Index, job.Attempt)
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrInvalidTransition) {
		return nil
	}
	return err
}
