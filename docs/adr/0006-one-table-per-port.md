---
status: accepted
---

# One DynamoDB table per port

The engine kept everything in one table with generic keys (`pk`, `sk`). The
prefix of `pk` told the row types apart: `DECISION#` for a single evaluation,
`BATCH#` with `ITEM#` for a batch item, and `IDEMPOTENCY#` for an
`Idempotency-Key`. Single-table design pays off when one `Query` reads several
entities or one transaction writes them. The engine does neither: every read
and every write touches exactly one row type. So the shared table bought
nothing, and it cost the engine in four places:

- **The stream carried rows the relay does not need.** The relay read every
  decision and every key, the customer's name included, only to skip them.
- **TTL applied to the whole table** for the one row type that expires.
- **IAM could not tell the row types apart.** The worker and the DLQ consumer
  could read and write decisions and keys.
- **The table's properties did not describe any one entity**, which made the
  data model harder to explain than the data.

Now each port has its own table, keyed by its own IDs:

| Table | Port | Keys | Stream | TTL | Point-in-time recovery |
|---|---|---|---|---|---|
| `Decisions` | `evaluate.DecisionStore` | `decision_id` | no | no | yes |
| `BatchItems` | `batch.Items` | `batch_id`, `item_id` | `NEW_IMAGE`, to the relay | no | yes |
| `IdempotencyKeys` | `idempotency.Store` | `idempotency_key` | no | `expires_at` | no |

`adapter/ddb` implements the three ports as `ddb.Decisions`, `ddb.Items`, and
`ddb.Keys`. The HTTP Lambda reads and writes all three. The worker and the DLQ
consumer reach only `BatchItems`, and the relay reads only its stream.

## Considered options

- **Keep one table and defend it here.** Rejected: the only argument left was
  one resource instead of three, and the costs above are real.
- **Keep the `pk` and `sk` names in the new tables.** Rejected: with one row
  type per table, a prefix in the key carries no information, and
  `batch_id`, `item_id` read as what they are.
- **Put the idempotency key and the decision in one transaction.**
  `TransactWriteItems` works across tables, so the split does not rule it out.
  ADR 0005 already rejected it for another reason: a batch writes more items
  than a transaction takes.

## Consequences

- The relay no longer skips rows: every record on the `BatchItems` stream is a
  batch item. The name in a decision or in a stored idempotency response no
  longer travels in a stream.
- The stack has three tables and three `CfnOutput`s: `DecisionsTable`,
  `BatchItemsTable`, and `IdempotencyKeysTable`. The Lambdas read their table
  names from `DECISIONS_TABLE`, `BATCH_ITEMS_TABLE`, and
  `IDEMPOTENCY_KEYS_TABLE`.
- `IdempotencyKeys` has no point-in-time recovery: a key lives 24 hours and
  holds nothing an audit needs.
- The change replaces the tables, so a deployed stack starts empty. On Floci,
  run `make local-redeploy`.
