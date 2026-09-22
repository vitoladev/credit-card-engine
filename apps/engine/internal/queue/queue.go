package queue

import (
	"context"
	"sync"

	"engine/internal/domain"
)

type Job struct {
	ReportID string          `json:"report_id"`
	Queued   int             `json:"queued"`
	Customer domain.Customer `json:"customer"`
}

type Publisher interface {
	Publish(ctx context.Context, jobs []Job) error
}

type Memory struct {
	mu   sync.Mutex
	Jobs []Job
}

func NewMemory() *Memory { return &Memory{} }

func (m *Memory) Publish(_ context.Context, jobs []Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Jobs = append(m.Jobs, jobs...)
	return nil
}
