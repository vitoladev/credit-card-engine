package rules

import "engine/internal/domain"

type RecentSpend struct {
	link
	Months      int
	MaxShareBPS int64
}

func (r *RecentSpend) Name() string { return "recent_spend" }

func (r *RecentSpend) Handle(c domain.Customer) (bool, string) {
	if c.CreditLimitCents <= 0 {
		return false, "no_credit_limit"
	}
	if len(c.MonthlySpendCents) == 0 {
		return false, "insufficient_spend_history"
	}
	n := min(r.Months, len(c.MonthlySpendCents))
	var sum int64
	for _, v := range c.MonthlySpendCents[len(c.MonthlySpendCents)-n:] {
		sum += v
	}
	avg := sum / int64(n)
	if avg*10000 > c.CreditLimitCents*r.MaxShareBPS {
		return false, "recent_spend_above_share"
	}
	return r.forward(c)
}
