package telemetry

import (
	"log/slog"
	"time"

	"engine/internal/domain"
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

// EMF writes one CloudWatch Embedded Metric Format line per event to the
// default slog logger. It is the Emitter of both evaluate and batch.
type EMF struct{}

// Evaluated writes the decision line of a single evaluation.
func (EMF) Evaluated(decisionID string, r domain.Result, latency time.Duration) {
	decision(r, latency, 0, ids{decisionID: decisionID})
}

// ItemDecided writes the decision line of a batch item, with ItemEndToEndMs:
// the time from queued (submit or retry) to decided.
func (EMF) ItemDecided(batchID, itemID string, r domain.Result, latency, sinceQueued time.Duration) {
	decision(r, latency, sinceQueued, ids{batchID: batchID, itemID: itemID})
}

// ItemFailed writes one ItemsFailed line for an item that moved to failed.
func (EMF) ItemFailed(batchID, itemID string, attempt int) {
	emit("item_failed", []metricSet{{
		Namespace:  Namespace,
		Dimensions: [][]string{},
		Metrics:    []metricDef{{Name: "ItemsFailed", Unit: "Count"}},
	}}, map[string]any{"ItemsFailed": 1}, []slog.Attr{
		slog.String("batch_id", batchID),
		slog.String("item_id", itemID),
		slog.Int("attempt", attempt),
	})
}

// ids identifies a decision in EMF properties: a single evaluation by
// decision_id, or a batch item by batch_id and item_id.
type ids struct {
	decisionID string
	batchID    string
	itemID     string
}

func decision(r domain.Result, latency, sinceQueued time.Duration, id ids) {
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
	if sinceQueued > 0 {
		values["ItemEndToEndMs"] = sinceQueued.Milliseconds()
		metrics = append(metrics, metricSet{
			Namespace:  Namespace,
			Dimensions: [][]string{},
			Metrics:    []metricDef{{Name: "ItemEndToEndMs", Unit: "Milliseconds"}},
		})
	}
	props := []slog.Attr{
		slog.String("decision", string(r.Decision)),
		slog.String("reason", reason),
		slog.Int64("latency_ms", ms),
		slog.String("cpf_masked", r.CPFMasked),
	}
	if id.decisionID != "" {
		props = append(props, slog.String("decision_id", id.decisionID))
	} else {
		props = append(props, slog.String("batch_id", id.batchID), slog.String("item_id", id.itemID))
	}
	emit("decision", metrics, values, props)
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
