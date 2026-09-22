package httpapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	"engine/internal/adapter/httpapi"
	"engine/internal/adapter/sqs"
	"engine/internal/evaluate"
	"engine/internal/processjob"
	"engine/internal/queue"
	"engine/internal/rules"
	"engine/internal/store"
)

// Ana and Carla are approved (250_000 and 800_000), Bruno is denied.
const threeCustomers = `[
{"name":"Ana","cpf":"39053344705","credit_score":780,"current_invoice_cents":50000,"credit_limit_cents":500000,"monthly_spend_cents":[80000,90000,70000]},
{"name":"Bruno","cpf":"12345678909","credit_score":520,"current_invoice_cents":200000,"credit_limit_cents":400000,"monthly_spend_cents":[80000]},
{"name":"Carla","cpf":"98765432100","credit_score":820,"current_invoice_cents":100000,"credit_limit_cents":1000000,"monthly_spend_cents":[100000]}
]`

var fullCPFs = []string{"39053344705", "12345678909", "98765432100"}

type entry struct {
	Index                int      `json:"index"`
	Name                 string   `json:"name"`
	CPFMasked            string   `json:"cpf_masked"`
	Decision             string   `json:"decision"`
	Reasons              []string `json:"reasons"`
	RevolvingAmountCents int64    `json:"revolving_amount_cents"`
	Attempts             int      `json:"attempts"`
}

type batchReport struct {
	BatchID   string         `json:"batch_id"`
	Status    string         `json:"status"`
	Counters  store.Counters `json:"counters"`
	Approved  []entry        `json:"approved"`
	Denied    []entry        `json:"denied"`
	Failed    []entry        `json:"failed"`
	Cancelled []entry        `json:"cancelled"`
	Total     int64          `json:"total_revolving_amount_cents"`
}

type harness struct {
	t      *testing.T
	http   httpapi.Handler
	worker sqs.Handler
	mem    *store.Memory
	q      *queue.Memory
	bodies []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h, mem, q := composed()
	return &harness{t: t, http: h, worker: sqs.New(processjob.New(evaluate.New(rules.NewPolicy()), mem)), mem: mem, q: q}
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
	h.bodies = append(h.bodies, resp.Body)
	return resp
}

