package evaluate

import (
	"context"

	"engine/internal/domain"
	"engine/internal/rules"
	"engine/internal/store"
)

type UseCase struct {
	policy rules.Policy
	store  store.DecisionStore
}

func New(policy rules.Policy, decisions store.DecisionStore) UseCase {
	if policy.Chain == nil || policy.Amount == nil {
		policy = rules.NewPolicy()
	}
	if decisions == nil {
		decisions = store.NewMemory()
	}
	return UseCase{policy: policy, store: decisions}
}

func (u UseCase) Execute(ctx context.Context, reportID string, c domain.Customer) (domain.Result, error) {
	out := domain.Result{
		Name:      c.Name,
		CPFMasked: domain.MaskCPF(c.CPF),
		Decision:  domain.Approved,
	}
	ok, reason := u.policy.Chain.Handle(c)
	if !ok {
		out.Decision = domain.Denied
		out.Reasons = []string{reason}
		return u.save(ctx, reportID, out)
	}
	out.RevolvingAmountCents = u.policy.Amount.Amount(c)
	out.Reasons = []string{"eligible"}
	return u.save(ctx, reportID, out)
}

func (u UseCase) save(ctx context.Context, reportID string, r domain.Result) (domain.Result, error) {
	if err := u.store.Save(ctx, reportID, r); err != nil {
		return domain.Result{}, err
	}
	return r, nil
}
