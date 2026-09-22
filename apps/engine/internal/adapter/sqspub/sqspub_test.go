package sqspub_test

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"engine/internal/adapter/sqspub"
	"engine/internal/domain"
	"engine/internal/queue"
)

// These tests need an SQS endpoint (Floci), e.g.
// AWS_TEST_ENDPOINT=http://localhost:4566 go test ./internal/adapter/sqspub/
func awsConfig(t *testing.T) aws.Config {
	t.Helper()
	endpoint := os.Getenv("AWS_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("AWS_TEST_ENDPOINT not set")
	}
	cfg, err := config.LoadDefaultConfig(t.Context(),
		config.WithRegion("us-east-1"),
		config.WithBaseEndpoint(endpoint),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func jobs(n int) []queue.Job {
	out := make([]queue.Job, n)
	for i := range out {
		out[i] = queue.Job{BatchID: "b1", Index: i, Attempt: 1, Customer: domain.Customer{Name: "Ana", CPF: "39053344705"}}
	}
	return out
}

func TestPublishSendsEveryJob(t *testing.T) {
	cfg := awsConfig(t)
	client := sqs.NewFromConfig(cfg)
	created, err := client.CreateQueue(t.Context(), &sqs.CreateQueueInput{QueueName: aws.String("sqspub-test-" + t.Name())})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = client.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: created.QueueUrl})
	})

	failed, err := sqspub.New(cfg, aws.ToString(created.QueueUrl)).Publish(t.Context(), jobs(25))
	if err != nil || len(failed) != 0 {
		t.Fatalf("failed=%v err=%v", failed, err)
	}

	var got []int
	for len(got) < 25 {
		out, err := client.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{QueueUrl: created.QueueUrl, MaxNumberOfMessages: 10, WaitTimeSeconds: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Messages) == 0 {
			break
		}
		for _, m := range out.Messages {
			var j queue.Job
			if err := json.Unmarshal([]byte(aws.ToString(m.Body)), &j); err != nil {
				t.Fatal(err)
			}
			if j.BatchID != "b1" || j.Attempt != 1 {
				t.Fatalf("%+v", j)
			}
			got = append(got, j.Index)
		}
	}
	slices.Sort(got)
	if len(got) != 25 || got[0] != 0 || got[24] != 24 {
		t.Fatalf("received %v", got)
	}
}

func TestPublishReturnsEveryFailedIndex(t *testing.T) {
	cfg := awsConfig(t)
	failed, err := sqspub.New(cfg, os.Getenv("AWS_TEST_ENDPOINT")+"/000000000000/missing-queue").Publish(t.Context(), jobs(25))
	if err == nil || len(failed) != 25 || failed[0] != 0 || failed[24] != 24 {
		t.Fatalf("failed=%v err=%v", failed, err)
	}
}
