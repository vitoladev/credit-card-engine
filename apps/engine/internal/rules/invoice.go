package rules

import "engine/internal/domain"

type InvoiceWithinLimit struct{ link }

func (r *InvoiceWithinLimit) Name() string { return "invoice_within_limit" }

func (r *InvoiceWithinLimit) Handle(c domain.Customer) (bool, string) {
	if c.CurrentInvoiceCents > c.AvailableLimitCents {
		return false, "invoice_exceeds_available_limit"
	}
	return r.forward(c)
}
