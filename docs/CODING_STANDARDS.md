# Coding standards

The standards a change to this repo is reviewed against. Each section is a
partition: a review reads only the sections its diff touches. Vocabulary comes
from [`CONTEXT.md`](../CONTEXT.md). Decisions come from [`docs/adr/`](adr/).

Toolchain commands run inside the Dev Container:
`devcontainer exec --workspace-folder . <command>`. Git runs on the host.

## 1. General

- Write the minimum code that meets the issue's requirements. No speculative
  options, flags, or abstractions around single-use code.
- Name things with the glossary terms: `CreditLimitCents`, `BatchItem`,
  `Attempt`, `Report`. Never use a term the glossary lists under _Avoid_.
- Code, comments, docs, commit messages, and error codes are in English.
- Comments explain why, not what. A comment that restates the next line is
  noise.

## 2. Package layout and dependencies (`apps/engine`)

The dependency direction is enforced by `architecture_test.go` and `depguard`.
A change that needs a new edge updates the architecture test in the same
commit and says why.

| Layer | Packages | May import |
|---|---|---|
| Domain | `internal/domain` | standard library only |
| Policy | `internal/rules` | `domain` |
| Modules | `internal/evaluate`, `internal/batch` | `domain`, `rules`; never each other, never an adapter |
| Request guard | `internal/idempotency` | standard library only; imported by `adapter/httpapi` and implemented by `adapter/ddb` |
| Adapters | `internal/adapter/*` | modules, `domain`, AWS SDK; never another adapter |
| Test helper | `internal/flocitest` | anything; imported only by `_test.go` files |
| Composition | `cmd/*` | anything; wiring only, no logic |

- `domain`, `rules`, `idempotency`, and the modules never import `github.com/aws/...`.
- A module is a deep package named after a glossary concept (`batch`,
  `evaluate`). It exposes a `Module` built by `New` and methods named after
  what the caller wants done, and it declares the ports it needs.
- Ports are interfaces declared in the module that consumes them. An adapter
  implements them even when it is the only implementation
  ([ADR 0002](adr/0002-keep-aws-behind-adapters-with-one-implementation.md)).
  There are no in-memory stores and no mocking libraries.
- Adapters translate wire types (API Gateway, SQS events, DynamoDB items) into
  domain types at the edge. Wire types never reach a module.

## 3. Domain and rules

- Money is `int64` cents. Rates are basis points (`int64`, 10000 = 100%). No
  floats in a decision path.
- Domain code does no I/O, reads no clock or environment, and is deterministic
  for the same input.
- A rule is one constructor in its own file that takes its cutoffs and
  returns a `rules.Rule`, which denies with a stable reason code
  (`snake_case`, for example `score_below_600`). Reason codes are part of the API
  contract: changing one is a breaking change.
- The policy (rule order and score bands) is assembled only in
  `rules.NewPolicy()`. Adding or changing a rule never edits a module.
- Validation lives in the domain and returns every violation as field + code.
  It runs at the adapter edge before any module is called.

## 4. Errors

- Wrap with `fmt.Errorf("...: %w", err)`; compare with `errors.Is` and `errors.AsType`.
- Sentinel errors are named `ErrX`; error types are named `XError`.
- Adapters map errors to HTTP status codes; modules never know about HTTP.
  `400` malformed input, `404` unknown id, `409` invalid transition, `422`
  invalid customer, `503` persistence failed.
- A handler that returns a `5xx` status logs the cause once, at the adapter.
- Never swallow an error. `nilerr` and `errcheck` (with type assertions) are
  on.

## 5. Persistence and queues

- Writes that decide a batch item are idempotent: each item keeps its stable
  `item_id` (a version 7 UUID fixed at submit), and state
  transitions are conditional writes on the current status and attempt.
- A client retry of a `POST` is made safe by the `Idempotency-Key` (ADR 0005):
  the key is claimed with one conditional `PutItem`, never a read then a
  write.
- Each port has its own table, keyed by its own IDs
  ([ADR 0006](adr/0006-one-table-per-port.md)). A new entity gets a new table.
- A `Query` never filters out most of what it reads: DynamoDB bills every item
  read. Read the items you need through a key or an index
  ([ADR 0007](adr/0007-list-items-by-status-from-an-index.md)). There is no
  `Scan` outside tests.
- Counters are derived from the items on read, never stored, so a transition
  writes only its own item.
- The sync path fails closed (ADR 0001): no stored decision, no decision
  returned.
- The worker reports partial batch failures; it never fails a whole SQS batch
  for one record.

## 6. Security and privacy

- A full CPF never appears in a log line, a metric dimension, an error
  message, or an API response. Outputs use the masked CPF.
- A customer name never appears in a log line, a metric dimension, or an
  error message. API responses may carry it: a decision and a batch item name
  the customer they are about, next to the masked CPF.
- Full CPF and name live only in DynamoDB and its stream. Queue messages
  carry item events with no customer data (ADR 0004).
- Every route except `/health` sits behind the IAM authorizer.
- No secrets in code or in `cdk.json`; configuration comes from environment
  variables set by the stack.

## 7. Observability

- Log with `log/slog` and the JSON handler. Keys are `snake_case`.
- One EMF line per decision in namespace `CreditCardEngine`. New metrics go
  through the same EMF logger; do not call the CloudWatch API from the hot
  path.
- Every new failure mode gets an alarm in the stack, or the PR says why not.

## 8. Tests

- The module interface is the test surface. `batch` and `evaluate` tests run
  the module over the real adapters on Floci (`internal/flocitest`), inject
  failures with AWS SDK middleware, and assert on outcomes read back through
  the module. Adapter tests cover only what the adapter adds: routing and
  status mapping, partial batch responses, log redaction, conditional
  updates. Assert on outcomes, not on private functions.
- Tests that need Floci fail when no endpoint is set; they never skip. Run
  them inside the Dev Container, where `AWS_ENDPOINT_URL` points at Floci.
- Rule, validation, and amount logic use table tests with `t.Run` and named
  cases. Test packages are external (`package x_test`).
- Use `t.Context()` for contexts in tests.
- Test data uses valid CPFs (correct check digits) unless the case tests an
  invalid one.
- A bug fix starts with a failing test that reproduces it.
- Infra changes are asserted in the CDK stack test.
- A slice that changes runtime behavior is also run against local Floci
  through the Dev Container, and a change on the batch path runs the k6 load
  test (`make loadtest`, 100 req/s). Floci reaches about 10 req/s; see
  [loadtest.md](loadtest.md).

## 9. Infra (`packages/infra-iac`)

- CDK in Go, one stack. Names and tuning numbers are constants in one place,
  not literals spread across constructs.
- Lambdas: `provided.al2023`, arm64, no VPC. The 1 s SLO budget rules out
  anything on the cold path that adds an ENI or a network hop.
- Grant least privilege with the construct's `Grant*` methods on the specific
  resource. Write no wildcard IAM statement. CDK generates two that stay,
  because AWS does not scope these actions to a resource: the X-Ray writes
  (`xray:PutTraceSegments`, `xray:PutTelemetryRecords`) and the relay's
  `dynamodb:ListStreams`.

## 10. Style and lint

- `golangci-lint` with `apps/engine/.golangci.yml` must pass. `gofmt` and
  `goimports` (local prefix `engine`) format the code.
- `//nolint` needs the specific linter and a reason.
- Cognitive complexity stays under 15 per function; split by early return
  before extracting helpers.
- `context.Context` is the first argument of every function that does I/O.
