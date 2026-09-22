package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	"engine/internal/queue"
	"engine/internal/store"
)

func record(t *testing.T, job queue.Job) events.SQSEvent {
	t.Helper()
	body, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	return events.SQSEvent{Records: []events.SQSMessage{{MessageId: "m1", Body: string(body)}}}
}

// takeJobs removes the published jobs from the queue, so a test delivers them
// one by one.
func (h *harness) takeJobs() []queue.Job {
	jobs := h.q.Jobs
	h.q.Jobs = nil
	return jobs
}

// deliver hands one record to the worker and returns whether it was reported
// as failed.
func (h *harness) deliver(job queue.Job) bool {
	h.t.Helper()
	resp, err := h.worker.Handle(h.t.Context(), record(h.t, job))
	if err != nil {
		h.t.Fatal(err)
	}
	return len(resp.BatchItemFailures) == 1 && resp.BatchItemFailures[0].ItemIdentifier == "m1"
}

// deadLetter hands one record to the DLQ consumer and returns whether it was
// reported as failed.
func (h *harness) deadLetter(job queue.Job) bool {
	h.t.Helper()
	resp, err := h.dlq.Handle(h.t.Context(), record(h.t, job))
	if err != nil {
		h.t.Fatal(err)
	}
	return len(resp.BatchItemFailures) > 0
}

// failThroughDLQ makes the store fail on every one of the maxReceiveCount=3
// deliveries of job, then hands it to the DLQ consumer, as SQS would.
func (h *harness) failThroughDLQ(job queue.Job) {
	h.t.Helper()
	h.mem.FailNextWrites(3)
	for range 3 {
		if !h.deliver(job) {
			h.t.Fatalf("worker did not report the record on a store failure: %+v", job)
		}
	}
	if h.deadLetter(job) {
		h.t.Fatal("DLQ consumer reported the record")
	}
}

func (h *harness) wantStatus(resp events.APIGatewayV2HTTPResponse, code int, body string) {
	h.t.Helper()
	if resp.StatusCode != code || (body != "" && resp.Body != body) {
		h.t.Fatalf("status=%d body=%s, want %d %s", resp.StatusCode, resp.Body, code, body)
	}
}

// failedBatch submits threeCustomers, fails item 0 through the DLQ, and
// decides the other two.
func (h *harness) failedBatch() (string, queue.Job) {
	h.t.Helper()
	id := h.submit(threeCustomers)
	jobs := h.takeJobs()
	h.failThroughDLQ(jobs[0])
	for _, j := range jobs[1:] {
		if h.deliver(j) {
			h.t.Fatalf("worker reported %+v", j)
		}
	}
	return id, jobs[0]
}

func TestFailedItemIsRetriedToCompletion(t *testing.T) {
	h := newHarness(t)
	id, _ := h.failedBatch()

	got, _ := h.report(id)
	if got.Status != "NEEDS_ATTENTION" || got.Counters != (store.Counters{Decided: 2, Failed: 1}) {
		t.Fatalf("%+v", got)
	}
	if len(got.Failed) != 1 {
		t.Fatalf("failed=%+v", got.Failed)
	}
	if f := got.Failed[0]; f.Index != 0 || f.CPFMasked != "***05" || f.Attempts != 1 || f.Decision != "" {
		t.Fatalf("failed[0]=%+v", f)
	}

	h.wantStatus(h.do("POST", "/batches/"+id+"/items/0/retry", ""), http.StatusAccepted, `{"attempts":2,"index":0}`)
	h.drain()

	got, _ = h.report(id)
	if got.Status != "COMPLETED" || got.Counters != (store.Counters{Decided: 3}) || got.Total != 1_050_000 {
		t.Fatalf("%+v", got)
	}
	if a := got.Approved[0]; a.Index != 0 || a.Attempts != 2 {
		t.Fatalf("approved[0]=%+v", a)
	}
	h.assertNoFullCPF()
}

func TestRetryAtMaxAttemptsIsRefused(t *testing.T) {
	h := newHarness(t)
	id, job := h.failedBatch()
	for attempt := 2; attempt <= store.MaxAttempts; attempt++ {
		h.wantStatus(h.do("POST", "/batches/"+id+"/items/0/retry", ""), http.StatusAccepted, "")
		jobs := h.takeJobs()
		if len(jobs) != 1 || jobs[0].Attempt != attempt || jobs[0].Customer.CPF != job.Customer.CPF {
			t.Fatalf("attempt %d: jobs=%+v", attempt, jobs)
		}
		h.failThroughDLQ(jobs[0])
	}

	got, _ := h.report(id)
	if got.Status != "NEEDS_ATTENTION" || got.Failed[0].Attempts != store.MaxAttempts {
		t.Fatalf("%+v", got)
	}
	h.wantStatus(h.do("POST", "/batches/"+id+"/items/0/retry", ""), http.StatusConflict, `{"error":"max_attempts_reached"}`)
	h.wantStatus(h.do("POST", "/batches/"+id+"/retry-failed", ""), http.StatusAccepted, `{"requeued":0}`)
	if len(h.q.Jobs) != 0 {
		t.Fatalf("published %+v", h.q.Jobs)
	}
}

