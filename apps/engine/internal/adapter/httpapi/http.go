package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/aws/aws-lambda-go/events"

	"engine/internal/domain"
	"engine/internal/evaluate"
	"engine/internal/queue"
	"engine/internal/report"
	"engine/internal/rules"
	"engine/internal/store"
	"engine/internal/submit"
)

type Handler struct {
	evaluate  evaluate.UseCase
	decisions store.DecisionStore
	submit    submit.UseCase
	report    report.UseCase
}

func New(ev evaluate.UseCase, decisions store.DecisionStore, sub submit.UseCase, rep report.UseCase) Handler {
	return Handler{evaluate: ev, decisions: decisions, submit: sub, report: rep}
}

func Default() Handler {
	mem := store.NewMemory()
	return New(evaluate.New(rules.NewPolicy()), mem, submit.New(mem, queue.NewMemory(), submit.DefaultBatchSize), report.New(mem))
}

func (h Handler) Handle(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	method := req.RequestContext.HTTP.Method
	path := req.RequestContext.HTTP.Path
	// API Gateway base64-encodes bodies with a non-text content type, such as
	// the application/x-www-form-urlencoded that `curl -d` sends.
	if req.IsBase64Encoded {
		body, err := base64.StdEncoding.DecodeString(req.Body)
		if err != nil {
			return jsonResp(400, map[string]string{"error": "invalid_body"}), nil //nolint:nilerr // the adapter maps the error to a status code
		}
		req.Body = string(body)
	}
	switch {
	case method == "GET" && path == "/health":
		return jsonResp(200, map[string]string{"status": "ok"}), nil
	case method == "POST" && path == "/evaluations":
		return h.one(ctx, req)
	case method == "POST" && path == "/evaluations/batch":
		return h.enqueue(ctx, req)
	}
	if method != "GET" {
		return notFound(), nil
	}
	if id, ok := pathID(path, "/evaluations/", ""); ok {
		return h.decision(ctx, id)
	}
	if id, ok := pathID(path, "/batches/", "/report"); ok {
		return h.batchReport(ctx, id)
	}
	return notFound(), nil
}

// pathID returns the single segment between prefix and suffix.
func pathID(path, prefix, suffix string) (string, bool) {
	rest, ok := strings.CutPrefix(path, prefix)
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, suffix)
	if !ok || id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return id, true
}

type decisionResponse struct {
	DecisionID string `json:"decision_id"`
	domain.Result
}

func (h Handler) one(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	var c domain.Customer
	if err := json.Unmarshal([]byte(req.Body), &c); err != nil {
		return jsonResp(400, map[string]string{"error": "invalid_json"}), nil //nolint:nilerr // the adapter maps the error to a status code
	}
	if v := c.Validate(); len(v) > 0 {
		return jsonResp(422, map[string]any{"error": "invalid_customer", "violations": v}), nil
	}
	r := h.evaluate.Execute(ctx, c)
	id := newID()
	if err := h.decisions.Save(ctx, id, c, r); err != nil {
		return jsonResp(500, map[string]string{"error": "store_failed"}), nil //nolint:nilerr // the adapter maps the error to a status code
	}
	return jsonResp(200, decisionResponse{DecisionID: id, Result: r}), nil
}

func (h Handler) decision(ctx context.Context, id string) (events.APIGatewayV2HTTPResponse, error) {
	r, err := h.decisions.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return notFound(), nil
	}
	if err != nil {
		return jsonResp(500, map[string]string{"error": "store_failed"}), nil //nolint:nilerr // the adapter maps the error to a status code
	}
	return jsonResp(200, decisionResponse{DecisionID: id, Result: r}), nil
}

func (h Handler) enqueue(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	customers, err := parseCustomers(req.Body)
	if err != nil {
		return jsonResp(400, map[string]string{"error": "invalid_batch"}), nil //nolint:nilerr // the adapter maps the error to a status code
	}
	var violations []indexedViolation
	for i := range customers {
		for _, v := range customers[i].Validate() {
			violations = append(violations, indexedViolation{Index: i, Violation: v})
		}
	}
	if len(violations) > 0 {
		return jsonResp(422, map[string]any{"error": "invalid_customer", "violations": violations}), nil
	}
	acc, err := h.submit.Execute(ctx, customers)
	if errors.Is(err, submit.ErrBatchTooLarge) {
		return jsonResp(422, map[string]any{"error": "batch_too_large", "max": h.submit.MaxCustomers()}), nil
	}
	if err != nil {
		return jsonResp(500, map[string]string{"error": "enqueue_failed"}), nil //nolint:nilerr // the adapter maps the error to a status code
	}
	return jsonResp(202, acc), nil
}

func (h Handler) batchReport(ctx context.Context, id string) (events.APIGatewayV2HTTPResponse, error) {
	rep, err := h.report.Execute(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return notFound(), nil
	}
	if err != nil {
		return jsonResp(500, map[string]string{"error": "store_failed"}), nil //nolint:nilerr // the adapter maps the error to a status code
	}
	return jsonResp(200, rep), nil
}

func notFound() events.APIGatewayV2HTTPResponse {
	return jsonResp(404, map[string]string{"error": "not_found"})
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

type indexedViolation struct {
	Index int `json:"index"`
	domain.Violation
}

func parseCustomers(body string) ([]domain.Customer, error) {
	var in struct {
		Customers []domain.Customer `json:"customers"`
	}
	body = strings.TrimSpace(body)
	if err := json.Unmarshal([]byte(body), &in); err == nil && len(in.Customers) > 0 {
		return in.Customers, nil
	}
	var list []domain.Customer
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list) == 0 {
		return nil, errors.New("invalid_batch")
	}
	return list, nil
}

func jsonResp(code int, v any) events.APIGatewayV2HTTPResponse {
	b, err := json.Marshal(v)
	if err != nil {
		return events.APIGatewayV2HTTPResponse{
			StatusCode: 500,
			Headers:    map[string]string{"content-type": "application/json"},
			Body:       `{"error":"encode_failed"}`,
		}
	}
	return events.APIGatewayV2HTTPResponse{
		StatusCode: code,
		Headers:    map[string]string{"content-type": "application/json"},
		Body:       string(b),
	}
}
