package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsapigatewayv2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsapigatewayv2authorizers"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsapigatewayv2integrations"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsdynamodb"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslambda"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslambdaeventsources"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslogs"
	"github.com/aws/aws-cdk-go/awscdk/v2/awssqs"
	"github.com/aws/aws-cdk-go/awscdklambdagoalpha/v2"
	"github.com/aws/constructs-go/constructs/v10"
	"github.com/aws/jsii-runtime-go"
)

type stackProps struct {
	awscdk.StackProps
}

func NewStack(scope constructs.Construct, id string, props *stackProps) awscdk.Stack {
	var sprops awscdk.StackProps
	if props != nil {
		sprops = props.StackProps
	}
	stack := awscdk.NewStack(scope, &id, &sprops)
	size, err := batchSize()
	if err != nil {
		panic(err)
	}

	table := awsdynamodb.NewTable(stack, jsii.String(tableID), &awsdynamodb.TableProps{
		PartitionKey: &awsdynamodb.Attribute{
			Name: jsii.String(tablePartitionKey),
			Type: awsdynamodb.AttributeType_STRING,
		},
		SortKey: &awsdynamodb.Attribute{
			Name: jsii.String(tableSortKey),
			Type: awsdynamodb.AttributeType_STRING,
		},
		BillingMode: awsdynamodb.BillingMode_PAY_PER_REQUEST,
		Encryption:  awsdynamodb.TableEncryption_AWS_MANAGED,
		Stream:      awsdynamodb.StreamViewType_NEW_IMAGE,
		// Continuous backups: restore the table to any second of the last 35
		// days after a bad write or an operator error.
		PointInTimeRecoverySpecification: &awsdynamodb.PointInTimeRecoverySpecification{
			PointInTimeRecoveryEnabled: jsii.Bool(true),
		},
		RemovalPolicy: awscdk.RemovalPolicy_DESTROY,
	})

	// Both queues declare SSE-SQS at rest and deny any request not over TLS.
	dlq := awssqs.NewQueue(stack, jsii.String(dlqID), &awssqs.QueueProps{
		RetentionPeriod: awscdk.Duration_Days(jsii.Number(dlqRetentionDays)),
		Encryption:      awssqs.QueueEncryption_SQS_MANAGED,
		EnforceSSL:      jsii.Bool(true),
	})
	queue := awssqs.NewQueue(stack, jsii.String(queueID), &awssqs.QueueProps{
		Encryption: awssqs.QueueEncryption_SQS_MANAGED,
		EnforceSSL: jsii.Bool(true),
		// AWS: six times the function timeout, plus the batching window.
		VisibilityTimeout: awscdk.Duration_Seconds(jsii.Number(lambdaTimeoutS*6 + workerBatchingWindow)),
		DeadLetterQueue: &awssqs.DeadLetterQueue{
			Queue:           dlq,
			MaxReceiveCount: jsii.Number(maxReceiveCount),
		},
	})

	// The HTTP Lambda only writes items: it has no access to the queue.
	fn := goLambda(stack, functionID, "EvaluateLogs", "http", map[string]*string{
		"DECISIONS_TABLE": table.TableName(),
		"BATCH_SIZE":      jsii.String(size),
	}, flociEvaluateConcurrency)
	table.GrantReadWriteData(fn)

	// The relay turns the table's stream into queue messages (ADR 0004). The
	// event source grants the stream read; the relay reads no rows.
	relay := goLambda(stack, relayFunctionID, "RelayLogs", "relay", map[string]*string{
		"QUEUE_URL": queue.QueueUrl(),
	}, flociRelayConcurrency)
	queue.GrantSendMessages(relay)
	relay.AddEventSource(awslambdaeventsources.NewDynamoEventSource(table, &awslambdaeventsources.DynamoEventSourceProps{
		StartingPosition:        awslambda.StartingPosition_TRIM_HORIZON,
		BatchSize:               jsii.Number(streamBatchSize),
		BisectBatchOnError:      jsii.Bool(true),
		ReportBatchItemFailures: jsii.Bool(true),
	}))
	worker := goLambda(stack, workerFunctionID, "WorkerLogs", "worker", map[string]*string{
		"DECISIONS_TABLE": table.TableName(),
	}, flociWorkerConcurrency)
	table.GrantReadWriteData(worker)
	queue.GrantConsumeMessages(worker)
	worker.AddEventSource(awslambdaeventsources.NewSqsEventSource(queue, &awslambdaeventsources.SqsEventSourceProps{
		BatchSize:               jsii.Number(workerBatch()),
		MaxBatchingWindow:       awscdk.Duration_Seconds(jsii.Number(workerBatchingWindow)),
		ReportBatchItemFailures: jsii.Bool(true),
	}))

	dlqConsumer := goLambda(stack, dlqFunctionID, "DlqConsumerLogs", "dlq", map[string]*string{
		"DECISIONS_TABLE": table.TableName(),
	}, flociDlqConcurrency)
	table.GrantReadWriteData(dlqConsumer)
	// The event source grants consume on the DLQ, and nothing else on SQS.
	dlqConsumer.AddEventSource(awslambdaeventsources.NewSqsEventSource(dlq, &awslambdaeventsources.SqsEventSourceProps{
		BatchSize:               jsii.Number(sqsBatchSize),
		ReportBatchItemFailures: jsii.Bool(true),
	}))

	api := awsapigatewayv2.NewHttpApi(stack, jsii.String(apiID), &awsapigatewayv2.HttpApiProps{
		ApiName:            jsii.String("credit-card-engine"),
		CreateDefaultStage: jsii.Bool(false),
	})
	integration := awsapigatewayv2integrations.NewHttpLambdaIntegration(
		jsii.String("EvaluateIntegration"),
		fn,
		&awsapigatewayv2integrations.HttpLambdaIntegrationProps{},
	)
	authorizer := awsapigatewayv2authorizers.NewHttpIamAuthorizer()
	for _, route := range []struct {
		path    string
		methods []awsapigatewayv2.HttpMethod
	}{
		{"/evaluations", []awsapigatewayv2.HttpMethod{awsapigatewayv2.HttpMethod_POST}},
		{"/evaluations/{id}", []awsapigatewayv2.HttpMethod{awsapigatewayv2.HttpMethod_GET}},
		{"/evaluations/batch", []awsapigatewayv2.HttpMethod{awsapigatewayv2.HttpMethod_POST}},
		{"/batches/{id}/items", []awsapigatewayv2.HttpMethod{awsapigatewayv2.HttpMethod_GET}},
		{"/batches/{id}/items/{item_id}/retry", []awsapigatewayv2.HttpMethod{awsapigatewayv2.HttpMethod_POST}},
		{"/batches/{id}/items/{item_id}/cancel", []awsapigatewayv2.HttpMethod{awsapigatewayv2.HttpMethod_POST}},
		{"/batches/{id}/retry-failed", []awsapigatewayv2.HttpMethod{awsapigatewayv2.HttpMethod_POST}},
		{"/health", []awsapigatewayv2.HttpMethod{awsapigatewayv2.HttpMethod_GET}},
	} {
		opts := &awsapigatewayv2.AddRoutesOptions{
			Path:        jsii.String(route.path),
			Methods:     &route.methods,
			Integration: integration,
		}
		if route.path != "/health" {
			opts.Authorizer = authorizer
		}
		api.AddRoutes(opts)
	}

	stage := awsapigatewayv2.NewHttpStage(stack, jsii.String("Default"), &awsapigatewayv2.HttpStageProps{
		HttpApi:    api,
		StageName:  jsii.String("$default"),
		AutoDeploy: jsii.Bool(true),
		Throttle: &awsapigatewayv2.ThrottleSettings{
			RateLimit:  jsii.Number(stageRateLimit),
			BurstLimit: jsii.Number(stageBurstLimit),
		},
	})

	wireObservability(stack, fn, worker, dlqConsumer, relay, api, queue, dlq)

	awscdk.NewCfnOutput(stack, jsii.String("ApiUrl"), &awscdk.CfnOutputProps{
		Value: stage.Url(),
	})
	awscdk.NewCfnOutput(stack, jsii.String("DecisionsTable"), &awscdk.CfnOutputProps{
		Value: table.TableName(),
	})
	awscdk.NewCfnOutput(stack, jsii.String("EvaluationQueueUrl"), &awscdk.CfnOutputProps{
		Value: queue.QueueUrl(),
	})
	awscdk.NewCfnOutput(stack, jsii.String("EvaluationDlqUrl"), &awscdk.CfnOutputProps{
		Value: dlq.QueueUrl(),
	})
	return stack
}

