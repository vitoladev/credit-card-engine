package main

import (
	"testing"
	"uuid"

	"engine/internal/adapter/ddb"
	"engine/internal/batch"
	"engine/internal/domain"
	"engine/internal/flocitest"
)

func TestComposeNeedsTheQueue(t *testing.T) {
	t.Setenv("QUEUE_URL", "")
	if _, err := compose(t.Context()); err == nil {
		t.Fatal("composed with no QUEUE_URL")
	}
}

// The relay as the stack wires it, with no table at all: it reads the
// BatchItems stream and sends each queued item's event to the queue.
func TestTheRelaySendsQueuedItemsFromTheStream(t *testing.T) {
	cfg, _ := flocitest.Config(t)
	tables := flocitest.CreateTables(t, cfg)
	queue := flocitest.Queue(t, cfg)
	t.Setenv("QUEUE_URL", queue)
	h, err := compose(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stream := flocitest.TableStream(t, cfg, tables.Items)
	st := ddb.NewItems(cfg, tables.Items)
	items := []batch.Item{
		{ID: uuid.NewV7().String(), Customer: domain.Customer{Name: "Ana", CPF: "39053344705"}},
		{ID: uuid.NewV7().String(), Customer: domain.Customer{Name: "Bruno", CPF: "12345678909"}},
	}
	if err := st.Create(t.Context(), "b1", items); err != nil {
		t.Fatal(err)
	}
	resp, err := h.Handle(t.Context(), stream.Next())
	if err != nil || len(resp.BatchItemFailures) != 0 {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	got := flocitest.Decode[batch.ItemEvent](t, flocitest.Receive(t, cfg, queue))
	if len(got) != 2 || got[0].Status != batch.Queued || got[1].Status != batch.Queued {
		t.Fatalf("events=%+v", got)
	}
}
