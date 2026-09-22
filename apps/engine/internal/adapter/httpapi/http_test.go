package httpapi_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"

	"engine/internal/adapter/ddb"
	"engine/internal/adapter/httpapi"
	"engine/internal/adapter/sqspub"
	"engine/internal/batch"
	"engine/internal/domain"
	"engine/internal/evaluate"
	"engine/internal/flocitest"
	"engine/internal/rules"
)

// Ana and Carla are approved, Bruno is denied.
const threeCustomers = `[
{"name":"Ana","cpf":"39053344705","credit_score":780,"current_invoice_cents":50000,"credit_limit_cents":500000,"monthly_spend_cents":[80000,90000,70000]},
{"name":"Bruno","cpf":"12345678909","credit_score":520,"current_invoice_cents":200000,"credit_limit_cents":400000,"monthly_spend_cents":[80000]},
{"name":"Carla","cpf":"98765432100","credit_score":820,"current_invoice_cents":100000,"credit_limit_cents":1000000,"monthly_spend_cents":[100000]}
]`

type discard struct{}

func (discard) Evaluated(string, domain.Result, time.Duration)        {}
func (discard) ItemDecided(string, int, domain.Result, time.Duration) {}
func (discard) ItemFailed(string, int, int)                           {}

// harness is the HTTP handler over the real modules on Floci.
type harness struct {
	t      *testing.T
	http   httpapi.Handler
	batch  batch.Module
	cfg    aws.Config
	faults *flocitest.Faults
	table  string
	queue  string
}

func newHarness(t *testing.T) *harness {
	return newHarnessWithBatchSize(t, batch.DefaultBatchSize)
}

func newHarnessWithBatchSize(t *testing.T, size int) *harness {
	t.Helper()
	cfg, faults := flocitest.Config(t)
	h := &harness{t: t, cfg: cfg, faults: faults, table: flocitest.Table(t, cfg), queue: flocitest.Queue(t, cfg)}
	st := ddb.New(cfg, h.table)
	h.batch = batch.New(batch.Deps{
		Items: st, Publisher: sqspub.New(cfg, h.queue), Policy: rules.NewPolicy(), Emitter: discard{}, MaxCustomers: size,
	})
	h.http = httpapi.New(evaluate.New(rules.NewPolicy(), st, discard{}), h.batch)
	return h
}

func (h *harness) do(method, path, body string) events.APIGatewayV2HTTPResponse {
	h.t.Helper()
	resp, err := h.http.Handle(h.t.Context(), events.APIGatewayV2HTTPRequest{
		Body: body,
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: method, Path: path},
		},
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return resp
}

func (h *harness) want(resp events.APIGatewayV2HTTPResponse, code int, body string) {
	h.t.Helper()
	if resp.StatusCode != code || (body != "" && resp.Body != body) {
		h.t.Fatalf("status=%d body=%s, want %d %s", resp.StatusCode, resp.Body, code, body)
	}
}

func (h *harness) attempts() []batch.Attempt {
	h.t.Helper()
	return flocitest.Decode[batch.Attempt](h.t, flocitest.Receive(h.t, h.cfg, h.queue))
}

// submit posts threeCustomers and returns the batch id.
func (h *harness) submit() string {
	h.t.Helper()
	resp := h.do("POST", "/evaluations/batch", threeCustomers)
	h.want(resp, http.StatusAccepted, "")
	var acc batch.Accepted
	if err := json.Unmarshal([]byte(resp.Body), &acc); err != nil {
		h.t.Fatal(err)
	}
	if acc.BatchID == "" || acc.Queued != 3 {
		h.t.Fatalf("body=%s", resp.Body)
	}
	return acc.BatchID
}

// failItem0 submits a batch, decides items 1 and 2, and dead-letters item 0.
func (h *harness) failItem0() string {
	h.t.Helper()
	id := h.submit()
	for _, a := range h.attempts() {
		var err error
		if a.Index == 0 {
			err = h.batch.DeadLetter(h.t.Context(), a)
		} else {
			err = h.batch.Process(h.t.Context(), a)
		}
		if err != nil {
			h.t.Fatal(err)
		}
	}
	return id
}

func TestHealth(t *testing.T) {
	h := newHarness(t)
	h.want(h.do("GET", "/health", ""), http.StatusOK, `{"status":"ok"}`)
}

func TestUnknownRoutesAndIDsReturn404(t *testing.T) {
	h := newHarness(t)
	id := h.submit()
	for _, r := range []struct{ method, path string }{
		{"GET", "/nope"},
		{"PUT", "/evaluations"},
		{"GET", "/evaluations/nope"},
		{"GET", "/batches/nope/report"},
		{"POST", "/batches/nope/items/0/retry"},
		{"POST", "/batches/nope/items/0/cancel"},
		{"POST", "/batches/" + id + "/items/3/retry"},
		{"POST", "/batches/" + id + "/items/x/retry"},
		{"POST", "/batches/" + id + "/items/-1/cancel"},
		{"POST", "/batches/" + id + "/items/0/delete"},
		{"POST", "/batches/nope/retry-failed"},
	} {
		if resp := h.do(r.method, r.path, ""); resp.StatusCode != http.StatusNotFound || resp.Body != `{"error":"not_found"}` {
			t.Errorf("%s %s: status=%d body=%s", r.method, r.path, resp.StatusCode, resp.Body)
		}
	}
}

