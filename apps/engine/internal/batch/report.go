package batch

import (
	"context"

	"engine/internal/domain"
)

// Status is derived from the items on every read; it is never stored.
type Status string

const (
	Processing     Status = "PROCESSING"
	NeedsAttention Status = "NEEDS_ATTENTION"
	Completed      Status = "COMPLETED"
)

type Counters struct {
	Queued    int `json:"queued"`
	Decided   int `json:"decided"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
}

// Entry is one batch item as an operator sees it: masked CPF, never the full one.
type Entry struct {
	Index                int             `json:"index"`
	Name                 string          `json:"name"`
	CPFMasked            string          `json:"cpf_masked"`
	Decision             domain.Decision `json:"decision,omitempty"`
	Reasons              []string        `json:"reasons"`
	RevolvingAmountCents int64           `json:"revolving_amount_cents"`
	Attempts             int             `json:"attempts"`
}

type Report struct {
	BatchID                   string   `json:"batch_id"`
	Status                    Status   `json:"status"`
	Counters                  Counters `json:"counters"`
	Approved                  []Entry  `json:"approved"`
	Denied                    []Entry  `json:"denied"`
	Failed                    []Entry  `json:"failed"`
	Cancelled                 []Entry  `json:"cancelled"`
	TotalRevolvingAmountCents int64    `json:"total_revolving_amount_cents"`
}

// Report reads every item of the batch and derives its status and totals.
func (m Module) Report(ctx context.Context, batchID string) (Report, error) {
	items, err := m.items.All(ctx, batchID)
	if err != nil {
		return Report{}, err
	}
	r := Report{
		BatchID:   batchID,
		Approved:  []Entry{},
		Denied:    []Entry{},
		Failed:    []Entry{},
		Cancelled: []Entry{},
	}
	for _, it := range items {
		e := entryOf(it)
		switch it.Status {
		case Queued:
			r.Counters.Queued++
		case Decided:
			r.Counters.Decided++
			r.addDecided(e)
		case Failed:
			r.Counters.Failed++
			r.Failed = append(r.Failed, e)
		case Cancelled:
			r.Counters.Cancelled++
			r.Cancelled = append(r.Cancelled, e)
		default:
			panic("unhandled item status: " + string(it.Status))
		}
	}
	r.Status = statusOf(r.Counters)
	return r, nil
}

func (r *Report) addDecided(e Entry) {
	switch e.Decision {
	case domain.Approved:
		r.Approved = append(r.Approved, e)
		r.TotalRevolvingAmountCents += e.RevolvingAmountCents
	case domain.Denied:
		r.Denied = append(r.Denied, e)
	default:
		panic("unhandled decision: " + e.Decision.String())
	}
}

func statusOf(c Counters) Status {
	switch {
	case c.Queued > 0:
		return Processing
	case c.Failed > 0:
		return NeedsAttention
	default:
		return Completed
	}
}

func entryOf(it Item) Entry {
	return Entry{
		Index:                it.Index,
		Name:                 it.Customer.Name,
		CPFMasked:            domain.MaskCPF(it.Customer.CPF),
		Decision:             it.Result.Decision,
		Reasons:              it.Result.Reasons,
		RevolvingAmountCents: it.Result.RevolvingAmountCents,
		Attempts:             it.Attempts,
	}
}
