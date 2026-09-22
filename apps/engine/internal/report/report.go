package report

import (
	"context"

	"engine/internal/domain"
	"engine/internal/store"
)

// Status is derived from the counters on every read; it is never stored.
type Status string

const (
	Processing     Status = "PROCESSING"
	NeedsAttention Status = "NEEDS_ATTENTION"
	Completed      Status = "COMPLETED"
)

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
	BatchID                   string         `json:"batch_id"`
	Status                    Status         `json:"status"`
	Counters                  store.Counters `json:"counters"`
	Approved                  []Entry        `json:"approved"`
	Denied                    []Entry        `json:"denied"`
	Failed                    []Entry        `json:"failed"`
	Cancelled                 []Entry        `json:"cancelled"`
	TotalRevolvingAmountCents int64          `json:"total_revolving_amount_cents"`
}

func From(b store.Batch) Report {
	r := Report{
		BatchID:   b.ID,
		Status:    statusOf(b.Counters),
		Counters:  b.Counters,
		Approved:  []Entry{},
		Denied:    []Entry{},
		Failed:    []Entry{},
		Cancelled: []Entry{},
	}
	for _, it := range b.Items {
		e := entryOf(it)
		switch it.Status {
		case store.Queued:
		case store.Decided:
			r.addDecided(e)
		case store.Failed:
			r.Failed = append(r.Failed, e)
		case store.Cancelled:
			r.Cancelled = append(r.Cancelled, e)
		default:
			panic("unhandled item status: " + string(it.Status))
		}
	}
	return r
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

func statusOf(c store.Counters) Status {
	switch {
	case c.Queued > 0:
		return Processing
	case c.Failed > 0:
		return NeedsAttention
	default:
		return Completed
	}
}

func entryOf(it store.Item) Entry {
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

type UseCase struct {
	batches store.BatchStore
}

func New(batches store.BatchStore) UseCase {
	return UseCase{batches: batches}
}

func (u UseCase) Execute(ctx context.Context, batchID string) (Report, error) {
	b, err := u.batches.Report(ctx, batchID)
	if err != nil {
		return Report{}, err
	}
	return From(b), nil
}
