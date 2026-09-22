package telemetry_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"engine/internal/adapter/telemetry"
	"engine/internal/domain"
)

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
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

func oneLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := emfLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("emf lines=%d logs=%s", len(lines), buf.String())
	}
	return lines[0]
}

func TestEvaluatedApprovalWritesOneApprovedLine(t *testing.T) {
	buf := captureLogs(t)
	telemetry.EMF{}.Evaluated("d1", domain.Result{
		Name: "Ana", CPFMasked: "***05", Decision: domain.Approved, RevolvingAmountCents: 250_000, Reasons: []string{"eligible"},
	}, 3*time.Millisecond)
	got := oneLine(t, buf)
	if !hasMetric(got, "Approved") || got["Approved"] != 1.0 || !hasMetric(got, "DecisionLatencyMs") || got["DecisionLatencyMs"] != 3.0 {
		t.Fatalf("%v", got)
	}
	if got["decision"] != "APPROVED" || got["reason"] != "eligible" || got["cpf_masked"] != "***05" || got["decision_id"] != "d1" {
		t.Fatalf("%v", got)
	}
	if _, ok := got["batch_id"]; ok {
		t.Fatalf("single evaluation has a batch_id: %v", got)
	}
	if bytes.Contains(buf.Bytes(), []byte("Ana")) {
		t.Fatalf("leaked the customer name: %s", buf.String())
	}
}

func TestItemDenialWritesDeniedAndDenyByReason(t *testing.T) {
	buf := captureLogs(t)
	telemetry.EMF{}.ItemDecided("b1", "0198f3a2-7c1e-7b3a-9d2f-4e5a6b7c8d90", domain.Result{
		Name: "Bruno", CPFMasked: "***09", Decision: domain.Denied, Reasons: []string{"score_below_600"},
	}, time.Millisecond)
	got := oneLine(t, buf)
	if !hasMetric(got, "Denied") || got["Denied"] != 1.0 {
		t.Fatalf("%v", got)
	}
	if !hasMetric(got, "DenyByReason") || got["DenyByReason"] != 1.0 || got["reason"] != "score_below_600" || !denyReasonDimension(got) {
		t.Fatalf("%v", got)
	}
	if got["batch_id"] != "b1" || got["item_id"] != "0198f3a2-7c1e-7b3a-9d2f-4e5a6b7c8d90" || got["cpf_masked"] != "***09" {
		t.Fatalf("%v", got)
	}
	if bytes.Contains(buf.Bytes(), []byte("Bruno")) {
		t.Fatalf("leaked the customer name: %s", buf.String())
	}
}

func TestItemFailedWritesItemsFailed(t *testing.T) {
	buf := captureLogs(t)
	telemetry.EMF{}.ItemFailed("b1", "0198f3a2-7c1e-7b3a-9d2f-4e5a6b7c8d91", 3)
	got := oneLine(t, buf)
	if !hasMetric(got, "ItemsFailed") || got["ItemsFailed"] != 1.0 || got["batch_id"] != "b1" || got["item_id"] != "0198f3a2-7c1e-7b3a-9d2f-4e5a6b7c8d91" || got["attempt"] != 3.0 {
		t.Fatalf("%v", got)
	}
}
