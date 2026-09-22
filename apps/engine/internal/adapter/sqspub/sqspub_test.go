package sqspub_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go/middleware"

	"engine/internal/adapter/sqspub"
	"engine/internal/batch"
	"engine/internal/domain"
	"engine/internal/flocitest"
)

func attempts(n int) []batch.Attempt {
	out := make([]batch.Attempt, n)
	for i := range out {
		out[i] = batch.Attempt{BatchID: "b1", Index: i, Number: 1, Customer: domain.Customer{Name: "Ana", CPF: "39053344705"}}
	}
	return out
}

func TestPublishSendsEveryAttempt(t *testing.T) {
	cfg, _ := flocitest.Config(t)
	url := flocitest.Queue(t, cfg)
	failed, err := sqspub.New(cfg, url).Publish(t.Context(), attempts(25))
	if err != nil || len(failed) != 0 {
		t.Fatalf("failed=%v err=%v", failed, err)
	}

	var got []int
	for _, a := range flocitest.Decode[batch.Attempt](t, flocitest.Receive(t, cfg, url)) {
		if a.BatchID != "b1" || a.Number != 1 || a.Customer.CPF != "39053344705" {
			t.Fatalf("%+v", a)
		}
		got = append(got, a.Index)
	}
	slices.Sort(got)
	if len(got) != 25 || got[0] != 0 || got[24] != 24 {
		t.Fatalf("received %v", got)
	}
}

func TestPublishReturnsEveryFailedIndex(t *testing.T) {
	cfg, _ := flocitest.Config(t)
	url := flocitest.Queue(t, cfg)
	missing := url[:strings.LastIndex(url, "/")] + "/missing-queue"
	failed, err := sqspub.New(cfg, missing).Publish(t.Context(), attempts(25))
	if err == nil || len(failed) != 25 || failed[0] != 0 || failed[24] != 24 {
		t.Fatalf("failed=%v err=%v", failed, err)
	}
}

func TestPublishReturnsTheEntriesSQSRejected(t *testing.T) {
	cfg, faults := flocitest.Config(t)
	url := flocitest.Queue(t, cfg)
	faults.DropEntries(2)
	failed, err := sqspub.New(cfg, url).Publish(t.Context(), attempts(3))
	if err == nil || !slices.Equal(failed, []int{0, 1}) {
		t.Fatalf("failed=%v err=%v", failed, err)
	}
	if got := flocitest.Decode[batch.Attempt](t, flocitest.Receive(t, cfg, url)); len(got) != 1 || got[0].Index != 2 {
		t.Fatalf("received %+v", got)
	}
}

const lambdaTraceHeader = "Root=1-5759e988-bd862e3fe1be46a994272793;Parent=53995c3f42cd8ad8;Sampled=1"

func TestPublishAttachesTheLambdaTraceHeader(t *testing.T) {
	t.Setenv("_X_AMZN_TRACE_ID", lambdaTraceHeader)
	in := capturedBatch(t, attempts(1))
	got := in.Entries[0].MessageSystemAttributes[string(types.MessageSystemAttributeNameForSendsAWSTraceHeader)]
	if aws.ToString(got.DataType) != "String" || aws.ToString(got.StringValue) != lambdaTraceHeader {
		t.Fatalf("%+v", in.Entries[0].MessageSystemAttributes)
	}
}

func TestPublishOmitsTheTraceHeaderWithoutATrace(t *testing.T) {
	t.Setenv("_X_AMZN_TRACE_ID", "")
	in := capturedBatch(t, attempts(1))
	if in.Entries[0].MessageSystemAttributes != nil {
		t.Fatalf("%+v", in.Entries[0].MessageSystemAttributes)
	}
}

func TestPublishDeliversTheTraceHeaderOnTheMessage(t *testing.T) {
	t.Setenv("_X_AMZN_TRACE_ID", lambdaTraceHeader)
	cfg, _ := flocitest.Config(t)
	url := flocitest.Queue(t, cfg)
	failed, err := sqspub.New(cfg, url).Publish(t.Context(), attempts(1))
	if err != nil || len(failed) != 0 {
		t.Fatalf("failed=%v err=%v", failed, err)
	}
	msgs := flocitest.Receive(t, cfg, url)
	if len(msgs) != 1 {
		t.Fatalf("messages=%d", len(msgs))
	}
	if got := msgs[0].Attributes[string(types.MessageSystemAttributeNameAWSTraceHeader)]; got != lambdaTraceHeader {
		t.Fatalf("AWSTraceHeader=%q attributes=%v", got, msgs[0].Attributes)
	}
}

// capturedBatch returns the SendMessageBatch request Publish builds, without
// sending it.
func capturedBatch(t *testing.T, attempts []batch.Attempt) *sqs.SendMessageBatchInput {
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
	_, _ = sqspub.New(cfg, "http://127.0.0.1:1/queue").Publish(t.Context(), attempts)
	if got == nil || len(got.Entries) != len(attempts) {
		t.Fatalf("SendMessageBatch not captured: %+v", got)
	}
	return got
}
