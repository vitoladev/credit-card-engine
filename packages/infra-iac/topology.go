package main

import (
	"path/filepath"
	"runtime"
)

const (
	stackName  = "CreditCardEngine"
	functionID       = "Evaluate"
	workerFunctionID = "Worker"
	queueID          = "EvaluationJobs"
	dlqID            = "EvaluationJobsDLQ"
	dlqFunctionID    = "DlqConsumer"
	apiID            = "Api"
	tableID          = "Decisions"
	alarmID          = "EvaluateAlarms"

	// Generic keys: one table holds DECISION#, BATCH# META and ITEM# rows.
	tablePartitionKey = "pk"
	tableSortKey      = "sk"

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
	maxReceiveCount  = 3
	dlqRetentionDays = 14
	sqsBatchSize     = 10

	lambdaTimeoutS = 3
	lambdaMemoryMB = 256
	latencyAlarmMs = 800
)

func repoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("runtime.Caller")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func lambdaEntry() string {
	return filepath.Join(repoRoot(), "apps", "engine", "cmd", "http")
}

func workerEntry() string {
	return filepath.Join(repoRoot(), "apps", "engine", "cmd", "worker")
}

func dlqEntry() string {
	return filepath.Join(repoRoot(), "apps", "engine", "cmd", "dlq")
}
