package rules

import "engine/internal/domain"

type RecentSpend struct {
	link
	Months      int
	MaxShareBPS int64
}

func (r *RecentSpend) Name() string { return "recent_spend" }

func (r *RecentSpend) Handle(c domain.Customer) (bool, string) {
	if c.AvailableLimitCents <= 0 {
		return false, "no_available_limit"
	}
	n := min(r.Months, len(c.MonthlySpendCents))
	if n == 0 {
		return r.forward(c)
	}
	var sum int64
	for _, v := range c.MonthlySpendCents[len(c.MonthlySpendCents)-n:] {
		sum += v
	}
	avg := sum / int64(n)
	if avg*10000 > c.AvailableLimitCents*r.MaxShareBPS {
		return false, "recent_spend_above_share"
	}
	return r.forward(c)
}
