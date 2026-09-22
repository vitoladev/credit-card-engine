package rules

import (
	"fmt"

	"engine/internal/domain"
)

type MaxLatePayments struct {
	link
	Max int
}

func (r *MaxLatePayments) Name() string { return "max_late_payments" }

func (r *MaxLatePayments) Handle(c domain.Customer) (bool, string) {
	if c.LatePayments > r.Max {
		return false, fmt.Sprintf("late_payments_above_%d", r.Max)
	}
	return r.forward(c)
}
