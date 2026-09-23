package rules_test

import (
	"testing"

	"engine/internal/domain"
	"engine/internal/rules"
)

var ana = domain.Customer{
	Name: "Ana", CPF: "39053344705", CreditScore: 720,
	CurrentInvoiceCents: 80_000, CreditLimitCents: 500_000,
	LatePayments: 0, MonthlySpendCents: []int64{100_000, 110_000, 90_000},
}

func TestEachRule(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*domain.Customer)
		rule   rules.Rule
		reason string
	}{
		{name: "score too low", mutate: func(c *domain.Customer) { c.CreditScore = 500 }, rule: rules.MinScore(600), reason: "score_below_600"},
		{name: "score ok", mutate: func(*domain.Customer) {}, rule: rules.MinScore(600)},
		{name: "too many lates", mutate: func(c *domain.Customer) { c.LatePayments = 3 }, rule: rules.MaxLatePayments(2), reason: "late_payments_above_2"},
		{name: "lates at the cap", mutate: func(c *domain.Customer) { c.LatePayments = 2 }, rule: rules.MaxLatePayments(2)},
		{name: "invoice over limit", mutate: func(c *domain.Customer) { c.CurrentInvoiceCents = 600_000 }, rule: rules.InvoiceWithinLimit(), reason: "invoice_exceeds_credit_limit"},
		{name: "spend spike", mutate: func(c *domain.Customer) { c.MonthlySpendCents = []int64{480_000, 490_000, 500_000} }, rule: rules.RecentSpend(3, 9000), reason: "recent_spend_above_share"},
		{name: "spend ok", mutate: func(*domain.Customer) {}, rule: rules.RecentSpend(3, 9000)},
		{name: "empty spend history", mutate: func(c *domain.Customer) { c.MonthlySpendCents = nil }, rule: rules.RecentSpend(3, 9000), reason: "insufficient_spend_history"},
		{name: "no credit limit", mutate: func(c *domain.Customer) { c.CreditLimitCents = 0; c.CurrentInvoiceCents = 0 }, rule: rules.RecentSpend(3, 9000), reason: "no_credit_limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := ana
			tc.mutate(&c)
			reason, denied := tc.rule(c)
			if denied != (tc.reason != "") {
				t.Fatalf("denied=%v reason=%s, want reason %q", denied, reason, tc.reason)
			}
			if denied && reason != tc.reason {
				t.Fatalf("reason=%s want %s", reason, tc.reason)
			}
		})
	}
}

func TestPolicyEvaluate(t *testing.T) {
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
		{name: "first deny wins", mutate: func(c *domain.Customer) {
			c.CreditScore, c.CurrentInvoiceCents, c.LatePayments = 400, 600_000, 5
		}, decision: domain.Denied, reason: "score_below_600"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := ana
			tc.mutate(&c)
			got := rules.NewPolicy().Evaluate(c)
			if got.Decision != tc.decision || len(got.Reasons) != 1 || got.Reasons[0] != tc.reason {
				t.Fatalf("got %+v, want %s %s", got, tc.decision, tc.reason)
			}
			if got.Decision == domain.Approved && got.RevolvingAmountCents <= 0 {
				t.Fatal("approved with zero amount")
			}
			if got.Decision == domain.Denied && got.RevolvingAmountCents != 0 {
				t.Fatalf("denied with amount %d", got.RevolvingAmountCents)
			}
			if got.Name != "Ana" || got.CPFMasked != "390.***.***-05" {
				t.Fatalf("result=%+v", got)
			}
		})
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
		{name: "invoice at the limit grants nothing", score: 820, invoiceCents: 1_000_000, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rules.NewPolicy().Evaluate(domain.Customer{
				Name: "Ana", CPF: "39053344705", CreditScore: tc.score,
				CreditLimitCents: 1_000_000, CurrentInvoiceCents: tc.invoiceCents,
				MonthlySpendCents: []int64{100_000},
			})
			if got.Decision != domain.Approved || got.RevolvingAmountCents != tc.want {
				t.Fatalf("got %+v, want approved with %d", got, tc.want)
			}
		})
	}
}

func TestZeroPolicyPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("zero Policy evaluated")
		}
	}()
	var p rules.Policy
	p.Evaluate(ana)
}
