// Package batch is the lifecycle of a batch and its items: submit, evaluate
// each attempt, fail it after the DLQ, and the operator's retry and cancel
// (ADR 0001). Item status transitions are atomic in the Items adapter; this
// module decides which transition to ask for and keeps every queued item
// backed by a message.
package batch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
	"uuid"

	"engine/internal/domain"
	"engine/internal/rules"
)

const (
	DefaultBatchSize = 100
	MaxBatchSize     = 1000
	// MaxAttempts caps the attempts of one batch item (ADR 0001).
	MaxAttempts = 5
)

var (
	ErrNotFound          = errors.New("not found")
	ErrInvalidTransition = errors.New("invalid transition")
	ErrMaxAttempts       = errors.New("max attempts reached")
	ErrTooLarge          = errors.New("batch too large")
	// ErrNotRecorded means the batch was not stored and nothing was published.
	ErrNotRecorded = errors.New("batch not recorded")
	// ErrEnqueueFailed means a retry's message was not published and the item
	// is failed again.
	ErrEnqueueFailed = errors.New("enqueue failed")
)

type ItemStatus string

const (
	Queued    ItemStatus = "QUEUED"
	Decided   ItemStatus = "DECIDED"
	Failed    ItemStatus = "FAILED"
	Cancelled ItemStatus = "CANCELLED"
)

// Item is one batch item: the customer input and where it stands.
type Item struct {
	Index    int
	Customer domain.Customer
	Status   ItemStatus
	Attempts int
	Result   domain.Result
}

// Attempt is one pass of a batch item through evaluation, and the body of its
// queue message.
type Attempt struct {
	BatchID  string          `json:"batch_id"`
	Index    int             `json:"index"`
	Number   int             `json:"attempt"`
	Customer domain.Customer `json:"customer"`
}

// ParseAttempt decodes a queue message body.
func ParseAttempt(body string) (Attempt, error) {
	var a Attempt
	if err := json.Unmarshal([]byte(body), &a); err != nil {
		return Attempt{}, err
	}
	return a, nil
}

// Items stores batch items and moves them between statuses. Every transition
// is conditional on the item's current status and attempt, so concurrent
// workers and operators cannot both win.
type Items interface {
	// Create stores every customer as a queued item on attempt 1. It writes
	// nothing when it fails.
	Create(ctx context.Context, batchID string, customers []domain.Customer) error
	// Decide moves a queued item on the given attempt to decided.
	Decide(ctx context.Context, batchID string, index, attempt int, r domain.Result) error
	// Fail moves a queued item on the given attempt to failed.
	Fail(ctx context.Context, batchID string, index, attempt int) error
	// Retry moves a failed item on the given attempt back to queued on
	// attempt+1, or returns ErrMaxAttempts at MaxAttempts.
	Retry(ctx context.Context, batchID string, index, attempt int) error
	// RetryMany retries each item and returns the ones it moved. Items that
	// cannot transition are skipped; a store error stops the rest.
	RetryMany(ctx context.Context, batchID string, items []Item) ([]Item, error)
	// Cancel moves a failed item to cancelled; a cancelled item stays so.
	Cancel(ctx context.Context, batchID string, index int) error
	Item(ctx context.Context, batchID string, index int) (Item, error)
	// Failed returns the batch's failed items.
	Failed(ctx context.Context, batchID string) ([]Item, error)
	// All returns every item of the batch, ordered by index.
	All(ctx context.Context, batchID string) ([]Item, error)
}

// Publisher sends attempts to the queue. It returns the indexes of the
// attempts it did not send.
type Publisher interface {
	Publish(ctx context.Context, attempts []Attempt) (failed []int, err error)
}

// Emitter records what happened to items as metrics.
type Emitter interface {
	ItemDecided(batchID string, index int, r domain.Result, latency time.Duration)
	ItemFailed(batchID string, index, attempt int)
}

// Deps wires the module. Publisher is needed only to submit and retry, and
// MaxCustomers only to submit.
type Deps struct {
	Items        Items
	Publisher    Publisher
	Policy       rules.Policy
	Emitter      Emitter
	MaxCustomers int
}

type Module struct {
	items        Items
	pub          Publisher
	policy       rules.Policy
	emit         Emitter
	maxCustomers int
}

