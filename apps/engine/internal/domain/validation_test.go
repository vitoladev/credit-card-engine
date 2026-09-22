package domain_test

import (
	"slices"
	"testing"

	"engine/internal/domain"
)

func validCustomer() domain.Customer {
	return domain.Customer{
		Name: "Ana", CPF: "39053344705", CreditScore: 720,
		CurrentInvoiceCents: 80_000, CreditLimitCents: 500_000,
		MonthlySpendCents: []int64{100_000, 110_000, 90_000},
	}
}

func TestValidateCPF(t *testing.T) {
	cases := []struct {
		name, cpf, wantCode, wantCPF string
	}{
		{name: "valid bare", cpf: "39053344705", wantCPF: "39053344705"},
		{name: "valid masked", cpf: "390.533.447-05", wantCPF: "39053344705"},
		{name: "wrong first digit", cpf: "39053344715", wantCode: "invalid_check_digits"},
		{name: "wrong second digit", cpf: "39053344706", wantCode: "invalid_check_digits"},
		{name: "repeated", cpf: "11111111111", wantCode: "repeated_digits"},
		{name: "short", cpf: "3905334470", wantCode: "invalid_length"},
		{name: "long", cpf: "390533447051", wantCode: "invalid_length"},
		{name: "letters", cpf: "3905334470a", wantCode: "invalid_length"},
		{name: "empty", cpf: "", wantCode: "invalid_length"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validCustomer()
			c.CPF = tc.cpf
			got := c.Validate()
			if tc.wantCode == "" {
				if len(got) != 0 {
					t.Fatalf("violations=%v", got)
				}
				if c.CPF != tc.wantCPF {
					t.Fatalf("cpf=%q want %q", c.CPF, tc.wantCPF)
				}
				return
			}
			want := []domain.Violation{{Field: "cpf", Code: tc.wantCode}}
			if !slices.Equal(got, want) {
				t.Fatalf("violations=%v want %v", got, want)
			}
		})
	}
}

func TestValidateFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*domain.Customer)
		want   []domain.Violation
	}{
		{name: "valid", mutate: func(*domain.Customer) {}},
		{name: "blank name", mutate: func(c *domain.Customer) { c.Name = "  " }, want: []domain.Violation{{Field: "name", Code: "required"}}},
		{name: "negative score", mutate: func(c *domain.Customer) { c.CreditScore = -1 }, want: []domain.Violation{{Field: "credit_score", Code: "negative"}}},
		{name: "negative invoice", mutate: func(c *domain.Customer) { c.CurrentInvoiceCents = -1 }, want: []domain.Violation{{Field: "current_invoice_cents", Code: "negative"}}},
		{name: "negative limit", mutate: func(c *domain.Customer) { c.CreditLimitCents = -1 }, want: []domain.Violation{{Field: "credit_limit_cents", Code: "negative"}}},
		{name: "negative lates", mutate: func(c *domain.Customer) { c.LatePayments = -1 }, want: []domain.Violation{{Field: "late_payments", Code: "negative"}}},
		{name: "negative spend entry", mutate: func(c *domain.Customer) { c.MonthlySpendCents[1] = -1 }, want: []domain.Violation{{Field: "monthly_spend_cents[1]", Code: "negative"}}},
		{
			name: "every violation",
			mutate: func(c *domain.Customer) {
				c.CPF = "123"
				c.Name = ""
				c.CreditScore = -5
			},
			want: []domain.Violation{
				{Field: "cpf", Code: "invalid_length"},
				{Field: "name", Code: "required"},
				{Field: "credit_score", Code: "negative"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validCustomer()
			tc.mutate(&c)
			if got := c.Validate(); !slices.Equal(got, tc.want) {
				t.Fatalf("violations=%v want %v", got, tc.want)
			}
		})
	}
}
