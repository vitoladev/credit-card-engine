---
status: accepted
---

# Claim the Idempotency-Key with a conditional write

`POST /evaluations` and `POST /evaluations/batch` were not idempotent. A client
that lost the response (a timeout after the write) and sent the request again
got a second `DECISION#` row, or a second batch with every item evaluated
again. The decision is the same, because the rules are deterministic, but the
audit record and the batch are duplicated.

Both routes now accept an `Idempotency-Key` header: 1 to 255 visible ASCII
characters, such as a UUID. The `idempotency` module runs a request at most
once per key, and the `ddb` adapter keeps the key in the engine's table:

| `pk` | `sk` | Attributes |
|---|---|---|
| `IDEMPOTENCY#<key>` | `KEY` | `fingerprint`, `owner`, `state` (`PENDING` or `DONE`), `lease_until`, `expires_at`, and once `DONE` the `status_code` and `body` it replays |

1. **Claim.** One `PutItem` with the condition
   `attribute_not_exists(pk) OR expires_at < now OR (state = PENDING AND lease_until < now)`.
   Two requests with the same key race on this write, and DynamoDB lets
   exactly one of them win. The loser gets the row back from the failed
   condition (`ReturnValuesOnConditionCheckFailure = ALL_OLD`), so it needs no
   second read.
2. **Run.** The winner runs the route as before.
3. **Complete or release.** A `2xx` response is stored on the row
   (`state = DONE`), conditional on the winner still owning the claim. Any
   other response deletes the claim, so the client can fix the request and
   send it again with the same key.

A later request with the key gets:

| The row | Response |
|---|---|
| `DONE`, same route and body | the stored response, with `idempotent-replayed: true` |
| another route or body (SHA-256 fingerprint of method, path, and body) | `422 {"error":"idempotency_key_reused"}` |
| `PENDING`, lease running | `409 {"error":"idempotency_key_in_progress"}` |
| `PENDING`, lease ended, or expired | a new claim: the request runs |

Without the header, a request runs every time, as before.

## Lease and TTL

- The lease is 10 s, longer than the HTTP Lambda's 3 s timeout. A claim left
  by an invocation that crashed frees itself, and the next request with the
  key runs. That request runs the route again: a crash between the write and
  the complete can still record twice. The window is one invocation, not every
  client retry.
- `expires_at` is 24 hours after the claim, in epoch seconds, and is the
  table's TTL attribute. DynamoDB deletes expired rows within days, not on
  time, so the claim's condition also treats an expired row as absent.
- The stream carries the key rows. The relay reads only `BATCH#` rows, so it
  skips them.

## Considered options

- **Put the key and the decision in one `TransactWriteItems`.** Atomic for the
  single evaluation, but a batch writes up to 1000 rows, more than a
  transaction takes (100). It would be a second mechanism for one route.
- **Store every response, `4xx` included** (Stripe's behavior). Rejected: a
  client that sent an invalid body would need a new key to send the fixed one.
  Here only a success is replayed.
- **Derive the `decision_id` or `batch_id` from the key.** Rejected: a
  repeat would overwrite the rows instead of replaying the response, and a
  batch overwrite moves decided items back to `QUEUED`.
- **Scope keys by caller.** Every route is behind the IAM authorizer, and this
  cut has one caller. Add the caller's ARN to the key when there are several.
