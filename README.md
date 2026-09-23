# Credit Card Engine

A revolving-credit rules engine in Go. CDK in Go describes the HTTP API,
Lambda, SQS, and DynamoDB.

The cut, the rules, and the API are in [Architecture](docs/architecture.md).
[How a batch moves](docs/architecture.md#how-a-batch-moves) is the batch path.
You do not need an AWS account: everything runs on Floci, a local AWS
emulator, inside a Dev Container.

## Run the engine in the Dev Container

You need Docker and the Dev Container CLI. Install the CLI with Homebrew or npm:

```bash
brew install devcontainer
```

```bash
npm install -g @devcontainers/cli
```

Without a global install, `npx --yes @devcontainers/cli` is the same binary.

From the repo root:

```bash
devcontainer up --workspace-folder .
```

Wait until that command finishes. It installs Go 1.27, Node 22, AWS CLI, k6,
CDK, and `cdklocal`, then waits until Floci answers at `http://floci:4566`.
Dummy credentials (`test` / `test`) are already in the environment.

Then, still from the repo root:

```bash
devcontainer exec --workspace-folder . make test
devcontainer exec --workspace-folder . make local-bootstrap
devcontainer exec --workspace-folder . make local-deploy
```

Every later `make` and `curl` in this README is that `exec` line, with the
command after `.`. Git stays on the host.

Cursor or VS Code with the Dev Containers extension does the same stack:
**Reopen in Container**, then run `make` in the container terminal.

### Call the API

The request and response bodies below are from a live run of the Open
Collection in [docs/requests](docs/requests) (`make requests`, 30/30).
IDs change each run. Bruno opens the same folder. The list GETs on the
first batch were recaptured after the worker decided those items.

Inside the Dev Container the credentials are already set. Sign every
route except `GET /health`:

```bash
BASE="$(make -s api-url)"
AUTH=(--aws-sigv4 "aws:amz:us-east-1:execute-api" --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY")
curl -s "$BASE/health"
curl -s "${AUTH[@]}" -X POST "$BASE/evaluations" \
  -H 'content-type: application/json' --data '<request body>'
```

On the host, `BASE` uses `localhost:4566` in place of `floci:4566`.
With more than one query parameter, write them in alphabetical order
(`cursor`, `limit`, `status`). The curl in the Dev Container (7.88)
signs the query string in the order you type it, and SigV4 expects
sorted keys, so any other order returns `403`. The AWS SDKs sort for you.

A batch has no status. `FAILED` is an item the DLQ consumer marked.
On a healthy stack, retry and cancel of a decided item return
`409 invalid_transition`, and `retry-failed` returns `{"requeued":0}`.
A `BATCH_SIZE` outside 1..1000 fails the synth. Default is 100, max 1000.

#### Health

**Health** (`docs/requests/health.yml`)

```
GET /health
```

`200`:

```json
{
  "status": "ok"
}
```

#### One customer

**Approved: Ana** (`docs/requests/1-single-evaluation/approved-ana.yml`)

```
POST /evaluations
```

Request:

```json
{
  "name": "Ana",
  "cpf": "390.533.447-05",
  "credit_score": 780,
  "current_invoice_cents": 50000,
  "credit_limit_cents": 500000,
  "late_payments": 0,
  "monthly_spend_cents": [
    80000,
    90000,
    70000
  ]
}
```

`200`:

```json
{
  "decision_id": "d313417d-6f3e-4dbb-9568-1f3dd3e2fb74",
  "name": "Ana",
  "cpf_masked": "390.***.***-05",
  "decision": "APPROVED",
  "revolving_amount_cents": 250000,
  "reasons": [
    "eligible"
  ]
}
```

**Read the decision back** (`docs/requests/1-single-evaluation/read-the-decision-back.yml`)

```
GET /evaluations/d313417d-6f3e-4dbb-9568-1f3dd3e2fb74
```

`200`:

```json
{
  "decision_id": "d313417d-6f3e-4dbb-9568-1f3dd3e2fb74",
  "name": "Ana",
  "cpf_masked": "390.***.***-05",
  "decision": "APPROVED",
  "revolving_amount_cents": 250000,
  "reasons": [
    "eligible"
  ]
}
```

**Denied: score below 600 (Bruno)** (`docs/requests/1-single-evaluation/denied-score-below-600-bruno.yml`)

```
POST /evaluations
```

Request:

```json
{
  "name": "Bruno",
  "cpf": "123.456.789-09",
  "credit_score": 520,
  "current_invoice_cents": 200000,
  "credit_limit_cents": 400000,
  "late_payments": 0,
  "monthly_spend_cents": [
    80000
  ]
}
```

`200`:

```json
{
  "decision_id": "8ebd0ecd-2d7d-443e-97fe-34543a7dfb92",
  "name": "Bruno",
  "cpf_masked": "123.***.***-09",
  "decision": "DENIED",
  "revolving_amount_cents": 0,
  "reasons": [
    "score_below_600"
  ]
}
```

**Denied: more than 2 late payments (Diego)** (`docs/requests/1-single-evaluation/denied-more-than-2-late-payments-diego.yml`)

```
POST /evaluations
```

Request:

```json
{
  "name": "Diego",
  "cpf": "111.222.333-96",
  "credit_score": 810,
  "current_invoice_cents": 50000,
  "credit_limit_cents": 1200000,
  "late_payments": 3,
  "monthly_spend_cents": [
    200000,
    180000,
    190000
  ]
}
```

`200`:

```json
{
  "decision_id": "be4acb75-0cfc-4bd6-9126-3c604430948c",
  "name": "Diego",
  "cpf_masked": "111.***.***-96",
  "decision": "DENIED",
  "revolving_amount_cents": 0,
  "reasons": [
    "late_payments_above_2"
  ]
}
```

**Denied: invoice over the credit limit (Carla)** (`docs/requests/1-single-evaluation/denied-invoice-over-the-credit-limit-carla.yml`)

```
POST /evaluations
```

Request:

```json
{
  "name": "Carla",
  "cpf": "987.654.321-00",
  "credit_score": 690,
  "current_invoice_cents": 900000,
  "credit_limit_cents": 500000,
  "late_payments": 1,
  "monthly_spend_cents": [
    100000,
    120000,
    110000
  ]
}
```

`200`:

```json
{
  "decision_id": "faa6e70f-13ea-42cd-ae92-d571f2eda029",
  "name": "Carla",
  "cpf_masked": "987.***.***-00",
  "decision": "DENIED",
  "revolving_amount_cents": 0,
  "reasons": [
    "invoice_exceeds_credit_limit"
  ]
}
```

**Denied: no spend history (Helena)** (`docs/requests/1-single-evaluation/denied-no-spend-history-helena.yml`)

```
POST /evaluations
```

Request:

```json
{
  "name": "Helena",
  "cpf": "444.555.666-19",
  "credit_score": 740,
  "current_invoice_cents": 10000,
  "credit_limit_cents": 300000,
  "late_payments": 0,
  "monthly_spend_cents": []
}
```

`200`:

```json
{
  "decision_id": "403baf03-6779-447f-bfcc-b2c902826e52",
  "name": "Helena",
  "cpf_masked": "444.***.***-19",
  "decision": "DENIED",
  "revolving_amount_cents": 0,
  "reasons": [
    "insufficient_spend_history"
  ]
}
```

**Denied: recent spend above 90% of the limit (Igor)** (`docs/requests/1-single-evaluation/denied-recent-spend-above-90-of-the-limit-igor.yml`)

```
POST /evaluations
```

Request:

```json
{
  "name": "Igor",
  "cpf": "555.666.777-20",
  "credit_score": 710,
  "current_invoice_cents": 10000,
  "credit_limit_cents": 100000,
  "late_payments": 0,
  "monthly_spend_cents": [
    95000,
    96000,
    97000
  ]
}
```

`200`:

```json
{
  "decision_id": "c988259d-e55b-4cce-9d3c-152962ff37c1",
  "name": "Igor",
  "cpf_masked": "555.***.***-20",
  "decision": "DENIED",
  "revolving_amount_cents": 0,
  "reasons": [
    "recent_spend_above_share"
  ]
}
```

**Denied: no credit limit (Joana)** (`docs/requests/1-single-evaluation/denied-no-credit-limit-joana.yml`)

```
POST /evaluations
```

Request:

```json
{
  "name": "Joana",
  "cpf": "666.777.888-30",
  "credit_score": 750,
  "current_invoice_cents": 0,
  "credit_limit_cents": 0,
  "late_payments": 0,
  "monthly_spend_cents": [
    10000
  ]
}
```

`200`:

```json
{
  "decision_id": "0aead90b-bc69-4937-94af-a4bb3467a413",
  "name": "Joana",
  "cpf_masked": "666.***.***-30",
  "decision": "DENIED",
  "revolving_amount_cents": 0,
  "reasons": [
    "no_credit_limit"
  ]
}
```

#### Idempotency-Key

**Evaluate with a new key** (`docs/requests/2-idempotency-key/evaluate-with-a-new-key.yml`)

```
POST /evaluations
Idempotency-Key: eb9060ab-c793-48a4-9fe2-ea39fa3c8c4e
```

Request:

```json
{
  "name": "Eva",
  "cpf": "222.333.444-05",
  "credit_score": 820,
  "current_invoice_cents": 100000,
  "credit_limit_cents": 1000000,
  "late_payments": 0,
  "monthly_spend_cents": [
    100000,
    110000,
    90000
  ]
}
```

`200`:

```json
{
  "decision_id": "fbd83d9e-c5aa-460f-b9b5-15fbf9106eeb",
  "name": "Eva",
  "cpf_masked": "222.***.***-05",
  "decision": "APPROVED",
  "revolving_amount_cents": 800000,
  "reasons": [
    "eligible"
  ]
}
```

**Replay: same key, same body** (`docs/requests/2-idempotency-key/replay-same-key-same-body.yml`)

```
POST /evaluations
Idempotency-Key: eb9060ab-c793-48a4-9fe2-ea39fa3c8c4e
```

Request:

```json
{
  "name": "Eva",
  "cpf": "222.333.444-05",
  "credit_score": 820,
  "current_invoice_cents": 100000,
  "credit_limit_cents": 1000000,
  "late_payments": 0,
  "monthly_spend_cents": [
    100000,
    110000,
    90000
  ]
}
```

`200`, header `idempotent-replayed: true`:

```json
{
  "decision_id": "fbd83d9e-c5aa-460f-b9b5-15fbf9106eeb",
  "name": "Eva",
  "cpf_masked": "222.***.***-05",
  "decision": "APPROVED",
  "revolving_amount_cents": 800000,
  "reasons": [
    "eligible"
  ]
}
```

**Same key, another body: 422** (`docs/requests/2-idempotency-key/same-key-another-body-422.yml`)

```
POST /evaluations
Idempotency-Key: eb9060ab-c793-48a4-9fe2-ea39fa3c8c4e
```

Request:

```json
{
  "name": "Ana",
  "cpf": "390.533.447-05",
  "credit_score": 780,
  "current_invoice_cents": 50000,
  "credit_limit_cents": 500000,
  "late_payments": 0,
  "monthly_spend_cents": [
    80000,
    90000,
    70000
  ]
}
```

`422`:

```json
{
  "error": "idempotency_key_reused"
}
```

**Invalid key: 400** (`docs/requests/2-idempotency-key/invalid-key-400.yml`)

```
POST /evaluations
Idempotency-Key: has space
```

Request:

```json
{
  "name": "Ana",
  "cpf": "390.533.447-05",
  "credit_score": 780,
  "current_invoice_cents": 50000,
  "credit_limit_cents": 500000,
  "late_payments": 0,
  "monthly_spend_cents": [
    80000,
    90000,
    70000
  ]
}
```

`400`:

```json
{
  "error": "invalid_idempotency_key",
  "max_length": 255
}
```

#### Batch

**Submit a batch** (`docs/requests/3-batch/submit-a-batch.yml`)

```
POST /evaluations/batch
```

Request:

```json
[
  {
    "name": "Ana",
    "cpf": "390.533.447-05",
    "credit_score": 780,
    "current_invoice_cents": 50000,
    "credit_limit_cents": 500000,
    "late_payments": 0,
    "monthly_spend_cents": [
      80000,
      90000,
      70000
    ]
  },
  {
    "name": "Bruno",
    "cpf": "123.456.789-09",
    "credit_score": 520,
    "current_invoice_cents": 200000,
    "credit_limit_cents": 400000,
    "late_payments": 0,
    "monthly_spend_cents": [
      80000
    ]
  },
  {
    "name": "Carla",
    "cpf": "987.654.321-00",
    "credit_score": 690,
    "current_invoice_cents": 900000,
    "credit_limit_cents": 500000,
    "late_payments": 1,
    "monthly_spend_cents": [
      100000,
      120000,
      110000
    ]
  },
  {
    "name": "Diego",
    "cpf": "111.222.333-96",
    "credit_score": 810,
    "current_invoice_cents": 50000,
    "credit_limit_cents": 1200000,
    "late_payments": 3,
    "monthly_spend_cents": [
      200000,
      180000,
      190000
    ]
  }
]
```

`202`:

```json
{
  "batch_id": "9b8156d7-972d-418d-ac1d-0fda6e2918f7",
  "queued": 4,
  "item_ids": [
    "01a0cf75-997b-73f8-9893-ada9e6ad902a",
    "01a0cf75-997b-73fb-b8fc-cfa964b754ed",
    "01a0cf75-997b-73fc-91ec-71f6f128fa1b",
    "01a0cf75-997b-73fe-b60f-5e53399c0a64"
  ]
}
```

**Submit a batch with an Idempotency-Key** (`docs/requests/3-batch/submit-a-batch-with-an-idempotency-key.yml`)

```
POST /evaluations/batch
Idempotency-Key: cc824d10-214c-4b9b-bab8-142a7f2d11ae
```

Request:

```json
[
  {
    "name": "Ana",
    "cpf": "390.533.447-05",
    "credit_score": 780,
    "current_invoice_cents": 50000,
    "credit_limit_cents": 500000,
    "late_payments": 0,
    "monthly_spend_cents": [
      80000,
      90000,
      70000
    ]
  },
  {
    "name": "Bruno",
    "cpf": "123.456.789-09",
    "credit_score": 520,
    "current_invoice_cents": 200000,
    "credit_limit_cents": 400000,
    "late_payments": 0,
    "monthly_spend_cents": [
      80000
    ]
  },
  {
    "name": "Carla",
    "cpf": "987.654.321-00",
    "credit_score": 690,
    "current_invoice_cents": 900000,
    "credit_limit_cents": 500000,
    "late_payments": 1,
    "monthly_spend_cents": [
      100000,
      120000,
      110000
    ]
  },
  {
    "name": "Diego",
    "cpf": "111.222.333-96",
    "credit_score": 810,
    "current_invoice_cents": 50000,
    "credit_limit_cents": 1200000,
    "late_payments": 3,
    "monthly_spend_cents": [
      200000,
      180000,
      190000
    ]
  }
]
```

`202`:

```json
{
  "batch_id": "d9f90ea7-51fb-497b-8306-d136b92f5ca7",
  "queued": 4,
  "item_ids": [
    "01a0cf75-9a8a-7ccf-b612-5f0cb5028c51",
    "01a0cf75-9a8a-7cd3-ba23-181ff8e92e3d",
    "01a0cf75-9a8a-7cd5-be32-f42547bcfafc",
    "01a0cf75-9a8a-7cd7-a1fe-9587e1c19b2c"
  ]
}
```

**List every item** (`docs/requests/3-batch/list-every-item.yml`)

```
GET /batches/9b8156d7-972d-418d-ac1d-0fda6e2918f7/items
```

`200`:

```json
{
  "batch_id": "9b8156d7-972d-418d-ac1d-0fda6e2918f7",
  "items": [
    {
      "item_id": "01a0cf75-997b-73f8-9893-ada9e6ad902a",
      "name": "Ana",
      "cpf_masked": "390.***.***-05",
      "status": "APPROVED",
      "reasons": [
        "eligible"
      ],
      "revolving_amount_cents": 250000,
      "attempts": 1
    },
    {
      "item_id": "01a0cf75-997b-73fb-b8fc-cfa964b754ed",
      "name": "Bruno",
      "cpf_masked": "123.***.***-09",
      "status": "DENIED",
      "reasons": [
        "score_below_600"
      ],
      "revolving_amount_cents": 0,
      "attempts": 1
    },
    {
      "item_id": "01a0cf75-997b-73fc-91ec-71f6f128fa1b",
      "name": "Carla",
      "cpf_masked": "987.***.***-00",
      "status": "DENIED",
      "reasons": [
        "invoice_exceeds_credit_limit"
      ],
      "revolving_amount_cents": 0,
      "attempts": 1
    },
    {
      "item_id": "01a0cf75-997b-73fe-b60f-5e53399c0a64",
      "name": "Diego",
      "cpf_masked": "111.***.***-96",
      "status": "DENIED",
      "reasons": [
        "late_payments_above_2"
      ],
      "revolving_amount_cents": 0,
      "attempts": 1
    }
  ]
}
```

**List a page of 2** (`docs/requests/3-batch/list-a-page-of-2.yml`)

```
GET /batches/9b8156d7-972d-418d-ac1d-0fda6e2918f7/items?limit=2
```

`200`:

```json
{
  "batch_id": "9b8156d7-972d-418d-ac1d-0fda6e2918f7",
  "items": [
    {
      "item_id": "01a0cf75-997b-73f8-9893-ada9e6ad902a",
      "name": "Ana",
      "cpf_masked": "390.***.***-05",
      "status": "APPROVED",
      "reasons": [
        "eligible"
      ],
      "revolving_amount_cents": 250000,
      "attempts": 1
    },
    {
      "item_id": "01a0cf75-997b-73fb-b8fc-cfa964b754ed",
      "name": "Bruno",
      "cpf_masked": "123.***.***-09",
      "status": "DENIED",
      "reasons": [
        "score_below_600"
      ],
      "revolving_amount_cents": 0,
      "attempts": 1
    }
  ],
  "next_cursor": "MDFhMGNmNzUtOTk3Yi03M2ZiLWI4ZmMtY2ZhOTY0Yjc1NGVk"
}
```

**Next page** (`docs/requests/3-batch/next-page.yml`)

```
GET /batches/9b8156d7-972d-418d-ac1d-0fda6e2918f7/items?cursor=MDFhMGNmNzUtOTk3Yi03M2ZiLWI4ZmMtY2ZhOTY0Yjc1NGVk&limit=2
```

`200`:

```json
{
  "batch_id": "9b8156d7-972d-418d-ac1d-0fda6e2918f7",
  "items": [
    {
      "item_id": "01a0cf75-997b-73fc-91ec-71f6f128fa1b",
      "name": "Carla",
      "cpf_masked": "987.***.***-00",
      "status": "DENIED",
      "reasons": [
        "invoice_exceeds_credit_limit"
      ],
      "revolving_amount_cents": 0,
      "attempts": 1
    },
    {
      "item_id": "01a0cf75-997b-73fe-b60f-5e53399c0a64",
      "name": "Diego",
      "cpf_masked": "111.***.***-96",
      "status": "DENIED",
      "reasons": [
        "late_payments_above_2"
      ],
      "revolving_amount_cents": 0,
      "attempts": 1
    }
  ]
}
```

**Only the denied items** (`docs/requests/3-batch/only-the-denied-items.yml`)

```
GET /batches/9b8156d7-972d-418d-ac1d-0fda6e2918f7/items?status=DENIED
```

`200`:

```json
{
  "batch_id": "9b8156d7-972d-418d-ac1d-0fda6e2918f7",
  "items": [
    {
      "item_id": "01a0cf75-997b-73fb-b8fc-cfa964b754ed",
      "name": "Bruno",
      "cpf_masked": "123.***.***-09",
      "status": "DENIED",
      "reasons": [
        "score_below_600"
      ],
      "revolving_amount_cents": 0,
      "attempts": 1
    },
    {
      "item_id": "01a0cf75-997b-73fc-91ec-71f6f128fa1b",
      "name": "Carla",
      "cpf_masked": "987.***.***-00",
      "status": "DENIED",
      "reasons": [
        "invoice_exceeds_credit_limit"
      ],
      "revolving_amount_cents": 0,
      "attempts": 1
    },
    {
      "item_id": "01a0cf75-997b-73fe-b60f-5e53399c0a64",
      "name": "Diego",
      "cpf_masked": "111.***.***-96",
      "status": "DENIED",
      "reasons": [
        "late_payments_above_2"
      ],
      "revolving_amount_cents": 0,
      "attempts": 1
    }
  ]
}
```

**Only the approved items** (`docs/requests/3-batch/only-the-approved-items.yml`)

```
GET /batches/9b8156d7-972d-418d-ac1d-0fda6e2918f7/items?status=APPROVED
```

`200`:

```json
{
  "batch_id": "9b8156d7-972d-418d-ac1d-0fda6e2918f7",
  "items": [
    {
      "item_id": "01a0cf75-997b-73f8-9893-ada9e6ad902a",
      "name": "Ana",
      "cpf_masked": "390.***.***-05",
      "status": "APPROVED",
      "reasons": [
        "eligible"
      ],
      "revolving_amount_cents": 250000,
      "attempts": 1
    }
  ]
}
```

**Is the batch done?** (`docs/requests/3-batch/is-the-batch-done.yml`)

```
GET /batches/9b8156d7-972d-418d-ac1d-0fda6e2918f7/items?limit=1&status=QUEUED
```

`200`:

```json
{
  "batch_id": "9b8156d7-972d-418d-ac1d-0fda6e2918f7",
  "items": []
}
```

**Invalid status: 400** (`docs/requests/3-batch/invalid-status-400.yml`)

```
GET /batches/9b8156d7-972d-418d-ac1d-0fda6e2918f7/items?status=BOGUS
```

`400`:

```json
{
  "error": "invalid_status"
}
```

**Unknown batch: 404** (`docs/requests/3-batch/unknown-batch-404.yml`)

```
GET /batches/00000000-0000-0000-0000-000000000000/items
```

`404`:

```json
{
  "error": "not_found"
}
```

#### Recover a failed item

**Retry a decided item: 409** (`docs/requests/4-recovery/retry-a-decided-item-409.yml`)

```
POST /batches/9b8156d7-972d-418d-ac1d-0fda6e2918f7/items/01a0cf75-997b-73f8-9893-ada9e6ad902a/retry
```

`409`:

```json
{
  "error": "invalid_transition"
}
```

**Cancel a decided item: 409** (`docs/requests/4-recovery/cancel-a-decided-item-409.yml`)

```
POST /batches/9b8156d7-972d-418d-ac1d-0fda6e2918f7/items/01a0cf75-997b-73f8-9893-ada9e6ad902a/cancel
```

`409`:

```json
{
  "error": "invalid_transition"
}
```

**Retry every failed item** (`docs/requests/4-recovery/retry-every-failed-item.yml`)

```
POST /batches/9b8156d7-972d-418d-ac1d-0fda6e2918f7/retry-failed
```

`202`:

```json
{
  "requeued": 0
}
```

#### Errors

**Invalid customer: 422** (`docs/requests/5-errors/invalid-customer-422.yml`)

```
POST /evaluations
```

Request:

```json
{
  "name": "Ana",
  "cpf": "390.533.447-06",
  "credit_score": -1,
  "current_invoice_cents": 50000,
  "credit_limit_cents": 500000,
  "late_payments": 0,
  "monthly_spend_cents": [
    80000,
    90000,
    70000
  ]
}
```

`422`:

```json
{
  "error": "invalid_customer",
  "violations": [
    {
      "field": "cpf",
      "code": "invalid_check_digits"
    },
    {
      "field": "credit_score",
      "code": "negative"
    }
  ]
}
```

**Malformed JSON: 400** (`docs/requests/5-errors/malformed-json-400.yml`)

```
POST /evaluations
```

Request:

```
{"name":
```

`400`:

```json
{
  "error": "invalid_json"
}
```

**Unknown decision: 404** (`docs/requests/5-errors/unknown-decision-404.yml`)

```
GET /evaluations/00000000-0000-0000-0000-000000000000
```

`404`:

```json
{
  "error": "not_found"
}
```

**Batch with an invalid customer: 422** (`docs/requests/5-errors/batch-with-an-invalid-customer-422.yml`)

```
POST /evaluations/batch
```

Request:

```json
[
  {
    "name": "Ana",
    "cpf": "390.533.447-05",
    "credit_score": 780,
    "current_invoice_cents": 50000,
    "credit_limit_cents": 500000,
    "late_payments": 0,
    "monthly_spend_cents": [
      80000,
      90000,
      70000
    ]
  },
  {
    "name": "Bruno",
    "cpf": "123",
    "credit_score": 520,
    "current_invoice_cents": 200000,
    "credit_limit_cents": 400000,
    "late_payments": 0,
    "monthly_spend_cents": [
      80000
    ]
  }
]
```

`422`:

```json
{
  "error": "invalid_customer",
  "violations": [
    {
      "index": 1,
      "field": "cpf",
      "code": "invalid_length"
    }
  ]
}
```

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

Stop Compose with `docker compose -f .devcontainer/docker-compose.yml down`.

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
| `make requests` | Runs the HTTP collection in `docs/requests` (OpenCollection, for Bruno) against the local stack. |
| `make local-bootstrap` | CDK bootstrap on Floci account `000000000000`. |
| `make local-deploy` | `cdklocal deploy`. Works once per stack on Floci. |
| `make local-redeploy` | `local-destroy` then `local-deploy`. Use it to deploy a change: Floci cannot update the relay's stream event source mapping in place. The tables start empty. |
| `make api-url` | Prints the HTTP API base URL on the emulator. |
| `make synth` | `cdk synth` (template, no deploy). |
| `make floci-up` and `make floci-down` | Only the `floci` service from `.devcontainer/docker-compose.yml`. No-op inside the Dev Container. |
| `make floci-reap` | Removes this stack's Floci Lambda containers (Evaluate, Relay, Worker, DlqConsumer). The Floci sidecar stays up. |

`make loadtest` runs k6 at `LOADTEST_RATE` req/s (default 100) for
`LOADTEST_DURATION` (default 10 s), then removes the Floci Lambda containers,
whether the run passed or failed. [docs/loadtest.md](docs/loadtest.md) has
every setting, the results on Floci, and what limits them.

## What a local run does not prove

Floci differs from AWS in ways that change what a local run shows:

- Floci serves about 10 Lambda invocations a second in total, so a local
  `make loadtest` reaches about 10 req/s whatever `LOADTEST_RATE` asks for.
- Its CloudFormation ignores point-in-time recovery, the SQS batching window,
  and the `IdempotencyKeys` TTL, and it stubs log metric filters. The CDK tests assert
  all four in the template. Expired idempotency keys stay in their table on
  Floci, but the claim's condition treats them as absent, so keys still
  expire after 24 hours.
- A `Query` whose `ExclusiveStartKey` names an item that no longer exists
  restarts from the first item on Floci. DynamoDB continues after the key.
  So a `?status=` cursor whose item changed status between two pages can
  repeat items locally.
- A failed update can leave an API, functions, event source mappings, and a
  tables behind. `make api-url` asks the stack for its own API, so it never
  picks a leftover one.

## Layout

```
apps/engine/          The evaluate, batch, and idempotency modules, the rules, and the http, relay, worker, and dlq commands
packages/infra-iac/   CDK in Go
packages/loadtest/    k6 load test for both evaluation routes
docs/                 architecture.md, pictures/, loadtest.md, CODING_STANDARDS.md, the ADRs in adr/, and the HTTP collection in requests/
CONTEXT.md            The glossary: the name of each domain concept
```
