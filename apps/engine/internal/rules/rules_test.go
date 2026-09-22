package rules_test

import (
	"testing"

	"engine/internal/domain"
	"engine/internal/rules"
)

func TestEachGate(t *testing.T) {
	ok := domain.Customer{
		Name: "Ana", CPF: "39053344705", CreditScore: 720,
		CurrentInvoiceCents: 80_000, AvailableLimitCents: 500_000,
		LatePayments: 0, MonthlySpendCents: []int64{100_000, 110_000, 90_000},
	}
	cases := []struct {
		name   string
		mutate func(*domain.Customer)
		rule   rules.Handler
		wantOK bool
	}{
		{name: "score too low", mutate: func(c *domain.Customer) { c.CreditScore = 500 }, rule: &rules.MinScore{Min: 600}, wantOK: false},
		{name: "score ok", mutate: func(*domain.Customer) {}, rule: &rules.MinScore{Min: 600}, wantOK: true},
		{name: "too many lates", mutate: func(c *domain.Customer) { c.LatePayments = 3 }, rule: &rules.MaxLatePayments{Max: 2}, wantOK: false},
		{name: "invoice over limit", mutate: func(c *domain.Customer) { c.CurrentInvoiceCents = 600_000 }, rule: &rules.InvoiceWithinLimit{}, wantOK: false},
		{name: "spend spike", mutate: func(c *domain.Customer) { c.MonthlySpendCents = []int64{480_000, 490_000, 500_000} }, rule: &rules.RecentSpend{Months: 3, MaxShareBPS: 9000}, wantOK: false},
		{name: "spend ok", mutate: func(*domain.Customer) {}, rule: &rules.RecentSpend{Months: 3, MaxShareBPS: 9000}, wantOK: true},
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
		})
	}
}

func TestChainFirstDenyWins(t *testing.T) {
	c := domain.Customer{
		Name: "Bruno", CPF: "12345678901", CreditScore: 400,
		CurrentInvoiceCents: 600_000, AvailableLimitCents: 500_000,
		LatePayments: 5,
	}
	ok, reason := rules.NewChain().Handle(c)
	if ok || reason != "score_below_600" {
		t.Fatalf("ok=%v reason=%s", ok, reason)
	}
}
