package batch

import (
	"context"
	"encoding/base64"
	"uuid"

	"engine/internal/domain"
)

// Entry is one batch item as an operator sees it: masked CPF, never the full one.
type Entry struct {
	ItemID               string     `json:"item_id"`
	Name                 string     `json:"name"`
	CPFMasked            string     `json:"cpf_masked"`
	Status               ItemStatus `json:"status"`
	Reasons              []string   `json:"reasons"`
	RevolvingAmountCents int64      `json:"revolving_amount_cents"`
	Attempts             int        `json:"attempts"`
}

// ItemList is one page of a batch's items. NextCursor is empty on the last page.
type ItemList struct {
	BatchID    string  `json:"batch_id"`
	Items      []Entry `json:"items"`
	NextCursor string  `json:"next_cursor,omitempty"`
}

// ListQuery asks for one page of ListItems. An empty Status lists every
// item. An empty Cursor starts at the first item.
type ListQuery struct {
	Status ItemStatus
	Cursor string
	Limit  int
}

// ListItems returns one page of the batch's items in submission order. It is
// the batch report: each item carries its status and revolving amount, and
// there are no batch-level counters or totals.
func (m Module) ListItems(ctx context.Context, batchID string, q ListQuery) (ItemList, error) {
	after, err := decodeCursor(q.Cursor)
	if err != nil {
		return ItemList{}, err
	}
	p, err := m.items.Page(ctx, batchID, PageQuery{Status: q.Status, After: after, Limit: q.Limit})
	if err != nil {
		return ItemList{}, err
	}
	list := ItemList{BatchID: batchID, Items: make([]Entry, len(p.Items))}
	for i, it := range p.Items {
		list.Items[i] = entryOf(it)
	}
	if p.More && len(p.Items) > 0 {
		list.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(p.Items[len(p.Items)-1].ID))
	}
	return list, nil
}

// decodeCursor returns the item ID a cursor points after.
func decodeCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", ErrInvalidCursor
	}
	if _, err := uuid.Parse(string(raw)); err != nil {
		return "", ErrInvalidCursor
	}
	return string(raw), nil
}

func entryOf(it Item) Entry {
	reasons := it.Result.Reasons
	if reasons == nil {
		reasons = []string{}
	}
	return Entry{
		ItemID:               it.ID,
		Name:                 it.Customer.Name,
		CPFMasked:            domain.MaskCPF(it.Customer.CPF),
		Status:               it.Status,
		Reasons:              reasons,
		RevolvingAmountCents: it.Result.RevolvingAmountCents,
		Attempts:             it.Attempts,
	}
}
