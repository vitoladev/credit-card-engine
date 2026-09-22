package sqspub

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"engine/internal/queue"
)

type Publisher struct {
	client *sqs.Client
	url    string
}

func New(cfg aws.Config, queueURL string) *Publisher {
	return &Publisher{client: sqs.NewFromConfig(cfg), url: queueURL}
}

func (p *Publisher) Publish(ctx context.Context, jobs []queue.Job) error {
	for start := 0; start < len(jobs); start += 10 {
		end := min(start+10, len(jobs))
		entries := make([]types.SendMessageBatchRequestEntry, 0, end-start)
		for i, job := range jobs[start:end] {
			body, err := json.Marshal(job)
			if err != nil {
				return err
			}
			entries = append(entries, types.SendMessageBatchRequestEntry{
				Id:          aws.String(strconv.Itoa(start + i)),
				MessageBody: aws.String(string(body)),
			})
		}
		out, err := p.client.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{
			QueueUrl: aws.String(p.url),
			Entries:  entries,
		})
		if err != nil {
			return err
		}
		if len(out.Failed) > 0 {
			return fmt.Errorf("sqs batch failed: %d", len(out.Failed))
		}
	}
	return nil
}
