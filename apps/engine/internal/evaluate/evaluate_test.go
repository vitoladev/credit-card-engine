package evaluate_test

import (
	"testing"

	"engine/internal/domain"
	"engine/internal/evaluate"
	"engine/internal/rules"
)

func TestExecuteTable(t *testing.T) {
	uc := evaluate.New(rules.NewPolicy())
	good := domain.Customer{
		Name: "Ana", CPF: "39053344705", CreditScore: 720,
		CurrentInvoiceCents: 80_000, CreditLimitCents: 500_000,
		LatePayments: 0, MonthlySpendCents: []int64{100_000, 110_000, 90_000},
	}
	cases := []struct {
		name     string
		mutate   func(*domain.Customer)
		decision domain.Decision
		reason   string
	}{
		{name: "approved", mutate: func(*domain.Customer) {}, decision: domain.Approved, reason: "eligible"},
		{name: "low score", mutate: func(c *domain.Customer) { c.CreditScore = 500 }, decision: domain.Denied, reason: "score_below_600"},
		{name: "lates", mutate: func(c *domain.Customer) { c.LatePayments = 4 }, decision: domain.Denied, reason: "late_payments_above_2"},
		{name: "invoice", mutate: func(c *domain.Customer) { c.CurrentInvoiceCents = 600_000 }, decision: domain.Denied, reason: "invoice_exceeds_credit_limit"},
		{name: "empty spend history", mutate: func(c *domain.Customer) { c.MonthlySpendCents = nil }, decision: domain.Denied, reason: "insufficient_spend_history"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := good
			tc.mutate(&c)
			got := uc.Execute(t.Context(), c)
			if got.Decision != tc.decision {
				t.Fatalf("decision=%s want %s reasons=%v", got.Decision, tc.decision, got.Reasons)
			}
			if len(got.Reasons) == 0 || got.Reasons[0] != tc.reason {
				t.Fatalf("reasons=%v want %s", got.Reasons, tc.reason)
			}
			if got.Decision == domain.Approved && got.RevolvingAmountCents <= 0 {
				t.Fatal("approved with zero amount")
			}
			if got.Decision == domain.Denied && got.RevolvingAmountCents != 0 {
				t.Fatalf("denied with amount %d", got.RevolvingAmountCents)
			}
			if got.CPFMasked == c.CPF {
				t.Fatal("result leaked raw CPF")
			}
		})
	}
}

func TestAmountUsesScoreBand(t *testing.T) {
	uc := evaluate.New(rules.NewPolicy())
	got := uc.Execute(t.Context(), domain.Customer{
		Name: "Boa", CPF: "12345678909", CreditScore: 820,
		CurrentInvoiceCents: 100_000, CreditLimitCents: 1_000_000,
		MonthlySpendCents: []int64{100_000},
	})
	if got.RevolvingAmountCents != 800_000 {
		t.Fatalf("amount=%d", got.RevolvingAmountCents)
	}
}
