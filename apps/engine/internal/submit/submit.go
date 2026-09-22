package submit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"engine/internal/domain"
	"engine/internal/queue"
	"engine/internal/store"
)

const (
	DefaultBatchSize = 100
	MaxBatchSize     = 1000
)

var (
	ErrBatchTooLarge = errors.New("batch too large")
	// ErrNotRecorded means the batch was not stored and nothing was published.
	ErrNotRecorded = errors.New("batch not recorded")
)

type Accepted struct {
	BatchID string `json:"batch_id"`
	Queued  int    `json:"queued"`
}

// ParseBatchSize reads the BATCH_SIZE setting: empty means DefaultBatchSize,
// anything outside 1..MaxBatchSize is an error rather than a silent clamp.
func ParseBatchSize(s string) (int, error) {
	if s == "" {
		return DefaultBatchSize, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > MaxBatchSize {
		return 0, fmt.Errorf("BATCH_SIZE must be an integer in 1..%d, got %q", MaxBatchSize, s)
	}
	return n, nil
}

type UseCase struct {
	batches      store.BatchStore
	pub          queue.Publisher
	maxCustomers int
}

func New(batches store.BatchStore, pub queue.Publisher, maxCustomers int) UseCase {
	return UseCase{batches: batches, pub: pub, maxCustomers: maxCustomers}
}

func (u UseCase) MaxCustomers() int { return u.maxCustomers }

// Execute stores every item before publishing, so a message never names an
// item that does not exist. An item whose publish failed is marked failed, not
// left queued with no message, and the batch is still accepted (ADR 0001).
func (u UseCase) Execute(ctx context.Context, customers []domain.Customer) (Accepted, error) {
	if len(customers) > u.maxCustomers {
		return Accepted{}, ErrBatchTooLarge
	}
	id := newID()
	if err := u.batches.Create(ctx, id, customers); err != nil {
		return Accepted{}, fmt.Errorf("%w: %w", ErrNotRecorded, err)
	}
	jobs := make([]queue.Job, len(customers))
	for i, c := range customers {
		jobs[i] = queue.Job{BatchID: id, Index: i, Attempt: 1, Customer: c}
	}
	// The publish error is not returned: every index it names becomes a
	// failed item that the report shows and an operator retries.
	failed, _ := u.pub.Publish(ctx, jobs)
	for _, i := range failed {
		if err := u.batches.Fail(ctx, id, i, 1); err != nil {
			return Accepted{}, fmt.Errorf("mark item %d failed after its publish failed: %w", i, err)
		}
	}
	return Accepted{BatchID: id, Queued: len(customers) - len(failed)}, nil
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
