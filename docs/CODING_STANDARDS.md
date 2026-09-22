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
| Ports | `internal/store`, `internal/queue` | `domain` |
| Use cases | `internal/evaluate`, `submit`, `report`, `processjob`, and new ones | `domain`, `rules`, ports; never another use case except the one it orchestrates, never an adapter |
| Adapters | `internal/adapter/*` | use cases, ports, `domain`, AWS SDK |
| Composition | `cmd/*` | anything; wiring only, no logic |

- `domain`, `rules`, ports, and use cases never import `github.com/aws/...`.
- One package per use case. A use case exposes `New(...) UseCase` and
  `Execute(ctx, ...)`.
- Ports are interfaces defined next to their in-memory implementation. The
  in-memory implementation is the test double; do not add mocking libraries.
- Adapters translate wire types (API Gateway, SQS events, DynamoDB items) into
  domain types at the edge. Wire types never reach a use case.

## 3. Domain and rules

- Money is `int64` cents. Rates are basis points (`int64`, 10000 = 100%). No
  floats in a decision path.
- Domain code does no I/O, reads no clock or environment, and is deterministic
  for the same input.
- A rule is one type in its own file, implements `rules.Handler`, holds its
  cutoffs as fields, and returns a stable reason code
  (`snake_case`, e.g. `score_below_600`). Reason codes are part of the API
  contract: changing one is a breaking change.
- The policy (rule order and amount policy) is assembled only in the rules
  factory. Adding or changing a rule never edits a use case.
- Validation lives in the domain and returns every violation as field + code.
  It runs at the adapter edge before any use case is called.

## 4. Errors

- Wrap with `fmt.Errorf("...: %w", err)`; compare with `errors.Is` / `errors.As`.
- Sentinel errors are named `ErrX`; error types are named `XError`.
- Adapters map errors to HTTP status codes; use cases never know about HTTP.
  `400` malformed input, `404` unknown id, `409` invalid transition, `422`
  invalid customer, `503` persistence failed.
- A handler that returns a `5xx` status logs the cause once, at the adapter.
- Never swallow an error. `nilerr` and `errcheck` (with type assertions) are
  on.

## 5. Persistence and queues

- Writes that decide a batch item are idempotent: keys are deterministic
  (`ITEM#<index>`, never a random id), and state transitions are conditional
  writes on the current status.
- Counters and item transitions change in the same transaction.
- The sync path fails closed (ADR 0001): no stored decision, no decision
  returned.
- The worker reports partial batch failures; it never fails a whole SQS batch
  for one record.

## 6. Security and privacy

- A full CPF or a customer name never appears in a log line, a metric
  dimension, an error message, or an API response. Outputs use the masked CPF.
- Full CPF and name live only in DynamoDB and SQS messages.
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

- Tests drive behavior through the highest seam: the Lambda handlers composed
  over the in-memory queue and store. Assert on responses and stored outcomes,
  not on private functions.
- Rule, validation, and amount logic use table tests with `t.Run` and named
  cases. Test packages are external (`package x_test`).
- Use `t.Context()` for contexts in tests.
- Test data uses valid CPFs (correct check digits) unless the case tests an
  invalid one.
- A bug fix starts with a failing test that reproduces it.
- Infra changes are asserted in the CDK stack test.
- A slice that changes runtime behavior is also run against local Floci
  through the Dev Container, and a change on the batch path runs the k6 load
  test (`make loadtest`, 100 req/s on Floci; the 1000 req/s NFR run is
  `LOADTEST_RATE=1000 make loadtest` against a real AWS stack).

## 9. Infra (`packages/infra-iac`)

- CDK in Go, one stack. Names and tuning numbers are constants in one place,
  not literals spread across constructs.
- Lambdas: `provided.al2023`, arm64, no VPC. The 1 s SLO budget rules out
  anything on the cold path that adds an ENI or a network hop.
- Grant least privilege with the construct's `Grant*` methods on the specific
  resource. No wildcard IAM statements.

## 10. Style and lint

- `golangci-lint` with `apps/engine/.golangci.yml` must pass. `gofmt` and
  `goimports` (local prefix `engine`) format the code.
- `//nolint` needs the specific linter and a reason.
- Cognitive complexity stays under 15 per function; split by early return
  before extracting helpers.
- `context.Context` is the first argument of every function that does I/O.
