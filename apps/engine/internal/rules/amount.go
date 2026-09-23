package rules

import "engine/internal/domain"

type band struct {
	minScore int
	shareBPS int64
}

// scoreBands grants a share of the credit limit by score, capped at the limit
// left after the current invoice. Bands are ordered by minScore, highest first.
type scoreBands []band

func (bands scoreBands) amount(c domain.Customer) int64 {
	var bps int64
	for _, b := range bands {
		if c.CreditScore >= b.minScore {
			bps = b.shareBPS
			break
		}
	}
	byScore := c.CreditLimitCents * bps / 10000
	headroom := c.CreditLimitCents - c.CurrentInvoiceCents
	return max(0, min(byScore, headroom))
}
