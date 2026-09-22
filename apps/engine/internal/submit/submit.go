package submit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"engine/internal/domain"
	"engine/internal/queue"
	"engine/internal/store"
)

// MaxCustomers is the largest batch accepted.
const MaxCustomers = 1000

var ErrBatchTooLarge = errors.New("batch too large")

type Accepted struct {
	BatchID string `json:"batch_id"`
	Queued  int    `json:"queued"`
}

type UseCase struct {
	batches store.BatchStore
	pub     queue.Publisher
}

func New(batches store.BatchStore, pub queue.Publisher) UseCase {
	return UseCase{batches: batches, pub: pub}
}

// Execute stores every item before publishing, so a message never names an
// item that does not exist.
func (u UseCase) Execute(ctx context.Context, customers []domain.Customer) (Accepted, error) {
	if len(customers) > MaxCustomers {
		return Accepted{}, ErrBatchTooLarge
	}
	id := newID()
	if err := u.batches.Create(ctx, id, customers); err != nil {
		return Accepted{}, fmt.Errorf("create batch: %w", err)
	}
	jobs := make([]queue.Job, len(customers))
	for i, c := range customers {
		jobs[i] = queue.Job{BatchID: id, Index: i, Attempt: 1, Customer: c}
	}
	if failed, err := u.pub.Publish(ctx, jobs); err != nil {
		return Accepted{}, fmt.Errorf("publish %d of %d jobs: %w", len(failed), len(jobs), err)
	}
	return Accepted{BatchID: id, Queued: len(customers)}, nil
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
