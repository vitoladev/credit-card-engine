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
	apiID            = "Api"
	tableID          = "Decisions"
	alarmID          = "EvaluateAlarms"

	// Generic keys: one table holds DECISION#, BATCH# META and ITEM# rows.
	tablePartitionKey = "pk"
	tableSortKey      = "sk"

	// Loadtest: 1000 req/s on POST /evaluations/batch (SQS enqueue).
	// Stage headroom above 10k evals/min (~167 rps).
	stageRateLimit  = 1200
	stageBurstLimit = 2400

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
