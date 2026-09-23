// Package flocitest gives tests an AWS config pointed at Floci, fresh tables
// and queues, and faults injected in the AWS SDK. Only test code imports it.
package flocitest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"uuid"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodbstreams"
	streamtypes "github.com/aws/aws-sdk-go-v2/service/dynamodbstreams/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go/middleware"
)

// ErrInjected is the error an injected call failure returns.
var ErrInjected = errors.New("injected failure")

// Faults holds the failures the next AWS calls hit. Every config from Config
// reads the same Faults, so a test arms a failure right before the call.
type Faults struct {
	mu      sync.Mutex
	calls   map[string]int
	skips   map[string]int
	entries int
}

// FailCalls makes the next n calls of the operation fail with ErrInjected,
// for example FailCalls("UpdateItem", 1).
func (f *Faults) FailCalls(op string, n int) {
	f.FailCallsAfter(op, 0, n)
}

// FailCallsAfter lets the next skip calls of the operation through, then fails
// the n after them, for example FailCallsAfter("Query", 1, 1) to fail the
// second page of a pagination.
func (f *Faults) FailCallsAfter(op string, skip, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.skips[op] = skip
	f.calls[op] = n
}

// DropEntries makes SQS report the next n SendMessageBatch entries as failed.
// They are removed from the request, so they are never sent.
func (f *Faults) DropEntries(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = n
}

func (f *Faults) takeCall(op string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls[op] == 0 {
		return false
	}
	if f.skips[op] > 0 {
		f.skips[op]--
		return false
	}
	f.calls[op]--
	return true
}

func (f *Faults) takeEntries(limit int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := min(f.entries, limit)
	f.entries -= n
	return n
}

// Config returns an AWS config for Floci with fault injection installed. It
// fails the test when no Floci endpoint is set: these tests are the proof that
// the engine works against DynamoDB and SQS, so they never skip silently.
func Config(t *testing.T) (aws.Config, *Faults) {
	t.Helper()
	endpoint := os.Getenv("AWS_TEST_ENDPOINT")
	if endpoint == "" {
		endpoint = os.Getenv("AWS_ENDPOINT_URL")
	}
	if endpoint == "" {
		t.Fatal("no Floci endpoint: run inside the Dev Container, or set AWS_TEST_ENDPOINT=http://localhost:4566")
	}
	cfg, err := config.LoadDefaultConfig(t.Context(),
		config.WithRegion("us-east-1"),
		config.WithBaseEndpoint(endpoint),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	f := &Faults{calls: map[string]int{}, skips: map[string]int{}}
	cfg.APIOptions = append(cfg.APIOptions, f.failCalls, f.dropEntries)
	return cfg, f
}

func (f *Faults) failCalls(stack *middleware.Stack) error {
	return stack.Initialize.Add(middleware.InitializeMiddlewareFunc("flocitestFailCalls",
		func(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
			if f.takeCall(awsmiddleware.GetOperationName(ctx)) {
				return middleware.InitializeOutput{}, middleware.Metadata{}, ErrInjected
			}
			return next.HandleInitialize(ctx, in)
		}), middleware.After)
}

func (f *Faults) dropEntries(stack *middleware.Stack) error {
	return stack.Initialize.Add(middleware.InitializeMiddlewareFunc("flocitestDropEntries",
		func(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
			input, ok := in.Parameters.(*sqs.SendMessageBatchInput)
			if !ok {
				return next.HandleInitialize(ctx, in)
			}
			n := f.takeEntries(len(input.Entries))
			if n == 0 {
				return next.HandleInitialize(ctx, in)
			}
			dropped := input.Entries[:n]
			trimmed := *input
			trimmed.Entries = input.Entries[n:]
			var out middleware.InitializeOutput
			result := &sqs.SendMessageBatchOutput{}
			if len(trimmed.Entries) > 0 {
				in.Parameters = &trimmed
				var md middleware.Metadata
				var err error
				out, md, err = next.HandleInitialize(ctx, in)
				if err != nil {
					return out, md, err
				}
				result, _ = out.Result.(*sqs.SendMessageBatchOutput)
			}
			for _, e := range dropped {
				result.Failed = append(result.Failed, sqstypes.BatchResultErrorEntry{
					Id: e.Id, Code: new("InjectedFailure"), SenderFault: false,
				})
			}
			out.Result = result
			return out, middleware.Metadata{}, nil
		}), middleware.After)
}

// Table creates the engine's single table (pk, sk) with its stream (NEW_IMAGE)
// on, as the stack declares it, and deletes it when the test ends.
func Table(t *testing.T, cfg aws.Config) string {
	t.Helper()
	client := dynamodb.NewFromConfig(cfg)
	name := "engine-test-" + uuid.New().String()
	_, err := client.CreateTable(t.Context(), &dynamodb.CreateTableInput{
		TableName:   new(name),
		BillingMode: ddbtypes.BillingModePayPerRequest,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: new("pk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: new("sk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: new("pk"), KeyType: ddbtypes.KeyTypeHash},
			{AttributeName: new("sk"), KeyType: ddbtypes.KeyTypeRange},
		},
		StreamSpecification: &ddbtypes.StreamSpecification{
			StreamEnabled:  new(true),
			StreamViewType: ddbtypes.StreamViewTypeNewImage,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = client.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: new(name)})
	})
	return name
}

