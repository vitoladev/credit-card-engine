package rules

import (
	"fmt"

	"engine/internal/domain"
)

type MinScore struct {
	link
	Min int
}

func (r *MinScore) Name() string { return "min_score" }

func (r *MinScore) Handle(c domain.Customer) (bool, string) {
	if c.CreditScore < r.Min {
		return false, fmt.Sprintf("score_below_%d", r.Min)
	}
	return r.forward(c)
}
