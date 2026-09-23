package idempotency_test

import (
	"context"
	"errors"
	"testing"

	"engine/internal/idempotency"
)

// held is a store where another caller holds every key.
type held struct{ rec idempotency.Record }

func (h held) Claim(context.Context, string, idempotency.Claim) (idempotency.Record, bool, error) {
	return h.rec, false, nil
}
func (held) Complete(context.Context, string, string, idempotency.Response) error { return nil }
func (held) Release(context.Context, string, string) error                        { return nil }

func TestAKeyAnotherCallerHoldsIsInProgress(t *testing.T) {
	// The first Do only learns the request's fingerprint from a free store.
	var fp string
	probe := idempotency.New(recorder{onClaim: func(c idempotency.Claim) { fp = c.Fingerprint }})
	if _, _, err := probe.Do(t.Context(), "k", "POST /evaluations\n{}", func() idempotency.Response {
		return idempotency.Response{Status: 200}
	}); err != nil {
		t.Fatal(err)
	}

	m := idempotency.New(held{rec: idempotency.Record{Fingerprint: fp}})
	ran := false
	_, _, err := m.Do(t.Context(), "k", "POST /evaluations\n{}", func() idempotency.Response {
		ran = true
		return idempotency.Response{Status: 200}
	})
	if !errors.Is(err, idempotency.ErrInProgress) || ran {
		t.Fatalf("err=%v ran=%v", err, ran)
	}
}

func TestAFailedCompleteStillReturnsTheResponse(t *testing.T) {
	m := idempotency.New(recorder{completeErr: errors.New("down")})
	resp, replayed, err := m.Do(t.Context(), "k", "req", func() idempotency.Response {
		return idempotency.Response{Status: 202, Body: "ok"}
	})
	if err != nil || replayed || resp.Body != "ok" {
		t.Fatalf("resp=%+v replayed=%v err=%v", resp, replayed, err)
	}
}

// recorder is a free store: every claim wins.
type recorder struct {
	onClaim     func(idempotency.Claim)
	completeErr error
}

func (r recorder) Claim(_ context.Context, _ string, c idempotency.Claim) (idempotency.Record, bool, error) {
	if r.onClaim != nil {
		r.onClaim(c)
	}
	return idempotency.Record{}, true, nil
}
func (r recorder) Complete(context.Context, string, string, idempotency.Response) error {
	return r.completeErr
}
func (recorder) Release(context.Context, string, string) error { return nil }
