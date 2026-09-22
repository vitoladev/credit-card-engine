package rules

import "engine/internal/domain"

// Rule is one eligibility criterion. It denies with a stable reason code or
// passes.
type Rule func(c domain.Customer) (reason string, denied bool)
