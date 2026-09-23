// Package idempotency answers a request repeated with the same Idempotency-Key
// with the first response, so a client retry never evaluates or records twice
// (ADR 0005). The key is claimed with one conditional write before the request
// runs; only one caller at a time holds the claim.
package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"uuid"
)

const (
	MaxKeyLength = 255
	// TTL is how long a key answers with its first response.
	TTL = 24 * time.Hour
	// Lease is how long a claim blocks the key before another caller may take
	// it over. It outlives the HTTP Lambda's timeout, so a claim left by a
	// crashed invocation frees itself.
	Lease = 10 * time.Second
)

var (
	ErrInvalidKey = errors.New("invalid idempotency key")
	// ErrKeyReused means the key answered another request (another route or
	// body), so it cannot answer this one.
	ErrKeyReused = errors.New("idempotency key reused")
	// ErrInProgress means another caller holds the key's claim.
	ErrInProgress = errors.New("idempotency key in progress")
)

// Response is what the key replays: the first successful response.
type Response struct {
	Status int
	Body   string
}

// Claim is a pending key: who holds it and until when.
type Claim struct {
	Fingerprint string
	Owner       string
	Now         time.Time
	LeaseUntil  time.Time
	ExpiresAt   time.Time
}

// Record is a key as stored.
type Record struct {
	Fingerprint string
	Done        bool
	Response    Response
}

// Store keeps the keys. Every write is conditional, so the store decides who
// holds a key.
type Store interface {
	// Claim writes c unless the key is live: completed and not expired, or
	// pending with its lease not ended. It returns the live record and false
	// when it did not claim.
	Claim(ctx context.Context, key string, c Claim) (Record, bool, error)
	// Complete stores the response of the claim that owner holds.
	Complete(ctx context.Context, key, owner string, r Response) error
	// Release deletes the pending claim that owner holds.
	Release(ctx context.Context, key, owner string) error
}

type Module struct {
	store Store
	now   func() time.Time
}

func New(s Store) Module {
	return Module{store: s, now: time.Now}
}

// Do runs run once for key. request identifies the request (route and body):
// the same key with another request is ErrKeyReused. replayed is true when the
// response is the stored one and run did not run. Only a 2xx response is
// stored; any other response releases the key, so the client can fix the
// request and send it again with the same key.
func (m Module) Do(ctx context.Context, key, request string, run func() Response) (resp Response, replayed bool, err error) {
	if !validKey(key) {
		return Response{}, false, ErrInvalidKey
	}
	now := m.now()
	c := Claim{
		Fingerprint: fingerprint(request),
		Owner:       uuid.New().String(),
		Now:         now,
		LeaseUntil:  now.Add(Lease),
		ExpiresAt:   now.Add(TTL),
	}
	rec, claimed, err := m.store.Claim(ctx, key, c)
	if err != nil {
		return Response{}, false, fmt.Errorf("claim idempotency key: %w", err)
	}
	if !claimed {
		switch {
		case rec.Fingerprint != c.Fingerprint:
			return Response{}, false, ErrKeyReused
		case !rec.Done:
			return Response{}, false, ErrInProgress
		}
		return rec.Response, true, nil
	}

	resp = run()
	// The response already happened, so a failed bookkeeping write does not
	// fail it: the claim's lease ends and a retry runs the request again.
	if resp.Status >= 200 && resp.Status < 300 {
		if err := m.store.Complete(ctx, key, c.Owner, resp); err != nil {
			slog.Warn("idempotency_complete_failed", slog.String("error", err.Error()))
		}
		return resp, false, nil
	}
	if err := m.store.Release(ctx, key, c.Owner); err != nil {
		slog.Warn("idempotency_release_failed", slog.String("error", err.Error()))
	}
	return resp, false, nil
}

// validKey accepts 1..MaxKeyLength visible ASCII characters, such as a UUID.
func validKey(key string) bool {
	if key == "" || len(key) > MaxKeyLength {
		return false
	}
	for i := range len(key) {
		if key[i] < '!' || key[i] > '~' {
			return false
		}
	}
	return true
}

func fingerprint(request string) string {
	sum := sha256.Sum256([]byte(request))
	return hex.EncodeToString(sum[:])
}
