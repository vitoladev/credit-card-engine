package submit

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"engine/internal/domain"
	"engine/internal/queue"
)

type Accepted struct {
	ReportID string `json:"report_id"`
	Queued   int    `json:"queued"`
}

type UseCase struct {
	pub queue.Publisher
}

func New(pub queue.Publisher) UseCase {
	return UseCase{pub: pub}
}

func (u UseCase) Execute(ctx context.Context, customers []domain.Customer) (Accepted, error) {
	id := newID()
	jobs := make([]queue.Job, len(customers))
	for i, c := range customers {
		jobs[i] = queue.Job{ReportID: id, Queued: len(customers), Customer: c}
	}
	if err := u.pub.Publish(ctx, jobs); err != nil {
		return Accepted{}, err
	}
	return Accepted{ReportID: id, Queued: len(customers)}, nil
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
