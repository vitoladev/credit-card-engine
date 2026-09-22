---
status: accepted
---

# Fail closed, and let operators recover failed batch items

A credit decision that is not recorded cannot be audited, so a single
evaluation returns `503` when the decision cannot be persisted, even though the
rules already produced a result. In a batch, a batch item that still fails after
SQS redelivery (`maxReceiveCount=3`) lands in the DLQ and becomes a failed item.
From there an operator retries or cancels that item through the API, without
engineering involvement, with at most 5 attempts per item. Decisions are keyed
by the item's index in the batch, so a retry overwrites instead of duplicating.

## Considered options

- **Fail open on the sync path** (return the decision, persist later).
  Rejected: an approval that was never recorded breaks the audit trail.
- **Redrive the DLQ** (`StartMessageMoveTask`). Rejected: the DLQ is shared by
  every batch, so a redrive cannot target one batch or one item.
- **Cancel the whole batch.** Rejected: nothing asked for it, and a batch
  finishes in seconds. The unit an operator acts on is the failed item.
- **Cancel automatically after the last attempt.** Rejected: it would complete
  the batch without anyone looking. Cancelling is always an operator's call.

## Consequences

- `submit` persists each customer's input in its batch item (`ITEM#<index>`),
  so a retry can re-enqueue one item. The writes are batched
  (`BatchWriteItem`), never one call per customer.
- A batch has no stored status. It is derived from item counters: processing,
  needs attention, or completed.
- The DLQ is a signal (alarm and investigation), not a recovery mechanism.
