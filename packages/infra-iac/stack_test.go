package main

import (
	"testing"

	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/assertions"
	"github.com/aws/jsii-runtime-go"
)

func TestStackHasTheDayZeroSurface(t *testing.T) {
	t.Cleanup(jsii.Close)
	app := awscdk.NewApp(nil)
	stack := NewStack(app, "Test", nil)
	template := assertions.Template_FromStack(stack, nil)

	template.ResourceCountIs(jsii.String("AWS::DynamoDB::Table"), jsii.Number(1))
	template.ResourceCountIs(jsii.String("AWS::ApiGatewayV2::Api"), jsii.Number(1))
	template.ResourceCountIs(jsii.String("AWS::ApiGatewayV2::Stage"), jsii.Number(1))
	template.ResourceCountIs(jsii.String("AWS::Lambda::Function"), jsii.Number(2))
	template.ResourceCountIs(jsii.String("AWS::SQS::Queue"), jsii.Number(1))
	template.ResourceCountIs(jsii.String("AWS::CloudWatch::Alarm"), jsii.Number(2))

	template.HasResourceProperties(jsii.String("AWS::DynamoDB::Table"), map[string]any{
		"BillingMode": "PAY_PER_REQUEST",
		"KeySchema": []any{
			map[string]any{"AttributeName": "report_id", "KeyType": "HASH"},
			map[string]any{"AttributeName": "sk", "KeyType": "RANGE"},
		},
		"SSESpecification": map[string]any{
			"SSEEnabled": true,
		},
	})
	template.HasResourceProperties(jsii.String("AWS::Lambda::Function"), map[string]any{
		"Architectures": []any{"arm64"},
		"Timeout":       3,
		"MemorySize":    256,
		"TracingConfig": map[string]any{"Mode": "Active"},
	})
	template.HasResourceProperties(jsii.String("AWS::ApiGatewayV2::Stage"), map[string]any{
		"DefaultRouteSettings": map[string]any{
			"ThrottlingRateLimit":  1200,
			"ThrottlingBurstLimit": 2400,
		},
	})
}

func TestLambdaEntriesPointAtApps(t *testing.T) {
	if got := lambdaEntry(); got == "" {
		t.Fatal("empty evaluate entry")
	}
	if got := workerEntry(); got == "" {
		t.Fatal("empty worker entry")
	}
}
