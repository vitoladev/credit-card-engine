// Package evaluate is the single evaluation: apply the policy to one customer
// and record the decision before returning it.
package evaluate

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"engine/internal/domain"
	"engine/internal/rules"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrNotRecorded means the decision was not stored, so it is not returned
	// (ADR 0001).
	ErrNotRecorded = errors.New("decision not recorded")
)

// DecisionStore keeps single evaluations. The customer input is stored as the
// audit record; only the result is read back.
type DecisionStore interface {
	Save(ctx context.Context, decisionID string, c domain.Customer, r domain.Result) error
	// Get returns ErrNotFound for an unknown decision.
	Get(ctx context.Context, decisionID string) (domain.Result, error)
}

// Emitter records a recorded decision as a metric.
type Emitter interface {
	Evaluated(decisionID string, r domain.Result, latency time.Duration)
}

type Module struct {
	policy    rules.Policy
	decisions DecisionStore
	emit      Emitter
}

func New(policy rules.Policy, decisions DecisionStore, emit Emitter) Module {
	return Module{policy: policy, decisions: decisions, emit: emit}
}

// Evaluate decides for one customer and records the decision. It fails closed:
// a decision that was not recorded is never returned.
func (m Module) Evaluate(ctx context.Context, c domain.Customer) (string, domain.Result, error) {
	start := time.Now()
	r := m.policy.Evaluate(c)
	id := uuid.New().String()
	if err := m.decisions.Save(ctx, id, c, r); err != nil {
		return "", domain.Result{}, fmt.Errorf("%w: %w", ErrNotRecorded, err)
	}
	m.emit.Evaluated(id, r, time.Since(start))
	return id, r, nil
}

// Get returns a recorded decision.
func (m Module) Get(ctx context.Context, decisionID string) (domain.Result, error) {
	return m.decisions.Get(ctx, decisionID)
}
