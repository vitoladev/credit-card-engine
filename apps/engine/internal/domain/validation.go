package domain

import (
	"strconv"
	"strings"
)

// Violation is one field that fails validation. Code is a stable API value.
type Violation struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

// Validate normalizes the CPF in place, so the customer that reaches a use
// case carries 11 bare digits, and returns every violation found.
func (c *Customer) Validate() []Violation {
	c.CPF = strings.NewReplacer(".", "", "-", "").Replace(c.CPF)
	var out []Violation
	if code := cpfCode(c.CPF); code != "" {
		out = append(out, Violation{Field: "cpf", Code: code})
	}
	if strings.TrimSpace(c.Name) == "" {
		out = append(out, Violation{Field: "name", Code: "required"})
	}
	negatives := []struct {
		field string
		value int64
	}{
		{"credit_score", int64(c.CreditScore)},
		{"current_invoice_cents", c.CurrentInvoiceCents},
		{"credit_limit_cents", c.CreditLimitCents},
		{"late_payments", int64(c.LatePayments)},
	}
	for _, n := range negatives {
		if n.value < 0 {
			out = append(out, Violation{Field: n.field, Code: "negative"})
		}
	}
	for i, v := range c.MonthlySpendCents {
		if v < 0 {
			out = append(out, Violation{Field: "monthly_spend_cents[" + strconv.Itoa(i) + "]", Code: "negative"})
		}
	}
	return out
}

func cpfCode(cpf string) string {
	if len(cpf) != 11 || strings.Trim(cpf, "0123456789") != "" {
		return "invalid_length"
	}
	if strings.Count(cpf, cpf[:1]) == 11 {
		return "repeated_digits"
	}
	if cpf[9] != checkDigit(cpf[:9]) || cpf[10] != checkDigit(cpf[:10]) {
		return "invalid_check_digits"
	}
	return ""
}

// checkDigit is the mod 11 CPF digit over the given prefix (9 or 10 digits).
func checkDigit(prefix string) byte {
	sum := 0
	weight := len(prefix) + 1
	for i := range len(prefix) {
		sum += int(prefix[i]-'0') * (weight - i)
	}
	return byte('0' + sum*10%11%10)
}
