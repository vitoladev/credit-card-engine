# Architecture

A revolving-credit evaluation simulation. There is no bureau, core banking
system, or frontend: the cut is the rules engine, the report, and the AWS
infra that backs the evaluation criteria.

IaC is **CDK in Go** (`packages/infra-iac/`). The product lives in
`apps/engine/`, split by use case. This file, `apps/engine/architecture_test.go`,
and the stack describe the cut.

## Cut

| Piece | Role |
|---|---|
| `internal/domain` | Customer profile, its validation, and the decision. No I/O. |
| `internal/rules` | Chain of Responsibility plus the `AmountPolicy` strategy. `NewPolicy()` is the policy factory. |
| `internal/evaluate` | Use case: one customer → `Result`. Applies the `rules.Policy`: the chain decides, the amount policy sizes an approval. |
| `internal/submit` | Use case: list → `202` + one SQS job per customer, shared `report_id`. |
| `internal/processjob` | Use case: queue job → evaluate + persist on the report. |
| `internal/report` | Use case: `report_id` → snapshot (approved, denied, amount). |
| `internal/queue` / `internal/store` | Ports. Memory in tests; SQS and Dynamo in adapters. |
| `internal/adapter/httpapi` | HTTP API v2. Validates customers before calling a use case. |
| `internal/adapter/sqs` | Worker: consumes the queue. |
| `internal/adapter/sqspub` / `ddb` | Publishes jobs and writes decisions. |
| `cmd/http` `cmd/worker` | Composition root. |
| `packages/infra-iac` | CDK: HTTP API, two Lambdas, SQS, DynamoDB, logs, alarms. |
| `packages/loadtest` | k6 at **1000 req/s** on `POST /evaluations/batch` (10s = 10k jobs). |

## Runtime

```mermaid
flowchart LR
  subgraph clients [Ingress]
    sync["POST /evaluations"]
    batch["POST /evaluations/batch"]
    get["GET /reports/:id"]
  end

  subgraph edge [AWS — CDK]
    api["HTTP API v2\n1200 rps / 2400 burst"]
    fn["Lambda HTTP\narm64 / 3s"]
    q["SQS EvaluationJobs"]
    worker["Lambda worker\nSQS batch 10"]
    ddb[("DynamoDB\nPK report_id / SK")]
    logs["CloudWatch Logs\n14d"]
    alarms["Alarms\nerrors and p99 > 800ms"]
  end

  subgraph core [apps/engine]
    ev["evaluate"]
    chain["rules.NewPolicy()"]
    domain["domain.Result"]
  end

  sync --> api --> fn --> ev
  batch --> api --> fn --> q --> worker --> ev
  get --> api --> fn --> ddb
  ev --> chain --> domain
  ev --> ddb
  fn --> logs
  fn --> alarms
```

Two paths, on purpose:

- **Sync** (`POST /evaluations`): one customer, 1s SLO, decide now.
- **Batch** (`POST /evaluations/batch` → SQS → worker → `GET /reports/:id`):
  HTTP only enqueues. This is the 1000 req/s loadtest path.

Dynamo runs **after** the verdict. If the table is down on the sync path, the
decision is already computed and persist returns 500 (the client retries). On
batch, HTTP already returned 202; SQS retries the worker.

## Packages

The same DAG that `architecture_test.go` locks. `domain` does not import AWS.
A new rule is a `Handler` plus one `SetNext` in the factory. A new amount
calculation is an `AmountPolicy` set in the same factory. Use cases and
adapters do not change.

```mermaid
flowchart TB
  http["cmd/http"] --> httpapi["adapter/httpapi"]
  http --> ddbA["adapter/ddb"]
  http --> pub["adapter/sqspub"]
  worker["cmd/worker"] --> sqs["adapter/sqs"]
  worker --> ddbA
  httpapi --> evaluate
  httpapi --> submit
  httpapi --> report
  submit --> queue["queue"]
  report --> store
  sqs --> job["processjob"]
  job --> evaluate
  job --> queue
  evaluate["evaluate"] --> chain["rules"]
  evaluate --> store["store"]
  chain --> domain["domain"]
  store --> domain
  queue --> domain
```

## Validation

The HTTP adapter validates every customer with `domain.Customer.Validate()`
before it calls a use case. Validation does no I/O and returns every
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
before it reaches a use case.

- `POST /evaluations`: malformed JSON → `400 {"error":"invalid_json"}`. An
  invalid customer → `422 {"error":"invalid_customer","violations":[...]}`.
- `POST /evaluations/batch`: if any customer is invalid, the whole batch is
  rejected with `422`, each violation carries the customer's `index`, and
  nothing is published.

```json
{"error":"invalid_customer","violations":[{"index":1,"field":"cpf","code":"invalid_check_digits"}]}
```

## Decision

The policy is a `rules.Policy` built by `rules.NewPolicy()`: the rule chain
plus the amount policy. Chain of Responsibility: the first rule that denies
stops the chain, with one reason. The amount is computed only if every rule
passes.

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