func TestCancelFailedItemIsIdempotentAndCompletesTheBatch(t *testing.T) {
	h := newHarness(t)
	id, _ := h.failedBatch()

	for range 2 {
		h.wantStatus(h.do("POST", "/batches/"+id+"/items/0/cancel", ""), http.StatusOK, `{"index":0,"status":"CANCELLED"}`)
	}
	got, _ := h.report(id)
	if got.Status != "COMPLETED" || got.Counters != (store.Counters{Decided: 2, Cancelled: 1}) {
		t.Fatalf("%+v", got)
	}
	if len(got.Cancelled) != 1 || got.Cancelled[0].Index != 0 || got.Cancelled[0].CPFMasked != "***05" {
		t.Fatalf("cancelled=%+v", got.Cancelled)
	}
	h.wantStatus(h.do("POST", "/batches/"+id+"/items/0/retry", ""), http.StatusConflict, `{"error":"invalid_transition"}`)
	h.assertNoFullCPF()
}

func TestRetryOrCancelDecidedOrQueuedItemConflicts(t *testing.T) {
	h := newHarness(t)
	id, _ := h.failedBatch()
	for _, action := range []string{"retry", "cancel"} {
		h.wantStatus(h.do("POST", "/batches/"+id+"/items/1/"+action, ""), http.StatusConflict, `{"error":"invalid_transition"}`)
	}

	// A second operator's retry of the same failed item loses the race.
	h.wantStatus(h.do("POST", "/batches/"+id+"/items/0/retry", ""), http.StatusAccepted, "")
	h.wantStatus(h.do("POST", "/batches/"+id+"/items/0/retry", ""), http.StatusConflict, `{"error":"invalid_transition"}`)
	h.wantStatus(h.do("POST", "/batches/"+id+"/items/0/cancel", ""), http.StatusConflict, `{"error":"invalid_transition"}`)
	if len(h.q.Jobs) != 1 {
		t.Fatalf("jobs=%+v", h.q.Jobs)
	}
}

func TestUnknownRecoveryTargetsReturn404(t *testing.T) {
	h := newHarness(t)
	id := h.submit(threeCustomers)
	for _, path := range []string{
		"/batches/nope/items/0/retry",
		"/batches/nope/items/0/cancel",
		"/batches/" + id + "/items/3/retry",
		"/batches/" + id + "/items/3/cancel",
		"/batches/" + id + "/items/x/retry",
		"/batches/" + id + "/items/-1/cancel",
		"/batches/nope/retry-failed",
	} {
		h.wantStatus(h.do("POST", path, ""), http.StatusNotFound, `{"error":"not_found"}`)
	}
}

func TestPublishFailuresOnSubmitAreFailedThenRetriedTogether(t *testing.T) {
	h := newHarness(t)
	h.q.FailNextPublishes(2)
	id, queued := h.submitAccepted(threeCustomers)
	if queued != 1 || len(h.q.Jobs) != 1 {
		t.Fatalf("queued=%d jobs=%d", queued, len(h.q.Jobs))
	}
	got, _ := h.report(id)
	if got.Counters != (store.Counters{Queued: 1, Failed: 2}) {
		t.Fatalf("every queued item must have a message: %+v", got.Counters)
	}
	h.drain()
	got, _ = h.report(id)
	if got.Status != "NEEDS_ATTENTION" || len(got.Failed) != 2 || got.Failed[0].Index != 0 || got.Failed[1].Index != 1 {
		t.Fatalf("%+v", got)
	}

	h.wantStatus(h.do("POST", "/batches/"+id+"/retry-failed", ""), http.StatusAccepted, `{"requeued":2}`)
	h.drain()
	got, _ = h.report(id)
	if got.Status != "COMPLETED" || got.Counters != (store.Counters{Decided: 3}) || got.Approved[0].Attempts != 2 {
		t.Fatalf("%+v", got)
	}
}

