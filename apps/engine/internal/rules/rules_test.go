package rules_test

import (
	"testing"

	"engine/internal/domain"
	"engine/internal/rules"
)

func TestEachGate(t *testing.T) {
	ok := domain.Customer{
		Name: "Ana", CPF: "39053344705", CreditScore: 720,
		CurrentInvoiceCents: 80_000, CreditLimitCents: 500_000,
		LatePayments: 0, MonthlySpendCents: []int64{100_000, 110_000, 90_000},
	}
	cases := []struct {
		name   string
		mutate func(*domain.Customer)
		rule   rules.Handler
		wantOK bool
		reason string
	}{
		{name: "score too low", mutate: func(c *domain.Customer) { c.CreditScore = 500 }, rule: &rules.MinScore{Min: 600}, wantOK: false},
		{name: "score ok", mutate: func(*domain.Customer) {}, rule: &rules.MinScore{Min: 600}, wantOK: true},
		{name: "too many lates", mutate: func(c *domain.Customer) { c.LatePayments = 3 }, rule: &rules.MaxLatePayments{Max: 2}, wantOK: false},
		{name: "invoice over limit", mutate: func(c *domain.Customer) { c.CurrentInvoiceCents = 600_000 }, rule: &rules.InvoiceWithinLimit{}, wantOK: false},
		{name: "spend spike", mutate: func(c *domain.Customer) { c.MonthlySpendCents = []int64{480_000, 490_000, 500_000} }, rule: &rules.RecentSpend{Months: 3, MaxShareBPS: 9000}, wantOK: false},
		{name: "spend ok", mutate: func(*domain.Customer) {}, rule: &rules.RecentSpend{Months: 3, MaxShareBPS: 9000}, wantOK: true},
		{name: "empty spend history", mutate: func(c *domain.Customer) { c.MonthlySpendCents = nil }, rule: &rules.RecentSpend{Months: 3, MaxShareBPS: 9000}, wantOK: false, reason: "insufficient_spend_history"},
		{name: "no credit limit", mutate: func(c *domain.Customer) { c.CreditLimitCents = 0; c.CurrentInvoiceCents = 0 }, rule: &rules.RecentSpend{Months: 3, MaxShareBPS: 9000}, wantOK: false, reason: "no_credit_limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := ok
			tc.mutate(&c)
			got, reason := tc.rule.Handle(c)
			if got != tc.wantOK {
				t.Fatalf("ok=%v reason=%s want %v", got, reason, tc.wantOK)
			}
			if !tc.wantOK && reason == "" {
				t.Fatal("denied without reason")
			}
			if tc.reason != "" && reason != tc.reason {
				t.Fatalf("reason=%s want %s", reason, tc.reason)
			}
		})
	}
}

func TestChainFirstDenyWins(t *testing.T) {
	c := domain.Customer{
		Name: "Bruno", CPF: "12345678909", CreditScore: 400,
		CurrentInvoiceCents: 600_000, CreditLimitCents: 500_000,
		LatePayments: 5,
	}
	ok, reason := rules.NewPolicy().Chain.Handle(c)
	if ok || reason != "score_below_600" {
		t.Fatalf("ok=%v reason=%s", ok, reason)
	}
}

func TestPolicyAmountByBand(t *testing.T) {
	cases := []struct {
		name         string
		score        int
		invoiceCents int64
		want         int64
	}{
		{name: "800+ takes 80%", score: 800, invoiceCents: 100_000, want: 800_000},
		{name: "700-799 takes 50%", score: 799, invoiceCents: 100_000, want: 500_000},
		{name: "600-699 takes 30%", score: 600, invoiceCents: 100_000, want: 300_000},
		{name: "800+ capped at limit minus invoice", score: 850, invoiceCents: 400_000, want: 600_000},
		{name: "700-799 capped at limit minus invoice", score: 700, invoiceCents: 700_000, want: 300_000},
		{name: "600-699 capped at limit minus invoice", score: 650, invoiceCents: 900_000, want: 100_000},
		{name: "never negative", score: 820, invoiceCents: 1_200_000, want: 0},
	}
	amount := rules.NewPolicy().Amount
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := amount.Amount(domain.Customer{
				CreditScore: tc.score, CreditLimitCents: 1_000_000, CurrentInvoiceCents: tc.invoiceCents,
			})
			if got != tc.want {
				t.Fatalf("amount=%d want %d", got, tc.want)
			}
		})
	}
}
