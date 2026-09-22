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
)

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
	mu        sync.Mutex
	decisions map[string]decision
	batches   map[string]*Batch
}

func NewMemory() *Memory {
	return &Memory{decisions: map[string]decision{}, batches: map[string]*Batch{}}
}

func (m *Memory) Save(_ context.Context, decisionID string, c domain.Customer, r domain.Result) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	b := &Batch{ID: batchID, Counters: Counters{Queued: len(customers)}, Items: make([]Item, len(customers))}
	for i, c := range customers {
		b.Items[i] = Item{Index: i, Customer: c, Status: Queued, Attempts: 1}
	}
	m.batches[batchID] = b
	return nil
}

func (m *Memory) Decide(_ context.Context, batchID string, index, attempt int, r domain.Result) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batches[batchID]
	if !ok || index < 0 || index >= len(b.Items) {
		return ErrNotFound
	}
	it := &b.Items[index]
	if it.Status != Queued || it.Attempts != attempt {
		return ErrInvalidTransition
	}
	it.Status = Decided
	it.Result = r
	b.Counters.Queued--
	b.Counters.Decided++
	return nil
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
