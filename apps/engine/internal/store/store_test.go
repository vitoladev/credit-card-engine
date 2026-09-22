package store_test

import (
	"testing"

	"engine/internal/domain"
	"engine/internal/store"
)

func TestRetryManyQueuesFailedItemsAndSkipsTheRest(t *testing.T) {
	mem := store.NewMemory()
	customers := []domain.Customer{
		{Name: "Ana", CPF: "39053344705"},
		{Name: "Bruno", CPF: "12345678909"},
		{Name: "Carla", CPF: "98765432100"},
	}
	if err := mem.Create(t.Context(), "b1", customers); err != nil {
		t.Fatal(err)
	}
	for i := range customers {
		if err := mem.Fail(t.Context(), "b1", i, 1); err != nil {
			t.Fatal(err)
		}
	}
	for a := 1; a < store.MaxAttempts; a++ {
		if err := mem.Retry(t.Context(), "b1", 2, a); err != nil {
			t.Fatal(err)
		}
		if err := mem.Fail(t.Context(), "b1", 2, a+1); err != nil {
			t.Fatal(err)
		}
	}

	failed, err := mem.Failed(t.Context(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	queued, err := mem.RetryMany(t.Context(), "b1", failed)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 2 || queued[0].Index != 0 || queued[1].Index != 1 {
		t.Fatalf("queued=%+v", queued)
	}
	b, err := mem.Report(t.Context(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if b.Counters != (store.Counters{Queued: 2, Failed: 1}) {
		t.Fatalf("%+v", b.Counters)
	}
	if b.Items[0].Attempts != 2 || b.Items[1].Attempts != 2 || b.Items[2].Attempts != store.MaxAttempts {
		t.Fatalf("attempts=%d,%d,%d", b.Items[0].Attempts, b.Items[1].Attempts, b.Items[2].Attempts)
	}
}

func TestRetryManyStopsOnAStoreError(t *testing.T) {
	mem := store.NewMemory()
	customers := []domain.Customer{{Name: "Ana"}, {Name: "Bruno"}}
	if err := mem.Create(t.Context(), "b1", customers); err != nil {
		t.Fatal(err)
	}
	for i := range customers {
		if err := mem.Fail(t.Context(), "b1", i, 1); err != nil {
			t.Fatal(err)
		}
	}
	mem.FailNextWrites(1)
	queued, err := mem.RetryMany(t.Context(), "b1", []store.Item{
		{Index: 0, Attempts: 1},
		{Index: 1, Attempts: 1},
	})
	if err == nil || err.Error() != "injected write failure" {
		t.Fatalf("err=%v queued=%+v", err, queued)
	}
	if len(queued) != 0 {
		t.Fatalf("queued=%+v", queued)
	}
}
