package report

import (
	"context"

	"engine/internal/domain"
	"engine/internal/store"
)

type Snapshot struct {
	ReportID      string          `json:"report_id"`
	Processed     int             `json:"processed"`
	Approved      []domain.Result `json:"approved"`
	Denied        []domain.Result `json:"denied"`
	ReleasedCents int64           `json:"released_cents"`
}

func From(reportID string, items []domain.Result) Snapshot {
	s := Snapshot{ReportID: reportID, Processed: len(items)}
	for _, r := range items {
		switch r.Decision {
		case domain.Approved:
			s.Approved = append(s.Approved, r)
			s.ReleasedCents += r.RevolvingAmountCents
		case domain.Denied:
			s.Denied = append(s.Denied, r)
		default:
			unhandledDecision(r.Decision)
		}
	}
	return s
}

func unhandledDecision(d domain.Decision) {
	panic("unhandled decision: " + d.String())
}

type UseCase struct {
	store store.DecisionStore
}

func New(decisions store.DecisionStore) UseCase {
	return UseCase{store: decisions}
}

func (u UseCase) Execute(ctx context.Context, reportID string) (Snapshot, error) {
	items, err := u.store.List(ctx, reportID)
	if err != nil {
		return Snapshot{}, err
	}
	return From(reportID, items), nil
}
