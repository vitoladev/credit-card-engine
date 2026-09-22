package submit_test

import (
	"testing"

	"engine/internal/domain"
	"engine/internal/queue"
	"engine/internal/submit"
)

func TestExecuteEnqueuesOneJobPerCustomer(t *testing.T) {
	q := queue.NewMemory()
	got, err := submit.New(q).Execute(t.Context(), []domain.Customer{
		{Name: "Ana", CPF: "39053344705", CreditScore: 720},
		{Name: "Bruno", CPF: "12345678901", CreditScore: 400},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Queued != 2 || got.ReportID == "" {
		t.Fatalf("%+v", got)
	}
	if len(q.Jobs) != 2 {
		t.Fatalf("jobs=%d", len(q.Jobs))
	}
	if q.Jobs[0].ReportID != got.ReportID || q.Jobs[1].Customer.Name != "Bruno" {
		t.Fatalf("%+v", q.Jobs)
	}
}
