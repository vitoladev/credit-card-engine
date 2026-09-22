package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
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
	evaluate evaluate.UseCase
	submit   submit.UseCase
	report   report.UseCase
}

func New(ev evaluate.UseCase, sub submit.UseCase, rep report.UseCase) Handler {
	return Handler{evaluate: ev, submit: sub, report: rep}
}

func Default() Handler {
	mem := store.NewMemory()
	ev := evaluate.New(rules.NewChain(), mem)
	return New(ev, submit.New(queue.NewMemory()), report.New(mem))
}

func (h Handler) Handle(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	method := req.RequestContext.HTTP.Method
	path := req.RequestContext.HTTP.Path
	switch {
	case method == "GET" && path == "/health":
		return jsonResp(200, map[string]string{"status": "ok"}), nil
	case method == "POST" && path == "/evaluations":
		return h.one(ctx, req)
	case method == "POST" && path == "/evaluations/batch":
		return h.enqueue(ctx, req)
	case method == "GET" && (strings.HasPrefix(path, "/reports/") || req.PathParameters["id"] != ""):
		id := req.PathParameters["id"]
		if id == "" {
			id = strings.TrimPrefix(path, "/reports/")
		}
		return h.snapshot(ctx, id)
	default:
		return jsonResp(404, map[string]string{"error": "not_found"}), nil
	}
}

func (h Handler) one(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	var c domain.Customer
	if err := json.Unmarshal([]byte(req.Body), &c); err != nil {
		return jsonResp(400, map[string]string{"error": "invalid_customer"}), nil
	}
	r, err := h.evaluate.Execute(ctx, "sync", c)
	if err != nil {
		return jsonResp(500, map[string]string{"error": "store_failed"}), nil
	}
	return jsonResp(200, r), nil
}

func (h Handler) enqueue(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	customers, err := parseCustomers(req.Body)
	if err != nil {
		return jsonResp(400, map[string]string{"error": "invalid_batch"}), nil
	}
	acc, err := h.submit.Execute(ctx, customers)
	if err != nil {
		return jsonResp(500, map[string]string{"error": "enqueue_failed"}), nil
	}
	return jsonResp(202, acc), nil
}

func (h Handler) snapshot(ctx context.Context, id string) (events.APIGatewayV2HTTPResponse, error) {
	if id == "" {
		return jsonResp(400, map[string]string{"error": "missing_report_id"}), nil
	}
	snap, err := h.report.Execute(ctx, id)
	if err != nil {
		return jsonResp(500, map[string]string{"error": "store_failed"}), nil
	}
	return jsonResp(200, snap), nil
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
		return nil, fmt.Errorf("invalid_batch")
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
