package store

import (
	"context"
	"sync"

	"engine/internal/domain"
)

type DecisionStore interface {
	Save(ctx context.Context, reportID string, r domain.Result) error
	List(ctx context.Context, reportID string) ([]domain.Result, error)
}

type Memory struct {
	mu    sync.Mutex
	items map[string][]domain.Result
}

func NewMemory() *Memory {
	return &Memory{items: map[string][]domain.Result{}}
}

func (m *Memory) Save(_ context.Context, reportID string, r domain.Result) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[reportID] = append(m.items[reportID], r)
	return nil
}

func (m *Memory) List(_ context.Context, reportID string) ([]domain.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]domain.Result(nil), m.items[reportID]...), nil
}

func (m *Memory) All() []domain.Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Result
	for _, rs := range m.items {
		out = append(out, rs...)
	}
	return out
}