func TestSingleEvaluationRoundTrip(t *testing.T) {
	h := newHarness(t)
	resp := h.do("POST", "/evaluations", customerJSON(t, func(map[string]any) {}))
	h.want(resp, http.StatusOK, "")
	var created struct {
		DecisionID string `json:"decision_id"`
		domain.Result
	}
	if err := json.Unmarshal([]byte(resp.Body), &created); err != nil {
		t.Fatal(err)
	}
	if created.DecisionID == "" || created.Decision != domain.Approved || created.CPFMasked != "***05" {
		t.Fatalf("body=%s", resp.Body)
	}
	h.want(h.do("GET", "/evaluations/"+created.DecisionID, ""), http.StatusOK, resp.Body)
}

func TestSingleEvaluationFailsClosedWith503(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h := newHarness(t)
	h.faults.FailCalls("PutItem", 1)
	h.want(h.do("POST", "/evaluations", customerJSON(t, func(map[string]any) {})), http.StatusServiceUnavailable, `{"error":"decision_not_recorded"}`)
	if n := flocitest.Rows(t, h.cfg, h.table); n != 0 {
		t.Fatalf("rows=%d", n)
	}
	logs := buf.String()
	if !strings.Contains(logs, `"msg":"handler_failed"`) {
		t.Fatalf("missing error log:\n%s", logs)
	}
	if strings.Contains(logs, "Ana") || strings.Contains(logs, "39053344705") {
		t.Fatalf("leaked customer data:\n%s", logs)
	}
}

func TestBatchReportRoute(t *testing.T) {
	h := newHarness(t)
	id := h.submit()
	resp := h.do("GET", "/batches/"+id+"/report", "")
	h.want(resp, http.StatusOK, "")
	var r batch.Report
	if err := json.Unmarshal([]byte(resp.Body), &r); err != nil {
		t.Fatal(err)
	}
	if r.BatchID != id || r.Status != batch.Processing || r.Counters != (batch.Counters{Queued: 3}) {
		t.Fatalf("body=%s", resp.Body)
	}
	for _, cpf := range []string{"39053344705", "12345678909", "98765432100"} {
		if strings.Contains(resp.Body, cpf) {
			t.Fatalf("report leaked full CPF: %s", resp.Body)
		}
	}
}

func TestBatchStoreFailureIs503(t *testing.T) {
	h := newHarness(t)
	h.faults.FailCalls("BatchWriteItem", 1)
	h.want(h.do("POST", "/evaluations/batch", threeCustomers), http.StatusServiceUnavailable, `{"error":"batch_not_recorded"}`)
}

func TestBatchSizeLimit(t *testing.T) {
	h := newHarnessWithBatchSize(t, 2)
	h.want(h.do("POST", "/evaluations/batch", threeCustomers), http.StatusUnprocessableEntity, `{"error":"batch_too_large","max":2}`)
}

func TestRecoveryRoutes(t *testing.T) {
	h := newHarness(t)
	id := h.failItem0()
	items := "/batches/" + id + "/items/"

	h.want(h.do("POST", items+"1/retry", ""), http.StatusConflict, `{"error":"invalid_transition"}`)
	h.faults.DropEntries(1)
	h.want(h.do("POST", items+"0/retry", ""), http.StatusServiceUnavailable, `{"error":"enqueue_failed"}`)
	h.want(h.do("POST", items+"0/retry", ""), http.StatusAccepted, `{"attempts":3,"index":0}`)
	h.want(h.do("POST", items+"0/cancel", ""), http.StatusConflict, `{"error":"invalid_transition"}`)
	h.want(h.do("POST", "/batches/"+id+"/retry-failed", ""), http.StatusAccepted, `{"requeued":0}`)

	a := h.attempts()
	if len(a) != 1 || a[0].Number != 3 {
		t.Fatalf("attempts=%+v", a)
	}
	if err := h.batch.DeadLetter(t.Context(), a[0]); err != nil {
		t.Fatal(err)
	}
	h.want(h.do("POST", items+"0/cancel", ""), http.StatusOK, `{"index":0,"status":"CANCELLED"}`)
	h.want(h.do("POST", items+"0/cancel", ""), http.StatusOK, `{"index":0,"status":"CANCELLED"}`)
}

func TestRetryAtMaxAttemptsIs409(t *testing.T) {
	h := newHarness(t)
	id := h.failItem0()
	for attempt := 2; attempt <= batch.MaxAttempts; attempt++ {
		h.want(h.do("POST", "/batches/"+id+"/items/0/retry", ""), http.StatusAccepted, `{"attempts":`+strconv.Itoa(attempt)+`,"index":0}`)
		for _, a := range h.attempts() {
			if err := h.batch.DeadLetter(t.Context(), a); err != nil {
				t.Fatal(err)
			}
		}
	}
	h.want(h.do("POST", "/batches/"+id+"/items/0/retry", ""), http.StatusConflict, `{"error":"max_attempts_reached"}`)
}

// API Gateway base64-encodes bodies whose content type is not text, which is
// what `curl -d` sends (application/x-www-form-urlencoded).
func TestBase64EncodedBodiesAreDecoded(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		path, body string
		code       int
	}{
		{"/evaluations", customerJSON(t, func(map[string]any) {}), http.StatusOK},
		{"/evaluations/batch", threeCustomers, http.StatusAccepted},
	} {
		resp, err := h.http.Handle(t.Context(), events.APIGatewayV2HTTPRequest{
			Body:            base64.StdEncoding.EncodeToString([]byte(tc.body)),
			IsBase64Encoded: true,
			RequestContext: events.APIGatewayV2HTTPRequestContext{
				HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{Method: "POST", Path: tc.path},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != tc.code {
			t.Fatalf("%s: status=%d body=%s", tc.path, resp.StatusCode, resp.Body)
		}
	}
}
