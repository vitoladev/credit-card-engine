package main

import (
	"reflect"
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
			map[string]any{"AttributeName": "pk", "KeyType": "HASH"},
			map[string]any{"AttributeName": "sk", "KeyType": "RANGE"},
		},
		"SSESpecification": map[string]any{
			"SSEEnabled": true,
		},
	})
	routes := []string{
		"POST /evaluations",
		"GET /evaluations/{id}",
		"POST /evaluations/batch",
		"GET /batches/{id}/report",
		"GET /health",
	}
	template.ResourceCountIs(jsii.String("AWS::ApiGatewayV2::Route"), jsii.Number(len(routes)))
	for _, key := range routes {
		template.HasResourceProperties(jsii.String("AWS::ApiGatewayV2::Route"), map[string]any{"RouteKey": key})
	}
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

func lambdaEndpoints(t *testing.T) []string {
	t.Helper()
	app := awscdk.NewApp(nil)
	template := assertions.Template_FromStack(NewStack(app, "Test", nil), nil)
	fns := template.FindResources(jsii.String("AWS::Lambda::Function"), nil)
	var got []string
	for _, fn := range *fns {
		vars := (*fn)["Properties"].(map[string]any)["Environment"].(map[string]any)["Variables"].(map[string]any)
		if v, ok := vars["AWS_ENDPOINT_URL"]; ok {
			got = append(got, v.(string))
		}
	}
	return got
}

// Floci runs each Lambda in its own container on the compose network, where
// localhost is the Lambda container itself.
func TestLambdaEndpointReachesFloci(t *testing.T) {
	t.Cleanup(jsii.Close)
	for _, tc := range []struct {
		name, endpoint, override string
		want                     []string
	}{
		{"real AWS", "", "", nil},
		{"floci default", "http://localhost:4566", "", []string{"http://floci:4566", "http://floci:4566"}},
		{"override", "http://floci:4566", "http://other:4566", []string{"http://other:4566", "http://other:4566"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AWS_ENDPOINT_URL", tc.endpoint)
			t.Setenv("LAMBDA_AWS_ENDPOINT_URL", tc.override)
			if got := lambdaEndpoints(t); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func httpLambdaBatchSize(t *testing.T) any {
	t.Helper()
	app := awscdk.NewApp(nil)
	template := assertions.Template_FromStack(NewStack(app, "Test", nil), nil)
	var got []any
	for _, fn := range *template.FindResources(jsii.String("AWS::Lambda::Function"), nil) {
		vars := (*fn)["Properties"].(map[string]any)["Environment"].(map[string]any)["Variables"].(map[string]any)
		if v, ok := vars["BATCH_SIZE"]; ok {
			got = append(got, v)
		}
	}
	if len(got) != 1 {
		t.Fatalf("BATCH_SIZE on %d Lambdas, want only the HTTP one", len(got))
	}
	return got[0]
}

func TestHTTPLambdaGetsBatchSize(t *testing.T) {
	t.Cleanup(jsii.Close)
	for _, tc := range []struct{ env, want string }{
		{"", "100"},
		{"1000", "1000"},
	} {
		t.Run("BATCH_SIZE="+tc.env, func(t *testing.T) {
			t.Setenv("BATCH_SIZE", tc.env)
			if got := httpLambdaBatchSize(t); got != tc.want {
				t.Fatalf("got %v want %s", got, tc.want)
			}
		})
	}
}

func TestBatchSizeOutOfRangeFailsSynth(t *testing.T) {
	for _, env := range []string{"0", "1001", "abc"} {
		t.Setenv("BATCH_SIZE", env)
		if got, err := batchSize(); err == nil {
			t.Fatalf("BATCH_SIZE=%q gave %s, want an error", env, got)
		}
	}
}