func (h *harness) drain() {
	h.t.Helper()
	err := h.q.Drain(h.t.Context(), func(ctx context.Context, body []byte) error {
		return h.worker.Handle(ctx, events.SQSEvent{Records: []events.SQSMessage{{Body: string(body)}}})
	})
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) submit(body string) string {
	h.t.Helper()
	resp := h.do("POST", "/evaluations/batch", body)
	if resp.StatusCode != http.StatusAccepted {
		h.t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var acc struct {
		BatchID string `json:"batch_id"`
		Queued  int    `json:"queued"`
	}
	if err := json.Unmarshal([]byte(resp.Body), &acc); err != nil {
		h.t.Fatal(err)
	}
	if acc.BatchID == "" || acc.Queued != 3 {
		h.t.Fatalf("body=%s", resp.Body)
	}
	return acc.BatchID
}

func (h *harness) report(batchID string) (batchReport, string) {
	h.t.Helper()
	resp := h.do("GET", "/batches/"+batchID+"/report", "")
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var r batchReport
	if err := json.Unmarshal([]byte(resp.Body), &r); err != nil {
		h.t.Fatal(err)
	}
	return r, resp.Body
}

// assertNoFullCPF checks every response body the harness has seen.
func (h *harness) assertNoFullCPF() {
	h.t.Helper()
	for _, body := range h.bodies {
		for _, cpf := range fullCPFs {
			if strings.Contains(body, cpf) {
				h.t.Fatalf("response leaked full CPF %s: %s", cpf, body)
			}
		}
	}
}

func TestBatchReportCompletesAfterDrain(t *testing.T) {
	h := newHarness(t)
	id := h.submit(threeCustomers)
	h.drain()

	got, _ := h.report(id)
	if got.BatchID != id || got.Status != "COMPLETED" {
		t.Fatalf("%+v", got)
	}
	if got.Counters != (store.Counters{Decided: 3}) {
		t.Fatalf("counters=%+v", got.Counters)
	}
	if len(got.Approved) != 2 || len(got.Denied) != 1 || len(got.Failed) != 0 || len(got.Cancelled) != 0 {
		t.Fatalf("%+v", got)
	}
	if got.Total != 1_050_000 {
		t.Fatalf("total=%d", got.Total)
	}
	want := entry{Index: 0, Name: "Ana", CPFMasked: "***05", Decision: "APPROVED", Reasons: []string{"eligible"}, RevolvingAmountCents: 250_000, Attempts: 1}
	if !reflect.DeepEqual(got.Approved[0], want) {
		t.Fatalf("approved[0]=%+v", got.Approved[0])
	}
	if d := got.Denied[0]; d.Index != 1 || d.Reasons[0] != "score_below_600" || d.RevolvingAmountCents != 0 {
		t.Fatalf("denied[0]=%+v", d)
	}
	h.assertNoFullCPF()
}

func TestBatchReportBeforeDrainIsProcessing(t *testing.T) {
	h := newHarness(t)
	id := h.submit(threeCustomers)

	got, _ := h.report(id)
	if got.Status != "PROCESSING" || got.Counters != (store.Counters{Queued: 3}) || got.Total != 0 {
		t.Fatalf("%+v", got)
	}
	h.assertNoFullCPF()
}

func TestBatchRedeliveryLeavesReportUnchanged(t *testing.T) {
	h := newHarness(t)
	id := h.submit(threeCustomers)
	jobs := append([]queue.Job(nil), h.q.Jobs...)
	h.drain()
	_, first := h.report(id)

	if _, err := h.q.Publish(t.Context(), jobs); err != nil {
		t.Fatal(err)
	}
	h.drain()
	_, second := h.report(id)

	if first != second {
		t.Fatalf("report changed on redelivery:\n%s\n%s", first, second)
	}
}

func TestBatchOverTheLimitStoresAndQueuesNothing(t *testing.T) {
	h := newHarness(t)
	one := `{"name":"Ana","cpf":"39053344705","credit_score":780,"current_invoice_cents":50000,"credit_limit_cents":500000,"monthly_spend_cents":[80000]}`
	body := "[" + strings.Repeat(one+",", 1000) + one + "]"

	resp := h.do("POST", "/evaluations/batch", body)
	if resp.StatusCode != http.StatusUnprocessableEntity || resp.Body != `{"error":"batch_too_large","max":1000}` {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	if !h.mem.Empty() || len(h.q.Jobs) != 0 {
		t.Fatal("stored or queued a rejected batch")
	}
}

func TestSingleEvaluationRoundTrip(t *testing.T) {
	h := newHarness(t)
	resp := h.do("POST", "/evaluations", customerJSON(t, func(map[string]any) {}))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var created struct {
		DecisionID string `json:"decision_id"`
	}
	if err := json.Unmarshal([]byte(resp.Body), &created); err != nil {
		t.Fatal(err)
	}
	if created.DecisionID == "" {
		t.Fatalf("body=%s", resp.Body)
	}

	got := h.do("GET", "/evaluations/"+created.DecisionID, "")
	if got.StatusCode != http.StatusOK || got.Body != resp.Body {
		t.Fatalf("status=%d body=%s want %s", got.StatusCode, got.Body, resp.Body)
	}
	h.assertNoFullCPF()
}

func TestUnknownIDsReturn404(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/evaluations/nope", "/batches/nope/report"} {
		resp := h.do("GET", path, "")
		if resp.StatusCode != http.StatusNotFound || resp.Body != `{"error":"not_found"}` {
			t.Fatalf("%s: status=%d body=%s", path, resp.StatusCode, resp.Body)
		}
	}
}

func TestBatchOfOneThousandIsAccepted(t *testing.T) {
	h := newHarness(t)
	var b strings.Builder
	b.WriteString("[")
	for i := range 1000 {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"name":"C` + strconv.Itoa(i) + `","cpf":"39053344705","credit_score":780,"current_invoice_cents":50000,"credit_limit_cents":500000,"monthly_spend_cents":[80000]}`)
	}
	b.WriteString("]")
	resp := h.do("POST", "/evaluations/batch", b.String())
	if resp.StatusCode != http.StatusAccepted || len(h.q.Jobs) != 1000 {
		t.Fatalf("status=%d jobs=%d", resp.StatusCode, len(h.q.Jobs))
	}
}

// API Gateway base64-encodes bodies whose content type is not text, which is
// what `curl -d` sends (application/x-www-form-urlencoded).
func TestBase64EncodedBodiesAreDecoded(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct{ path, body string }{
		{"/evaluations", customerJSON(t, func(map[string]any) {})},
		{"/evaluations/batch", threeCustomers},
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
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
			t.Fatalf("%s: status=%d body=%s", tc.path, resp.StatusCode, resp.Body)
		}
	}
}
