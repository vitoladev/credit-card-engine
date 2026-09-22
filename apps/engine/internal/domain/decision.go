package domain

type Decision string

const (
	Approved Decision = "APPROVED"
	Denied   Decision = "DENIED"
)

type Result struct {
	Name           string   `json:"name"`
	CPFMasked      string   `json:"cpf_masked"`
	Decision       Decision `json:"decision"`
	MaxAmountCents int64    `json:"max_amount_cents"`
	Reasons        []string `json:"reasons"`
}

func (d Decision) String() string {
	return string(d)
}
