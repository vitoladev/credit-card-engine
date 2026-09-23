package ddb_test

import (
	"sync"
	"testing"
	"time"
	"uuid"

	"engine/internal/adapter/ddb"
	"engine/internal/idempotency"
)

func claimAt(fp string, now time.Time) idempotency.Claim {
	return idempotency.Claim{
		Fingerprint: fp,
		Owner:       uuid.New().String(),
		Now:         now,
		LeaseUntil:  now.Add(idempotency.Lease),
		ExpiresAt:   now.Add(idempotency.TTL),
	}
}

func claim(t *testing.T, k ddb.Keys, key string, c idempotency.Claim) (idempotency.Record, bool) {
	t.Helper()
	rec, ok, err := k.Claim(t.Context(), key, c)
	if err != nil {
		t.Fatal(err)
	}
	return rec, ok
}

func TestAPendingKeyIsHeldUntilItsLeaseEnds(t *testing.T) {
	k := ddb.NewKeys(newStore(t).Store)
	now := time.Now()
	first := claimAt("fp", now)
	if _, ok := claim(t, k, "key", first); !ok {
		t.Fatal("first claim lost")
	}
	rec, ok := claim(t, k, "key", claimAt("fp", now.Add(time.Second)))
	if ok || rec.Done || rec.Fingerprint != "fp" {
		t.Fatalf("second claim ok=%v rec=%+v", ok, rec)
	}

	// The first owner crashed: after the lease another caller takes the key,
	// and the first owner can no longer complete or release it.
	second := claimAt("fp", now.Add(idempotency.Lease+time.Second))
	if _, ok := claim(t, k, "key", second); !ok {
		t.Fatal("takeover after the lease lost")
	}
	if err := k.Complete(t.Context(), "key", first.Owner, idempotency.Response{Status: 200, Body: "{}"}); err == nil {
		t.Fatal("stale owner completed the key")
	}
	if err := k.Release(t.Context(), "key", first.Owner); err == nil {
		t.Fatal("stale owner released the key")
	}
}

func TestACompletedKeyReplaysUntilItExpires(t *testing.T) {
	k := ddb.NewKeys(newStore(t).Store)
	now := time.Now()
	c := claimAt("fp", now)
	claim(t, k, "key", c)
	want := idempotency.Response{Status: 202, Body: `{"batch_id":"b"}`}
	if err := k.Complete(t.Context(), "key", c.Owner, want); err != nil {
		t.Fatal(err)
	}

	// A completed key outlives its lease.
	rec, ok := claim(t, k, "key", claimAt("other", now.Add(time.Hour)))
	if ok || !rec.Done || rec.Fingerprint != "fp" || rec.Response != want {
		t.Fatalf("ok=%v rec=%+v", ok, rec)
	}
	// Past expires_at the row counts as absent, even before TTL deletes it.
	if _, ok := claim(t, k, "key", claimAt("other", now.Add(idempotency.TTL+time.Second))); !ok {
		t.Fatal("expired key was not claimed")
	}
}

func TestAReleasedKeyCanBeClaimedAgain(t *testing.T) {
	k := ddb.NewKeys(newStore(t).Store)
	c := claimAt("fp", time.Now())
	claim(t, k, "key", c)
	if err := k.Release(t.Context(), "key", c.Owner); err != nil {
		t.Fatal(err)
	}
	if _, ok := claim(t, k, "key", claimAt("fp", time.Now())); !ok {
		t.Fatal("released key was not claimed")
	}
}

// Claims that race on one key: DynamoDB's condition lets exactly one win.
func TestRacingClaimsHaveOneWinner(t *testing.T) {
	k := ddb.NewKeys(newStore(t).Store)
	now := time.Now()
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
	)
	for range 8 {
		wg.Go(func() {
			_, ok, err := k.Claim(t.Context(), "key", claimAt("fp", now))
			if err != nil {
				t.Error(err)
				return
			}
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("wins=%d", wins)
	}
}
