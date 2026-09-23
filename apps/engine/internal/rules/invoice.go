package rules

import "engine/internal/domain"

func InvoiceWithinLimit() Rule {
	return func(c domain.Customer) (string, bool) {
		return "invoice_exceeds_credit_limit", c.CurrentInvoiceCents > c.CreditLimitCents
	}
}
