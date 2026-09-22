package telemetry

import (
	"context"
	"log/slog"
	"time"

	"engine/internal/domain"
	"engine/internal/store"
)

const Namespace = "CreditCardEngine"

type metricDef struct {
	Name string `json:"Name"`
	Unit string `json:"Unit"`
}

type metricSet struct {
	Namespace  string      `json:"Namespace"`
	Dimensions [][]string  `json:"Dimensions"`
	Metrics    []metricDef `json:"Metrics"`
}

type awsBlock struct {
	Timestamp         int64       `json:"Timestamp"`
	CloudWatchMetrics []metricSet `json:"CloudWatchMetrics"`
}

// IDs identifies a decision in EMF properties: a single evaluation by
// decision_id, or a batch item by batch_id and index.
type IDs struct {
	DecisionID string
	BatchID    string
	Index      int
}

func Decision(r domain.Result, latency time.Duration, ids IDs) {
	reason := ""
	if len(r.Reasons) > 0 {
		reason = r.Reasons[0]
	}
	ms := latency.Milliseconds()
	values := map[string]any{"DecisionLatencyMs": ms}
	var metrics []metricSet
	if r.Decision == domain.Approved {
		values["Approved"] = 1
		metrics = []metricSet{{
			Namespace:  Namespace,
			Dimensions: [][]string{},
			Metrics: []metricDef{
				{Name: "Approved", Unit: "Count"},
				{Name: "DecisionLatencyMs", Unit: "Milliseconds"},
			},
		}}
	} else {
		values["Denied"] = 1
		values["DenyByReason"] = 1
		metrics = []metricSet{
			{
				Namespace:  Namespace,
				Dimensions: [][]string{},
				Metrics: []metricDef{
					{Name: "Denied", Unit: "Count"},
					{Name: "DecisionLatencyMs", Unit: "Milliseconds"},
				},
			},
			{
				Namespace:  Namespace,
				Dimensions: [][]string{{"reason"}},
				Metrics:    []metricDef{{Name: "DenyByReason", Unit: "Count"}},
			},
		}
	}
	props := []slog.Attr{
		slog.String("decision", string(r.Decision)),
		slog.String("reason", reason),
		slog.Int64("latency_ms", ms),
		slog.String("cpf_masked", r.CPFMasked),
	}
	if ids.DecisionID != "" {
		props = append(props, slog.String("decision_id", ids.DecisionID))
	} else {
		props = append(props, slog.String("batch_id", ids.BatchID), slog.Int("index", ids.Index))
	}
	emit("decision", metrics, values, props)
}

func Failed(batchID string, index, attempt int) {
	emit("item_failed", []metricSet{{
		Namespace:  Namespace,
		Dimensions: [][]string{},
		Metrics:    []metricDef{{Name: "ItemsFailed", Unit: "Count"}},
	}}, map[string]any{"ItemsFailed": 1}, []slog.Attr{
		slog.String("batch_id", batchID),
		slog.Int("index", index),
		slog.Int("attempt", attempt),
	})
}

// Store wraps BatchStore and writes one ItemsFailed EMF line when Fail
// actually moves the item to FAILED.
type Store struct {
	store.BatchStore
}

func Observe(inner store.BatchStore) store.BatchStore {
	return Store{BatchStore: inner}
}

func (s Store) Fail(ctx context.Context, batchID string, index, attempt int) error {
	if err := s.BatchStore.Fail(ctx, batchID, index, attempt); err != nil {
		return err
	}
	Failed(batchID, index, attempt)
	return nil
}

func emit(msg string, metrics []metricSet, values map[string]any, props []slog.Attr) {
	attrs := make([]any, 0, 2+len(values)+len(props))
	attrs = append(attrs, slog.Any("_aws", awsBlock{
		Timestamp:         time.Now().UnixMilli(),
		CloudWatchMetrics: metrics,
	}))
	for k, v := range values {
		attrs = append(attrs, slog.Any(k, v))
	}
	for _, p := range props {
		attrs = append(attrs, p)
	}
	slog.Info(msg, attrs...)
}
