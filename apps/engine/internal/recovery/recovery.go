// Package recovery is the operator's recovery of failed batch items: retry
// one, retry every failed one, or cancel one (ADR 0001).
package recovery

import (
	"context"
	"errors"
	"fmt"

	"engine/internal/queue"
	"engine/internal/store"
)

// ErrEnqueueFailed means the retry's message was not published and the item
// is failed again.
var ErrEnqueueFailed = errors.New("enqueue failed")

type UseCase struct {
	batches store.BatchStore
	pub     queue.Publisher
}

func New(batches store.BatchStore, pub queue.Publisher) UseCase {
	return UseCase{batches: batches, pub: pub}
}

// Retry requeues one failed item on its next attempt and returns that attempt.
func (u UseCase) Retry(ctx context.Context, batchID string, index int) (int, error) {
	it, err := u.batches.Item(ctx, batchID, index)
	if err != nil {
		return 0, err
	}
	if err := u.batches.Retry(ctx, batchID, index, it.Attempts); err != nil {
		return 0, err
	}
	job := queue.Job{BatchID: batchID, Index: index, Attempt: it.Attempts + 1, Customer: it.Customer}
	failed, err := u.requeue(ctx, []queue.Job{job})
	if err != nil {
		return 0, err
	}
	if len(failed) > 0 {
		return 0, ErrEnqueueFailed
	}
	return job.Attempt, nil
}

// RetryFailed requeues every failed item under MaxAttempts and returns how
// many were published. The failed items come from one query and are
// published together, never one read or one publish per item.
func (u UseCase) RetryFailed(ctx context.Context, batchID string) (int, error) {
	items, err := u.batches.Failed(ctx, batchID)
	if err != nil {
		return 0, err
	}
	queued, retryErr := u.batches.RetryMany(ctx, batchID, items)
	jobs := make([]queue.Job, len(queued))
	for i, it := range queued {
		jobs[i] = queue.Job{BatchID: batchID, Index: it.Index, Attempt: it.Attempts + 1, Customer: it.Customer}
	}
	if len(jobs) == 0 {
		return 0, retryErr
	}
	failed, err := u.requeue(ctx, jobs)
	if err != nil {
		return 0, errors.Join(retryErr, err)
	}
	return len(jobs) - len(failed), retryErr
}

// requeue publishes the jobs and moves every item whose publish failed back to
// failed, so no item stays queued with no message behind it.
func (u UseCase) requeue(ctx context.Context, jobs []queue.Job) ([]int, error) {
	// The publish error is not returned: the indexes it names are failed again.
	failed, _ := u.pub.Publish(ctx, jobs)
	attempts := make(map[int]int, len(jobs))
	for _, j := range jobs {
		attempts[j.Index] = j.Attempt
	}
	for _, i := range failed {
		if err := u.batches.Fail(ctx, jobs[0].BatchID, i, attempts[i]); err != nil {
			return nil, fmt.Errorf("mark item %d failed after its publish failed: %w", i, err)
		}
	}
	return failed, nil
}

func (u UseCase) Cancel(ctx context.Context, batchID string, index int) error {
	return u.batches.Cancel(ctx, batchID, index)
}
