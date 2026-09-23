// Package batch is the lifecycle of a batch and its items: submit, relay the
// items that need evaluating to the queue, evaluate each attempt, fail it after
// the DLQ, and the operator's retry and cancel (ADR 0001, ADR 0004). Item status
// transitions are atomic in the Items adapter; this module decides which
// transition to ask for. Submit and retry only write items: the table's stream
// feeds Relay, so every queued item gets a message without a second write.
package batch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
	"uuid"

	"engine/internal/domain"
	"engine/internal/rules"
)

const (
	DefaultBatchSize = 100
	MaxBatchSize     = 1000
	// MaxAttempts caps the attempts of one batch item (ADR 0001).
	MaxAttempts = 5
	// DefaultPageLimit and MaxPageLimit bound one page of ListItems.
	DefaultPageLimit = 100
	MaxPageLimit     = 1000
)

var (
	ErrNotFound          = errors.New("not found")
	ErrInvalidTransition = errors.New("invalid transition")
	ErrMaxAttempts       = errors.New("max attempts reached")
	ErrTooLarge          = errors.New("batch too large")
	// ErrNotRecorded means the batch was not stored and nothing was published.
	ErrNotRecorded = errors.New("batch not recorded")
	// ErrInvalidCursor means a ListItems cursor was not issued by ListItems.
	ErrInvalidCursor = errors.New("invalid cursor")
)

// ItemStatus is where a batch item stands. A decided item carries its
// decision as its status.
type ItemStatus string

const (
	Queued    ItemStatus = "QUEUED"
	Approved  ItemStatus = ItemStatus(domain.Approved)
	Denied    ItemStatus = ItemStatus(domain.Denied)
	Failed    ItemStatus = "FAILED"
	Cancelled ItemStatus = "CANCELLED"
)

// ParseStatus reads an item status from the wire.
func ParseStatus(s string) (ItemStatus, bool) {
	st := ItemStatus(s)
	switch st {
	case Queued, Approved, Denied, Failed, Cancelled:
		return st, true
	}
	return "", false
}

// DecidedAs is the status an item takes when r decides it.
func DecidedAs(r domain.Result) ItemStatus {
	switch r.Decision {
	case domain.Approved:
		return Approved
	case domain.Denied:
		return Denied
	default:
		panic("unhandled decision: " + r.Decision.String())
	}
}

// Item is one batch item: the customer input and where it stands. ID is a
// version 7 UUID, so item IDs sort in the order the customers were submitted.
type Item struct {
	ID       string
	Customer domain.Customer
	Status   ItemStatus
	Attempts int
	Result   domain.Result
}

// ItemEvent is one status change of a batch item, read from the table's
// stream, and the body of a queue message. It carries no customer data: a
// consumer that needs the input reads the item. The same transition always has
// the same ID, so a consumer can drop a duplicate delivery.
type ItemEvent struct {
	ID      string     `json:"event_id"`
	BatchID string     `json:"batch_id"`
	ItemID  string     `json:"item_id"`
	Attempt int        `json:"attempt"`
	Status  ItemStatus `json:"status"`
}

// NewItemEvent is the event of an item reaching status on attempt.
func NewItemEvent(batchID, itemID string, attempt int, status ItemStatus) ItemEvent {
	return ItemEvent{
		ID:      fmt.Sprintf("%s:%s:%d:%s", batchID, itemID, attempt, status),
		BatchID: batchID,
		ItemID:  itemID,
		Attempt: attempt,
		Status:  status,
	}
}

// ParseItemEvent decodes a queue message body.
func ParseItemEvent(body string) (ItemEvent, error) {
	var e ItemEvent
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		return ItemEvent{}, err
	}
	return e, nil
}

// PageQuery selects one page of a batch's items: those with Status (any
// status when empty), after the item with ID After (from the first item when
// empty), at most Limit of them.
type PageQuery struct {
	Status ItemStatus
	After  string
	Limit  int
}

