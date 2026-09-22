package store

import (
	"context"
	"errors"
	"sync"

	"engine/internal/domain"
)

var (
	ErrNotFound          = errors.New("not found")
	ErrInvalidTransition = errors.New("invalid transition")
	ErrMaxAttempts       = errors.New("max attempts reached")
	errInjected          = errors.New("injected write failure")
)

// MaxAttempts caps the attempts of one batch item (ADR 0001).
const MaxAttempts = 5

type ItemStatus string

const (
	Queued    ItemStatus = "QUEUED"
	Decided   ItemStatus = "DECIDED"
	Failed    ItemStatus = "FAILED"
	Cancelled ItemStatus = "CANCELLED"
)

// Item is one batch item: the customer input and where it stands, in one record.
type Item struct {
	Index    int
	Customer domain.Customer
	Status   ItemStatus
	Attempts int
	Result   domain.Result
}

type Counters struct {
	Queued    int `json:"queued"`
	Decided   int `json:"decided"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
}

// Batch is everything stored for one batch. Items are ordered by index.
type Batch struct {
	ID       string
	Counters Counters
	Items    []Item
}

// BatchStore owns the item status transitions. Callers never re-check status.
type BatchStore interface {
	// Create stores every customer as a queued item with its first attempt.
	Create(ctx context.Context, batchID string, customers []domain.Customer) error
	// Decide moves a queued item on the given attempt to decided and updates
	// the counters atomically. Any other state returns ErrInvalidTransition.
	Decide(ctx context.Context, batchID string, index, attempt int, r domain.Result) error
	// Fail moves a queued item on the given attempt to failed.
	Fail(ctx context.Context, batchID string, index, attempt int) error
	// Retry moves a failed item on the given attempt back to queued on
	// attempt+1. It returns ErrMaxAttempts when attempt is already MaxAttempts.
	Retry(ctx context.Context, batchID string, index, attempt int) error
	// Cancel moves a failed item to cancelled. Cancelling a cancelled item
	// succeeds and changes nothing.
	Cancel(ctx context.Context, batchID string, index int) error
	Item(ctx context.Context, batchID string, index int) (Item, error)
	// Failed returns the batch's failed items, ErrNotFound for an unknown batch.
	Failed(ctx context.Context, batchID string) ([]Item, error)
	Report(ctx context.Context, batchID string) (Batch, error)
}

// DecisionStore keeps single evaluations. The customer input is stored as the
// audit record; only the result is read back.
type DecisionStore interface {
	Save(ctx context.Context, decisionID string, c domain.Customer, r domain.Result) error
	Get(ctx context.Context, decisionID string) (domain.Result, error)
}

type decision struct {
	customer domain.Customer
	result   domain.Result
}

type Memory struct {
	mu         sync.Mutex
	decisions  map[string]decision
	batches    map[string]*Batch
	failWrites int
}

func NewMemory() *Memory {
	return &Memory{decisions: map[string]decision{}, batches: map[string]*Batch{}}
}

// FailNextWrites makes the next n writes fail. Tests only.
func (m *Memory) FailNextWrites(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failWrites = n
}

// injected consumes one injected failure. The caller holds mu.
func (m *Memory) injected() error {
	if m.failWrites == 0 {
		return nil
	}
	m.failWrites--
	return errInjected
}

func (m *Memory) Save(_ context.Context, decisionID string, c domain.Customer, r domain.Result) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.injected(); err != nil {
		return err
	}
	m.decisions[decisionID] = decision{customer: c, result: r}
	return nil
}

func (m *Memory) Get(_ context.Context, decisionID string) (domain.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.decisions[decisionID]
	if !ok {
		return domain.Result{}, ErrNotFound
	}
	return d.result, nil
}

func (m *Memory) Create(_ context.Context, batchID string, customers []domain.Customer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.injected(); err != nil {
		return err
	}
	b := &Batch{ID: batchID, Counters: Counters{Queued: len(customers)}, Items: make([]Item, len(customers))}
	for i, c := range customers {
		b.Items[i] = Item{Index: i, Customer: c, Status: Queued, Attempts: 1}
	}
	m.batches[batchID] = b
	return nil
}

// item returns the batch and item to transition, after the injected failure
// check a real write would hit. The caller holds mu.
func (m *Memory) item(batchID string, index int) (*Batch, *Item, error) {
	if err := m.injected(); err != nil {
		return nil, nil, err
	}
	b, ok := m.batches[batchID]
	if !ok || index < 0 || index >= len(b.Items) {
		return nil, nil, ErrNotFound
	}
	return b, &b.Items[index], nil
}

func (m *Memory) Decide(_ context.Context, batchID string, index, attempt int, r domain.Result) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, it, err := m.item(batchID, index)
	if err != nil {
		return err
	}
	if it.Status != Queued || it.Attempts != attempt {
		return ErrInvalidTransition
	}
	it.Status = Decided
	it.Result = r
	b.Counters.Queued--
	b.Counters.Decided++
	return nil
}

func (m *Memory) Fail(_ context.Context, batchID string, index, attempt int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, it, err := m.item(batchID, index)
	if err != nil {
		return err
	}
	if it.Status != Queued || it.Attempts != attempt {
		return ErrInvalidTransition
	}
	it.Status = Failed
	b.Counters.Queued--
	b.Counters.Failed++
	return nil
}

func (m *Memory) Retry(_ context.Context, batchID string, index, attempt int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, it, err := m.item(batchID, index)
	if err != nil {
		return err
	}
	if it.Status != Failed || it.Attempts != attempt {
		return ErrInvalidTransition
	}
	if it.Attempts >= MaxAttempts {
		return ErrMaxAttempts
	}
	it.Status = Queued
	it.Attempts++
	b.Counters.Failed--
	b.Counters.Queued++
	return nil
}

func (m *Memory) Cancel(_ context.Context, batchID string, index int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, it, err := m.item(batchID, index)
	if err != nil {
		return err
	}
	switch it.Status {
	case Cancelled:
		return nil
	case Failed:
		it.Status = Cancelled
		b.Counters.Failed--
		b.Counters.Cancelled++
		return nil
	default:
		return ErrInvalidTransition
	}
}

func (m *Memory) Item(_ context.Context, batchID string, index int) (Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batches[batchID]
	if !ok || index < 0 || index >= len(b.Items) {
		return Item{}, ErrNotFound
	}
	return b.Items[index], nil
}

func (m *Memory) Failed(_ context.Context, batchID string) ([]Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batches[batchID]
	if !ok {
		return nil, ErrNotFound
	}
	var out []Item
	for _, it := range b.Items {
		if it.Status == Failed {
			out = append(out, it)
		}
	}
	return out, nil
}

func (m *Memory) Report(_ context.Context, batchID string) (Batch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batches[batchID]
	if !ok {
		return Batch{}, ErrNotFound
	}
	out := *b
	out.Items = append([]Item(nil), b.Items...)
	return out, nil
}

// Empty reports whether nothing was ever stored.
func (m *Memory) Empty() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.decisions) == 0 && len(m.batches) == 0
}
