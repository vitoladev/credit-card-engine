package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	"engine/internal/adapter/httpapi"
	"engine/internal/domain"
)

func TestHandleOneApproved(t *testing.T) {
	h := httpapi.Default()
	body, err := json.Marshal(domain.Customer{
		Name: "Ana", CPF: "39053344705", CreditScore: 720,
		CurrentInvoiceCents: 80_000, CreditLimitCents: 500_000,
		MonthlySpendCents: []int64{100_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := h.Handle(t.Context(), events.APIGatewayV2HTTPRequest{
		Body: string(body),
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
				Method: "POST",
				Path:   "/evaluations",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var got domain.Result
	if err := json.Unmarshal([]byte(resp.Body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Decision != domain.Approved {
		t.Fatalf("%+v", got)
	}
}

func TestHandleBatchAccepted(t *testing.T) {
	h := httpapi.Default()
	body := `[{"name":"Ana","cpf":"39053344705","credit_score":720,"current_invoice_cents":80000,"credit_limit_cents":500000,"monthly_spend_cents":[100000]}]`
	resp, err := h.Handle(t.Context(), events.APIGatewayV2HTTPRequest{
		Body: body,
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
				Method: "POST",
				Path:   "/evaluations/batch",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var acc struct {
		ReportID string `json:"report_id"`
		Queued   int    `json:"queued"`
	}
	if err := json.Unmarshal([]byte(resp.Body), &acc); err != nil {
		t.Fatal(err)
	}
	if acc.ReportID == "" || acc.Queued != 1 {
		t.Fatalf("%+v", acc)
	}
}

func TestHandleReportAfterOne(t *testing.T) {
	h := httpapi.Default()
	body, err := json.Marshal(domain.Customer{
		Name: "Ana", CPF: "39053344705", CreditScore: 720,
		CurrentInvoiceCents: 80_000, CreditLimitCents: 500_000,
		MonthlySpendCents: []int64{100_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Handle(t.Context(), events.APIGatewayV2HTTPRequest{
		Body: string(body),
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
				Method: "POST",
				Path:   "/evaluations",
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	resp, err := h.Handle(t.Context(), events.APIGatewayV2HTTPRequest{
		PathParameters: map[string]string{"id": "sync"},
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
				Method: "GET",
				Path:   "/reports/sync",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var snap struct {
		Processed int `json:"processed"`
	}
	if err := json.Unmarshal([]byte(resp.Body), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Processed != 1 {
		t.Fatalf("%+v", snap)
	}
}

func TestHandleUnknownRoute(t *testing.T) {
	resp, err := httpapi.Default().Handle(t.Context(), events.APIGatewayV2HTTPRequest{
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
				Method: "GET",
				Path:   "/nope",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}
