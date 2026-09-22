package rules

import "engine/internal/domain"

// Policy is what the product applies: the chain decides, the amount policy
// sizes an approval.
type Policy struct {
	Chain  Handler
	Amount AmountPolicy
}

// NewPolicy builds the product policy. Chain order is the decision: first deny wins.
func NewPolicy() Policy {
	score := &MinScore{Min: 600}
	late := &MaxLatePayments{Max: 2}
	invoice := &InvoiceWithinLimit{}
	spend := &RecentSpend{Months: 3, MaxShareBPS: 9000}
	score.SetNext(late).SetNext(invoice).SetNext(spend)
	return Policy{
		Chain: score,
		Amount: ScoreBands{Bands: []Band{
			{MinScore: 800, ShareBPS: 8000},
			{MinScore: 700, ShareBPS: 5000},
			{MinScore: 600, ShareBPS: 3000},
		}},
	}
}

// AmountPolicy sizes the revolving amount of a customer the chain approved.
type AmountPolicy interface {
	Amount(c domain.Customer) int64
}
