package main

import (
	"os"

	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsapigatewayv2"
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

	table := awsdynamodb.NewTable(stack, jsii.String(tableID), &awsdynamodb.TableProps{
		PartitionKey: &awsdynamodb.Attribute{
			Name: jsii.String(tablePartitionKey),
			Type: awsdynamodb.AttributeType_STRING,
		},
		SortKey: &awsdynamodb.Attribute{
			Name: jsii.String(tableSortKey),
			Type: awsdynamodb.AttributeType_STRING,
		},
		BillingMode:   awsdynamodb.BillingMode_PAY_PER_REQUEST,
		Encryption:    awsdynamodb.TableEncryption_AWS_MANAGED,
		RemovalPolicy: awscdk.RemovalPolicy_DESTROY,
	})

	queue := awssqs.NewQueue(stack, jsii.String(queueID), &awssqs.QueueProps{
		VisibilityTimeout: awscdk.Duration_Seconds(jsii.Number(lambdaTimeoutS * 6)),
	})

	fnLogs := awslogs.NewLogGroup(stack, jsii.String("EvaluateLogs"), &awslogs.LogGroupProps{
		Retention:     awslogs.RetentionDays_TWO_WEEKS,
		RemovalPolicy: awscdk.RemovalPolicy_DESTROY,
	})
	fn := awscdklambdagoalpha.NewGoFunction(stack, jsii.String(functionID), &awscdklambdagoalpha.GoFunctionProps{
		Runtime:      awslambda.Runtime_PROVIDED_AL2023(),
		Architecture: awslambda.Architecture_ARM_64(),
		Entry:        jsii.String(lambdaEntry()),
		Timeout:      awscdk.Duration_Seconds(jsii.Number(lambdaTimeoutS)),
		MemorySize:   jsii.Number(lambdaMemoryMB),
		Tracing:      awslambda.Tracing_ACTIVE,
		LogGroup:     fnLogs,
		Environment: lambdaEnv(map[string]*string{
			"DECISIONS_TABLE": table.TableName(),
			"QUEUE_URL":       queue.QueueUrl(),
		}),
		Bundling: &awscdklambdagoalpha.BundlingOptions{
			GoBuildFlags: jsii.Strings(`-ldflags "-s -w"`),
		},
	})
	table.GrantReadWriteData(fn)
	queue.GrantSendMessages(fn)
	workerLogs := awslogs.NewLogGroup(stack, jsii.String("WorkerLogs"), &awslogs.LogGroupProps{
		Retention:     awslogs.RetentionDays_TWO_WEEKS,
		RemovalPolicy: awscdk.RemovalPolicy_DESTROY,
	})
	worker := awscdklambdagoalpha.NewGoFunction(stack, jsii.String(workerFunctionID), &awscdklambdagoalpha.GoFunctionProps{
		Runtime:      awslambda.Runtime_PROVIDED_AL2023(),
		Architecture: awslambda.Architecture_ARM_64(),
		Entry:        jsii.String(workerEntry()),
		Timeout:      awscdk.Duration_Seconds(jsii.Number(lambdaTimeoutS)),
		MemorySize:   jsii.Number(lambdaMemoryMB),
		Tracing:      awslambda.Tracing_ACTIVE,
		LogGroup:     workerLogs,
		Environment: lambdaEnv(map[string]*string{
			"DECISIONS_TABLE": table.TableName(),
		}),
		Bundling: &awscdklambdagoalpha.BundlingOptions{
			GoBuildFlags: jsii.Strings(`-ldflags "-s -w"`),
		},
	})
	table.GrantReadWriteData(worker)
	queue.GrantConsumeMessages(worker)
	worker.AddEventSource(awslambdaeventsources.NewSqsEventSource(queue, &awslambdaeventsources.SqsEventSourceProps{
		BatchSize: jsii.Number(10),
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
	for _, route := range []struct {
		path    string
		methods []awsapigatewayv2.HttpMethod
	}{
		{"/evaluations", []awsapigatewayv2.HttpMethod{awsapigatewayv2.HttpMethod_POST}},
		{"/evaluations/{id}", []awsapigatewayv2.HttpMethod{awsapigatewayv2.HttpMethod_GET}},
		{"/evaluations/batch", []awsapigatewayv2.HttpMethod{awsapigatewayv2.HttpMethod_POST}},
		{"/batches/{id}/report", []awsapigatewayv2.HttpMethod{awsapigatewayv2.HttpMethod_GET}},
		{"/health", []awsapigatewayv2.HttpMethod{awsapigatewayv2.HttpMethod_GET}},
	} {
		api.AddRoutes(&awsapigatewayv2.AddRoutesOptions{
			Path:        jsii.String(route.path),
			Methods:     &route.methods,
			Integration: integration,
		})
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

	wireAlarms(stack, fn)

	awscdk.NewCfnOutput(stack, jsii.String("ApiUrl"), &awscdk.CfnOutputProps{
		Value: stage.Url(),
	})
	awscdk.NewCfnOutput(stack, jsii.String("DecisionsTable"), &awscdk.CfnOutputProps{
		Value: table.TableName(),
	})
	awscdk.NewCfnOutput(stack, jsii.String("EvaluationQueueUrl"), &awscdk.CfnOutputProps{
		Value: queue.QueueUrl(),
	})
	return stack
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
