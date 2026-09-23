package main

import (
	"path/filepath"
	"runtime"
)

const (
	stackName        = "CreditCardEngine"
	functionID       = "Evaluate"
	workerFunctionID = "Worker"
	queueID          = "EvaluationJobs"
	dlqID            = "EvaluationJobsDLQ"
	dlqFunctionID    = "DlqConsumer"
	relayFunctionID  = "Relay"
	apiID            = "Api"
	decisionsTableID = "Decisions"
	itemsTableID     = "BatchItems"
	keysTableID      = "IdempotencyKeys"
	alarmID          = "EvaluateAlarms"

	// statusIndexName is the BatchItems index that lists items by status. The
	// engine's adapter queries it as ddb.StatusIndex.
	statusIndexName = "by-status"
	// keysTTLAttribute expires an idempotency key 24 hours after its claim.
	keysTTLAttribute = "expires_at"

	// Loadtest: 100 req/s by default on Floci; the 1000 req/s NFR run is
	// LOADTEST_RATE=1000 make loadtest against a real AWS stack.
	// Stage headroom above 10k evals/min (~167 rps).
	stageRateLimit  = 1200
	stageBurstLimit = 2400

	// BATCH_SIZE caps customers per POST /evaluations/batch. The default keeps
	// local runs light; the engine refuses anything above the hard max.
	defaultBatchSize = 100
	maxBatchSize     = 1000

	// A record that fails this many receives moves to the DLQ (ADR 0001).
	// AWS recommends at least 5 for a Lambda source, so a short outage does
	// not fail an item.
	maxReceiveCount  = 5
	dlqRetentionDays = 14
	sqsBatchSize     = 10
	// The worker takes up to 50 messages per invocation and handles them
	// concurrently. SQS allows a batch over 10 only with a batching window,
	// which adds up to 1 s when traffic is low.
	workerBatchSize      = 50
	workerBatchingWindow = 1
	// The relay reads the table stream in batches of this size. A record
	// that keeps failing is retried until it expires (24 h); the IteratorAge
	// alarm fires long before that.
	streamBatchSize = 100
	// The relay waits up to this long to fill a batch: near real time, and
	// fewer invocations when traffic is low.
	relayBatchingWindow = 1
	iteratorAgeAlarmMs  = 60_000
	// The batch SLO the ItemEndToEnd alarm guards, and the queue backlog that
	// breaks it first.
	itemEndToEndAlarmMs = 5_000
	queueAgeAlarmS      = 60

	// lambdaTimeoutS must stay under idempotency.Lease (10 s, ADR 0005), or a
	// retry could take a key while the first request still runs.
	lambdaTimeoutS = 3
	lambdaMemoryMB = 256
	latencyAlarmMs = 800

	// Floci starts one container per concurrent invoke. Cap local concurrency
	// so make loadtest does not stall the API under leftover containers.
	flociEvaluateConcurrency = 8
	flociWorkerConcurrency   = 4
	flociDlqConcurrency      = 2
	flociRelayConcurrency    = 2

	metricsNamespace = "CreditCardEngine"
	dashboardName    = "CreditCardEngine"

	api5xxAlarmThreshold           = 1
	workerErrorAlarmThreshold      = 1
	dlqConsumerErrorAlarmThreshold = 1
	dlqVisibleAlarmThreshold       = 0
	dlqVisibleEvaluationPeriods    = 5
)

func repoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("runtime.Caller")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// cmdEntry is the Go main package of one engine binary.
func cmdEntry(cmd string) string {
	return filepath.Join(repoRoot(), "apps", "engine", "cmd", cmd)
}
