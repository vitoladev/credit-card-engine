# Load tests on Floci

These runs measure how the engine behaves under load in the Dev Container. On
Floci they measure the emulator's ceiling, not the engine's. The 1000 req/s
requirement needs a real AWS stack, which this cut does not have.

All runs were on 2026-09-23, on branch `feat/idempotency-key`, with Floci 2.1.0
on the colima `development` profile (6 CPUs, 12 GB). The stack caps the HTTP
Lambda at 8 concurrent invocations on Floci, and k6 uses at most 8 VUs.

## How to run

```bash
make local-redeploy          # a fresh stack; the table starts empty
LOADTEST_PATH=batch  LOADTEST_RATE=100 LOADTEST_BATCH_CUSTOMERS=3-5 make loadtest
LOADTEST_PATH=single LOADTEST_RATE=100 make loadtest
```

| Variable | Default | Meaning |
|---|---|---|
| `LOADTEST_PATH` | `batch` | `batch` posts to `POST /evaluations/batch`; `single` posts one customer to `POST /evaluations`, the synchronous route. |
| `LOADTEST_RATE` | `100` | Requests per second k6 tries to start (a constant arrival rate). |
| `LOADTEST_DURATION` | `10s` | How long k6 keeps that rate. |
| `LOADTEST_BATCH_CUSTOMERS` | `10` | Customers per batch: a number, or `MIN-MAX` drawn at random for each request. |

On Floci the gate is p95 under 2 s and under 1% errors. On AWS it is p99 under
800 ms. k6 counts an arrival it had no free VU for as a dropped iteration, so
`http_reqs/s` is the rate reached and `dropped_iterations` is the shortfall.

To see how fast the queue drains, count the items still `QUEUED` until none
are left. Sum the pages, because the scan returns one count per page:

```bash
aws dynamodb scan --table-name "$TABLE" --select COUNT \
  --filter-expression "begins_with(sk,:i) AND #s = :q" \
  --expression-attribute-names '{"#s":"status"}' \
  --expression-attribute-values '{":i":{"S":"ITEM#"},":q":{"S":"QUEUED"}}' \
  --query Count --output text | tr '\t\n' '  ' | awk '{s=0; for(i=1;i<=NF;i++) s+=$i; print s}'
```

## Results

### Synchronous: `POST /evaluations`, 1 customer per request

The route evaluates and records the decision before it answers.

| Run | Reached | Errors | Median | p95 |
|---|---|---|---|---|
| 100 req/s for 10 s | 15.5 req/s (170 requests) | 0 | 435 ms | 1.05 s |
| 1000 req/s for 10 s (an earlier run) | 18.5 req/s (196 requests) | 0 | — | 734 ms |

Every response is a `200` with a `decision_id`.

### Batch: `POST /evaluations/batch`

The route stores the items as `QUEUED` and answers `202`. The stream, relay,
queue, and worker decide them afterwards.

| Customers per batch | Reached | Errors | Median | p95 | Items | Decided | Drain |
|---|---|---|---|---|---|---|---|
| 3 to 5 | 10.7 req/s (114 batches) | 0 | 569 ms | 1.46 s | 466 | 172 approved, 294 denied, 0 failed | done 46 s after the load, ~9 items/s |
| 10 | 10.6 req/s (115 batches) | 0 | 519 ms | 1.24 s | 1,150 | 451 approved, 703 denied, 0 failed | done 121 s after the load, ~9.5 items/s |
| 50 | 4.5 req/s (55 batches) | 0 | 1.57 s | 2.6 s | 2,750 | 1,088 approved, 1,662 denied, 0 failed | ~10 items/s |

More customers per batch means more items accepted per request: 50 per batch
took 2,750 items in 10 s. It does not speed up the drain, because each item is
its own queue message.

### Where one request spends its time

These are single requests in sequence, on warm containers, with no other load:

