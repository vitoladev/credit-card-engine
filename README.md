# Credit Card Engine

A revolving-credit rules engine in Go. CDK in Go describes the HTTP API,
Lambda, SQS, and DynamoDB.

The cut, the rules, and the API are in [Architecture](docs/architecture.md).
You do not need an AWS account.

## Run the engine in the Dev Container

You need Docker, and Cursor or VS Code with the Dev Containers extension.

1. Open this folder.
2. Run **Reopen in Container** from the Command Palette.
3. Wait until `postCreate` finishes (`aws-cdk`, `cdklocal`, `go work sync`)
   and Floci answers at `http://floci:4566`.
4. In the container terminal, run:

```bash
make test
make local-bootstrap
make local-deploy
```

The image already has Go 1.27, Node 22, AWS CLI, `cdklocal`, Turborepo, and
k6. Dummy credentials (`test` / `test`) are already in the environment.
Compose starts Floci.

### Call the API

```bash
BASE="$(make -s api-url)"
curl -s "$BASE/health"
```

`GET /health` returns `{"status":"ok"}`.

Then evaluate Ana:

```bash
curl -s --aws-sigv4 "aws:amz:us-east-1:execute-api" --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" \
  -X POST "$BASE/evaluations" \
  -H 'content-type: application/json' \
  --data '{"name":"Ana","cpf":"390.533.447-05","credit_score":780,"current_invoice_cents":50000,"credit_limit_cents":500000,"monthly_spend_cents":[80000,90000,70000]}'
```

`POST /evaluations` returns `200` with a `decision_id`, the decision, and
`revolving_amount_cents`. Ana is `APPROVED`. You may send a CPF masked or
bare.

To read the decision back:

```bash
curl -s --aws-sigv4 "aws:amz:us-east-1:execute-api" --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" \
  "$BASE/evaluations/<decision_id>"
```

An unknown id returns `404`. If the store cannot record the decision, the
handler returns `503 {"error":"decision_not_recorded"}` and no decision.

Malformed JSON returns `400`. An invalid customer returns `422` with every
violation:

```bash
curl -s --aws-sigv4 "aws:amz:us-east-1:execute-api" --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" \
  -X POST "$BASE/evaluations" \
  -H 'content-type: application/json' \
  --data '{"name":"Ana","cpf":"39053344706","credit_score":-1,"current_invoice_cents":50000,"credit_limit_cents":500000,"monthly_spend_cents":[80000]}'
# {"error":"invalid_customer","violations":[{"field":"cpf","code":"invalid_check_digits"},{"field":"credit_score","code":"negative"}]}
```

### Retry safely with an Idempotency-Key

Send the same `Idempotency-Key` on a retry of either `POST /evaluations`
route. The first `2xx` response comes back again, marked
`idempotent-replayed: true`, and nothing is evaluated or stored twice:

```bash
KEY="$(uuidgen)"
for i in 1 2; do
  curl -si --aws-sigv4 "aws:amz:us-east-1:execute-api" --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" \
    -X POST "$BASE/evaluations" -H 'content-type: application/json' -H "Idempotency-Key: $KEY" \
    --data '{"name":"Ana","cpf":"390.533.447-05","credit_score":780,"current_invoice_cents":50000,"credit_limit_cents":500000,"monthly_spend_cents":[80000,90000,70000]}'
done
# The same decision_id twice; the second response has idempotent-replayed: true.
```

The same key with another body returns `422 {"error":"idempotency_key_reused"}`.
Keys live 24 hours ([ADR 0005](docs/adr/0005-idempotency-key-claimed-with-a-conditional-write.md)).

### Submit a batch

```bash
curl -s --aws-sigv4 "aws:amz:us-east-1:execute-api" --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" \
  -X POST "$BASE/evaluations/batch" \
  -H 'content-type: application/json' \
  --data-binary @apps/engine/testdata/customers.json
```

`POST /evaluations/batch` accepts at most `BATCH_SIZE` customers (default 100,
max 1000) and returns `202` with `batch_id`, `queued`, and `item_ids`. The
`item_ids` follow the order of the submitted customers. If any customer is
invalid, the handler returns `422` with each violation's `index`. A larger
batch returns `422 {"error":"batch_too_large","max":<BATCH_SIZE>}`. In both
error cases nothing is stored or queued.

A `BATCH_SIZE` outside 1..1000 fails the synth. To raise the cap, run
`BATCH_SIZE=1000 make local-deploy`.

The worker drains SQS and decides each batch item. Then list the items:

```bash
curl -s --aws-sigv4 "aws:amz:us-east-1:execute-api" --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" \
  "$BASE/batches/<batch_id>/items?limit=100"
```

Each item has its `item_id`, a masked CPF, its `status` (`QUEUED`,
`APPROVED`, `DENIED`, `FAILED`, or `CANCELLED`), `reasons`,
`revolving_amount_cents`, and `attempts`, in submission order. To read the
next page, pass `next_cursor` as `?cursor=`. The last page has no
`next_cursor`. To list one status only, add `?status=FAILED`. The batch is
done when `?limit=1&status=QUEUED` returns no items.

With more than one query parameter, write them in alphabetical order
(`cursor`, `limit`, `status`). The curl in the Dev Container (7.88) signs the
query string in the order you type it, and SigV4 expects sorted keys, so any
other order returns `403`. The AWS SDKs sort for you.

