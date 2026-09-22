package rules

import "engine/internal/domain"

type Band struct {
	MinScore int
	ShareBPS int64
}

// ScoreBands grants a share of the credit limit by score, capped at the
// limit left after the current invoice. Bands are ordered by MinScore, highest first.
type ScoreBands struct {
	Bands []Band
}

func (p ScoreBands) Amount(c domain.Customer) int64 {
	var bps int64
	for _, b := range p.Bands {
		if c.CreditScore >= b.MinScore {
			bps = b.ShareBPS
			break
		}
	}
	byScore := c.CreditLimitCents * bps / 10000
	headroom := c.CreditLimitCents - c.CurrentInvoiceCents
	return max(0, min(byScore, headroom))
}
