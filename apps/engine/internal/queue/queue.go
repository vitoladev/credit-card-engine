package queue

import (
	"context"
	"encoding/json"
	"sync"

	"engine/internal/domain"
)

// Job is the message for one attempt of one batch item.
type Job struct {
	BatchID  string          `json:"batch_id"`
	Index    int             `json:"index"`
	Attempt  int             `json:"attempt"`
	Customer domain.Customer `json:"customer"`
}

type Publisher interface {
	// Publish returns the indexes of the jobs that were not published, so the
	// caller can act on exactly those items.
	Publish(ctx context.Context, jobs []Job) (failed []int, err error)
}

type Memory struct {
	mu   sync.Mutex
	Jobs []Job
}

func NewMemory() *Memory { return &Memory{} }

func (m *Memory) Publish(_ context.Context, jobs []Job) ([]int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Jobs = append(m.Jobs, jobs...)
	return nil, nil
}

// Drain delivers every published job to handle as a message body, in order,
// and removes the delivered jobs.
func (m *Memory) Drain(ctx context.Context, handle func(context.Context, []byte) error) error {
	m.mu.Lock()
	jobs := m.Jobs
	m.Jobs = nil
	m.mu.Unlock()
	for _, job := range jobs {
		body, err := json.Marshal(job)
		if err != nil {
			return err
		}
		if err := handle(ctx, body); err != nil {
			return err
		}
	}
	return nil
}
