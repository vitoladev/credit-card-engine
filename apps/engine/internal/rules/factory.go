package rules

// NewChain builds the product policy. Order is the decision: first deny wins.
func NewChain() Handler {
	score := &MinScore{Min: 600}
	late := &MaxLatePayments{Max: 2}
	invoice := &InvoiceWithinLimit{}
	spend := &RecentSpend{Months: 3, MaxShareBPS: 9000}
	score.SetNext(late).SetNext(invoice).SetNext(spend)
	return score
}
