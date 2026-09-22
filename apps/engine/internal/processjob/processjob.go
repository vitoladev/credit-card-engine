package processjob

import (
	"context"
	"encoding/json"
	"errors"

	"engine/internal/evaluate"
	"engine/internal/queue"
)

type UseCase struct {
	evaluate evaluate.UseCase
}

func New(ev evaluate.UseCase) UseCase {
	return UseCase{evaluate: ev}
}

func (u UseCase) Execute(ctx context.Context, body []byte) error {
	var job queue.Job
	if err := json.Unmarshal(body, &job); err != nil {
		return err
	}
	if job.ReportID == "" {
		return errors.New("missing report_id")
	}
	_, err := u.evaluate.Execute(ctx, job.ReportID, job.Customer)
	return err
}