// Rows counts every row in the table.
func Rows(t *testing.T, cfg aws.Config, table string) int {
	t.Helper()
	p := dynamodb.NewScanPaginator(dynamodb.NewFromConfig(cfg), &dynamodb.ScanInput{
		TableName: new(table), ConsistentRead: new(true),
	})
	n := 0
	for p.HasMorePages() {
		page, err := p.NextPage(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		n += len(page.Items)
	}
	return n
}

// Queue creates a queue and deletes it when the test ends. It returns the
// queue URL.
func Queue(t *testing.T, cfg aws.Config) string {
	t.Helper()
	client := sqs.NewFromConfig(cfg)
	out, err := client.CreateQueue(t.Context(), &sqs.CreateQueueInput{
		QueueName: new("engine-test-" + uuid.New().String()),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = client.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: out.QueueUrl})
	})
	return *out.QueueUrl
}

// Message is one received SQS message.
type Message struct {
	ID         string
	Body       string
	Attributes map[string]string
}

// Receive takes every message currently in the queue and deletes it, so a test
// delivers each one exactly when it chooses to.
func Receive(t *testing.T, cfg aws.Config, queueURL string) []Message {
	t.Helper()
	client := sqs.NewFromConfig(cfg)
	var out []Message
	for {
		page, err := client.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{
			QueueUrl:                    new(queueURL),
			MaxNumberOfMessages:         10,
			WaitTimeSeconds:             0,
			MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{sqstypes.MessageSystemAttributeNameAll},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Messages) == 0 {
			return out
		}
		for _, m := range page.Messages {
			out = append(out, Message{ID: aws.ToString(m.MessageId), Body: aws.ToString(m.Body), Attributes: m.Attributes})
			if _, err := client.DeleteMessage(t.Context(), &sqs.DeleteMessageInput{
				QueueUrl: new(queueURL), ReceiptHandle: m.ReceiptHandle,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// Decode unmarshals every message body into T.
func Decode[T any](t *testing.T, msgs []Message) []T {
	t.Helper()
	out := make([]T, len(msgs))
	for i, m := range msgs {
		if err := json.Unmarshal([]byte(m.Body), &out[i]); err != nil {
			t.Fatalf("message %s: %v", m.ID, err)
		}
	}
	return out
}

// Stream reads a table's stream from its start, the way the relay's event
// source mapping does, and hands each new record to the test as a Lambda
// event.
type Stream struct {
	t         *testing.T
	client    *dynamodbstreams.Client
	arn       string
	iterators map[string]*string
}

func TableStream(t *testing.T, cfg aws.Config, table string) *Stream {
	t.Helper()
	out, err := dynamodb.NewFromConfig(cfg).DescribeTable(t.Context(), &dynamodb.DescribeTableInput{TableName: new(table)})
	if err != nil {
		t.Fatal(err)
	}
	return &Stream{t: t, client: dynamodbstreams.NewFromConfig(cfg), arn: aws.ToString(out.Table.LatestStreamArn), iterators: map[string]*string{}}
}

// Next returns the records written since the last call, oldest first per shard.
func (s *Stream) Next() events.DynamoDBEvent {
	s.t.Helper()
	desc, err := s.client.DescribeStream(s.t.Context(), &dynamodbstreams.DescribeStreamInput{StreamArn: new(s.arn)})
	if err != nil {
		s.t.Fatal(err)
	}
	var ev events.DynamoDBEvent
	for _, shard := range desc.StreamDescription.Shards {
		id := aws.ToString(shard.ShardId)
		var records []events.DynamoDBEventRecord
		records, s.iterators[id] = s.read(s.iterator(id))
		ev.Records = append(ev.Records, records...)
	}
	return ev
}

// iterator is where the shard was left, or its start on the first read.
func (s *Stream) iterator(shardID string) *string {
	if it, ok := s.iterators[shardID]; ok {
		return it
	}
	out, err := s.client.GetShardIterator(s.t.Context(), &dynamodbstreams.GetShardIteratorInput{
		StreamArn: new(s.arn), ShardId: new(shardID), ShardIteratorType: streamtypes.ShardIteratorTypeTrimHorizon,
	})
	if err != nil {
		s.t.Fatal(err)
	}
	return out.ShardIterator
}

// read takes every record after it and returns the iterator to continue from.
func (s *Stream) read(it *string) ([]events.DynamoDBEventRecord, *string) {
	var records []events.DynamoDBEventRecord
	for it != nil {
		out, err := s.client.GetRecords(s.t.Context(), &dynamodbstreams.GetRecordsInput{ShardIterator: it})
		if err != nil {
			s.t.Fatal(err)
		}
		for _, r := range out.Records {
			records = append(records, s.record(r))
		}
		it = out.NextShardIterator
		if len(out.Records) == 0 {
			break
		}
	}
	return records, it
}

func (s *Stream) record(r streamtypes.Record) events.DynamoDBEventRecord {
	img := map[string]events.DynamoDBAttributeValue{}
	for k, v := range r.Dynamodb.NewImage {
		switch v := v.(type) {
		case *streamtypes.AttributeValueMemberS:
			img[k] = events.NewStringAttribute(v.Value)
		case *streamtypes.AttributeValueMemberN:
			img[k] = events.NewNumberAttribute(v.Value)
		default:
			s.t.Fatalf("stream attribute %s has an unhandled type %T", k, v)
		}
	}
	return events.DynamoDBEventRecord{
		EventID:   aws.ToString(r.EventID),
		EventName: string(r.EventName),
		Change: events.DynamoDBStreamRecord{
			NewImage:       img,
			SequenceNumber: aws.ToString(r.Dynamodb.SequenceNumber),
		},
	}
}