Amount policy (`rules.AmountPolicy`, implemented by `rules.ScoreBands`): a
higher score gets a larger share of the credit limit on revolving credit,
capped at `credit_limit − current_invoice` and never negative:

| Score | Factor |
|---|---|
| ≥ 800 | 80% |
| 700–799 | 50% |
| 600–699 | 30% |
| < 600 | never reaches here — `min_score` denies |

Money is integer cents. Reports and logs use a masked CPF (`***` + last 2
digits), never the raw CPF.

## Why these rules

Criteria are documented here. This is not a real bureau.

1. **Minimum score 600** — below that, revolving credit is already materialized
   credit risk. One cut, testable, no model.
2. **At most 2 late payments** — recent delinquency is the cheapest predictor
   of revolving default. Three or more means collections already started.
3. **Invoice ≤ credit limit** — granting revolving on top of an over-limit
   invoice increases exposure with no collateral.
4. **Recent spend ≤ 90% of limit** — a customer already consuming the limit
   is revolving in practice. Average of the last 3 months, not a single month.
   A credit limit of zero or less denies with `no_credit_limit`. An empty spend
   history denies with `insufficient_spend_history`: there is no evidence to
   size the risk.

Change a cutoff = a field on the rule struct or a band. Change the policy = a
new file plus one `SetNext` (or a new `AmountPolicy`) in `rules.NewPolicy()`.

## Evaluation criteria → design

| Criterion | How the design answers |
|---|---|
| **Latency ≤ 1s** | Sync engine, no I/O on the hot path. Lambda timeout 3s, 256 MB, arm64, **no VPC**. p99 alarmed at 800 ms. |
| **Accuracy** | Customers validated at the edge (CPF check digits, no negatives). Deterministic rules. Table tests in `domain`, `rules`, and `evaluate`. Stable reason code per rule. |
| **Scale 10k/min** | NFR floor. The cut demonstrates **1000 req/s** on batch: HTTP enqueues (202), worker processes batch 10, Dynamo on-demand. Stage at 1200 rps / 2400 burst. k6: 1000/s × 10s = 10k jobs. |
| **Extensibility** | `rules.Handler` + `rules.AmountPolicy`, assembled in `rules.NewPolicy()`. `architecture_test.go` keeps domain off AWS. |
| **LGPD** | CPF masked on `Result`. Encryption at rest managed. IAM only on the decisions table. No API auth in this cut (fictional data); production would be IAM on the HTTP API. |
| **Observability** | 14-day logs. Error and duration alarms. Dashboard for volume and p99. Reason codes in response JSON = deny rate per rule. |
| **Resilience** | The decision is pure. Persistence is after. On batch, SQS isolates HTTP from the worker: 202 already left; visibility + retry if Put fails. On-demand table. 5xx alarm on the sync path. |

## API

Wire types: `events.APIGatewayV2HTTPRequest` / `HTTPResponse`.

| Method | Path | Body | Response |
|---|---|---|---|
| `GET` | `/health` | — | `{"status":"ok"}` |
| `POST` | `/evaluations` | one `Customer` | `200` + `Result` (sync, 1s SLO); `400` malformed JSON; `422` invalid customer |
| `POST` | `/evaluations/batch` | `{customers:[...]}` or array | `202` + `{report_id, queued}`; `422` with indexed violations, nothing published |
| `GET` | `/reports/{id}` | — | snapshot: approved, denied, `released_cents` |

## Infra (CDK Go)

`packages/infra-iac/` is the CDK app. `cdk.json` points at
`go run ./packages/infra-iac`. One stack, `us-east-1`.

| Resource | Choice | Why |
|---|---|---|
| HTTP API v2 | instead of REST API | less overhead; Floci covers it; no WAF/usage plan in this cut |
| Lambda `provided.al2023` arm64 | `GoFunction` | Go binary, cold start low enough for the SLO |
| Timeout 3s / 256 MB | — | SLO is 1s; 3s is a safety cap, not the budget |
| DynamoDB on-demand | instead of RDS | 1 Put per evaluation, PK `report_id`; no connection |
| AWS managed encryption | instead of CMK | a CMK does not change the case and costs more in the demo |
| No VPC | — | an ENI on cold start blows the 1s SLO for no reason; the table does not need a private network |
| No Cognito / authorizer | — | this cut is a simulation with fictional data |
| No SAM | — | one IaC, in the same language as the engine |
| Worker + SQS | 1000 req/s batch | HTTP only `SendMessageBatch`; the worker evaluates and persists |

```bash
make test
make local-bootstrap && make local-deploy
```

Evaluator: open the repo in the Dev Container (`.devcontainer/`). Steps and
curls: [`README.md`](../README.md).

## Rejected

| Alternative | Why not |
|---|---|
| SAM + CDK | Two IaC tools for the same stack. |
| RDS / Postgres | Latency and a connection pool for one Put per request. |
| `httpadapter` + `net/http` | Hides the HTTP API contract. The wire types are the AWS events. |
| Event sourcing / SNS / projectors | Closes none of the NFRs above. |
| Cognito + WAF + custom domain | Production theater. The cut is the engine and the rationale. |
