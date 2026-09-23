package rules

import "engine/internal/domain"

// RecentSpend denies when the average spend of the last months exceeds
// maxShareBPS of the credit limit.
func RecentSpend(months int, maxShareBPS int64) Rule {
	return func(c domain.Customer) (string, bool) {
		if c.CreditLimitCents <= 0 {
			return "no_credit_limit", true
		}
		if len(c.MonthlySpendCents) == 0 {
			return "insufficient_spend_history", true
		}
		n := min(months, len(c.MonthlySpendCents))
		var sum int64
		for _, v := range c.MonthlySpendCents[len(c.MonthlySpendCents)-n:] {
			sum += v
		}
		avg := sum / int64(n)
		return "recent_spend_above_share", avg*10000 > c.CreditLimitCents*maxShareBPS
	}
}
