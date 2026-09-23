---
status: accepted
---

# Relay queued items to the queue from the table's stream

Submit and retry used to write the item and then publish its message, two
writes that could split. A publish that failed marked the item `FAILED` inside
the request, so a blip in SQS became an operator task, and the routes carried a
`503 enqueue_failed` for it.

Now submit and retry only write the item. The table's stream (DynamoDB Streams,
`NEW_IMAGE`) delivers every item change to the `Relay` Lambda. `batch.Relay`
publishes the changes that move an item to `QUEUED`, and the worker consumes
the queue as before. An item that is stored always reaches the queue: the
stream retries a failed publish until the record expires after 24 hours.

The message is an item event with no customer data:
`{event_id, batch_id, item_id, attempt, status}`. `event_id` is
`<batch_id>:<item_id>:<attempt>:<status>`, the same for the same transition, so
a consumer can drop a duplicate. The worker reads the item with a consistent
`GetItem` and evaluates the current row. The full CPF and name stay in DynamoDB.

## Considered options

- **Kinesis Data Streams for DynamoDB**, with up to 1 year of retention, native
  replay, and 5 readers per shard (20 with enhanced fan-out). Rejected for now:
  it adds a stream with a fixed cost and change data capture units for one
  consumer, and it may deliver records out of order or twice. Adopt it when
  reprocessing past 24 hours or several independent readers become
  requirements. In Floci, CloudFormation ignores the table's
  `KinesisStreamSpecification` and Firehose ignores a Kinesis source, so
  adopting it also needs local shims.
- **SNS fan-out between the relay and the queue.** Rejected: there is one
  consumer. A topic with one subscription is a seam nothing varies across.
  Add it with the second consumer, with a filter policy per subscription.
- **An event archive (Firehose to S3).** Rejected: no reader needs it yet, and
  the full-image records carry PII, which an archive would keep for its whole
  retention.
- **The worker reads the stream directly.** Rejected: an error blocks the
  shard, and the DLQ and per-message retries of ADR 0001 would become per
  batch of records.
- **EventBridge Pipes as the relay.** Rejected: the event format would live in
  a transformation template instead of Go, and it is not known to run in Floci.
- **Event sourcing** (the event log as the source of truth). Rejected: the
  stream is change data capture. The item row and its conditional updates stay
  the truth, and the stream expires after 24 hours.
- **FIFO queue.** Rejected: the conditional `Decide` already ignores a late or
  repeated attempt, so ordering in transport buys nothing.

## Consequences

- `POST /evaluations/batch` answers once the items are written. `queued` is
  always the number of items. `retry` and `retry-failed` only move rows. No
  route returns `503 enqueue_failed`.
- The HTTP Lambda has no access to the queue. Only the relay may send to it.
- A publish that fails in the relay leaves the item `QUEUED`. The relay reports
  the record, and the event source mapping delivers it again (bisecting the
  batch on an error). A record the relay cannot read is logged
  (`stream_record_skipped`) and skipped, so it does not block its shard. The
  `RelaySkippedRecords` alarm counts those lines: a skipped record of a
  queued item leaves it `QUEUED` with no message, and the iterator age cannot
  show it because the record was acknowledged.
- The `RelayIteratorAge` alarm fires when the relay falls 60 seconds behind.
  An item stays `QUEUED` with no message only if the relay is down for the
  full 24 hours of the stream. To recover, update those items (any write
  emits a new stream record) or send their events to the queue; the worker
  ignores an event whose item already moved on.
- A `Create` that fails after some rows are written leaves stream records for
  rows it then deletes. The relay publishes them, the worker finds no item,
  and the DLQ consumer acknowledges them. The delete runs on its own 2 s
  deadline, past the request's. If it fails too, the rows stay and are
  evaluated: the log line `batch_rollback_failed` names the `batch_id` and the
  `EvaluateStoreBookkeeping` alarm fires.
- Reprocessing past the stream uses the table itself: list the items by status
  and send their events again. It is documented, not built.
- This supersedes the point of ADR 0001 where a failed publish fails the item.