func New(d Deps) Module {
	return Module{items: d.Items, pub: d.Publisher, policy: d.Policy, emit: d.Emitter, maxCustomers: d.MaxCustomers}
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

func (m Module) MaxCustomers() int { return m.maxCustomers }

type Accepted struct {
	BatchID string `json:"batch_id"`
	Queued  int    `json:"queued"`
}

// Submit stores every item before publishing, so a message never names an
// item that does not exist. An item whose publish failed is failed, not left
// queued with no message, and the batch is still accepted (ADR 0001).
func (m Module) Submit(ctx context.Context, customers []domain.Customer) (Accepted, error) {
	if len(customers) > m.maxCustomers {
		return Accepted{}, ErrTooLarge
	}
	id := uuid.New().String()
	if err := m.items.Create(ctx, id, customers); err != nil {
		return Accepted{}, fmt.Errorf("%w: %w", ErrNotRecorded, err)
	}
	attempts := make([]Attempt, len(customers))
	for i, c := range customers {
		attempts[i] = Attempt{BatchID: id, Index: i, Number: 1, Customer: c}
	}
	failed, err := m.publish(ctx, attempts)
	if err != nil {
		return Accepted{}, err
	}
	return Accepted{BatchID: id, Queued: len(customers) - len(failed)}, nil
}

// Process evaluates one attempt and decides its item. A redelivered attempt
// for an item that already moved on is acknowledged. An error means the
// message should be redelivered.
func (m Module) Process(ctx context.Context, a Attempt) error {
	if a.BatchID == "" {
		return errors.New("missing batch_id")
	}
	start := time.Now()
	r := m.policy.Evaluate(a.Customer)
	err := m.items.Decide(ctx, a.BatchID, a.Index, a.Number, r)
	if errors.Is(err, ErrInvalidTransition) {
		return nil
	}
	if err != nil {
		return err
	}
	m.emit.ItemDecided(a.BatchID, a.Index, r, time.Since(start))
	return nil
}

// DeadLetter fails the item of an attempt that exhausted its deliveries. It
// returns an error only when the store may succeed on a later try: an attempt
// that names no item, or whose item already moved on, is acknowledged so it
// does not loop in the DLQ.
func (m Module) DeadLetter(ctx context.Context, a Attempt) error {
	if a.BatchID == "" {
		return nil
	}
	err := m.fail(ctx, a.BatchID, a.Index, a.Number)
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidTransition) {
		return nil
	}
	return err
}

// Retry requeues one failed item on its next attempt and returns that attempt.
func (m Module) Retry(ctx context.Context, batchID string, index int) (int, error) {
	it, err := m.items.Item(ctx, batchID, index)
	if err != nil {
		return 0, err
	}
	if err := m.items.Retry(ctx, batchID, index, it.Attempts); err != nil {
		return 0, err
	}
	a := Attempt{BatchID: batchID, Index: index, Number: it.Attempts + 1, Customer: it.Customer}
	failed, err := m.publish(ctx, []Attempt{a})
	if err != nil {
		return 0, err
	}
	if len(failed) > 0 {
		return 0, ErrEnqueueFailed
	}
	return a.Number, nil
}

// RetryFailed requeues every failed item under MaxAttempts and returns how
// many were published. The failed items come from one query and are
// published together, never one read or one publish per item.
func (m Module) RetryFailed(ctx context.Context, batchID string) (int, error) {
	items, err := m.items.Failed(ctx, batchID)
	if err != nil {
		return 0, err
	}
	var eligible []Item
	for _, it := range items {
		if it.Attempts < MaxAttempts {
			eligible = append(eligible, it)
		}
	}
	if len(eligible) == 0 {
		return 0, nil
	}
	queued, retryErr := m.items.RetryMany(ctx, batchID, eligible)
	if len(queued) == 0 {
		return 0, retryErr
	}
	attempts := make([]Attempt, len(queued))
	for i, it := range queued {
		attempts[i] = Attempt{BatchID: batchID, Index: it.Index, Number: it.Attempts + 1, Customer: it.Customer}
	}
	failed, err := m.publish(ctx, attempts)
	if err != nil {
		return 0, errors.Join(retryErr, err)
	}
	return len(attempts) - len(failed), retryErr
}

// Cancel cancels a failed item. Cancelling a cancelled item changes nothing.
func (m Module) Cancel(ctx context.Context, batchID string, index int) error {
	return m.items.Cancel(ctx, batchID, index)
}

// publish sends the attempts and fails every item whose attempt was not sent,
// so no item stays queued with no message behind it. The publish error itself
// is not returned: the items it names are failed, and an operator retries them.
func (m Module) publish(ctx context.Context, attempts []Attempt) ([]int, error) {
	failed, _ := m.pub.Publish(ctx, attempts)
	numbers := make(map[int]int, len(attempts))
	for _, a := range attempts {
		numbers[a.Index] = a.Number
	}
	for _, i := range failed {
		if err := m.fail(ctx, attempts[0].BatchID, i, numbers[i]); err != nil {
			return nil, fmt.Errorf("fail item %d after its publish failed: %w", i, err)
		}
	}
	return failed, nil
}

// fail moves a queued item to failed and emits ItemsFailed only when it moved.
func (m Module) fail(ctx context.Context, batchID string, index, attempt int) error {
	if err := m.items.Fail(ctx, batchID, index, attempt); err != nil {
		return err
	}
	m.emit.ItemFailed(batchID, index, attempt)
	return nil
}
