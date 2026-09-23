# HTTP requests

An [OpenCollection](https://spec.opencollection.com) 1.0.0 collection with
every use case of the HTTP API and sample customers ready to send. Bruno
opens it as a collection, and `make requests` runs it from the terminal.

## Open it in Bruno

1. In Bruno 3 or later, choose **Open Collection** and pick this folder,
   `docs/requests`.
2. Pick the **floci** environment and set `apiId` to the ID in
   `make api-url`. For example, for
   `http://floci:4566/_aws/execute-api/2855482603/$default`, set
   `2855482603`. `baseUrl` uses `localhost:4566`, the port the Dev Container
   publishes on the host.
3. Run the folders in order. A request that creates something saves its ID
   in a variable (`decisionId`, `batchId`, `itemId`, `cursor`,
   `idempotencyKey`), and the requests after it use that variable.

Every request inherits AWS Signature Version 4 auth from the collection
(`execute-api`, `us-east-1`). On Floci the keys are `test` and `test`. For a
stack on AWS, pick the **aws** environment and fill in `baseUrl` and your
credentials.

## Run it from the terminal

```bash
make requests
```

The target runs the collection with the Bruno CLI against the stack from
`make local-deploy`, and fails if any check fails.

## What is in it

| Folder | Requests |
|---|---|
| `health.yml` | `GET /health`, the only route without auth |
| `1-single-evaluation` | one approval, one request for each denial reason, and reading a decision back |
| `2-idempotency-key` | a new key, its replay, a key reused with another body (`422`), and an invalid key (`400`) |
| `3-batch` | submit, submit with a key, list, pages with a cursor, lists by item status, the empty-`QUEUED` check, `400`, and `404` |
| `4-recovery` | retry and cancel an item, and retry every failed item |
| `5-errors` | an invalid customer (`422`), malformed JSON (`400`), an unknown decision (`404`), and a batch with an invalid customer (`422`) |

Recovery works on a `FAILED` item. An item fails only after 5 deliveries
reach the DLQ, so on a healthy stack retry and cancel answer
`409 invalid_transition`, and `retry-failed` answers `{"requeued":0}`.

The customers are the same as in `packages/loadtest`, with valid CPFs:

| Customer | Outcome |
|---|---|
| Ana, Eva | approved |
| Bruno | `score_below_600` |
| Diego | `late_payments_above_2` |
| Carla | `invoice_exceeds_credit_limit` |
| Helena | `insufficient_spend_history` |
| Igor | `recent_spend_above_share` |
| Joana | `no_credit_limit` |
