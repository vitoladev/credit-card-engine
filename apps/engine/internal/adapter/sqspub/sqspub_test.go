package sqspub_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go/middleware"

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
	created, err := client.CreateQueue(t.Context(), &sqs.CreateQueueInput{QueueName: new("sqspub-test-" + t.Name())})
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

const lambdaTraceHeader = "Root=1-5759e988-bd862e3fe1be46a994272793;Parent=53995c3f42cd8ad8;Sampled=1"

func TestPublishAttachesTheLambdaTraceHeader(t *testing.T) {
	t.Setenv("_X_AMZN_TRACE_ID", lambdaTraceHeader)
	in := capturedBatch(t, jobs(1))
	got := in.Entries[0].MessageSystemAttributes[string(types.MessageSystemAttributeNameForSendsAWSTraceHeader)]
	if aws.ToString(got.DataType) != "String" || aws.ToString(got.StringValue) != lambdaTraceHeader {
		t.Fatalf("%+v", in.Entries[0].MessageSystemAttributes)
	}
}

func TestPublishOmitsTheTraceHeaderWithoutATrace(t *testing.T) {
	t.Setenv("_X_AMZN_TRACE_ID", "")
	in := capturedBatch(t, jobs(1))
	if in.Entries[0].MessageSystemAttributes != nil {
		t.Fatalf("%+v", in.Entries[0].MessageSystemAttributes)
	}
}

func TestPublishDeliversTheTraceHeaderOnTheMessage(t *testing.T) {
	t.Setenv("_X_AMZN_TRACE_ID", lambdaTraceHeader)
	cfg := awsConfig(t)
	client := sqs.NewFromConfig(cfg)
	created, err := client.CreateQueue(t.Context(), &sqs.CreateQueueInput{QueueName: new("sqspub-trace-" + t.Name())})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = client.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: created.QueueUrl})
	})
	failed, err := sqspub.New(cfg, aws.ToString(created.QueueUrl)).Publish(t.Context(), jobs(1))
	if err != nil || len(failed) != 0 {
		t.Fatalf("failed=%v err=%v", failed, err)
	}
	out, err := client.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{
		QueueUrl:                    created.QueueUrl,
		MaxNumberOfMessages:         1,
		WaitTimeSeconds:             2,
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAWSTraceHeader},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 1 {
		t.Fatalf("messages=%d", len(out.Messages))
	}
	if got := out.Messages[0].Attributes[string(types.MessageSystemAttributeNameAWSTraceHeader)]; got != lambdaTraceHeader {
		t.Fatalf("AWSTraceHeader=%q attributes=%v", got, out.Messages[0].Attributes)
	}
}

func capturedBatch(t *testing.T, jobs []queue.Job) *sqs.SendMessageBatchInput {
	t.Helper()
	var got *sqs.SendMessageBatchInput
	cfg, err := config.LoadDefaultConfig(t.Context(),
		config.WithRegion("us-east-1"),
		config.WithBaseEndpoint("http://127.0.0.1:1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	cfg.APIOptions = append(cfg.APIOptions, func(stack *middleware.Stack) error {
		return stack.Initialize.Add(middleware.InitializeMiddlewareFunc("capture", func(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
			if req, ok := in.Parameters.(*sqs.SendMessageBatchInput); ok {
				got = req
			}
			return middleware.InitializeOutput{}, middleware.Metadata{}, errors.New("stop")
		}), middleware.After)
	})
	_, _ = sqspub.New(cfg, "http://127.0.0.1:1/queue").Publish(t.Context(), jobs)
	if got == nil || len(got.Entries) != len(jobs) {
		t.Fatalf("SendMessageBatch not captured: %+v", got)
	}
	return got
}
