package evaluate

import (
	"context"

	"engine/internal/domain"
	"engine/internal/rules"
)

// UseCase decides; it stores nothing. The caller records the decision.
type UseCase struct {
	policy rules.Policy
}

func New(policy rules.Policy) UseCase {
	if policy.Chain == nil || policy.Amount == nil {
		policy = rules.NewPolicy()
	}
	return UseCase{policy: policy}
}

func (u UseCase) Execute(_ context.Context, c domain.Customer) domain.Result {
	out := domain.Result{
		Name:      c.Name,
		CPFMasked: domain.MaskCPF(c.CPF),
		Decision:  domain.Approved,
	}
	ok, reason := u.policy.Chain.Handle(c)
	if !ok {
		out.Decision = domain.Denied
		out.Reasons = []string{reason}
		return out
	}
	out.RevolvingAmountCents = u.policy.Amount.Amount(c)
	out.Reasons = []string{"eligible"}
	return out
}
