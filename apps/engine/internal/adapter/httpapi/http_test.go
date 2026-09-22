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
	"uuid"

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

func (discard) Evaluated(string, domain.Result, time.Duration)           {}
func (discard) ItemDecided(string, string, domain.Result, time.Duration) {}
func (discard) ItemFailed(string, string, int)                           {}

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
	return h.get(method, path, body, nil)
}

func (h *harness) get(method, path, body string, query map[string]string) events.APIGatewayV2HTTPResponse {
	h.t.Helper()
	resp, err := h.http.Handle(h.t.Context(), events.APIGatewayV2HTTPRequest{
		Body:                  body,
		QueryStringParameters: query,
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

// submit posts threeCustomers and returns the accepted batch.
func (h *harness) submit() batch.Accepted {
	h.t.Helper()
	resp := h.do("POST", "/evaluations/batch", threeCustomers)
	h.want(resp, http.StatusAccepted, "")
	var acc batch.Accepted
	if err := json.Unmarshal([]byte(resp.Body), &acc); err != nil {
		h.t.Fatal(err)
	}
	if acc.BatchID == "" || acc.Queued != 3 || len(acc.ItemIDs) != 3 {
		h.t.Fatalf("body=%s", resp.Body)
	}
	return acc
}

// failFirst submits a batch, decides its second and third items, and
// dead-letters the first one.
func (h *harness) failFirst() batch.Accepted {
	h.t.Helper()
	acc := h.submit()
	for _, a := range h.attempts() {
		var err error
		if a.ItemID == acc.ItemIDs[0] {
			err = h.batch.DeadLetter(h.t.Context(), a)
		} else {
			err = h.batch.Process(h.t.Context(), a)
		}
		if err != nil {
			h.t.Fatal(err)
		}
	}
	return acc
}

func TestHealth(t *testing.T) {
	h := newHarness(t)
	h.want(h.do("GET", "/health", ""), http.StatusOK, `{"status":"ok"}`)
}

func TestUnknownRoutesAndIDsReturn404(t *testing.T) {
	h := newHarness(t)
	acc := h.submit()
	id, item, unknown := acc.BatchID, acc.ItemIDs[0], uuid.NewV7().String()
	for _, r := range []struct{ method, path string }{
		{"GET", "/nope"},
		{"PUT", "/evaluations"},
		{"GET", "/evaluations/nope"},
		{"GET", "/batches/nope/items"},
		{"GET", "/batches/" + id + "/report"},
		{"POST", "/batches/nope/items/" + item + "/retry"},
		{"POST", "/batches/nope/items/" + item + "/cancel"},
		{"POST", "/batches/" + id + "/items/" + unknown + "/retry"},
		{"POST", "/batches/" + id + "/items/0/retry"},
		{"POST", "/batches/" + id + "/items/x/cancel"},
		{"POST", "/batches/" + id + "/items/" + item + "/delete"},
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

func (h *harness) list(id string, query map[string]string) batch.ItemList {
	h.t.Helper()
	resp := h.get("GET", "/batches/"+id+"/items", "", query)
	h.want(resp, http.StatusOK, "")
	var l batch.ItemList
	if err := json.Unmarshal([]byte(resp.Body), &l); err != nil {
		h.t.Fatal(err)
	}
	for _, cpf := range []string{"39053344705", "12345678909", "98765432100"} {
		if strings.Contains(resp.Body, cpf) {
			h.t.Fatalf("list leaked full CPF: %s", resp.Body)
		}
	}
	return l
}

func TestListItemsRoute(t *testing.T) {
	h := newHarness(t)
	acc := h.failFirst()
	id := acc.BatchID

	l := h.list(id, nil)
	if l.BatchID != id || len(l.Items) != 3 || l.NextCursor != "" {
		t.Fatalf("%+v", l)
	}
	for i, want := range []batch.ItemStatus{batch.Failed, batch.Denied, batch.Approved} {
		if e := l.Items[i]; e.ItemID != acc.ItemIDs[i] || e.Status != want {
			t.Fatalf("items[%d]=%+v", i, e)
		}
	}

	first := h.list(id, map[string]string{"limit": "2"})
	if len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("%+v", first)
	}
	rest := h.list(id, map[string]string{"limit": "2", "cursor": first.NextCursor})
	if len(rest.Items) != 1 || rest.Items[0].ItemID != acc.ItemIDs[2] || rest.NextCursor != "" {
		t.Fatalf("%+v", rest)
	}

	failed := h.list(id, map[string]string{"status": "FAILED"})
	if len(failed.Items) != 1 || failed.Items[0].ItemID != acc.ItemIDs[0] {
		t.Fatalf("%+v", failed)
	}
	h.want(h.get("GET", "/batches/"+id+"/items", "", map[string]string{"status": "CANCELLED"}), http.StatusOK,
		`{"batch_id":"`+id+`","items":[]}`)
}

func TestListItemsRejectsABadQuery(t *testing.T) {
	h := newHarness(t)
	id := h.submit().BatchID
	for query, body := range map[string]string{
		"status=DECIDED":     `{"error":"invalid_status"}`,
		"status=approved":    `{"error":"invalid_status"}`,
		"limit=0":            `{"error":"invalid_limit","max":1000}`,
		"limit=1001":         `{"error":"invalid_limit","max":1000}`,
		"limit=ten":          `{"error":"invalid_limit","max":1000}`,
		"cursor=nope":        `{"error":"invalid_cursor"}`,
		"cursor=bm90LXV1aWQ": `{"error":"invalid_cursor"}`,
	} {
		k, v, _ := strings.Cut(query, "=")
		if resp := h.get("GET", "/batches/"+id+"/items", "", map[string]string{k: v}); resp.StatusCode != http.StatusBadRequest || resp.Body != body {
			t.Errorf("%s: status=%d body=%s", query, resp.StatusCode, resp.Body)
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
	acc := h.failFirst()
	id, failed := acc.BatchID, acc.ItemIDs[0]
	items := "/batches/" + id + "/items/"

	h.want(h.do("POST", items+acc.ItemIDs[1]+"/retry", ""), http.StatusConflict, `{"error":"invalid_transition"}`)
	h.faults.DropEntries(1)
	h.want(h.do("POST", items+failed+"/retry", ""), http.StatusServiceUnavailable, `{"error":"enqueue_failed"}`)
	h.want(h.do("POST", items+failed+"/retry", ""), http.StatusAccepted, `{"attempts":3,"item_id":"`+failed+`"}`)
	h.want(h.do("POST", items+failed+"/cancel", ""), http.StatusConflict, `{"error":"invalid_transition"}`)
	h.want(h.do("POST", "/batches/"+id+"/retry-failed", ""), http.StatusAccepted, `{"requeued":0}`)

	a := h.attempts()
	if len(a) != 1 || a[0].Number != 3 {
		t.Fatalf("attempts=%+v", a)
	}
	if err := h.batch.DeadLetter(t.Context(), a[0]); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		h.want(h.do("POST", items+failed+"/cancel", ""), http.StatusOK, `{"item_id":"`+failed+`","status":"CANCELLED"}`)
	}
}

func TestRetryAtMaxAttemptsIs409(t *testing.T) {
	h := newHarness(t)
	acc := h.failFirst()
	retry := "/batches/" + acc.BatchID + "/items/" + acc.ItemIDs[0] + "/retry"
	for attempt := 2; attempt <= batch.MaxAttempts; attempt++ {
		h.want(h.do("POST", retry, ""), http.StatusAccepted, `{"attempts":`+strconv.Itoa(attempt)+`,"item_id":"`+acc.ItemIDs[0]+`"}`)
		for _, a := range h.attempts() {
			if err := h.batch.DeadLetter(t.Context(), a); err != nil {
				t.Fatal(err)
			}
		}
	}
	h.want(h.do("POST", retry, ""), http.StatusConflict, `{"error":"max_attempts_reached"}`)
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
