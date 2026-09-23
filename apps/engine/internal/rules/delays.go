package rules

import (
	"fmt"

	"engine/internal/domain"
)

func MaxLatePayments(maxLate int) Rule {
	reason := fmt.Sprintf("late_payments_above_%d", maxLate)
	return func(c domain.Customer) (string, bool) {
		return reason, c.LatePayments > maxLate
	}
}
