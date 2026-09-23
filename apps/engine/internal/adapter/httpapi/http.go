package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"uuid"

	"github.com/aws/aws-lambda-go/events"

	"engine/internal/batch"
	"engine/internal/domain"
	"engine/internal/evaluate"
	"engine/internal/idempotency"
)

// idempotencyHeader names the client's key. API Gateway lowercases header
// names in the event.
const idempotencyHeader = "idempotency-key"

type Handler struct {
	evaluate evaluate.Module
	batch    batch.Module
	keys     idempotency.Module
}

func New(ev evaluate.Module, b batch.Module, keys idempotency.Module) Handler {
	return Handler{evaluate: ev, batch: b, keys: keys}
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
		return h.once(ctx, req, h.one), nil
	case method == "POST" && path == "/evaluations/batch":
		return h.once(ctx, req, h.enqueue), nil
	case method == "POST":
		return h.recoverItems(ctx, path), nil
	case method != "GET":
		return notFound(), nil
	}
	if id, ok := pathID(path, "/evaluations/", ""); ok {
		return h.decision(ctx, id)
	}
	if id, ok := pathID(path, "/batches/", "/items"); ok {
		return h.listItems(ctx, id, req.QueryStringParameters), nil
	}
	return notFound(), nil
}

// once runs an evaluation route at most once per Idempotency-Key (ADR 0005). A
// request without the header runs every time.
func (h Handler) once(ctx context.Context, req events.APIGatewayV2HTTPRequest,
	route func(context.Context, events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse,
) events.APIGatewayV2HTTPResponse {
	key, ok := req.Headers[idempotencyHeader]
	if !ok {
		return route(ctx, req)
	}
	request := req.RequestContext.HTTP.Method + " " + req.RequestContext.HTTP.Path + "\n" + req.Body
	r, replayed, err := h.keys.Do(ctx, key, request, func() idempotency.Response {
		resp := route(ctx, req)
		return idempotency.Response{Status: resp.StatusCode, Body: resp.Body}
	})
	switch {
	case errors.Is(err, idempotency.ErrInvalidKey):
		return jsonResp(400, map[string]any{"error": "invalid_idempotency_key", "max_length": idempotency.MaxKeyLength})
	case errors.Is(err, idempotency.ErrKeyReused):
		return jsonResp(422, map[string]string{"error": "idempotency_key_reused"})
	case errors.Is(err, idempotency.ErrInProgress):
		return jsonResp(409, map[string]string{"error": "idempotency_key_in_progress"})
	case err != nil:
		log5xx(err)
		return jsonResp(503, map[string]string{"error": "store_failed"})
	}
	resp := events.APIGatewayV2HTTPResponse{
		StatusCode: r.Status,
		Headers:    map[string]string{"content-type": "application/json"},
		Body:       r.Body,
	}
	if replayed {
		resp.Headers["idempotent-replayed"] = "true"
	}
	return resp
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

func (h Handler) one(ctx context.Context, req events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	var c domain.Customer
	if err := json.Unmarshal([]byte(req.Body), &c); err != nil {
		return jsonResp(400, map[string]string{"error": "invalid_json"})
	}
	if v := c.Validate(); len(v) > 0 {
		return jsonResp(422, map[string]any{"error": "invalid_customer", "violations": v})
	}
	id, r, err := h.evaluate.Evaluate(ctx, c)
	if err != nil {
		log5xx(err)
		return jsonResp(503, map[string]string{"error": "decision_not_recorded"})
	}
	return jsonResp(200, decisionResponse{DecisionID: id, Result: r})
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

func (h Handler) enqueue(ctx context.Context, req events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	customers, err := parseCustomers(req.Body)
	if err != nil {
		return jsonResp(400, map[string]string{"error": "invalid_batch"})
	}
	var violations []indexedViolation
	for i := range customers {
		for _, v := range customers[i].Validate() {
			violations = append(violations, indexedViolation{Index: i, Violation: v})
		}
	}
	if len(violations) > 0 {
		return jsonResp(422, map[string]any{"error": "invalid_customer", "violations": violations})
	}
	acc, err := h.batch.Submit(ctx, customers)
	if errors.Is(err, batch.ErrTooLarge) {
		return jsonResp(422, map[string]any{"error": "batch_too_large", "max": h.batch.MaxCustomers()})
	}
	if err != nil {
		log5xx(err)
		return jsonResp(503, map[string]string{"error": "batch_not_recorded"})
	}
	return jsonResp(202, acc)
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
	id, itemID, action, ok := itemPath(path)
	if !ok {
		return notFound()
	}
	if action == "cancel" {
		if err := h.batch.Cancel(ctx, id, itemID); err != nil {
			return recoveryErr(err)
		}
		return jsonResp(200, map[string]any{"item_id": itemID, "status": batch.Cancelled})
	}
	attempts, err := h.batch.Retry(ctx, id, itemID)
	if err != nil {
		return recoveryErr(err)
	}
	return jsonResp(202, map[string]any{"item_id": itemID, "attempts": attempts})
}

// itemPath parses /batches/{id}/items/{item_id}/{retry|cancel}. An item ID
// that is not a UUID names no item, so it is a 404 before any store call.
func itemPath(path string) (id, itemID, action string, ok bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) != 5 || parts[0] != "batches" || parts[1] == "" || parts[2] != "items" {
		return "", "", "", false
	}
	if parts[4] != "retry" && parts[4] != "cancel" {
		return "", "", "", false
	}
	if _, err := uuid.Parse(parts[3]); err != nil {
		return "", "", "", false
	}
	return parts[1], parts[3], parts[4], true
}

func recoveryErr(err error) events.APIGatewayV2HTTPResponse {
	switch {
	case errors.Is(err, batch.ErrNotFound):
		return notFound()
	case errors.Is(err, batch.ErrInvalidTransition):
		return jsonResp(409, map[string]string{"error": "invalid_transition"})
	case errors.Is(err, batch.ErrMaxAttempts):
		return jsonResp(409, map[string]string{"error": "max_attempts_reached"})
	default:
		log5xx(err)
		return jsonResp(503, map[string]string{"error": "store_failed"})
	}
}

// listItems serves GET /batches/{id}/items?status=&limit=&cursor=.
func (h Handler) listItems(ctx context.Context, id string, params map[string]string) events.APIGatewayV2HTTPResponse {
	q := batch.ListQuery{Cursor: params["cursor"], Limit: batch.DefaultPageLimit}
	if s, ok := params["status"]; ok {
		st, ok := batch.ParseStatus(s)
		if !ok {
			return jsonResp(400, map[string]string{"error": "invalid_status"})
		}
		q.Status = st
	}
	if s, ok := params["limit"]; ok {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > batch.MaxPageLimit {
			return jsonResp(400, map[string]any{"error": "invalid_limit", "max": batch.MaxPageLimit})
		}
		q.Limit = n
	}
	list, err := h.batch.ListItems(ctx, id, q)
	switch {
	case errors.Is(err, batch.ErrInvalidCursor):
		return jsonResp(400, map[string]string{"error": "invalid_cursor"})
	case errors.Is(err, batch.ErrNotFound):
		return notFound()
	case err != nil:
		log5xx(err)
		return jsonResp(500, map[string]string{"error": "store_failed"})
	}
	return jsonResp(200, list)
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