// batchSize reads BATCH_SIZE at synth time so an out-of-range value fails
// the deploy instead of the Lambda's cold start.
func batchSize() (string, error) {
	s := os.Getenv("BATCH_SIZE")
	if s == "" {
		return strconv.Itoa(defaultBatchSize), nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > maxBatchSize {
		return "", fmt.Errorf("BATCH_SIZE must be an integer in 1..%d, got %q", maxBatchSize, s)
	}
	return strconv.Itoa(n), nil
}

// lambdaEnv points the Lambdas at Floci when synthesising for it. Floci runs
// each invocation in its own container on the compose network, so the
// endpoint must be the floci service name, not localhost.
func lambdaEnv(env map[string]*string) *map[string]*string {
	if os.Getenv("AWS_ENDPOINT_URL") == "" {
		return &env
	}
	endpoint := os.Getenv("LAMBDA_AWS_ENDPOINT_URL")
	if endpoint == "" {
		endpoint = "http://floci:4566"
	}
	env["AWS_ENDPOINT_URL"] = jsii.String(endpoint)
	return &env
}

// goLambda is one engine binary from apps/engine/cmd/<cmd>: arm64, traced,
// with its own two-week log group.
func goLambda(stack awscdk.Stack, id, logsID, cmd string, env map[string]*string, flociConcurrency float64) awscdklambdagoalpha.GoFunction {
	logs := awslogs.NewLogGroup(stack, jsii.String(logsID), &awslogs.LogGroupProps{
		Retention:     awslogs.RetentionDays_TWO_WEEKS,
		RemovalPolicy: awscdk.RemovalPolicy_DESTROY,
	})
	return awscdklambdagoalpha.NewGoFunction(stack, jsii.String(id), &awscdklambdagoalpha.GoFunctionProps{
		Runtime:      awslambda.Runtime_PROVIDED_AL2023(),
		Architecture: awslambda.Architecture_ARM_64(),
		Entry:        jsii.String(cmdEntry(cmd)),
		Timeout:      awscdk.Duration_Seconds(jsii.Number(lambdaTimeoutS)),
		MemorySize:   jsii.Number(lambdaMemoryMB),
		Tracing:      awslambda.Tracing_ACTIVE,
		LogGroup:     logs,
		Environment:  lambdaEnv(env),
		Bundling: &awscdklambdagoalpha.BundlingOptions{
			GoBuildFlags: jsii.Strings(`-ldflags "-s -w"`),
		},
		ReservedConcurrentExecutions: flociReservedConcurrency(flociConcurrency),
	})
}

// workerBatch is the worker's SQS batch size. Floci's poller receives one
// message per poll when the batch is over the 10 of one ReceiveMessage call, so
// Floci keeps 10; AWS fills the batch across calls within the window.
func workerBatch() float64 {
	if os.Getenv("AWS_ENDPOINT_URL") == "" {
		return workerBatchSize
	}
	return sqsBatchSize
}

// flociReservedConcurrency caps in-flight containers on Floci. Real AWS
// keeps unreserved concurrency so the NFR run can scale.
func flociReservedConcurrency(n float64) *float64 {
	if os.Getenv("AWS_ENDPOINT_URL") == "" {
		return nil
	}
	return jsii.Number(n)
}