// Page is one page of items in ID order. More is true when items may follow
// the last one.
type Page struct {
	Items []Item
	More  bool
}

// Items stores batch items and moves them between statuses. Every transition
// is conditional on the item's current status and attempt, so concurrent
// workers and operators cannot both win.
type Items interface {
	// Create stores the items as queued on attempt 1. It writes nothing when
	// it fails.
	Create(ctx context.Context, batchID string, items []Item) error
	// Decide moves a queued item on the given attempt to DecidedAs(r).
	Decide(ctx context.Context, batchID, itemID string, attempt int, r domain.Result) error
	// Fail moves a queued item on the given attempt to failed.
	Fail(ctx context.Context, batchID, itemID string, attempt int) error
	// Retry moves a failed item on the given attempt back to queued on
	// attempt+1, or returns ErrMaxAttempts at MaxAttempts.
	Retry(ctx context.Context, batchID, itemID string, attempt int) error
	// RetryMany retries each item and returns the ones it moved. Items that
	// cannot transition are skipped; a store error stops the rest.
	RetryMany(ctx context.Context, batchID string, items []Item) ([]Item, error)
	// Cancel moves a failed item to cancelled; a cancelled item stays so.
	Cancel(ctx context.Context, batchID, itemID string) error
	Item(ctx context.Context, batchID, itemID string) (Item, error)
	// Page returns one page of the batch's items. A batch with no items at
	// all is ErrNotFound: every stored batch has at least one item.
	Page(ctx context.Context, batchID string, q PageQuery) (Page, error)
}

// Publisher sends item events to the queue. It returns the IDs of the events
// it did not send.
type Publisher interface {
	Publish(ctx context.Context, events []ItemEvent) (failed []string, err error)
}

// Emitter records what happened to items as metrics.
type Emitter interface {
	ItemDecided(batchID, itemID string, r domain.Result, latency time.Duration)
	ItemFailed(batchID, itemID string, attempt int)
}

// Deps wires the module. Publisher is needed only to relay, and MaxCustomers
// only to submit.
type Deps struct {
	Items        Items
	Publisher    Publisher
	Policy       rules.Policy
	Emitter      Emitter
	MaxCustomers int
}

type Module struct {
	items        Items
	pub          Publisher
	policy       rules.Policy
	emit         Emitter
	maxCustomers int
}

func New(d Deps) Module {
	return Module{items: d.Items, pub: d.Publisher, policy: d.Policy, emit: d.Emitter, maxCustomers: d.MaxCustomers}
}

