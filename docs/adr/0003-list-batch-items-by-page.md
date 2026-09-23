---
status: accepted
---

# List a batch's items by page, with no batch-level status or totals

The batch report was one response with four lists (`approved`, `denied`,
`failed`, `cancelled`), a `counters` object, a derived batch status, and
`total_revolving_amount_cents`. An item was known by its index in the
submitted array. That shape had to read the whole batch on every call, and the
index was an ID nobody was given: a caller had to count positions to act on an
item.

`GET /batches/{id}/items` now returns one page of items in submission order,
each with its own status (`QUEUED`, `APPROVED`, `DENIED`, `FAILED`, or
`CANCELLED`) and revolving amount. `?status=` filters, `?limit=` sizes the
page (100 by default, 1000 at most), and `next_cursor` reads the next page.
Every item has an `item_id`, a version 7 UUID that the `202` of
`POST /evaluations/batch` returns in the order of the submitted customers.
Retry and cancel take that ID.

The challenge asks for "um relatório com os clientes aprovados, reprovados e os
respectivos valores liberados". A list of items where each item carries its
status and amount answers it, and a caller can page it however large the batch
is.

## Considered options

- **Keep the report and add paging to each list.** Rejected: four cursors on
  one response, and the counters and total still need the whole batch.
- **Keep counters on a batch row.** Rejected: every transition would write that
  one row, the hot spot that 3b26ebe removed.
- **Keep the index as the item ID.** Rejected: a caller cannot tell which
  position a result belongs to without the original array, and the index
  leaks the storage layout into every route.
- **A random (version 4) item ID.** Rejected: a Query would return items in a
  random order. Version 7 IDs sort in the order they were made, so the list
  follows the submitted array with no index stored.

## Consequences

- The item row is `BATCH#<batch_id>` / `ITEM#<item_id>`. There is no batch row:
  a batch always has at least one item, so a partition with no rows is an
  unknown batch. `Page` reads that from `ScannedCount` on the first page.
- A decided item stores its decision as its status. `DECIDED` is gone, and
  `?status=APPROVED` is a plain attribute filter.
- Query's `Limit` counts rows before the status filter, so the adapter repeats
  the Query until the page is full or the partition ends. The cursor is the
  last returned item ID, encoded.
- A caller knows a batch is done when `?limit=1&status=QUEUED` returns no
  items. There is no batch status to read.
- This supersedes two points of ADR 0001: items are keyed by `item_id`, not by
  index, and a batch has no status, derived or stored. ADR 0001's reason for
  never cancelling automatically still holds: a failed item waits for an
  operator.
