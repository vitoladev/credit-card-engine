# Architecture

A revolving-credit evaluation. The cut is the rules engine, the batch item list, and
the AWS infra that backs the criteria.

IaC is CDK in Go (`packages/infra-iac/`). The product lives in
`apps/engine/`, split into two deep modules: `evaluate` and `batch`. This file, `apps/engine/architecture_test.go`,
and the stack describe the cut.

## Cut

| Piece | Role |
|---|---|
| `internal/domain` | Customer profile, its validation, and the decision. No I/O. |
| `internal/rules` | `Policy.Evaluate(Customer) Result`: ordered `Rule` funcs (the first that denies decides) plus the score bands that size an approval. `NewPolicy()` is the product policy. Pure. |
| `internal/evaluate` | The single evaluation. `Evaluate(ctx, customer)` applies the policy, records the decision, and only then returns it (fail closed). `Get` reads a recorded decision. Declares its ports: `DecisionStore` (`Save`, `Get`) and `Emitter`. |
| `internal/batch` | The batch and its items. `Submit`, `Relay([]ItemEvent)`, `Process(ItemEvent)`, `DeadLetter(ItemEvent)`, `Retry`, `RetryFailed`, `Cancel`, `ListItems`. Gives each item a version 7 `item_id`, writes items without publishing, relays the changes that queue an item, evaluates the item a queued event names, pages the item list, and emits the item metrics. Declares its ports: `Items`, `Publisher`, `Emitter` ([ADR 0001](adr/0001-fail-closed-and-operator-driven-item-recovery.md), [ADR 0004](adr/0004-relay-queued-items-from-the-table-stream.md)). |
| `internal/adapter/httpapi` | HTTP API v2. Validates customers, calls `evaluate` or `batch`, and maps their errors to status codes. |
| `internal/adapter/sqs` | One queue consumer with two roles: `Worker` (queue, calls `batch.Process`) and `DeadLetters` (DLQ, calls `batch.DeadLetter`). Reports partial batch failures. |
| `internal/adapter/ddb` | Implements `batch.Items` and `evaluate.DecisionStore` on one DynamoDB table. The item status transitions are its conditional updates. `Relay` reads the table's stream into item events, next to the row format it decodes. |
| `internal/adapter/sqspub` | Implements `batch.Publisher` with `SendMessageBatch`. Only the relay uses it. |
| `internal/adapter/telemetry` | `EMF` implements both `Emitter` ports. One JSON line per decision or failed item, no AWS SDK. |
| `internal/flocitest` | Test-only. AWS config for Floci, fresh tables (stream on) and queues, the table's stream as Lambda events, and faults injected in the SDK. |
| `cmd/http` `cmd/relay` `cmd/worker` `cmd/dlq` | Composition root. JSON `slog` on stdout. |
| `packages/infra-iac` | CDK. HTTP API with IAM authorizer except `/health`, four Lambdas, the table's stream, SQS with its DLQ, DynamoDB, logs, dashboard, alarms. |
| `packages/loadtest` | k6 at `LOADTEST_RATE` req/s (default 100 locally) on `POST /evaluations/batch` for 10 s, 8 VUs on Floci. Local gate is p95 under 2 s and under 1% errors. p99 under 800 ms is the real-AWS NFR. The 1000 req/s NFR run (10k requests) is `LOADTEST_RATE=1000 make loadtest` against a real AWS stack. |

## Runtime