// ParseBatchSize reads the BATCH_SIZE setting: empty means DefaultBatchSize,
// anything outside 1..MaxBatchSize is an error rather than a silent clamp.
func ParseBatchSize(s string) (int, error) {
	if s == "" {
		return DefaultBatchSize, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > MaxBatchSize {
		return 0, fmt.Errorf("BATCH_SIZE must be an integer in 1..%d, got %q", MaxBatchSize, s)
	}
	return n, nil
}

func (m Module) MaxCustomers() int { return m.maxCustomers }

// Accepted answers a submit. ItemIDs follow the order of the submitted
// customers, so a caller can match each item to its input.
type Accepted struct {
	BatchID string   `json:"batch_id"`
	Queued  int      `json:"queued"`
	ItemIDs []string `json:"item_ids"`
}

// Submit stores every customer as a queued item and returns. It publishes
// nothing: the stream delivers each new item to Relay.
func (m Module) Submit(ctx context.Context, customers []domain.Customer) (Accepted, error) {
	if len(customers) > m.maxCustomers {
		return Accepted{}, ErrTooLarge
	}
	id := uuid.New().String()
	items := make([]Item, len(customers))
	ids := make([]string, len(customers))
	for i, c := range customers {
		ids[i] = uuid.NewV7().String()
		items[i] = Item{ID: ids[i], Customer: c}
	}
	if err := m.items.Create(ctx, id, items); err != nil {
		return Accepted{}, fmt.Errorf("%w: %w", ErrNotRecorded, err)
	}
	return Accepted{BatchID: id, Queued: len(items), ItemIDs: ids}, nil
}

// Relay publishes the events that queue an item: the items the worker has to
// evaluate. Other transitions are not work. It returns the IDs of the events it
// did not publish, so the stream delivers them again.
func (m Module) Relay(ctx context.Context, events []ItemEvent) ([]string, error) {
	var work []ItemEvent
	for _, e := range events {
		if e.Status == Queued {
			work = append(work, e)
		}
	}
	if len(work) == 0 {
		return nil, nil
	}
	return m.pub.Publish(ctx, work)
}

// Process evaluates the item a queued event names and decides it. An event for
// an item that already moved on (decided, or queued again on a newer attempt)
// is acknowledged. An error means the message should be redelivered.
func (m Module) Process(ctx context.Context, e ItemEvent) error {
	if e.BatchID == "" {
		return errors.New("missing batch_id")
	}
	it, err := m.items.Item(ctx, e.BatchID, e.ItemID)
	if err != nil {
		return err
	}
	if it.Status != Queued || it.Attempts != e.Attempt {
		return nil
	}
	start := time.Now()
	r := m.policy.Evaluate(it.Customer)
	err = m.items.Decide(ctx, e.BatchID, e.ItemID, e.Attempt, r)
	if errors.Is(err, ErrInvalidTransition) {
		return nil
	}
	if err != nil {
		return err
	}
	m.emit.ItemDecided(e.BatchID, e.ItemID, r, time.Since(start))
	return nil
}

// DeadLetter fails the item of an event that exhausted its deliveries. It
// returns an error only when the store may succeed on a later try: an event
// that names no item, or whose item already moved on, is acknowledged so it
// does not loop in the DLQ.
func (m Module) DeadLetter(ctx context.Context, e ItemEvent) error {
	if e.BatchID == "" {
		return nil
	}
	err := m.fail(ctx, e.BatchID, e.ItemID, e.Attempt)
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidTransition) {
		return nil
	}
	return err
}

// Retry moves one failed item back to queued on its next attempt and returns
// that attempt. The stream delivers the change to Relay.
func (m Module) Retry(ctx context.Context, batchID, itemID string) (int, error) {
	it, err := m.items.Item(ctx, batchID, itemID)
	if err != nil {
		return 0, err
	}
	if err := m.items.Retry(ctx, batchID, itemID, it.Attempts); err != nil {
		return 0, err
	}
	return it.Attempts + 1, nil
}

// RetryFailed moves every failed item under MaxAttempts back to queued and
// returns how many moved. The failed items come from pages of MaxPageLimit,
// never one read per item.
func (m Module) RetryFailed(ctx context.Context, batchID string) (int, error) {
	var eligible []Item
	q := PageQuery{Status: Failed, Limit: MaxPageLimit}
	for {
		p, err := m.items.Page(ctx, batchID, q)
		if err != nil {
			return 0, err
		}
		for _, it := range p.Items {
			if it.Attempts < MaxAttempts {
				eligible = append(eligible, it)
			}
		}
		if !p.More || len(p.Items) == 0 {
			break
		}
		q.After = p.Items[len(p.Items)-1].ID
	}
	if len(eligible) == 0 {
		return 0, nil
	}
	queued, err := m.items.RetryMany(ctx, batchID, eligible)
	return len(queued), err
}

// Cancel cancels a failed item. Cancelling a cancelled item changes nothing.
func (m Module) Cancel(ctx context.Context, batchID, itemID string) error {
	return m.items.Cancel(ctx, batchID, itemID)
}

// fail moves a queued item to failed and emits ItemsFailed only when it moved.
func (m Module) fail(ctx context.Context, batchID, itemID string, attempt int) error {
	if err := m.items.Fail(ctx, batchID, itemID, attempt); err != nil {
		return err
	}
	m.emit.ItemFailed(batchID, itemID, attempt)
	return nil
}