Bruno (score 520), Carla (invoice over the credit limit), and Diego (3 late
payments) are `DENIED`.

### Recover a failed item

A batch item that still fails after 5 deliveries lands in the DLQ. The DLQ
consumer marks it `FAILED`. An operator then retries or cancels it, at most
5 attempts per item:

```bash
# Retry one failed item: 202 {"attempts":2,"item_id":"<item_id>"}
curl -s --aws-sigv4 "aws:amz:us-east-1:execute-api" --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" \
  -X POST "$BASE/batches/<batch_id>/items/<item_id>/retry"
# Retry every failed item under 5 attempts: 202 {"requeued":<n>}
curl -s --aws-sigv4 "aws:amz:us-east-1:execute-api" --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" \
  -X POST "$BASE/batches/<batch_id>/retry-failed"
# Cancel one failed item: 200 {"item_id":"<item_id>","status":"CANCELLED"}, again 200
curl -s --aws-sigv4 "aws:amz:us-east-1:execute-api" --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" \
  -X POST "$BASE/batches/<batch_id>/items/<item_id>/cancel"
```

A retry or cancel of an item that is not `FAILED` returns
`409 {"error":"invalid_transition"}`. A retry at 5 attempts returns
`409 {"error":"max_attempts_reached"}`.

The URL Floci serves is:

```
http://floci:4566/_aws/execute-api/<api-id>/$default
```

`make api-url` builds that URL. Port `4566` is also published on the host.
If you call the API from outside the container, use `localhost`.

CloudFormation prints `ApiUrl` as the AWS hostname. Ignore that value. Use
`make api-url`.

```bash
make local-destroy
```

Close the Dev Container to stop Compose (`shutdownAction: stopCompose`).

## Run without the Dev Container

Same stack, with the tools on the host:

- Go 1.27+, Docker, Node 22, AWS CLI v2, k6
- `npm install && npm install -g aws-cdk@2.1142.0 aws-cdk-local@3.0.4`
- `make floci-up && make local-bootstrap && make local-deploy`

`scripts/floci.env` points at `http://localhost:4566` when
`AWS_ENDPOINT_URL` is not already set.

Floci runs each Lambda invocation in its own container on the Compose
network, so the deployed Lambdas reach Floci at `http://floci:4566`, not
`localhost`. If your Floci answers under another name on that network, set
`LAMBDA_AWS_ENDPOINT_URL` before `make local-deploy`.

## Make targets

| Target | What it does |
|---|---|
| `make test` | `turbo run test` for the engine, the Lambdas, and the stack. Then reaps Floci Lambda containers this stack left behind. |
| `make loadtest` | k6 against `POST /evaluations/batch` on a local deploy. `LOADTEST_PATH=single` targets `POST /evaluations` instead. `LOADTEST_BATCH_CUSTOMERS=3-5` sends 3 to 5 customers per batch, at random. |
| `make local-bootstrap` | CDK bootstrap on Floci account `000000000000`. |
| `make local-deploy` | `cdklocal deploy`. Works once per stack on Floci. |
| `make local-redeploy` | `local-destroy` then `local-deploy`. Use it to deploy a change: Floci cannot update the relay's stream event source mapping in place. The table starts empty. |
| `make api-url` | Prints the HTTP API base URL on the emulator. |
| `make synth` | `cdk synth` (template, no deploy). |
| `make floci-up` and `make floci-down` | Only the `floci` service from `.devcontainer/docker-compose.yml`. No-op inside the Dev Container. |
| `make floci-reap` | Removes this stack's Floci Lambda containers (Evaluate, Relay, Worker, DlqConsumer). The Floci sidecar stays up. |

`make loadtest` runs at `LOADTEST_RATE` req/s (default 100) for 10 s. On
Floci, k6 stays at 8 VUs and the Lambdas stay at reserved concurrency 8 (Evaluate), 4 (Worker), and 2
(Relay, DlqConsumer). Local thresholds are p95 under 2 s and under 1% errors. The target reaps
Floci Lambda containers afterward, on success or fail. The 1000 req/s NFR
runs are against a real AWS stack. Results and the Floci limits behind them are
in [Benchmarks on Floci](docs/architecture.md#benchmarks-on-floci).

Floci differs from AWS in ways that change what a local run proves:

- Floci serves about 10 Lambda invocations a second in total, so a local
  `make loadtest` reaches ~10 req/s whatever `LOADTEST_RATE` asks for. The
  Dev Container sets `FLOCI_SERVICES_LAMBDA_POLL_INTERVAL_MS=100` so the
  queue drains at ~12 items/s instead of ~10. Recreate the `floci` service
  after changing it.

- Its CloudFormation ignores point-in-time recovery, the SQS batching
  window, and the table's TTL. The CDK tests assert all three in the
  template. Expired idempotency keys stay in the table on Floci, but the
  claim's condition treats them as absent, so keys still expire after 24
  hours.
- A failed update can leave an API, a function, and a table behind.
  `make api-url` asks the stack for its own API, so it never picks one of
  those.

## Layout

```
apps/engine/          The evaluate and batch modules, the rules, and the http, worker, and dlq commands
packages/infra-iac/   CDK in Go
packages/loadtest/    k6 (batch via SQS, LOADTEST_RATE req/s, default 100)
docs/                 architecture.md
```
