# Load tests

What load the engine takes on Floci, the local AWS emulator, and what those
runs show.

## In short

- Floci serves about 10 Lambda invocations a second across the whole stack.
  A local run reaches 5 to 13 req/s, whatever rate k6 asks for. The numbers
  measure Floci.
- `POST /evaluations` at 60 s reaches 13.1 req/s, with 2.0% errors and p95
  1.12 s. The batch of 3 to 5 customers at 60 s reaches 6.8 req/s, with 7.7%
  errors and p95 5.31 s. That batch decides 1,563 items. The queue then
  drains in 268 s, about 5 items a second.
- Every error is a 3 s Lambda timeout inside Floci.

## Results on Floci, 60 seconds per scenario

Each scenario runs for 60 s at a target of 100 req/s, then waits until the
`QUEUED` count reaches 0. These figures are from 2026-09-23, with Floci 2.1.0
on the colima `development` profile (6 CPUs, 12 GB), the HTTP Lambda capped
at 8 concurrent invocations, and k6 at 8 VUs.

| Scenario | Reached | Errors | Median | p95 | Items | Drain after the load |
|---|---|---|---|---|---|---|
| `POST /evaluations`, 1 customer | 13.1 req/s (812) | 2.0% (16) | 439 ms | 1.12 s | — | — |
| Batch, 3 to 5 customers | 6.8 req/s (418) | 7.7% (32) | 575 ms | 5.31 s | 1,563 decided | 268 s, ~5 items/s |

- **Every error is a 3 s Lambda timeout inside Floci.** The Floci log shows
  `Function CreditCardEngine-Evaluate… timed out after 3s` once per failed
  request. The more rows a request writes, the more often it hits the timeout.
  Floci's DynamoDB slows as the tables grow, and the worker drains at the
  same time.
- **The engine's sync path is the `PutItem`.** `DecisionLatencyMs` for the
  sync scenario (a sample of 271 requests, from the lines Floci copies from
  each Lambda) is p50 422 ms, p95 538 ms, and p99 978 ms.
- **`Create` stops writing a second before the Lambda deadline.** The
  invocation then deletes the rows it wrote.
  `TestCreateStopsWritingInTimeToRollBack` covers the rollback. The load test
  caps a batch at 10 customers.

### Where one request spends its time on Floci

These are single requests in sequence, on warm containers, with no other load:

| Request | Median | Fastest |
|---|---|---|
| `POST /evaluations`, 1 customer | 753 ms | 78 ms |
| `POST /evaluations` with an `Idempotency-Key` | 1,594 ms | 920 ms |
| `POST /evaluations/batch`, 10 customers | 700 ms | 66 ms |

The same request finishes in about 70 ms or about 700 ms. The 630 ms
difference is a wait in Floci's invoke path, before the engine code runs.
Floci 2.1.0 writes no `REPORT` lines, so these times come from `curl`.

## Floci's limits

- **About 10 invocations a second in total.** Raising the HTTP Lambda's
  concurrency and k6's VUs from 8 to 32 kept the batch route at about 10 req/s
  and raised its median from 0.5 s to 2.7 s.
- **One poll at a time per event source mapping**, every
  `FLOCI_SERVICES_LAMBDA_POLL_INTERVAL_MS` (1 s by default), with at most 10
  messages per poll. A 100 ms poll raised the drain from ~9.5 to ~12.4 items/s
  but cut the sync route from 15.5 to 6.0 req/s, so the Dev Container keeps
  1 s.
- **A batch above 10 messages breaks the SQS poller.** Floci turns a receive
  of more than 10 messages into a receive of 1. The stack uses a worker batch
  of 10 on Floci and 50 on AWS.
- **Settings CloudFormation ignores or stubs:** point-in-time recovery, the
  SQS batching window, TTL, and `AWS::Logs::MetricFilter`.
  `ScalingConfig.MaximumConcurrency` is accepted but not enforced.
- **Old deploys leave event source mappings behind.** Delete a mapping whose
  function is not in the current stack:

  ```bash
  aws cloudformation describe-stack-resources --stack-name CreditCardEngine \
    --query "StackResources[?ResourceType=='AWS::Lambda::Function'].PhysicalResourceId"
  aws lambda list-event-source-mappings --query "EventSourceMappings[].[UUID,FunctionArn]"
  aws lambda delete-event-source-mapping --uuid <uuid>
  ```

## How to run the local test

```bash
make local-redeploy          # a fresh stack; the tables start empty
LOADTEST_PATH=batch  LOADTEST_RATE=100 LOADTEST_BATCH_CUSTOMERS=3-5 LOADTEST_DURATION=60s make loadtest
LOADTEST_PATH=single LOADTEST_RATE=100 LOADTEST_DURATION=60s make loadtest
```

| Variable | Default | Meaning |
|---|---|---|
| `LOADTEST_PATH` | `batch` | `batch` posts to `POST /evaluations/batch`; `single` posts one customer to `POST /evaluations`. |
| `LOADTEST_RATE` | `100` | Requests per second k6 tries to start (a constant arrival rate). |
| `LOADTEST_DURATION` | `10s` | How long k6 keeps that rate. |
| `LOADTEST_BATCH_CUSTOMERS` | `10` | Customers per batch, at most 10: a number, or `MIN-MAX` drawn at random for each request. |

On Floci the gate is p95 under 2 s and under 1% errors. k6 counts an arrival it had no free VU for as a dropped iteration, so
`http_reqs/s` is the rate reached.

To see how fast the queue drains, count the items still `QUEUED` until none
are left. The scan returns one count per page, so sum them:

```bash
TABLE=$(aws cloudformation describe-stacks --stack-name CreditCardEngine \
  --query "Stacks[0].Outputs[?OutputKey=='BatchItemsTable'].OutputValue" --output text)
aws dynamodb scan --table-name "$TABLE" --select COUNT \
  --filter-expression "#s = :q" \
  --expression-attribute-names '{"#s":"status"}' \
  --expression-attribute-values '{":q":{"S":"QUEUED"}}' \
  --query Count --output text | tr '\t\n' '  ' | awk '{s=0; for(i=1;i<=NF;i++) s+=$i; print s}'
```
