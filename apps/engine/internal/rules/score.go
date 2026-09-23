package rules

import (
	"fmt"

	"engine/internal/domain"
)

func MinScore(minScore int) Rule {
	reason := fmt.Sprintf("score_below_%d", minScore)
	return func(c domain.Customer) (string, bool) {
		return reason, c.CreditScore < minScore
	}
}
