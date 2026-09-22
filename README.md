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
curl -s -X POST "$BASE/evaluations" \
  -H 'content-type: application/json' \
  --data '{"name":"Ana","cpf":"39053344705","credit_score":780,"current_invoice_cents":50000,"available_limit_cents":500000,"monthly_spend_cents":[80000,90000,70000]}'
curl -s -X POST "$BASE/evaluations/batch" \
  -H 'content-type: application/json' \
  --data-binary @apps/engine/testdata/customers.json
```

`POST /evaluations/batch` returns `202` with `report_id` and `queued`.
The worker drains SQS and writes to Dynamo. Then:

```bash
curl -s "$BASE/reports/<report_id>"
```

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

## Make

| Target | What it does |
|---|---|
| `make test` | `turbo run test` — engine, lambdas, and stack |
| `make loadtest` | k6 at 1000 req/s against `POST /evaluations/batch` (needs a local deploy) |
| `make local-bootstrap` | CDK bootstrap on Floci account `000000000000` |
| `make local-deploy` | `cdklocal deploy` |
| `make api-url` | Prints the HTTP API base URL on the emulator |
| `make synth` | `cdk synth` (template, no deploy) |
| `make floci-up` / `floci-down` | Only the `floci` service from `.devcontainer/docker-compose.yml` (no-op inside the Dev Container) |

## Layout

```
apps/engine/          use cases + CoR + cmds (http, worker)
packages/infra-iac/   CDK in Go
packages/loadtest/    k6 (1000 req/s batch via SQS)
docs/                 architecture.md
```
