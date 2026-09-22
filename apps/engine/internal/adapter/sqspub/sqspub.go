package sqspub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"engine/internal/queue"
)

const (
	chunkSize = 10 // SendMessageBatch limit
	inFlight  = 10
)

type Publisher struct {
	client *sqs.Client
	url    string
}

func New(cfg aws.Config, queueURL string) *Publisher {
	return &Publisher{client: sqs.NewFromConfig(cfg), url: queueURL}
}

// Publish sends the jobs in chunks of 10, several chunks at once. A chunk that
// fails as a whole fails every job in it.
func (p *Publisher) Publish(ctx context.Context, jobs []queue.Job) ([]int, error) {
	var (
		mu     sync.Mutex
		failed []int
		errs   []error
		wg     sync.WaitGroup
	)
	sem := make(chan struct{}, inFlight)
	for start := 0; start < len(jobs); start += chunkSize {
		chunk := jobs[start:min(start+chunkSize, len(jobs))]
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			f, err := p.send(ctx, chunk)
			if err == nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			failed = append(failed, f...)
			errs = append(errs, err)
		})
	}
	wg.Wait()
	slices.Sort(failed)
	return failed, errors.Join(errs...)
}

func (p *Publisher) send(ctx context.Context, chunk []queue.Job) ([]int, error) {
	trace := traceHeader()
	entries := make([]types.SendMessageBatchRequestEntry, 0, len(chunk))
	for _, job := range chunk {
		body, err := json.Marshal(job)
		if err != nil {
			return indexes(chunk), err
		}
		entries = append(entries, types.SendMessageBatchRequestEntry{
			Id:                      aws.String(strconv.Itoa(job.Index)),
			MessageBody:             aws.String(string(body)),
			MessageSystemAttributes: trace,
		})
	}
	out, err := p.client.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{
		QueueUrl: aws.String(p.url),
		Entries:  entries,
	})
	if err != nil {
		return indexes(chunk), fmt.Errorf("send message batch: %w", err)
	}
	if len(out.Failed) == 0 {
		return nil, nil
	}
	failed := make([]int, 0, len(out.Failed))
	for _, f := range out.Failed {
		i, err := strconv.Atoi(aws.ToString(f.Id))
		if err != nil {
			return indexes(chunk), fmt.Errorf("unexpected entry id %q: %w", aws.ToString(f.Id), err)
		}
		failed = append(failed, i)
	}
	return failed, fmt.Errorf("send message batch: %d entries failed, first code %s", len(out.Failed), aws.ToString(out.Failed[0].Code))
}

func indexes(jobs []queue.Job) []int {
	out := make([]int, len(jobs))
	for i, j := range jobs {
		out[i] = j.Index
	}
	return out
}

// Lambda Active tracing puts the current segment in _X_AMZN_TRACE_ID. SQS
// delivers it as AWSTraceHeader so the worker continues this invocation's trace.
func traceHeader() map[string]types.MessageSystemAttributeValue {
	header := os.Getenv("_X_AMZN_TRACE_ID")
	if header == "" {
		return nil
	}
	return map[string]types.MessageSystemAttributeValue{
		string(types.MessageSystemAttributeNameForSendsAWSTraceHeader): {
			DataType:    aws.String("String"),
			StringValue: aws.String(header),
		},
	}
}
