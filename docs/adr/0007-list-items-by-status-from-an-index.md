---
status: accepted
---

# List a batch's items by status from a global secondary index

`GET /batches/{id}/items?status=FAILED` and `POST /batches/{id}/retry-failed`
ran a `Query` on the batch and dropped the other statuses with a filter
expression. DynamoDB charges for every item a `Query` reads, "regardless of
whether a filter expression is present", and applies the 1 MB page limit
before the filter. So listing the 3 failed items of a 1,000-item batch read
all 1,000 items, and the page loop repeated the `Query` until it had enough
matches.

`BatchItems` now has a global secondary index, `by-status`:

| Key | Attribute | Value |
|---|---|---|
| Partition | `batch_status` | `<batch_id>#<status>`, for example `4362f754-…#FAILED` |
| Sort | `item_id` | the version 7 UUID, so the index keeps submission order |

The index projects every attribute, because a list returns the whole item.
A `Query` on `batch_status` reads only the items in that status. A list
without `?status=` still reads the table.

Every write that sets `status` also sets `batch_status`, in the same request:
`Create` puts both, and `move`, which every transition goes through, adds
`batch_status` to its `UpdateExpression`. DynamoDB moves the item between
index partitions when `batch_status` changes.

## Consistency

A global secondary index is eventually consistent. An item that just changed
status can show under its old status for a moment, usually well under a
second. That changes three reads:

- **A list by status** can lag a transition. A list without a status is still
  a consistent read.
- **`retry-failed`** can miss an item that failed a moment ago, or try one
  that just moved on. The retry is a conditional `UpdateItem`, so a stale
  entry fails its condition and is skipped. Nothing is retried twice.
- **"The batch is done when `?limit=1&status=QUEUED` is empty"** can say done
  a moment early, for example right after an operator retry. A caller that
  needs certainty reads the list without a status, which is consistent.

An empty first page with a status still tells an unknown batch apart: the
store runs one consistent `Query` with `Limit: 1` on the table, and a batch
with no items is a `404`.

## Partition load

DynamoDB gives each partition 3,000 read units and 1,000 write units a
second. Items here are under 1 KB, so a write costs 1 unit and a consistent
read 1 unit.

| Table or index | Partition key | Load |
|---|---|---|
| `Decisions` | `decision_id`, a random UUID | spread evenly |
| `IdempotencyKeys` | `idempotency_key`, one per client request | spread evenly |
| `BatchItems` | `batch_id` | one batch is one item collection |
| `by-status` | `<batch_id>#<status>` | one batch splits across up to 5 keys |

The one hot spot is a large batch. All its items share a `batch_id`, and at
submit they all share `<batch_id>#QUEUED` in the index. A batch of the
default 100 items costs about 100 writes at submit and 200 operations to
decide, far below the limit. The requirement's 10,000 evaluations a minute is
about 170 a second, spread across many batches. At the maximum `BATCH_SIZE`
of 1,000, one batch written in about a second is at the 1,000 writes a second
of one partition. Adaptive capacity can split a hot item collection by sort
key, but not when the traffic follows a sort key that only increases, and a
version 7 `item_id` does. A throttled write is retried: `BatchWriteItem`
retries its `UnprocessedItems` with backoff, and a throttled transition is
redelivered by SQS. So throttling costs latency, not items.

The `BatchItemsThrottled` alarm fires when `BatchItems` throttles in 3
consecutive minutes.

## Considered options

- **Keep the filter.** Rejected: the cost grows with the batch, not with the
  answer, and an operator's `retry-failed` on a large batch read every item.
- **A sparse index with only the statuses an operator acts on** (`FAILED` and
  `QUEUED`). Rejected for now: `?status=APPROVED` is how a client reads the
  approved customers, the report the challenge asks for.
- **An index keyed by `status` alone.** Rejected: every `QUEUED` item of every
  batch would share one partition key, the hot spot the batch key avoids.
- **Shard `batch_id`** (`<batch_id>#<n>`) to spread one batch over several
  partitions. Rejected for now: a list in submission order would have to merge
  n queries and their cursors, for a load the requirement does not reach.
  Adopt it if batches grow past 1,000 items or `BatchItemsThrottled` fires.

## Consequences

- Each transition writes the index too: on-demand bills an index write for the
  new `batch_status` and one for removing the old entry.
- The index doubles the storage of `BatchItems`, because it projects every
  attribute.
- The cursor of a list by status carries the same `item_id` as before; the
  store rebuilds the index key from the path's `batch_id` and the `status`.
- This supersedes the point of ADR 0003 where a list by status is a filter on
  the batch's items.
