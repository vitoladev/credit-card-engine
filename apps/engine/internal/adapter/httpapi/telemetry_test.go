package httpapi_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"engine/internal/adapter/dlq"
	"engine/internal/adapter/httpapi"
	"engine/internal/adapter/sqs"
	"engine/internal/adapter/telemetry"
	"engine/internal/evaluate"
	"engine/internal/markfailed"
	"engine/internal/processjob"
	"engine/internal/queue"
	"engine/internal/recovery"
	"engine/internal/report"
	"engine/internal/rules"
	"engine/internal/store"
	"engine/internal/submit"
)

var leakedCPF = regexp.MustCompile(`39053344705|12345678909|98765432100`)

func assertNoCustomerData(t *testing.T, logs string) {
	t.Helper()
	if leakedCPF.MatchString(logs) {
		t.Fatalf("log leaked an 11-digit CPF:\n%s", logs)
	}
	for _, name := range []string{"Ana", "Bruno", "Carla"} {
		if strings.Contains(logs, name) {
			t.Fatalf("log leaked customer name %q:\n%s", name, logs)
		}
	}
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func observedHarness(t *testing.T) *harness {
	t.Helper()
	mem := store.NewMemory()
	q := queue.NewMemory()
	batches := telemetry.Observe(mem)
	h := httpapi.New(evaluate.New(rules.NewPolicy()), mem, submit.New(batches, q, submit.DefaultBatchSize), report.New(mem), recovery.New(batches, q))
	return &harness{
		t: t, http: h, mem: mem, q: q,
		worker: sqs.New(processjob.New(evaluate.New(rules.NewPolicy()), mem)),
		dlq:    dlq.New(markfailed.New(batches)),
	}
}

func emfLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range bytes.SplitSeq(buf.Bytes(), []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("log line %s: %v", line, err)
		}
		if _, ok := m["_aws"]; ok {
			out = append(out, m)
		}
	}
	return out
}

func metricNames(m map[string]any) []string {
	aws, _ := m["_aws"].(map[string]any)
	var names []string
	sets, _ := aws["CloudWatchMetrics"].([]any)
	for _, set := range sets {
		s, _ := set.(map[string]any)
		if s["Namespace"] != "CreditCardEngine" {
			continue
		}
		mets, _ := s["Metrics"].([]any)
		for _, met := range mets {
			if n, ok := metricName(met); ok {
				names = append(names, n)
			}
		}
	}
	return names
}

func hasMetric(m map[string]any, name string) bool {
	for _, n := range metricNames(m) {
		if n == name {
			return true
		}
	}
	return false
}

func denyReasonDimension(m map[string]any) bool {
	aws, _ := m["_aws"].(map[string]any)
	sets, _ := aws["CloudWatchMetrics"].([]any)
	for _, set := range sets {
		s, _ := set.(map[string]any)
		if s["Namespace"] != "CreditCardEngine" {
			continue
		}
		mets, _ := s["Metrics"].([]any)
		for _, met := range mets {
			if n, ok := metricName(met); !ok || n != "DenyByReason" {
				continue
			}
			dims, _ := s["Dimensions"].([]any)
			for _, dim := range dims {
				row, _ := dim.([]any)
				for _, d := range row {
					if d == "reason" {
						return true
					}
				}
			}
		}
	}
	return false
}

func metricName(met any) (string, bool) {
	m, ok := met.(map[string]any)
	if !ok {
		return "", false
	}
	n, ok := m["Name"].(string)
	return n, ok
}

func TestSyncApprovalWritesOneApprovedEMFLine(t *testing.T) {
	buf := captureLogs(t)
	h := observedHarness(t)
	resp := h.do("POST", "/evaluations", customerJSON(t, func(map[string]any) {}))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	lines := emfLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("emf lines=%d logs=%s", len(lines), buf.String())
	}
	got := lines[0]
	if !hasMetric(got, "Approved") || got["Approved"] != 1.0 {
		t.Fatalf("%v", got)
	}
	if !hasMetric(got, "DecisionLatencyMs") {
		t.Fatalf("missing DecisionLatencyMs: %v", got)
	}
	if got["decision"] != "APPROVED" || got["cpf_masked"] != "***05" {
		t.Fatalf("%v", got)
	}
	if id, _ := got["decision_id"].(string); id == "" {
		t.Fatalf("missing decision_id: %v", got)
	}
	assertNoCustomerData(t, buf.String())
}

