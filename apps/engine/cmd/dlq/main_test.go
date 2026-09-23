package main

import (
	"encoding/json"
	"testing"
	"uuid"

	"github.com/aws/aws-lambda-go/events"

	"engine/internal/adapter/ddb"
	"engine/internal/batch"
	"engine/internal/domain"
	"engine/internal/flocitest"
)

func TestComposeNeedsTheBatchItemsTable(t *testing.T) {
	t.Setenv("BATCH_ITEMS_TABLE", "")
	if _, err := compose(t.Context()); err == nil {
		t.Fatal("composed with no BATCH_ITEMS_TABLE")
	}
}

// The Lambda as the stack wires it, with only the BatchItems table: a queued
// item's event fails the item.
func TestTheLambdaNeedsOnlyTheBatchItemsTable(t *testing.T) {
	cfg, _ := flocitest.Config(t)
	tables := flocitest.CreateTables(t, cfg)
	t.Setenv("BATCH_ITEMS_TABLE", tables.Items)
	h, err := compose(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	st := ddb.New(cfg, ddb.Tables{Items: tables.Items})
	item := batch.Item{ID: uuid.NewV7().String(), Customer: domain.Customer{
		Name: "Ana", CPF: "39053344705", CreditScore: 780, CurrentInvoiceCents: 50_000,
		CreditLimitCents: 500_000, MonthlySpendCents: []int64{80_000, 90_000, 70_000},
	}}
	if err := st.Create(t.Context(), "b1", []batch.Item{item}); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(batch.NewItemEvent("b1", item.ID, 1, batch.Queued))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := h.Handle(t.Context(), events.SQSEvent{Records: []events.SQSMessage{{MessageId: "m1", Body: string(body)}}})
	if err != nil || len(resp.BatchItemFailures) != 0 {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	got, err := st.Item(t.Context(), "b1", item.ID)
	if err != nil || got.Status != batch.Failed {
		t.Fatalf("item=%+v err=%v", got, err)
	}
	if n := flocitest.Rows(t, cfg, tables.Decisions, tables.Keys); n != 0 {
		t.Fatalf("rows outside BatchItems=%d", n)
	}
}
