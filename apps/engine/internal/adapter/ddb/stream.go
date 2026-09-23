package ddb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/aws/aws-lambda-go/events"

	"engine/internal/batch"
)

// Relay consumes the BatchItems stream (NEW_IMAGE) and hands each batch item
// change to batch.Relay as an item event (ADR 0004). The stream record format
// is the table's row format, so it is read here, next to the rows.
type Relay struct {
	b batch.Module
}

func NewRelay(b batch.Module) Relay {
	return Relay{b: b}
}

// Handle reports the records whose events were not published, so the event
// source mapping delivers them again from the lowest one. A record that cannot
// be read is logged and skipped: retrying it would block its shard until the
// record expires.
func (r Relay) Handle(ctx context.Context, ev events.DynamoDBEvent) (events.DynamoDBEventResponse, error) {
	var (
		itemEvents []batch.ItemEvent
		seq        = map[string]string{}
	)
	for _, rec := range ev.Records {
		e, ok, err := ItemEventFrom(rec)
		if err != nil {
			slog.Error("stream_record_skipped", slog.String("event_id", rec.EventID), slog.String("error", err.Error()))
			continue
		}
		if !ok {
			continue
		}
		itemEvents = append(itemEvents, e)
		seq[e.ID] = rec.Change.SequenceNumber
	}
	failed, err := r.b.Relay(ctx, itemEvents)
	if err != nil && len(failed) == 0 {
		return events.DynamoDBEventResponse{}, err
	}
	var resp events.DynamoDBEventResponse
	for _, id := range failed {
		resp.BatchItemFailures = append(resp.BatchItemFailures, events.DynamoDBBatchItemFailure{ItemIdentifier: seq[id]})
	}
	if err != nil {
		slog.Error("relay_publish_failed", slog.Int("events", len(failed)), slog.String("error", err.Error()))
	}
	return resp, nil
}

// ItemEventFrom reads the item event of one BatchItems stream record. ok is
// false for a removal, which is no item change.
func ItemEventFrom(rec events.DynamoDBEventRecord) (batch.ItemEvent, bool, error) {
	if rec.EventName == string(events.DynamoDBOperationTypeRemove) {
		return batch.ItemEvent{}, false, nil
	}
	img := rec.Change.NewImage
	batchID, err := streamS(img, "batch_id")
	if err != nil {
		return batch.ItemEvent{}, false, err
	}
	itemID, err := streamS(img, "item_id")
	if err != nil {
		return batch.ItemEvent{}, false, err
	}
	status, err := streamS(img, "status")
	if err != nil {
		return batch.ItemEvent{}, false, err
	}
	attempts, err := streamN(img, "attempts")
	if err != nil {
		return batch.ItemEvent{}, false, err
	}
	return batch.NewItemEvent(batchID, itemID, attempts, batch.ItemStatus(status)), true, nil
}

var errNoImage = errors.New("stream record has no new image")

func streamS(img map[string]events.DynamoDBAttributeValue, name string) (string, error) {
	if img == nil {
		return "", errNoImage
	}
	v, ok := img[name]
	if !ok || v.DataType() != events.DataTypeString {
		return "", fmt.Errorf("stream attribute %s missing or not a string", name)
	}
	return v.String(), nil
}

func streamN(img map[string]events.DynamoDBAttributeValue, name string) (int, error) {
	if img == nil {
		return 0, errNoImage
	}
	v, ok := img[name]
	if !ok || v.DataType() != events.DataTypeNumber {
		return 0, fmt.Errorf("stream attribute %s missing or not a number", name)
	}
	return strconv.Atoi(v.Number())
}
