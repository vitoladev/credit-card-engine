---
status: accepted
---

# Keep DynamoDB and SQS behind adapters, even with one implementation

The `batch` and `evaluate` modules declare the ports they need (`batch.Items`,
`batch.Publisher`, `evaluate.DecisionStore`). `adapter/ddb` and
`adapter/sqspub` implement them, and they are the only implementations: there
is no in-memory store or queue, and tests run against Floci. The ports stay
anyway, so the AWS SDK never reaches a module. `architecture_test.go` enforces
this.

## Considered options

- **Let `batch` and `evaluate` call DynamoDB and SQS directly.** One adapter
  per port is a hypothetical seam, and deleting it would put each module in
  charge of its own row format. Rejected: SDK types (attribute values,
  condition failures, batch-write retries) would leak into the modules that
  own the batch rules.
- **Keep an in-memory adapter as a second implementation.** Rejected: a fake
  store cannot prove atomic transitions. The `ConditionExpression`s in
  `adapter/ddb` enforce those, so the fake would be a second copy of the state
  machine that proves nothing.

## Consequences

- The item status transitions live in one place: the conditional updates in
  `adapter/ddb`. The `batch` module coordinates them and does not re-check
  status.
- Module tests need Floci. Failures are injected with AWS SDK middleware on
  the test `aws.Config`, not with a fake.
