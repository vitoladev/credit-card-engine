package domain

// Customer is the input a use case evaluates. Money is integer cents.
type Customer struct {
	Name                string  `json:"name"`
	CPF                 string  `json:"cpf"`
	CreditScore         int     `json:"credit_score"`
	CurrentInvoiceCents int64   `json:"current_invoice_cents"`
	AvailableLimitCents int64   `json:"available_limit_cents"`
	LatePayments        int     `json:"late_payments"`
	MonthlySpendCents   []int64 `json:"monthly_spend_cents"`
}

// MaskCPF keeps the last two digits for operator reports.
func MaskCPF(cpf string) string {
	if len(cpf) < 3 {
		return "***"
	}
	return "***" + cpf[len(cpf)-2:]
}