```mermaid
flowchart LR
  subgraph clients [Ingress]
    sync["POST /evaluations"]
    batch["POST /evaluations/batch"]
    getOne["GET /evaluations/:id"]
    getReport["GET /batches/:id/items"]
    recover["POST /batches/:id/items/:item_id/retry | cancel\nPOST /batches/:id/retry-failed"]
  end

  subgraph edge [AWS CDK]
    api["HTTP API v2\n1200 rps / 2400 burst"]
    fn["Lambda HTTP\narm64 / 3s"]
    stream[["DynamoDB Streams\nNEW_IMAGE, 24h"]]
    relay["Lambda Relay\nbatch 100 or 1 s, bisect, partial failures"]
    q["SQS EvaluationJobs"]
    worker["Lambda worker\nSQS batch 10, partial failures"]
    dlq["SQS EvaluationJobsDLQ\n14d, after 5 receives"]
    dlqFn["Lambda DlqConsumer\nSQS batch 10, partial failures"]
    ddb[("DynamoDB\npk / sk")]
    logs["CloudWatch Logs\n14d"]
    alarms["Alarms\nAPI 5xx, worker, DLQ, relay lag, p99"]
    dash["Dashboard CreditCardEngine"]
  end

  subgraph core [apps/engine]
    ev["evaluate"]
    chain["rules.NewPolicy()"]
    domain["domain.Result"]
  end

  sync --> api --> fn --> ev
  fn -->|"PutItem IDEMPOTENCY# if absent (Idempotency-Key)"| ddb
  fn -->|"DECISION# / RESULT"| ddb
  batch --> api --> fn -->|"BatchWriteItem ITEM#"| ddb
  ddb --> stream --> relay -->|"SendMessageBatch, QUEUED only"| q --> worker --> ev
  worker -->|"GetItem"| ddb
  worker -->|"UpdateItem QUEUED → APPROVED | DENIED"| ddb
  q -->|"maxReceiveCount 5"| dlq --> dlqFn
  dlqFn -->|"UpdateItem QUEUED → FAILED"| ddb
  recover --> api --> fn -->|"UpdateItem FAILED → QUEUED / CANCELLED"| ddb
  getOne --> api
  getReport --> api
  ev --> chain --> domain
  fn --> logs
  fn --> alarms
  fn --> dash
```

Two paths, on purpose:

- Sync (`POST /evaluations`). The handler evaluates one customer under a 1 s
  SLO and returns the decision now. The decision is stored under a new
  `decision_id` and read back with `GET /evaluations/{id}`.
- Batch (`POST /evaluations/batch`, then SQS, then the worker, then
  `GET /batches/{id}/items`). HTTP stores the batch items and enqueues.
  This is the loadtest path (1000 req/s NFR on real AWS, 100 req/s by
  default on Floci).

