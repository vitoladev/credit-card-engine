package evaluate

import (
	"context"

	"engine/internal/domain"
	"engine/internal/rules"
	"engine/internal/store"
)

type UseCase struct {
	chain rules.Handler
	store store.DecisionStore
}

func New(chain rules.Handler, decisions store.DecisionStore) UseCase {
	if chain == nil {
		chain = rules.NewChain()
	}
	if decisions == nil {
		decisions = store.NewMemory()
	}
	return UseCase{chain: chain, store: decisions}
}

func (u UseCase) Execute(ctx context.Context, reportID string, c domain.Customer) (domain.Result, error) {
	out := domain.Result{
		Name:      c.Name,
		CPFMasked: domain.MaskCPF(c.CPF),
		Decision:  domain.Approved,
	}
	ok, reason := u.chain.Handle(c)
	if !ok {
		out.Decision = domain.Denied
		out.Reasons = []string{reason}
		return u.save(ctx, reportID, out)
	}
	out.MaxAmountCents = amount(c)
	out.Reasons = []string{"eligible"}
	return u.save(ctx, reportID, out)
}

func (u UseCase) save(ctx context.Context, reportID string, r domain.Result) (domain.Result, error) {
	if err := u.store.Save(ctx, reportID, r); err != nil {
		return domain.Result{}, err
	}
	return r, nil
}

func amount(c domain.Customer) int64 {
	var bps int64
	switch {
	case c.CreditScore >= 800:
		bps = 8000
	case c.CreditScore >= 700:
		bps = 5000
	default:
		bps = 3000
	}
	byScore := c.AvailableLimitCents * bps / 10000
	headroom := c.AvailableLimitCents - c.CurrentInvoiceCents
	return max(0, min(byScore, headroom))
}
