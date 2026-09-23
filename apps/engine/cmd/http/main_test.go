package main

import (
	"strings"
	"testing"
	"uuid"

	"github.com/aws/aws-lambda-go/events"

	"engine/internal/flocitest"
)

const ana = `{"name":"Ana","cpf":"39053344705","credit_score":780,"current_invoice_cents":50000,"credit_limit_cents":500000,"monthly_spend_cents":[80000,90000,70000]}`

func TestComposeNeedsTheThreeTables(t *testing.T) {
	t.Setenv("DECISIONS_TABLE", "d")
	t.Setenv("BATCH_ITEMS_TABLE", "")
	t.Setenv("IDEMPOTENCY_KEYS_TABLE", "k")
	if _, err := compose(t.Context()); err == nil || !strings.Contains(err.Error(), "BATCH_ITEMS_TABLE") {
		t.Fatalf("err=%v", err)
	}
}

func TestComposeNeedsAValidBatchSize(t *testing.T) {
	t.Setenv("DECISIONS_TABLE", "d")
	t.Setenv("BATCH_ITEMS_TABLE", "i")
	t.Setenv("IDEMPOTENCY_KEYS_TABLE", "k")
	t.Setenv("BATCH_SIZE", "nope")
	if _, err := compose(t.Context()); err == nil || !strings.Contains(err.Error(), "BATCH_SIZE") {
		t.Fatalf("err=%v", err)
	}
}

// The HTTP Lambda as the stack wires it: each route writes its own table.
func TestTheHTTPLambdaWritesEachRouteToItsTable(t *testing.T) {
	cfg, _ := flocitest.Config(t)
	tables := flocitest.CreateTables(t, cfg)
	t.Setenv("DECISIONS_TABLE", tables.Decisions)
	t.Setenv("BATCH_ITEMS_TABLE", tables.Items)
	t.Setenv("IDEMPOTENCY_KEYS_TABLE", tables.Keys)
	h, err := compose(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	post := func(path, body string, headers map[string]string) {
		t.Helper()
		resp, err := h.Handle(t.Context(), events.APIGatewayV2HTTPRequest{
			Body: body, Headers: headers,
			RequestContext: events.APIGatewayV2HTTPRequestContext{
				HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: "POST", Path: path},
			},
		})
		if err != nil || resp.StatusCode >= 300 {
			t.Fatalf("POST %s: %d %s err=%v", path, resp.StatusCode, resp.Body, err)
		}
	}
	rows := func() [3]int {
		return [3]int{
			flocitest.Rows(t, cfg, tables.Decisions),
			flocitest.Rows(t, cfg, tables.Items),
			flocitest.Rows(t, cfg, tables.Keys),
		}
	}

	post("/evaluations", ana, nil)
	if got := rows(); got != [3]int{1, 0, 0} {
		t.Fatalf("after a single evaluation: %v", got)
	}
	post("/evaluations/batch", "["+ana+","+ana+"]", nil)
	if got := rows(); got != [3]int{1, 2, 0} {
		t.Fatalf("after a batch: %v", got)
	}
	post("/evaluations", ana, map[string]string{"idempotency-key": uuid.New().String()})
	if got := rows(); got != [3]int{2, 2, 1} {
		t.Fatalf("after a keyed evaluation: %v", got)
	}
}