DynamoDB writes after the decision. The sync path fails closed
([ADR 0001](adr/0001-fail-closed-and-operator-driven-item-recovery.md)).
If the decision cannot be stored, `POST /evaluations` returns
`503 {"error":"decision_not_recorded"}` and no decision. A decision that was
never recorded cannot be audited. On batch, HTTP already returned `202`. SQS
redelivers the record to the worker, then the DLQ takes the record (see
[Resilience](#resilience)).

### Batch submission

`POST /evaluations/batch` accepts at most `BATCH_SIZE` customers (default
100, max 1000). A larger batch returns
`422 {"error":"batch_too_large","max":<BATCH_SIZE>}` and nothing is stored or
queued. The CDK stack passes `BATCH_SIZE` from the synth environment to the
HTTP Lambda and fails the synth on a value outside 1..1000. A bad value also
fails the Lambda at startup. `scripts/floci.env` defaults `BATCH_SIZE` to 100.
`BATCH_SIZE=1000 make local-deploy` raises the cap.

Then `batch.Submit`:

1. Creates a `batch_id`, and one `item_id` per customer with `uuid.NewV7`.
   Version 7 UUIDs sort in the order they were made, so item IDs follow the
   submitted array.
2. Stores one `ITEM#<item_id>` row per customer (`status=QUEUED`,
   `attempts=1`, the customer input) with `BatchWriteItem`, 25 items per call,
   8 calls in flight. There is no batch row. The store retries
   `UnprocessedItems` with backoff. If a write fails, the store queries the
   batch partition and deletes leftover rows so a retry does not hit
   half-written keys. If that delete also fails, the leftover rows make a
   half-written batch readable, but its `batch_id` never reached a caller:
   `Submit` returned `503`.
3. Returns `202 {"batch_id","queued","item_ids"}`, where `queued` is the
   number of items and `item_ids` follow the submitted customers. Nothing is
   published in the request.

If storing the batch fails, the handler returns
`503 {"error":"batch_not_recorded"}`.

The table's stream carries each new row to the `Relay` Lambda
([ADR 0004](adr/0004-relay-queued-items-from-the-table-stream.md)). Its event
source mapping invokes the relay when 100 records are ready or 1 s after the
first one, whichever comes first, so a quiet stream still flows within a
second. A stream has no visibility timeout: the mapping keeps a checkpoint per
shard. The relay
reads each record into an item event, `{event_id, batch_id, item_id, attempt,
status}`, with `event_id` = `<batch_id>:<item_id>:<attempt>:<status>`.
`batch.Relay` publishes the events whose status is `QUEUED` with
`SendMessageBatch`, 10 per call, 10 calls in flight. Other transitions reach
the relay too and publish nothing. The message has no customer data: the
worker reads the item.

If a publish fails, the relay reports that record, and the event source mapping
delivers the batch again from it (bisecting the batch on an error). The item
stays `QUEUED`: a transient SQS error no longer fails it. A record the relay
cannot read is logged as `stream_record_skipped` and dropped, so it does not
block its shard. The `RelayIteratorAge` alarm fires when the relay falls 60 s
behind.

### Batch item status

The worker hands the message to `batch.Process`, which reads the item with a
consistent `GetItem`. An item that already moved on (decided, or queued again
on a newer attempt) is acknowledged. Otherwise `Process` evaluates the item's
customer and calls `Items.Decide(batch_id, item_id, attempt, result)`. One conditional
`UpdateItem` moves the item from `QUEUED` to its decision, `APPROVED` or
`DENIED`, and stores the result. The update applies only if the item is
`QUEUED` on the same attempt. A repeated delivery fails that condition and
returns `ErrInvalidTransition`. `Process` treats that as success, so the item
is decided once and its metric is emitted once.

Every transition is one `UpdateItem` on the item's own `ITEM#` row,
conditional on its current status and on attempt where that applies. No
transition writes a shared row. Workers that decide items of
one batch in parallel therefore never write the same row. Two operators, or an operator and a late
delivery, cannot both win a transition. These conditions are the only copy of
the item status rules: there is no in-memory store, and tests run against
Floci ([ADR 0002](adr/0002-keep-aws-behind-adapters-with-one-implementation.md)).

```mermaid
stateDiagram-v2
  [*] --> QUEUED: submit (attempts 1)
  QUEUED --> APPROVED: worker Decide (same attempt)
  QUEUED --> DENIED: worker Decide (same attempt)
  QUEUED --> FAILED: DLQ consumer Fail (same attempt)
  FAILED --> QUEUED: operator retry (attempts < 5, attempts + 1)
  FAILED --> CANCELLED: operator cancel
  CANCELLED --> CANCELLED: cancel again (200, no change)
  APPROVED --> [*]
  DENIED --> [*]
  CANCELLED --> [*]
```

| Transition | Called by | Condition | Update |
|---|---|---|---|
| `Decide(batch_id, item_id, attempt, result)` | `batch.Process` (worker) | `QUEUED` on `attempt` | `status=APPROVED` or `DENIED`, `result` |
| `Fail(batch_id, item_id, attempt)` | `batch.DeadLetter` (DLQ consumer) | `QUEUED` on `attempt` | `status=FAILED` |
| `Retry(batch_id, item_id, attempt)` | `batch.Retry`, `batch.RetryFailed` | `FAILED` on `attempt`, `attempts < 5` | `status=QUEUED`, `attempts+1` |
| `Cancel(batch_id, item_id)` | `batch.Cancel` | `FAILED`. Cancelling a `CANCELLED` item succeeds with no change | `status=CANCELLED` |

A retry's message carries the new attempt. A late message for an older attempt
fails the `Decide` condition, so only the current attempt can decide the item.
A failed item keeps its attempts, so the list shows how many passes it took.

### Item list

`GET /batches/{id}/items` returns one page of items in submission order
([ADR 0003](adr/0003-list-batch-items-by-page.md)). A batch has no status and
no totals: each item carries its own status and revolving amount. A caller
knows the batch is done when `?limit=1&status=QUEUED` returns no items.

`batch.ListItems` asks `Items.Page` for at most `limit` items (100 by default,
1000 at most) after the cursor's item. `Page` runs a `Query` on
`pk = BATCH#<batch_id>` and `begins_with(sk, "ITEM#")`, with
`#status = :status` as a filter when `?status=` is set. Query's `Limit`
counts rows before the filter, so `Page` repeats the Query until the page is
full or `LastEvaluatedKey` is empty. A 1 MB response ends a Query early the
same way. The cursor is the last returned `item_id`, base64url-encoded. The
server builds the `ExclusiveStartKey` from the path's `batch_id` and that ID.

A batch always has at least one item, so on the first page a `Query` that
scanned no rows (`ScannedCount = 0`) means an unknown batch: `404`. Rows
scanned with none matching the filter is a `200` with no items.

### Resilience

The worker returns `events.SQSEventResponse` and lists in
`batchItemFailures` only the records that failed. A decided record and a
redelivery that hits `ErrInvalidTransition` are not listed. The event source
has `ReportBatchItemFailures`, so SQS redelivers only the failed records.

`EvaluationJobs` sends a record to `EvaluationJobsDLQ` (14-day retention)
after `maxReceiveCount=5` receives (the AWS recommendation for a Lambda source). The `DlqConsumer` Lambda calls
`batch.DeadLetter` for each record, which calls
`Items.Fail(batch_id, item_id, attempt)` and makes the item `FAILED`. The DLQ
is a signal, not a
recovery path. Nothing redrives the DLQ
([ADR 0001](adr/0001-fail-closed-and-operator-driven-item-recovery.md)).

A record whose batch or item does not exist (for example left over from a
deleted stack) can never succeed. The worker treats that record like any
other failure, so the record reaches the DLQ after 5 receives instead of
looping forever. The DLQ consumer acknowledges that record, a record it
cannot parse, and a record whose item already moved on (decided, failed, or
retried on a newer attempt). The consumer reports a record as failed only
when the store write fails, so SQS retries that record within the DLQ's
retention.

`POST /batches/{id}/items/{item_id}/retry` reads the item
(`ITEM#<item_id>`) and moves it `FAILED → QUEUED` on the next attempt. The
stream delivers that change to the relay, which publishes the new attempt. An
`item_id` that is not a UUID is a `404` before any store call.

`POST /batches/{id}/retry-failed` reads the failed items with `Items.Page`
(`?status=FAILED`, pages of 1000). `RetryMany` moves them
`FAILED → QUEUED` with one conditional `UpdateItem` per item, 8 in flight, and
`requeued` counts the items that moved. The relay publishes them from the
stream. There is no whole-batch
cancel and no automatic cancel after the last attempt. Cancelling is always
an operator's call.

## Observability

CDK enables X-Ray tracing (Active) on all four Lambdas. The HTTP request's
trace ends at the table write: a stream record carries no trace header. The
relay's invocation starts the batch's trace, and `sqspub` copies
`_X_AMZN_TRACE_ID` onto every message as the SQS `AWSTraceHeader` system
attribute. The worker continues that trace. If the message reaches the DLQ,
the DLQ consumer continues the same trace.

Each Lambda logs JSON with `log/slog` on stdout. Keys are `snake_case`. A
handler that returns a `5xx` logs the cause once at `error`, with no customer
name or full CPF. The worker and DLQ consumer log `record_failed` at `error`
with `message_id`, `error`, and `batch_id` and `item_id` when the body parsed.
They never log the body or customer fields.

Each recorded decision writes one CloudWatch Embedded Metric Format line in
namespace `CreditCardEngine`, through the modules' `Emitter` port. A batch
item writes it only when `Items.Decide` succeeds, so a redelivered message is
not counted twice. A single evaluation writes it only after the decision is
recorded.

| Metric | When | Dimensions |
|---|---|---|
| `Approved` | decision is approved | none |
| `Denied` | decision is denied | none |
| `DenyByReason` | decision is denied | `reason` |
| `DecisionLatencyMs` | every recorded decision | none |
| `ItemsFailed` | `batch` moved the item to `FAILED` (DLQ consumer) | none |
| `ItemEndToEndMs` | a batch item is decided: time from `queued_at` (submit or retry) to the decision | none |

Decision line properties: `decision`, `reason`, `latency_ms`, `cpf_masked`,
and either `decision_id` (sync) or `batch_id` and `item_id` (batch). Failed-item
line properties: `batch_id`, `item_id`, `attempt`. Logs, EMF properties, and
metric dimensions never include a customer name or a full CPF.

The CloudWatch dashboard `CreditCardEngine` shows evaluations per minute,
approval rate, `DenyByReason` by reason, `DecisionLatencyMs` p99,
`ItemEndToEndMs` p99, the age of the oldest queued message, HTTP Lambda p99
duration, API 5xx, DLQ visible messages, and `ItemsFailed`. Missing data is
not breaching. Alarms:

| Alarm | Fires when |
|---|---|
| `Api5xx` | API Gateway 5xx ≥ 1 in 1 minute |
| `WorkerErrors` | worker errors ≥ 1 in 1 minute |
| `DlqConsumerErrors` | DLQ consumer errors ≥ 1 in 1 minute |
| `DlqVisible` | DLQ visible messages > 0 for 5 minutes |
| `EvaluateLatency` | HTTP Lambda p99 > 800 ms for 3 minutes (sync SLO is 1 s) |
| `RelayIteratorAge` | the relay is more than 60 s behind the table stream for 3 minutes |
| `QueueBacklog` | the oldest `EvaluationJobs` message is older than 60 s for 3 minutes |
| `ItemEndToEnd` | batch item p99 from queued to decided > 5 s for 3 minutes (the batch SLO) |

## Data model

One DynamoDB table, `Decisions`, with generic keys `pk` (string) and `sk`
(string).

| `pk` | `sk` | Attributes |
|---|---|---|
| `DECISION#<decision_id>` | `RESULT` | `customer` (input JSON), `result` (decision JSON) |
| `BATCH#<batch_id>` | `ITEM#<item_id>` | `item_id`, `customer` (input JSON), `status`, `attempts`, `result` once decided |
| `IDEMPOTENCY#<key>` | `KEY` | `fingerprint`, `owner`, `state`, `lease_until`, `expires_at` (TTL), `status_code` and `body` once `DONE` ([ADR 0005](adr/0005-idempotency-key-claimed-with-a-conditional-write.md)) |

A batch is its item rows. There is no batch row: an unknown batch is a
partition with no rows. `item_id` is a version 7 UUID, so a Query on the
partition returns items in submission order.

An `IDEMPOTENCY#` row holds one `Idempotency-Key` for 24 hours: the table's
TTL attribute is `expires_at`. It carries no customer data, only the
fingerprint of the request and the response it replays.

Each batch item stores the input and the item status together. Stored
decisions and batch items keep the full CPF and name with no TTL. They are
the audit record. Every API response masks the CPF (`***` + last 2 digits).

## Packages

The same DAG that `architecture_test.go` locks. `domain`, `rules`, `evaluate`,
and `batch` do not import AWS. The modules declare their ports, and the
adapters implement them.
A new rule is a constructor that returns a `Rule`, added to the list in
`rules.NewPolicy()`. The modules and adapters stay the same.

```mermaid
flowchart TB
  http["cmd/http"] --> httpapi["adapter/httpapi"]
  http --> ddbA["adapter/ddb"]
  http --> pub["adapter/sqspub"]
  http --> tel["adapter/telemetry"]
  worker["cmd/worker"] --> sqs["adapter/sqs"]
  worker --> ddbA
  worker --> tel
  dlqCmd["cmd/dlq"] --> sqs
  dlqCmd --> ddbA
  dlqCmd --> tel
  httpapi --> evaluate
  httpapi --> batch
  httpapi --> idem["idempotency"]
  ddbA --> idem
  sqs --> batch
  ddbA --> batch
  ddbA --> evaluate
  pub --> batch
  tel --> domain
  evaluate["evaluate"] --> rules["rules"]
  batch["batch"] --> rules
  rules --> domain["domain"]
```

## Validation

The HTTP adapter validates every customer with `domain.Customer.Validate()`
before it calls `evaluate` or `batch`. Validation does no I/O and returns every
violation as `{field, code}`. An invalid customer is never evaluated and gets
no decision.

| Field | Code | When |
|---|---|---|
| `cpf` | `invalid_length` | not 11 digits after removing dots and dash |
| `cpf` | `repeated_digits` | all 11 digits are the same (`11111111111`) |
| `cpf` | `invalid_check_digits` | either mod 11 check digit is wrong |
| `name` | `required` | empty or blank |
| `credit_score`, `current_invoice_cents`, `credit_limit_cents`, `late_payments` | `negative` | below zero |
| `monthly_spend_cents[i]` | `negative` | entry `i` is below zero |

The CPF is normalized to 11 bare digits (`390.533.447-05` → `39053344705`)
before it reaches `evaluate` or `batch`.

`POST /evaluations` returns `400 {"error":"invalid_json"}` for malformed JSON
and `422 {"error":"invalid_customer","violations":[...]}` for an invalid
customer. If any customer in `POST /evaluations/batch` is invalid, the whole
batch is rejected with `422`, each violation carries the customer's `index`,
and nothing is stored or published.

```json
{"error":"invalid_customer","violations":[{"index":1,"field":"cpf","code":"invalid_check_digits"}]}
```

## Decision

`rules.NewPolicy()` builds a `rules.Policy` from an ordered list of rules and
the score bands. Each rule is a `rules.Rule`, a function that denies with a
stable reason code or passes: a Strategy per criterion. `Policy.Evaluate`
applies them in order, and the first rule that denies decides, with one reason.
The amount is computed only if every rule passes. To add a rule, write one
`Rule` function and add it to the list in `NewPolicy()`. Nothing else changes.

```mermaid
flowchart TD
  in["Customer"] --> s["min_score ≥ 600"]
  s -->|no| d1["DENIED score_below_600"]
  s -->|yes| l["max_late_payments ≤ 2"]
  l -->|no| d2["DENIED late_payments_above_2"]
  l -->|yes| i["invoice_within_limit"]
  i -->|no| d3["DENIED invoice_exceeds_credit_limit"]
  i -->|yes| g["recent_spend: limit > 0, history not empty, 3m avg ≤ 90% of limit"]
  g -->|no| d4["DENIED no_credit_limit / insufficient_spend_history / recent_spend_above_share"]
  g -->|yes| amt["revolving_amount = min(credit_limit × factor, credit_limit − invoice), never negative"]
  amt --> ok["APPROVED"]
```

The score bands in `NewPolicy()` size an approval. A higher score gets a
larger share of the credit limit on revolving credit, capped at
`credit_limit − current_invoice` and never negative. Changing a band is a
number in `NewPolicy()`. Replacing the sizing model changes `policy.go`.

| Score | Factor |
|---|---|
| ≥ 800 | 80% |
| 700 to 799 | 50% |
| 600 to 699 | 30% |
| < 600 | never reaches here. `min_score` denies |

Money is integer cents. Reports and logs use a masked CPF (`***` + last 2
digits), never the raw CPF.

## Why these rules

Criteria are documented here. This is not a real bureau.

1. Minimum score 600. Below that, revolving credit is already credit risk.
   One cutoff, testable, no model.
2. At most 2 late payments. Recent late payments are the cheapest predictor
   of revolving default. Three or more means collections already started.
3. Current invoice at or under the credit limit. Granting revolving credit
   on top of an over-limit invoice increases exposure with no collateral.
4. Recent spend at or under 90% of the credit limit. A customer already
   consuming the limit is revolving in practice. Average of the last 3
   months, not a single month. A credit limit of zero or less denies with
   `no_credit_limit`. An empty spend history denies with
   `insufficient_spend_history` because there is no evidence to size the
   risk.

Change a cutoff by editing its argument or a band in `rules.NewPolicy()`.
Change the policy with a new rule constructor in its own file plus one entry
in that list.

## Evaluation criteria

| Criterion | How the design answers |
|---|---|
| Latency ≤ 1 s | The sync path evaluates in memory and makes one DynamoDB write. Lambda timeout 3 s, 256 MB, arm64. HTTP p99 is alarmed at 800 ms. The batch path answers after one `BatchWriteItem`; each item's queued-to-decided time is `ItemEndToEndMs`, alarmed at p99 > 5 s. See [Benchmarks on Floci](#benchmarks-on-floci). |
| Accuracy | Customers validated at the edge (CPF check digits, no negatives). Deterministic rules. Table tests in `domain` and `rules`. Stable reason code per rule. |
| Scale 10k/min | HTTP writes the items (202), the relay publishes from the stream, and the worker takes up to 50 messages per invocation (1 s window) and handles them concurrently. DynamoDB is on-demand. Stage throttle is 1200 rps / 2400 burst. On AWS, Lambda scales an SQS source to 5 concurrent batches and then adds up to 300 invocations a minute, up to 1,250. Floci cannot show this: see [Benchmarks on Floci](#benchmarks-on-floci). The 1000 req/s runs against real AWS are `LOADTEST_PATH=single LOADTEST_RATE=1000 make loadtest` and `LOADTEST_PATH=batch LOADTEST_RATE=100 make loadtest`. They have not been run: there is no AWS account in this cut. |
| Extensibility | Ordered `rules.Rule` list and score bands, assembled in `rules.NewPolicy()`. `architecture_test.go` keeps domain, rules, and the modules off AWS. |
| LGPD | CPF masked on `Result` and in logs. Encryption at rest on the table and both queues (SSE-SQS), and the queues deny requests not over TLS. IAM authorizer on every HTTP route except `/health`. Full CPF and name stay in DynamoDB. Queue messages and the relay carry no customer data. The HTTP Lambda has no access to the queue. |
| Observability | 14-day logs. One EMF line per decision (`Approved`/`Denied`, `DenyByReason`, `DecisionLatencyMs`) and per failed item (`ItemsFailed`). Dashboard `CreditCardEngine`. Alarms on API 5xx, worker errors, DLQ consumer errors, DLQ depth, relay iterator age over 60 s, and HTTP p99 > 800 ms. |
| Resilience | `rules` has no I/O. `evaluate` records after the decision, and the sync path fails closed with `503`. On batch, SQS isolates HTTP from the worker. The worker reports partial batch failures, a record that fails 5 receives goes to the DLQ, and the DLQ consumer marks its item `FAILED` for an operator to retry or cancel. Submit and retry only write the item; the table's stream feeds a relay that publishes, and retries a failed publish for up to 24 h (ADR 0004). Every transition is conditional. On-demand table with point-in-time recovery (35 days). 5xx alarm on the sync path. |

### Benchmarks on Floci

Run on Floci in the Dev Container on 2026-09-23, on this branch. The
numbers are the emulator's ceiling, not the engine's: Floci starts one container
per Lambda invocation, the local stack caps concurrency (Evaluate 8, Worker 4),
and Floci's SQS poller makes one `ReceiveMessage` per second.

| Run | Command | Result |
|---|---|---|
| Sync, 1000 req/s for 10 s | `LOADTEST_PATH=single LOADTEST_RATE=1000 make loadtest` | 18.5 req/s reached (196 sent, 9,805 dropped by k6 for lack of VUs), 0 errors, HTTP p95 734 ms. The Lambda's own time is p50 373 ms, mostly the emulator's `PutItem`. |
| Batch, 100 batches × 10 customers | `LOADTEST_PATH=batch LOADTEST_RATE=10 make loadtest` | 89 batches sent (k6 dropped 11 when Floci slowed), 0 errors. All 890 items decided, 0 failed, in 92 s: ~9.7 items/s, because the poller received 10 messages per poll, one poll a second. `ItemEndToEndMs` p50 43 s, p99 81 s, all of it queue wait. An earlier run with the sequential worker decided all 1,000 items of 100 batches (400 approved, 600 denied) at the same ~9.4 items/s, with submit p50 82 ms. |

A worker batch of 50 made Floci worse: its poller received 1 message per poll
when the batch was over the 10 of one `ReceiveMessage` call. The stack keeps 10
on Floci and 50 on AWS, where Lambda fills the batch across calls within the
window.

To reproduce, run `make local-redeploy` first: Floci cannot update the relay's
stream event source mapping in place, so a second `make local-deploy` fails.

## API

Wire types are `events.APIGatewayV2HTTPRequest` and `HTTPResponse`.

| Method | Path | Body | Response |
|---|---|---|---|
| `GET` | `/health` | — | `{"status":"ok"}` |
| `POST` | `/evaluations` | one `Customer` | `200` + `{decision_id, ...Result}` (sync, 1 s SLO); `400` malformed JSON; `422` invalid customer; `503 {"error":"decision_not_recorded"}` when the decision cannot be stored. Takes `Idempotency-Key` (see below) |
| `GET` | `/evaluations/{id}` | — | `200` + the same `{decision_id, ...Result}`; `404 {"error":"not_found"}` |
| `POST` | `/evaluations/batch` | `{customers:[...]}` or array, at most `BATCH_SIZE` (default 100, max 1000) | `202` + `{batch_id, queued, item_ids}`, `item_ids` in the order of the customers; `422` with indexed violations or `batch_too_large`, nothing stored; `503 {"error":"batch_not_recorded"}`. Takes `Idempotency-Key` |
| `GET` | `/batches/{id}/items?status=&limit=&cursor=` | — | `200` + one page of items; `400 {"error":"invalid_status"}`, `{"error":"invalid_limit","max":1000}`, or `{"error":"invalid_cursor"}`; `404 {"error":"not_found"}` |
| `POST` | `/batches/{id}/items/{item_id}/retry` | — | `202 {"attempts","item_id"}`; `409 {"error":"invalid_transition"}` if not `FAILED`; `409 {"error":"max_attempts_reached"}` at 5 attempts; `404` |
| `POST` | `/batches/{id}/items/{item_id}/cancel` | — | `200 {"item_id","status":"CANCELLED"}`, again `200` on a cancelled item; `409 {"error":"invalid_transition"}` otherwise; `404` |
| `POST` | `/batches/{id}/retry-failed` | — | `202 {"requeued": n}`; `404` |

Both `POST /evaluations` routes take an optional `Idempotency-Key` header
([ADR 0005](adr/0005-idempotency-key-claimed-with-a-conditional-write.md)).
The same key with the same route and body replays the first `2xx` response
with `idempotent-replayed: true`, and nothing is evaluated or stored again.
The same key with another body is `422 {"error":"idempotency_key_reused"}`,
while the first request still runs is `409 {"error":"idempotency_key_in_progress"}`,
and a key that is empty, longer than 255, or not visible ASCII is
`400 {"error":"invalid_idempotency_key","max_length":255}`. A non-`2xx`
response frees the key. Keys live 24 hours.

One page of items, `GET /batches/{id}/items?limit=2`:

```json
{
  "batch_id": "b5e2d9a0-1c3f-4e8b-a7d6-9f0c2e4b8a13",
  "items": [
    {"item_id": "0199a1b2-7c3d-7e4f-8a5b-6c7d8e9f0a1b", "name": "Ana Souza", "cpf_masked": "***05",
     "status": "APPROVED", "reasons": ["eligible"], "revolving_amount_cents": 250000, "attempts": 1},
    {"item_id": "0199a1b2-7c3d-7e4f-8a5b-6c7d8e9f0a1c", "name": "Bruno Lima", "cpf_masked": "***09",
     "status": "FAILED", "reasons": [], "revolving_amount_cents": 0, "attempts": 1}
  ],
  "next_cursor": "MDE5OWExYjItN2MzZC03ZTRmLThhNWItNmM3ZDhlOWYwYTFj"
}
```

The last page has no `next_cursor`.

## Infra (CDK Go)

`packages/infra-iac/` is the CDK app. `cdk.json` points at
`go run ./packages/infra-iac`. One stack, `us-east-1`.

| Resource | Choice | Why |
|---|---|---|
| HTTP API v2 | — | Floci covers it. Stage throttle is 1200 rps / 2400 burst. |
| Lambda `provided.al2023` arm64 | `GoFunction` | Go binary, cold start low enough for the SLO |
| Timeout 3 s / 256 MB | — | SLO is 1 s. 3 s is a safety cap, not the budget. |
| Reserved concurrency 8, 4, 2, and 2 | Floci only (`AWS_ENDPOINT_URL` set at synth) | Floci starts one container per concurrent invoke. The cap keeps `make loadtest` from stalling the API. Real AWS stays unreserved. |
| DynamoDB on-demand | keys `pk` and `sk` (see [Data model](#data-model)), TTL on `expires_at` | Batched writes on submit, one conditional `UpdateItem` per transition, one conditional `PutItem` per idempotency key. |
| AWS managed encryption | AWS managed | Default encryption at rest. |
| IAM authorizer | every route except `GET /health` | SigV4 on `execute-api`. `/health` stays open for probes. |
| Table stream + `Relay` Lambda | `NEW_IMAGE`, batch 100 or 1 s window, `TRIM_HORIZON`, bisect on error, `ReportBatchItemFailures` | The outbox of ADR 0004: HTTP only writes the items, and the relay publishes the queued ones with `SendMessageBatch`. |
| Worker + SQS | 1000 req/s batch | The worker reads the item, evaluates, and decides it. Batch size 10 with `ReportBatchItemFailures`. |
| `EvaluationJobsDLQ` | `maxReceiveCount=5`, 14-day retention | A record that keeps failing stops retrying and becomes a failed item ([ADR 0001](adr/0001-fail-closed-and-operator-driven-item-recovery.md)). |
| `DlqConsumer` Lambda | `cmd/dlq`, same runtime and sizing as the others, 14-day log group | Batch size 10 with `ReportBatchItemFailures`. Read and write on the table and consume on the DLQ, nothing else. |
| Routes | `POST /evaluations`, `GET /evaluations/{id}`, `POST /evaluations/batch`, `GET /batches/{id}/items`, `POST /batches/{id}/items/{item_id}/retry`, `POST /batches/{id}/items/{item_id}/cancel`, `POST /batches/{id}/retry-failed`, `GET /health` | One HTTP Lambda serves every route. The HTTP, worker, and DLQ Lambdas read and write the table. Only the relay sends to the queue. |

The run steps live in [README.md](../README.md).

## Rejected

| Alternative | Why not |
|---|---|
| SAM + CDK | Two IaC tools for the same stack. |
| REST API | Higher overhead. This cut does not need WAF or a usage plan. |
| RDS or Postgres | Latency and a connection pool for one Put per request. |
| Customer managed key | Does not change encryption this cut needs, and costs more in the demo. |
| VPC | An ENI on cold start blows the 1 s SLO. The table does not need a private network. |
| `httpadapter` + `net/http` | Hides the HTTP API contract. The wire types are the AWS events. |
| Event sourcing, SNS, or projectors | Closes none of the NFRs above. |
| Cognito + WAF + custom domain | IAM auth is on the HTTP API. Cognito, WAF, and a custom domain remain out of scope. |
