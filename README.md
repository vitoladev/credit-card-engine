# Credit Card Engine

A revolving-credit rules engine in Go, with an HTTP API, Lambda, SQS, and
DynamoDB described in CDK (also Go).

Architecture and design rationale: [`docs/architecture.md`](docs/architecture.md).

No AWS account required.

## Evaluator path (Dev Container)

Docker running. Cursor or VS Code with the **Dev Containers** extension.

1. Open this folder.
2. **Reopen in Container** (Command Palette).
3. Wait for `postCreate` (`aws-cdk` + `cdklocal` + `go work sync`) and for
   Floci to answer at `http://floci:4566`.
4. In the container terminal:

```bash
make test
make local-bootstrap
make local-deploy

BASE="$(make -s api-url)"
curl -s "$BASE/health"
curl -s --aws-sigv4 "aws:amz:us-east-1:execute-api" --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" \
  -X POST "$BASE/evaluations" \
  -H 'content-type: application/json' \
  --data '{"name":"Ana","cpf":"390.533.447-05","credit_score":780,"current_invoice_cents":50000,"credit_limit_cents":500000,"monthly_spend_cents":[80000,90000,70000]}'
curl -s --aws-sigv4 "aws:amz:us-east-1:execute-api" --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" \
  -X POST "$BASE/evaluations/batch" \
  -H 'content-type: application/json' \
  --data-binary @apps/engine/testdata/customers.json
```

`POST /evaluations` returns `200` with a `decision_id`, the decision, and
`revolving_amount_cents`. A CPF may be sent masked or bare. Malformed JSON
returns `400`; an invalid customer returns `422` with every violation:

```bash
curl -s --aws-sigv4 "aws:amz:us-east-1:execute-api" --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" \
  -X POST "$BASE/evaluations" \
  -H 'content-type: application/json' \
  --data '{"name":"Ana","cpf":"39053344706","credit_score":-1,"current_invoice_cents":50000,"credit_limit_cents":500000,"monthly_spend_cents":[80000]}'
# {"error":"invalid_customer","violations":[{"field":"cpf","code":"invalid_check_digits"},{"field":"credit_score","code":"negative"}]}
```

The decision is stored and can be read back; an unknown id returns `404`:

```bash
curl -s --aws-sigv4 "aws:amz:us-east-1:execute-api" --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" \
  "$BASE/evaluations/<decision_id>"
```

`POST /evaluations/batch` accepts at most `BATCH_SIZE` customers (default
100, max 1000) and returns `202` with `batch_id` and `queued`. A larger batch
returns `422 {"error":"batch_too_large","max":<BATCH_SIZE>}`. The default keeps
local runs light; raise it with `BATCH_SIZE=1000 make local-deploy`. A value
outside 1..1000 fails the synth. If any customer is invalid, it
returns `422` with each violation's `index`. In both cases nothing is stored
or queued. The worker drains SQS and decides each batch item. Then:

```bash
curl -s --aws-sigv4 "aws:amz:us-east-1:execute-api" --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" \
  "$BASE/batches/<batch_id>/report"
```

The report shows the batch status (`PROCESSING` while items are queued,
`NEEDS_ATTENTION` when none is queued and some item failed, `COMPLETED` when
every item is decided or cancelled), the counters, the `approved`, `denied`,
`failed`, and `cancelled` items with a masked CPF and their `attempts`, and
`total_revolving_amount_cents` over the approved items.

A batch item that still fails after 3 deliveries lands in the DLQ, and the
DLQ consumer marks it `FAILED`. An operator then retries or cancels it (at
most 5 attempts per item):

```bash
# Retry one failed item: 202 {"index":0,"attempts":2}
curl -s --aws-sigv4 "aws:amz:us-east-1:execute-api" --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" \
  -X POST "$BASE/batches/<batch_id>/items/0/retry"
# Retry every failed item under 5 attempts: 202 {"requeued":<n>}
curl -s --aws-sigv4 "aws:amz:us-east-1:execute-api" --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" \
  -X POST "$BASE/batches/<batch_id>/retry-failed"
# Cancel one failed item: 200 {"index":0,"status":"CANCELLED"}, again 200
curl -s --aws-sigv4 "aws:amz:us-east-1:execute-api" --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" \
  -X POST "$BASE/batches/<batch_id>/items/0/cancel"
```

Retrying or cancelling an item that is not `FAILED` returns
`409 {"error":"invalid_transition"}`; a retry at 5 attempts returns
`409 {"error":"max_attempts_reached"}`. If the decision of `POST /evaluations`
cannot be stored, it returns `503 {"error":"decision_not_recorded"}` and no
decision.

The Dev Container compose already starts Floci. Go 1.27, Node 22, AWS CLI,
`cdklocal`, Turborepo, and k6 ship in the image. Dummy credentials
(`test` / `test`) are already in the environment.

`/health` returns `{"status":"ok"}`. Ana should be `APPROVED`. Bruno
(score 520), Carla (invoice over limit), and Diego (3 late payments) are
`DENIED`.

The local URL Floci serves is:

```
http://floci:4566/_aws/execute-api/<api-id>/$default
```

`make api-url` builds that. Port `4566` is also published on the host:
use `localhost` if you call the API from outside the container.

The `ApiUrl` CloudFormation prints is the AWS hostname. Ignore it. Use
`make api-url`.

```bash
make local-destroy
```

Close the Dev Container to stop the compose (`shutdownAction: stopCompose`).

## Without the container

Same stack, tools on the host:

- Go 1.27+, Docker, Node 22, AWS CLI v2, k6
- `npm install && npm install -g aws-cdk@2.1142.0 aws-cdk-local@3.0.4`
- `make floci-up && make local-bootstrap && make local-deploy`

`scripts/floci.env` points at `http://localhost:4566` when
`AWS_ENDPOINT_URL` is not already set.

Floci runs each Lambda invocation in its own container on the compose
network, so the deployed Lambdas reach Floci at `http://floci:4566`, not
`localhost`. Set `LAMBDA_AWS_ENDPOINT_URL` before `make local-deploy` if
your Floci answers under another name on that network.

## Make

| Target | What it does |
|---|---|
| `make test` | `turbo run test` — engine, lambdas, and stack. Then reaps Floci Lambda containers this stack left behind. |
| `make loadtest` | k6 at `LOADTEST_RATE` req/s (default 100) for 10 s against `POST /evaluations/batch` (needs a local deploy). Always reaps Floci Lambda containers afterward (success or fail). The 1000 req/s NFR run is `LOADTEST_RATE=1000 make loadtest` against a real AWS stack |
| `make local-bootstrap` | CDK bootstrap on Floci account `000000000000` |
| `make local-deploy` | `cdklocal deploy` |
| `make api-url` | Prints the HTTP API base URL on the emulator |
| `make synth` | `cdk synth` (template, no deploy) |
| `make floci-up` / `floci-down` | Only the `floci` service from `.devcontainer/docker-compose.yml` (no-op inside the Dev Container) |
| `make floci-reap` | Remove this stack's Floci Lambda containers (Evaluate/Worker/DlqConsumer). The floci sidecar stays up. |

## Layout

```
apps/engine/          use cases + CoR + cmds (http, worker)
packages/infra-iac/   CDK in Go
packages/loadtest/    k6 (batch via SQS, LOADTEST_RATE req/s, default 100)
docs/                 architecture.md
```
