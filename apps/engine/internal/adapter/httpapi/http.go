package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/aws/aws-lambda-go/events"

	"engine/internal/batch"
	"engine/internal/domain"
	"engine/internal/evaluate"
)

type Handler struct {
	evaluate evaluate.Module
	batch    batch.Module
}

func New(ev evaluate.Module, b batch.Module) Handler {
	return Handler{evaluate: ev, batch: b}
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
	case method == "POST":
		return h.recoverItems(ctx, path), nil
	case method != "GET":
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
	id, r, err := h.evaluate.Evaluate(ctx, c)
	if err != nil {
		log5xx(err)
		return jsonResp(503, map[string]string{"error": "decision_not_recorded"}), nil
	}
	return jsonResp(200, decisionResponse{DecisionID: id, Result: r}), nil
}

func (h Handler) decision(ctx context.Context, id string) (events.APIGatewayV2HTTPResponse, error) {
	r, err := h.evaluate.Get(ctx, id)
	if errors.Is(err, evaluate.ErrNotFound) {
		return notFound(), nil
	}
	if err != nil {
		log5xx(err)
		return jsonResp(500, map[string]string{"error": "store_failed"}), nil
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
	acc, err := h.batch.Submit(ctx, customers)
	if errors.Is(err, batch.ErrTooLarge) {
		return jsonResp(422, map[string]any{"error": "batch_too_large", "max": h.batch.MaxCustomers()}), nil
	}
	if errors.Is(err, batch.ErrNotRecorded) {
		log5xx(err)
		return jsonResp(503, map[string]string{"error": "batch_not_recorded"}), nil
	}
	if err != nil {
		log5xx(err)
		return jsonResp(503, map[string]string{"error": "enqueue_failed"}), nil
	}
	return jsonResp(202, acc), nil
}

// recoverItems serves the operator routes on failed batch items.
func (h Handler) recoverItems(ctx context.Context, path string) events.APIGatewayV2HTTPResponse {
	if id, ok := pathID(path, "/batches/", "/retry-failed"); ok {
		n, err := h.batch.RetryFailed(ctx, id)
		if err != nil {
			return recoveryErr(err)
		}
		return jsonResp(202, map[string]int{"requeued": n})
	}
	id, index, action, ok := itemPath(path)
	if !ok {
		return notFound()
	}
	if action == "cancel" {
		if err := h.batch.Cancel(ctx, id, index); err != nil {
			return recoveryErr(err)
		}
		return jsonResp(200, map[string]any{"index": index, "status": batch.Cancelled})
	}
	attempts, err := h.batch.Retry(ctx, id, index)
	if err != nil {
		return recoveryErr(err)
	}
	return jsonResp(202, map[string]int{"index": index, "attempts": attempts})
}

// itemPath parses /batches/{id}/items/{index}/{retry|cancel}.
func itemPath(path string) (id string, index int, action string, ok bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) != 5 || parts[0] != "batches" || parts[1] == "" || parts[2] != "items" {
		return "", 0, "", false
	}
	if parts[4] != "retry" && parts[4] != "cancel" {
		return "", 0, "", false
	}
	index, err := strconv.Atoi(parts[3])
	if err != nil || index < 0 {
		return "", 0, "", false
	}
	return parts[1], index, parts[4], true
}

func recoveryErr(err error) events.APIGatewayV2HTTPResponse {
	switch {
	case errors.Is(err, batch.ErrNotFound):
		return notFound()
	case errors.Is(err, batch.ErrInvalidTransition):
		return jsonResp(409, map[string]string{"error": "invalid_transition"})
	case errors.Is(err, batch.ErrMaxAttempts):
		return jsonResp(409, map[string]string{"error": "max_attempts_reached"})
	case errors.Is(err, batch.ErrEnqueueFailed):
		log5xx(err)
		return jsonResp(503, map[string]string{"error": "enqueue_failed"})
	default:
		log5xx(err)
		return jsonResp(503, map[string]string{"error": "store_failed"})
	}
}

func (h Handler) batchReport(ctx context.Context, id string) (events.APIGatewayV2HTTPResponse, error) {
	rep, err := h.batch.Report(ctx, id)
	if errors.Is(err, batch.ErrNotFound) {
		return notFound(), nil
	}
	if err != nil {
		log5xx(err)
		return jsonResp(500, map[string]string{"error": "store_failed"}), nil
	}
	return jsonResp(200, rep), nil
}

func notFound() events.APIGatewayV2HTTPResponse {
	return jsonResp(404, map[string]string{"error": "not_found"})
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
	body, err := json.Marshal(v)
	if err != nil {
		log5xx(err)
		code, body = 500, []byte(`{"error":"encode_failed"}`)
	}
	return events.APIGatewayV2HTTPResponse{
		StatusCode: code,
		Headers:    map[string]string{"content-type": "application/json"},
		Body:       string(body),
	}
}

func log5xx(err error) {
	slog.Error("handler_failed", slog.String("error", err.Error()))
}
