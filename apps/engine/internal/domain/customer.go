package domain

// Customer is the input a use case evaluates. Money is integer cents.
type Customer struct {
	Name                string  `json:"name"`
	CPF                 string  `json:"cpf"`
	CreditScore         int     `json:"credit_score"`
	CurrentInvoiceCents int64   `json:"current_invoice_cents"`
	CreditLimitCents    int64   `json:"credit_limit_cents"`
	LatePayments        int     `json:"late_payments"`
	MonthlySpendCents   []int64 `json:"monthly_spend_cents"`
}

// MaskCPF keeps the first three and last two digits, in CPF punctuation.
func MaskCPF(cpf string) string {
	if len(cpf) != 11 {
		return "***"
	}
	return cpf[:3] + ".***.***-" + cpf[9:]
}
