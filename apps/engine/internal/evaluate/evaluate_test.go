package evaluate_test

import (
	"errors"
	"testing"
	"time"

	"engine/internal/adapter/ddb"
	"engine/internal/domain"
	"engine/internal/evaluate"
	"engine/internal/flocitest"
	"engine/internal/rules"
)

var ana = domain.Customer{
	Name: "Ana", CPF: "39053344705", CreditScore: 720,
	CurrentInvoiceCents: 80_000, CreditLimitCents: 500_000,
	MonthlySpendCents: []int64{100_000, 110_000, 90_000},
}

type evaluated struct {
	decisionID string
	result     domain.Result
}

type recorder struct{ events []evaluated }

func (r *recorder) Evaluated(decisionID string, res domain.Result, _ time.Duration) {
	r.events = append(r.events, evaluated{decisionID: decisionID, result: res})
}

func newModule(t *testing.T) (evaluate.Module, *flocitest.Faults, *recorder, func() int) {
	t.Helper()
	cfg, faults := flocitest.Config(t)
	tables := flocitest.CreateTables(t, cfg)
	rec := &recorder{}
	rows := func() int { return flocitest.Rows(t, cfg, tables.All()...) }
	return evaluate.New(rules.NewPolicy(), ddb.New(cfg, ddb.Tables(tables)), rec), faults, rec, rows
}

func TestEvaluateRecordsTheDecisionBeforeReturningIt(t *testing.T) {
	m, _, rec, _ := newModule(t)
	id, got, err := m.Evaluate(t.Context(), ana)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" || got.Decision != domain.Approved || got.CPFMasked != "390.***.***-05" || got.RevolvingAmountCents != 250_000 {
		t.Fatalf("id=%q result=%+v", id, got)
	}
	stored, err := m.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Decision != got.Decision || stored.RevolvingAmountCents != got.RevolvingAmountCents || stored.Reasons[0] != "eligible" {
		t.Fatalf("stored=%+v", stored)
	}
	if len(rec.events) != 1 || rec.events[0].decisionID != id || rec.events[0].result.Decision != domain.Approved {
		t.Fatalf("events=%+v", rec.events)
	}
}

func TestEvaluateFailsClosed(t *testing.T) {
	m, faults, rec, rows := newModule(t)
	faults.FailCalls("PutItem", 1)
	id, got, err := m.Evaluate(t.Context(), ana)
	if !errors.Is(err, evaluate.ErrNotRecorded) || id != "" || got.Decision != "" {
		t.Fatalf("id=%q result=%+v err=%v", id, got, err)
	}
	if n := rows(); n != 0 || len(rec.events) != 0 {
		t.Fatalf("rows=%d events=%+v", n, rec.events)
	}
}

func TestGetUnknownDecisionIsNotFound(t *testing.T) {
	m, _, _, _ := newModule(t)
	if _, err := m.Get(t.Context(), "nope"); !errors.Is(err, evaluate.ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}