func TestRetryFailedCountsOnlyPublishedItems(t *testing.T) {
	h := newHarness(t)
	h.q.FailNextPublishes(3)
	id, queued := h.submitAccepted(threeCustomers)
	if queued != 0 {
		t.Fatalf("queued=%d", queued)
	}

	h.q.FailNextPublishes(1)
	h.wantStatus(h.do("POST", "/batches/"+id+"/retry-failed", ""), http.StatusAccepted, `{"requeued":2}`)
	got, _ := h.report(id)
	if got.Counters != (store.Counters{Queued: 2, Failed: 1}) || len(h.q.Jobs) != 2 {
		t.Fatalf("counters=%+v jobs=%d", got.Counters, len(h.q.Jobs))
	}
	if f := got.Failed[0]; f.Index != 0 || f.Attempts != 2 {
		t.Fatalf("failed=%+v", got.Failed)
	}
}

func TestRetryPublishFailureFailsTheItemAgain(t *testing.T) {
	h := newHarness(t)
	id, _ := h.failedBatch()

	h.q.FailNextPublishes(1)
	h.wantStatus(h.do("POST", "/batches/"+id+"/items/0/retry", ""), http.StatusServiceUnavailable, `{"error":"enqueue_failed"}`)
	got, _ := h.report(id)
	if got.Status != "NEEDS_ATTENTION" || got.Counters != (store.Counters{Decided: 2, Failed: 1}) || got.Failed[0].Attempts != 2 {
		t.Fatalf("%+v", got)
	}
	if len(h.q.Jobs) != 0 {
		t.Fatalf("jobs=%+v", h.q.Jobs)
	}
}

func TestSingleEvaluationFailsClosed(t *testing.T) {
	h := newHarness(t)
	h.mem.FailNextWrites(1)
	h.wantStatus(h.do("POST", "/evaluations", customerJSON(t, func(map[string]any) {})), http.StatusServiceUnavailable, `{"error":"decision_not_recorded"}`)
	if !h.mem.Empty() {
		t.Fatal("stored a decision")
	}
}

func TestBatchStoreFailurePublishesNothing(t *testing.T) {
	h := newHarness(t)
	h.mem.FailNextWrites(1)
	h.wantStatus(h.do("POST", "/evaluations/batch", threeCustomers), http.StatusServiceUnavailable, `{"error":"batch_not_recorded"}`)
	if !h.mem.Empty() || len(h.q.Jobs) != 0 {
		t.Fatal("stored or published a batch that was not recorded")
	}
}

// A record whose item does not exist can never succeed. The worker reports it
// so it reaches the DLQ after maxReceiveCount, and the DLQ consumer
// acknowledges it instead of failing it forever.
func TestRecordForMissingItemIsDroppedByTheDLQConsumer(t *testing.T) {
	h := newHarness(t)
	id := h.submit(threeCustomers)
	jobs := h.takeJobs()
	for _, job := range []queue.Job{
		{BatchID: "nope", Index: 0, Attempt: 1, Customer: jobs[0].Customer},
		{BatchID: id, Index: 99, Attempt: 1, Customer: jobs[0].Customer},
	} {
		if !h.deliver(job) {
			t.Fatalf("worker acknowledged a record for a missing item: %+v", job)
		}
		if h.deadLetter(job) {
			t.Fatalf("DLQ consumer reported a record for a missing item: %+v", job)
		}
	}
	resp, err := h.dlq.Handle(t.Context(), events.SQSEvent{Records: []events.SQSMessage{{MessageId: "m1", Body: "not json"}}})
	if err != nil || len(resp.BatchItemFailures) != 0 {
		t.Fatalf("malformed record: resp=%+v err=%v", resp, err)
	}
	got, _ := h.report(id)
	if got.Counters != (store.Counters{Queued: 3}) {
		t.Fatalf("%+v", got.Counters)
	}
}

func TestDLQConsumerReportsTheRecordWhenTheStoreFails(t *testing.T) {
	h := newHarness(t)
	id := h.submit(threeCustomers)
	jobs := h.takeJobs()

	h.mem.FailNextWrites(1)
	if !h.deadLetter(jobs[0]) {
		t.Fatal("DLQ consumer acknowledged a record it could not mark failed")
	}
	if h.deadLetter(jobs[0]) {
		t.Fatal("DLQ consumer reported the record on its second try")
	}
	// A late DLQ record for an item already failed, or decided, is acknowledged.
	if h.deadLetter(jobs[0]) {
		t.Fatal("DLQ consumer reported a record for a failed item")
	}
	if h.deliver(jobs[1]) || h.deadLetter(jobs[1]) {
		t.Fatal("decided item")
	}
	got, _ := h.report(id)
	if got.Counters != (store.Counters{Queued: 1, Decided: 1, Failed: 1}) {
		t.Fatalf("%+v", got.Counters)
	}
}