| Request | Median | Fastest |
|---|---|---|
| `POST /evaluations`, 1 customer | 753 ms | 78 ms |
| `POST /evaluations` with an `Idempotency-Key` | 1,594 ms | 920 ms |
| `POST /evaluations/batch`, 1 customer | 699 ms | 66 ms |
| `POST /evaluations/batch`, 10 customers | 700 ms | 66 ms |
| `POST /evaluations/batch`, 50 customers | 845 ms | 814 ms |

- The same request finishes in about 70 ms or about 700 ms. The 630 ms
  difference is a wait inside Floci's invoke path, and it comes before the
  engine code runs.
- Going from 10 to 50 customers adds about 145 ms: `Create` sends two
  `BatchWriteItem` calls of 25 items in parallel. On AWS a `BatchWriteItem` of
  25 items takes tens of milliseconds.
- An `Idempotency-Key` adds two writes to the request: the claim and the
  complete. On Floci each write costs a few hundred milliseconds.
- Floci 2.1.0 writes no `REPORT` lines: the Lambda log streams are created
  and stay empty. These timings are measured from outside, with `curl`.

## Floci's limits

- **About 10 invocations a second.** Floci serves about 10 Lambda invocations a
  second across the whole stack. Raising the HTTP Lambda's concurrency and k6's
  VUs from 8 to 32 kept the batch route at ~10 req/s and raised its median from
  0.5 s to 2.7 s. The stack keeps 8.
- **One poll at a time per event source mapping.** Floci runs one poll per
  mapping at a time, every `FLOCI_SERVICES_LAMBDA_POLL_INTERVAL_MS` (1 s by
  default). A poll receives at most 10 messages. The worker never runs two
  invocations at once, whatever its reserved concurrency.
- **A 100 ms poll costs more than it gains.** With
  `FLOCI_SERVICES_LAMBDA_POLL_INTERVAL_MS=100`:
  - the drain rose from ~9.5 to ~12.4 items/s;
  - the synchronous route fell from 15.5 to 6.0 req/s, with p95 from 1.05 s to
    1.98 s;
  - the batch route's p95 rose from 1.46 s to 4.0 s.

  The Dev Container keeps the default of 1 s.
- **Batches above 10 break the poller.** Floci turns a receive of more than 10
  messages into a receive of 1, and the poller asks for `BatchSize` minus
  what it has buffered. A worker batch of 50 with a batching window would
  receive 1 message per poll until 40 are buffered. The stack uses 10 on Floci
  and 50 on AWS. The fix upstream would be
  `wanted = min(10, BatchSize - buffered)` in `SqsEventSourcePoller`.
- **Settings Floci ignores or does not enforce:**
  - CloudFormation ignores `MaximumBatchingWindowInSeconds`: the mappings show
    `None`.
  - `ScalingConfig.MaximumConcurrency` is accepted but not enforced. It is a
    cap in any case, not a way to add concurrency.
  - `FLOCI_SERVICES_LAMBDA_CODE_VOLUME_POPULATE_CONCURRENCY` applies only to
    functions of 32 MB or more; these Go binaries are 13 to 14 MB.
- **Old deploys leave mappings behind.** A failed destroy leaves functions and
  their event source mappings. The mappings of old DLQ consumers kept polling
  queues that no longer existed. Delete a mapping whose function is not in the
  current stack:

  ```bash
  aws cloudformation describe-stack-resources --stack-name CreditCardEngine \
    --query "StackResources[?ResourceType=='AWS::Lambda::Function'].PhysicalResourceId"
  aws lambda list-event-source-mappings --query "EventSourceMappings[].[UUID,FunctionArn]"
  aws lambda delete-event-source-mapping --uuid <uuid>
  ```

## On AWS

The same commands against a real stack are the requirement's runs:

```bash
LOADTEST_PATH=single LOADTEST_RATE=1000 make loadtest
LOADTEST_PATH=batch  LOADTEST_RATE=100 LOADTEST_BATCH_CUSTOMERS=3-5 make loadtest
```

On AWS, Lambda scales an SQS source to 5 concurrent batches, then adds up to 300
invocations a minute, up to 1,250. The worker takes up to 50 messages per
invocation with a 1 s window. None of this can be observed on Floci.
