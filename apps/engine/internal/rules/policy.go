package rules

import "engine/internal/domain"

// Policy is what the product applies: ordered rules decide, the score bands
// size an approval.
type Policy struct {
	rules  []Rule
	amount scoreBands
}

// NewPolicy builds the product policy. Rule order is the decision: the first
// rule that denies decides.
func NewPolicy() Policy {
	return Policy{
		rules: []Rule{
			MinScore(600),
			MaxLatePayments(2),
			InvoiceWithinLimit(),
			RecentSpend(3, 9000),
		},
		amount: scoreBands{
			{minScore: 800, shareBPS: 8000},
			{minScore: 700, shareBPS: 5000},
			{minScore: 600, shareBPS: 3000},
		},
	}
}

// Evaluate applies the policy to one customer. It stores nothing.
func (p Policy) Evaluate(c domain.Customer) domain.Result {
	// A zero Policy has no rules and would approve everyone.
	if len(p.rules) == 0 {
		panic("rules: zero Policy, use NewPolicy")
	}
	out := domain.Result{Name: c.Name, CPFMasked: domain.MaskCPF(c.CPF)}
	for _, rule := range p.rules {
		if reason, denied := rule(c); denied {
			out.Decision = domain.Denied
			out.Reasons = []string{reason}
			return out
		}
	}
	out.Decision = domain.Approved
	out.Reasons = []string{"eligible"}
	out.RevolvingAmountCents = p.amount.amount(c)
	return out
}