func TestBatchDenialWritesDeniedAndDenyByReason(t *testing.T) {
	buf := captureLogs(t)
	h := observedHarness(t)
	bruno := `[{"name":"Bruno","cpf":"12345678909","credit_score":520,"current_invoice_cents":200000,"credit_limit_cents":400000,"monthly_spend_cents":[80000]}]`
	id, queued := h.submitAccepted(bruno)
	if queued != 1 {
		t.Fatalf("queued=%d", queued)
	}
	h.drain()
	got, _ := h.report(id)
	if len(got.Denied) != 1 || got.Denied[0].Reasons[0] != "score_below_600" {
		t.Fatalf("%+v", got)
	}
	lines := emfLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("emf lines=%d logs=%s", len(lines), buf.String())
	}
	line := lines[0]
	if !hasMetric(line, "Denied") || line["Denied"] != 1.0 {
		t.Fatalf("%v", line)
	}
	if !hasMetric(line, "DenyByReason") || line["DenyByReason"] != 1.0 || line["reason"] != "score_below_600" {
		t.Fatalf("%v", line)
	}
	if !denyReasonDimension(line) {
		t.Fatalf("DenyByReason missing reason dimension: %v", line)
	}
	if line["batch_id"] != id || line["index"] != 0.0 || line["cpf_masked"] != "***09" {
		t.Fatalf("%v", line)
	}
	assertNoCustomerData(t, buf.String())
}

func TestDLQRecordWritesItemsFailed(t *testing.T) {
	buf := captureLogs(t)
	h := observedHarness(t)
	id, job := h.failedBatch()
	_ = job
	var failed []map[string]any
	for _, line := range emfLines(t, buf) {
		if hasMetric(line, "ItemsFailed") {
			failed = append(failed, line)
		}
	}
	if len(failed) != 1 {
		t.Fatalf("ItemsFailed lines=%d logs=%s", len(failed), buf.String())
	}
	got := failed[0]
	if got["ItemsFailed"] != 1.0 || got["batch_id"] != id || got["index"] != 0.0 || got["attempt"] != 1.0 {
		t.Fatalf("%v", got)
	}
	assertNoCustomerData(t, buf.String())
}

func TestRedeliveredBatchMessageWritesNoSecondDecisionLine(t *testing.T) {
	buf := captureLogs(t)
	h := observedHarness(t)
	ana := `[{"name":"Ana","cpf":"39053344705","credit_score":780,"current_invoice_cents":50000,"credit_limit_cents":500000,"monthly_spend_cents":[80000,90000,70000]}]`
	_, queued := h.submitAccepted(ana)
	if queued != 1 {
		t.Fatalf("queued=%d", queued)
	}
	jobs := append([]queue.Job(nil), h.q.Jobs...)
	h.drain()
	if _, err := h.q.Publish(t.Context(), jobs); err != nil {
		t.Fatal(err)
	}
	h.drain()
	var decisions int
	for _, line := range emfLines(t, buf) {
		if hasMetric(line, "Approved") || hasMetric(line, "Denied") {
			decisions++
		}
	}
	if decisions != 1 {
		t.Fatalf("decision lines=%d logs=%s", decisions, buf.String())
	}
	assertNoCustomerData(t, buf.String())
}

func TestPublishCompensationWritesItemsFailed(t *testing.T) {
	buf := captureLogs(t)
	h := observedHarness(t)
	h.q.FailNextPublishes(1)
	resp := h.do("POST", "/evaluations/batch", `[{"name":"Ana","cpf":"39053344705","credit_score":780,"current_invoice_cents":50000,"credit_limit_cents":500000,"monthly_spend_cents":[80000]}]`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	lines := emfLines(t, buf)
	if len(lines) != 1 || !hasMetric(lines[0], "ItemsFailed") || lines[0]["index"] != 0.0 || lines[0]["attempt"] != 1.0 {
		t.Fatalf("emf=%v logs=%s", lines, buf.String())
	}
	assertNoCustomerData(t, buf.String())
}

func TestFiveXXLogsCauseWithoutCustomerData(t *testing.T) {
	buf := captureLogs(t)
	h := observedHarness(t)
	h.mem.FailNextWrites(1)
	resp := h.do("POST", "/evaluations", customerJSON(t, func(map[string]any) {}))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	logs := buf.String()
	if !strings.Contains(logs, `"level":"ERROR"`) {
		t.Fatalf("missing error log:\n%s", logs)
	}
	if lines := emfLines(t, buf); len(lines) != 0 {
		t.Fatalf("5xx wrote EMF:\n%s", logs)
	}
	assertNoCustomerData(t, logs)
}

func TestAlreadyFailedItemWritesNoItemsFailedLine(t *testing.T) {
	buf := captureLogs(t)
	h := observedHarness(t)
	_, job := h.failedBatch()
	buf.Reset()
	if h.deadLetter(job) {
		t.Fatal("DLQ consumer reported an already-failed item")
	}
	if lines := emfLines(t, buf); len(lines) != 0 {
		t.Fatalf("second Fail wrote EMF: %v", lines)
	}
}
