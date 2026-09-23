package httpapi_test

import (
	"net/http"
	"strings"
	"testing"
	"uuid"

	"github.com/aws/aws-lambda-go/events"

	"engine/internal/flocitest"
)

// post sends body with an Idempotency-Key header, as API Gateway delivers it
// (header names lowercased).
func (h *harness) post(path, body, key string) events.APIGatewayV2HTTPResponse {
	h.t.Helper()
	resp, err := h.http.Handle(h.t.Context(), events.APIGatewayV2HTTPRequest{
		Body:    body,
		Headers: map[string]string{"idempotency-key": key},
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: "POST", Path: path},
		},
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return resp
}

func replayed(resp events.APIGatewayV2HTTPResponse) bool {
	return resp.Headers["idempotent-replayed"] == "true"
}

func TestSingleEvaluationWithTheSameKeyIsRecordedOnce(t *testing.T) {
	h := newHarness(t)
	key := uuid.New().String()
	body := customerJSON(t, func(map[string]any) {})

	first := h.post("/evaluations", body, key)
	h.want(first, http.StatusOK, "")
	if replayed(first) {
		t.Fatal("first response marked as replayed")
	}
	again := h.post("/evaluations", body, key)
	h.want(again, http.StatusOK, first.Body)
	if !replayed(again) {
		t.Fatalf("headers=%v", again.Headers)
	}
	// One decision and one idempotency key.
	if n := flocitest.Rows(t, h.cfg, h.tables.All()...); n != 2 {
		t.Fatalf("rows=%d", n)
	}
}

func TestBatchWithTheSameKeyIsSubmittedOnce(t *testing.T) {
	h := newHarness(t)
	key := uuid.New().String()
	first := h.post("/evaluations/batch", threeCustomers, key)
	h.want(first, http.StatusAccepted, "")
	again := h.post("/evaluations/batch", threeCustomers, key)
	h.want(again, http.StatusAccepted, first.Body)
	if !replayed(again) {
		t.Fatalf("headers=%v", again.Headers)
	}
	// Three batch items and one idempotency key.
	if n := flocitest.Rows(t, h.cfg, h.tables.All()...); n != 4 {
		t.Fatalf("rows=%d", n)
	}
	if got := h.attempts(); len(got) != 3 {
		t.Fatalf("attempts=%+v", got)
	}
}

func TestAKeyAnswersOnlyTheRequestItWasFirstUsedFor(t *testing.T) {
	h := newHarness(t)
	key := uuid.New().String()
	h.want(h.post("/evaluations", customerJSON(t, func(map[string]any) {}), key), http.StatusOK, "")

	other := customerJSON(t, func(c map[string]any) { c["credit_score"] = 400 })
	h.want(h.post("/evaluations", other, key), http.StatusUnprocessableEntity, `{"error":"idempotency_key_reused"}`)
	h.want(h.post("/evaluations/batch", threeCustomers, key), http.StatusUnprocessableEntity, `{"error":"idempotency_key_reused"}`)
}

func TestAFailedRequestFreesItsKey(t *testing.T) {
	h := newHarness(t)
	key := uuid.New().String()
	invalid := customerJSON(t, func(c map[string]any) { c["cpf"] = "123" })
	h.want(h.post("/evaluations", invalid, key), http.StatusUnprocessableEntity, "")

	// The Save of the decision (the second PutItem, after the claim) fails:
	// the request is 503 and the key is released again.
	valid := customerJSON(t, func(map[string]any) {})
	h.faults.FailCallsAfter("PutItem", 1, 1)
	h.want(h.post("/evaluations", valid, key), http.StatusServiceUnavailable, `{"error":"decision_not_recorded"}`)

	resp := h.post("/evaluations", valid, key)
	h.want(resp, http.StatusOK, "")
	if replayed(resp) {
		t.Fatal("a released key replayed a response")
	}
}

func TestInvalidOrUnstoredKeys(t *testing.T) {
	h := newHarness(t)
	body := customerJSON(t, func(map[string]any) {})
	bad := `{"error":"invalid_idempotency_key","max_length":255}`
	h.want(h.post("/evaluations", body, ""), http.StatusBadRequest, bad)
	h.want(h.post("/evaluations", body, "has space"), http.StatusBadRequest, bad)
	h.want(h.post("/evaluations", body, strings.Repeat("k", 256)), http.StatusBadRequest, bad)

	// The claim itself fails: nothing is evaluated or recorded.
	h.faults.FailCalls("PutItem", 1)
	h.want(h.post("/evaluations", body, uuid.New().String()), http.StatusServiceUnavailable, `{"error":"store_failed"}`)
	if n := flocitest.Rows(t, h.cfg, h.tables.All()...); n != 0 {
		t.Fatalf("rows=%d", n)
	}
}

func TestWithoutAKeyEveryRequestRuns(t *testing.T) {
	h := newHarness(t)
	body := customerJSON(t, func(map[string]any) {})
	a, b := h.do("POST", "/evaluations", body), h.do("POST", "/evaluations", body)
	h.want(a, http.StatusOK, "")
	h.want(b, http.StatusOK, "")
	if a.Body == b.Body {
		t.Fatal("two unkeyed requests share a decision")
	}
}
