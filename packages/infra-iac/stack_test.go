package main

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
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

	template.ResourceCountIs(jsii.String("AWS::DynamoDB::Table"), jsii.Number(3))
	template.ResourceCountIs(jsii.String("AWS::ApiGatewayV2::Api"), jsii.Number(1))
	template.ResourceCountIs(jsii.String("AWS::ApiGatewayV2::Stage"), jsii.Number(1))
	template.ResourceCountIs(jsii.String("AWS::Lambda::Function"), jsii.Number(4))
	template.ResourceCountIs(jsii.String("AWS::SQS::Queue"), jsii.Number(2))
	template.ResourceCountIs(jsii.String("AWS::CloudWatch::Alarm"), jsii.Number(11))
	// Logged-and-acknowledged failures alarm through metric filters.
	template.ResourceCountIs(jsii.String("AWS::Logs::MetricFilter"), jsii.Number(2))
	template.HasResourceProperties(jsii.String("AWS::Logs::MetricFilter"), map[string]any{
		"FilterPattern": `{ ($.msg = "stream_record_skipped") }`,
	})
	template.HasResourceProperties(jsii.String("AWS::Logs::MetricFilter"), map[string]any{
		"FilterPattern": `{ ($.msg = "batch_rollback_failed") || ($.msg = "idempotency_complete_failed") || ($.msg = "idempotency_release_failed") }`,
	})

	// ADR 0006: one table per port, each with its own natural key. Only the
	// batch items stream, only the idempotency keys expire, and the audit
	// tables keep point-in-time recovery.
	hash := func(name string) map[string]any { return map[string]any{"AttributeName": name, "KeyType": "HASH"} }
	for _, table := range []map[string]any{
		{
			"KeySchema":                        []any{hash("decision_id")},
			"StreamSpecification":              assertions.Match_Absent(),
			"TimeToLiveSpecification":          assertions.Match_Absent(),
			"PointInTimeRecoverySpecification": map[string]any{"PointInTimeRecoveryEnabled": true},
		},
		{
			"KeySchema":           []any{hash("batch_id"), map[string]any{"AttributeName": "item_id", "KeyType": "RANGE"}},
			"StreamSpecification": map[string]any{"StreamViewType": "NEW_IMAGE"},
			// ADR 0007: a list by status reads only that status's items.
			"GlobalSecondaryIndexes": []any{map[string]any{
				"IndexName":  statusIndexName,
				"KeySchema":  []any{hash("batch_status"), map[string]any{"AttributeName": "item_id", "KeyType": "RANGE"}},
				"Projection": map[string]any{"ProjectionType": "ALL"},
			}},
			"TimeToLiveSpecification":          assertions.Match_Absent(),
			"PointInTimeRecoverySpecification": map[string]any{"PointInTimeRecoveryEnabled": true},
		},
		{
			"KeySchema":               []any{hash("idempotency_key")},
			"StreamSpecification":     assertions.Match_Absent(),
			"TimeToLiveSpecification": map[string]any{"AttributeName": "expires_at", "Enabled": true},
		},
	} {
		table["BillingMode"] = "PAY_PER_REQUEST"
		table["SSESpecification"] = map[string]any{"SSEEnabled": true}
		template.HasResourceProperties(jsii.String("AWS::DynamoDB::Table"), table)
	}
	routes := []string{
		"POST /evaluations",
		"GET /evaluations/{id}",
		"POST /evaluations/batch",
		"GET /batches/{id}/items",
		"POST /batches/{id}/items/{item_id}/retry",
		"POST /batches/{id}/items/{item_id}/cancel",
		"POST /batches/{id}/retry-failed",
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

// ADR 0004: the batch items' stream feeds the relay, and the relay is the only
// Lambda that may send to the queue.
func TestRelayIsTheOutboxOfTheTableStream(t *testing.T) {
	t.Cleanup(jsii.Close)
	app := awscdk.NewApp(nil)
	template := assertions.Template_FromStack(NewStack(app, "Test", nil), nil)

	template.HasResourceProperties(jsii.String("AWS::DynamoDB::Table"), map[string]any{
		"StreamSpecification": map[string]any{"StreamViewType": "NEW_IMAGE"},
	})
	template.HasResourceProperties(jsii.String("AWS::Lambda::EventSourceMapping"), map[string]any{
		"StartingPosition":               "TRIM_HORIZON",
		"BatchSize":                      streamBatchSize,
		"MaximumBatchingWindowInSeconds": relayBatchingWindow,
		"BisectBatchOnFunctionError":     true,
		"FunctionResponseTypes":          []any{"ReportBatchItemFailures"},
		"FunctionName":                   map[string]any{"Ref": assertions.Match_StringLikeRegexp(jsii.String(relayFunctionID))},
	})
	template.HasResourceProperties(jsii.String("AWS::CloudWatch::Alarm"), map[string]any{
		"MetricName": "IteratorAge",
		"Threshold":  iteratorAgeAlarmMs,
	})

	var senders []string
	for _, p := range *template.FindResources(jsii.String("AWS::IAM::Policy"), nil) {
		props := (*p)["Properties"].(map[string]any)
		for _, st := range props["PolicyDocument"].(map[string]any)["Statement"].([]any) {
			for _, a := range resources(st.(map[string]any)["Action"]) {
				if a == "sqs:SendMessage" {
					for _, r := range props["Roles"].([]any) {
						senders = append(senders, r.(map[string]any)["Ref"].(string))
					}
				}
			}
		}
	}
	if len(senders) != 1 || !strings.HasPrefix(senders[0], relayFunctionID) {
		t.Fatalf("roles that may send to the queue: %v", senders)
	}
}

// Data at rest and in transit, backups, and the batch SLO alarms.
func TestStackProtectsDataAndWatchesTheBatchSLO(t *testing.T) {
	t.Cleanup(jsii.Close)
	app := awscdk.NewApp(nil)
	template := assertions.Template_FromStack(NewStack(app, "Test", nil), nil)

	for _, q := range *template.FindResources(jsii.String("AWS::SQS::Queue"), nil) {
		if (*q)["Properties"].(map[string]any)["SqsManagedSseEnabled"] != true {
			t.Fatalf("queue without SSE-SQS: %v", *q)
		}
	}
	template.ResourceCountIs(jsii.String("AWS::SQS::QueuePolicy"), jsii.Number(2))
	template.HasResourceProperties(jsii.String("AWS::SQS::QueuePolicy"), map[string]any{
		"PolicyDocument": map[string]any{"Statement": assertions.Match_ArrayWith(&[]any{assertions.Match_ObjectLike(&map[string]any{
			"Effect":    "Deny",
			"Condition": map[string]any{"Bool": map[string]any{"aws:SecureTransport": "false"}},
		})})},
	})
	template.HasResourceProperties(jsii.String("AWS::CloudWatch::Alarm"), map[string]any{
		"MetricName": "ApproximateAgeOfOldestMessage",
		"Threshold":  queueAgeAlarmS,
	})
	template.HasResourceProperties(jsii.String("AWS::CloudWatch::Alarm"), map[string]any{
		"MetricName":        "ItemEndToEndMs",
		"Namespace":         metricsNamespace,
		"ExtendedStatistic": "p99",
		"Threshold":         itemEndToEndAlarmMs,
	})
}

func TestStackHasTheDLQAndItsConsumer(t *testing.T) {
	t.Cleanup(jsii.Close)
	app := awscdk.NewApp(nil)
	stack := NewStack(app, "Test", nil)
	template := assertions.Template_FromStack(stack, nil)

	dlq := stack.GetLogicalId(stack.Node().FindChild(jsii.String(dlqID)).Node().DefaultChild().(awscdk.CfnElement))
	queue := stack.GetLogicalId(stack.Node().FindChild(jsii.String(queueID)).Node().DefaultChild().(awscdk.CfnElement))
	consumer := stack.GetLogicalId(stack.Node().FindChild(jsii.String(dlqFunctionID)).Node().DefaultChild().(awscdk.CfnElement))
	worker := stack.GetLogicalId(stack.Node().FindChild(jsii.String(workerFunctionID)).Node().DefaultChild().(awscdk.CfnElement))
	items := stack.GetLogicalId(stack.Node().FindChild(jsii.String(itemsTableID)).Node().DefaultChild().(awscdk.CfnElement))

	template.HasResourceProperties(jsii.String("AWS::SQS::Queue"), map[string]any{
		"MessageRetentionPeriod": 14 * 24 * 60 * 60,
	})
	template.HasResourceProperties(jsii.String("AWS::SQS::Queue"), map[string]any{
		"RedrivePolicy": map[string]any{
			"deadLetterTargetArn": map[string]any{"Fn::GetAtt": []any{*dlq, "Arn"}},
			"maxReceiveCount":     maxReceiveCount,
		},
	})
	for _, source := range []struct {
		queue, fn *string
		batch     int
		window    any
	}{{queue, worker, int(workerBatch()), workerBatchingWindow}, {dlq, consumer, sqsBatchSize, assertions.Match_Absent()}} {
		template.HasResourceProperties(jsii.String("AWS::Lambda::EventSourceMapping"), map[string]any{
			"EventSourceArn":                 map[string]any{"Fn::GetAtt": []any{*source.queue, "Arn"}},
			"FunctionName":                   map[string]any{"Ref": *source.fn},
			"BatchSize":                      source.batch,
			"MaximumBatchingWindowInSeconds": source.window,
			"FunctionResponseTypes":          []any{"ReportBatchItemFailures"},
		})
	}
	template.HasResourceProperties(jsii.String("AWS::Lambda::Function"), map[string]any{
		"Runtime":       "provided.al2023",
		"Architectures": []any{"arm64"},
		"Timeout":       3,
		"MemorySize":    256,
		"TracingConfig": map[string]any{"Mode": "Active"},
		"Environment": map[string]any{"Variables": map[string]any{
			"BATCH_ITEMS_TABLE": map[string]any{"Ref": assertions.Match_AnyValue()},
		}},
		"LoggingConfig": map[string]any{"LogGroup": map[string]any{"Ref": assertions.Match_StringLikeRegexp(jsii.String("DlqConsumerLogs"))}},
	})
	template.HasResourceProperties(jsii.String("AWS::Logs::LogGroup"), map[string]any{
		"RetentionInDays": 14,
	})

	// Least privilege: past the X-Ray grant that active tracing adds, the
	// worker touches only the batch items and its queue, and the DLQ consumer
	// only the batch items and the DLQ.
	onlyResources(t, template, *worker, *items, *queue)
	onlyResources(t, template, *consumer, *items, *dlq)

	// And only the DynamoDB actions their code calls.
	fnID := func(id string) string {
		return *stack.GetLogicalId(stack.Node().FindChild(jsii.String(id)).Node().DefaultChild().(awscdk.CfnElement))
	}
	for fn, want := range map[string][]string{
		fnID(functionID):       union(httpDecisionActions, httpItemActions, httpKeyActions),
		fnID(workerFunctionID): workerItemActions,
		fnID(dlqFunctionID):    dlqItemActions,
		fnID(relayFunctionID):  {"dynamodb:DescribeStream", "dynamodb:GetRecords", "dynamodb:GetShardIterator", "dynamodb:ListStreams"},
	} {
		if got := dynamoActions(template, fn); !slices.Equal(got, union(want)) {
			t.Fatalf("%s DynamoDB actions %v, want %v", fn, got, union(want))
		}
	}
}

// dynamoActions lists, sorted and once each, the dynamodb: actions of the
// function's role.
func dynamoActions(template assertions.Template, fn string) []string {
	role := (*template.FindResources(jsii.String("AWS::Lambda::Function"), map[string]any{}))[fn]
	roleRef := (*role)["Properties"].(map[string]any)["Role"].(map[string]any)["Fn::GetAtt"].([]any)[0].(string)
	var got []string
	for _, p := range *template.FindResources(jsii.String("AWS::IAM::Policy"), nil) {
		props := (*p)["Properties"].(map[string]any)
		mine := slices.ContainsFunc(props["Roles"].([]any), func(r any) bool { return r.(map[string]any)["Ref"] == roleRef })
		if !mine {
			continue
		}
		for _, st := range props["PolicyDocument"].(map[string]any)["Statement"].([]any) {
			for _, a := range resources(st.(map[string]any)["Action"]) {
				if s, _ := a.(string); strings.HasPrefix(s, "dynamodb:") {
					got = append(got, s)
				}
			}
		}
	}
	return union(got)
}

// union merges the lists, sorted and without repeats.
func union(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		out = append(out, l...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// onlyResources fails unless every statement of the function's role, past
// X-Ray, is on one of the allowed resources and none may send to a queue.
func onlyResources(t *testing.T, template assertions.Template, fn string, allowed ...string) {
	t.Helper()
	role := (*template.FindResources(jsii.String("AWS::Lambda::Function"), map[string]any{}))[fn]
	roleRef := (*role)["Properties"].(map[string]any)["Role"].(map[string]any)["Fn::GetAtt"].([]any)[0].(string)
	var policies []map[string]any
	for _, p := range *template.FindResources(jsii.String("AWS::IAM::Policy"), nil) {
		for _, r := range (*p)["Properties"].(map[string]any)["Roles"].([]any) {
			if r.(map[string]any)["Ref"] == roleRef {
				policies = append(policies, *p)
			}
		}
	}
	if len(policies) != 1 {
		t.Fatalf("%s role has %d policies, want 1", fn, len(policies))
	}
	for _, st := range policies[0]["Properties"].(map[string]any)["PolicyDocument"].(map[string]any)["Statement"].([]any) {
		stmt := st.(map[string]any)
		if onlyXRay(resources(stmt["Action"])) {
			continue
		}
		for _, res := range resources(stmt["Resource"]) {
			if !slices.ContainsFunc(allowed, func(a string) bool { return refersTo(res, a) }) {
				t.Fatalf("%s statement on %v: %v", fn, res, stmt)
			}
		}
		for _, a := range resources(stmt["Action"]) {
			if s, _ := a.(string); s == "sqs:SendMessage" || s == "*" {
				t.Fatalf("%s may %s", fn, s)
			}
		}
	}
}

func onlyXRay(actions []any) bool {
	for _, a := range actions {
		if s, _ := a.(string); !strings.HasPrefix(s, "xray:") {
			return false
		}
	}
	return true
}

func resources(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return []any{v}
}

// refersTo reports whether a policy resource (a Ref, a GetAtt, or a Join
// around one) names the logical id.
func refersTo(res any, logicalID string) bool {
	switch v := res.(type) {
	case map[string]any:
		for k, inner := range v {
			if k == "Ref" && inner == logicalID {
				return true
			}
			if k == "Fn::GetAtt" && inner.([]any)[0] == logicalID {
				return true
			}
			if refersTo(inner, logicalID) {
				return true
			}
		}
	case []any:
		for _, inner := range v {
			if refersTo(inner, logicalID) {
				return true
			}
		}
	}
	return false
}

func TestStackHasIAMAuthorizerDashboardAndAlarms(t *testing.T) {
	t.Cleanup(jsii.Close)
	app := awscdk.NewApp(nil)
	stack := NewStack(app, "Test", nil)
	template := assertions.Template_FromStack(stack, nil)

	protected := []string{
		"POST /evaluations",
		"GET /evaluations/{id}",
		"POST /evaluations/batch",
		"GET /batches/{id}/items",
		"POST /batches/{id}/items/{item_id}/retry",
		"POST /batches/{id}/items/{item_id}/cancel",
		"POST /batches/{id}/retry-failed",
	}
	for _, key := range protected {
		template.HasResourceProperties(jsii.String("AWS::ApiGatewayV2::Route"), map[string]any{
			"RouteKey":          key,
			"AuthorizationType": "AWS_IAM",
		})
	}
	template.HasResourceProperties(jsii.String("AWS::ApiGatewayV2::Route"), map[string]any{
		"RouteKey":          "GET /health",
		"AuthorizationType": "NONE",
	})

	template.ResourceCountIs(jsii.String("AWS::CloudWatch::Dashboard"), jsii.Number(1))
	template.HasResourceProperties(jsii.String("AWS::CloudWatch::Dashboard"), map[string]any{
		"DashboardName": dashboardName,
	})

	template.HasResourceProperties(jsii.String("AWS::CloudWatch::Alarm"), map[string]any{
		"Threshold":         api5xxAlarmThreshold,
		"EvaluationPeriods": 1,
		"TreatMissingData":  "notBreaching",
		"Namespace":         "AWS/ApiGateway",
		"MetricName":        "5xx",
	})
	template.HasResourceProperties(jsii.String("AWS::CloudWatch::Alarm"), map[string]any{
		"Threshold":         workerErrorAlarmThreshold,
		"EvaluationPeriods": 1,
		"TreatMissingData":  "notBreaching",
		"Namespace":         "AWS/Lambda",
		"MetricName":        "Errors",
		"Dimensions": []any{
			map[string]any{"Name": "FunctionName", "Value": map[string]any{"Ref": assertions.Match_StringLikeRegexp(jsii.String(workerFunctionID))}},
		},
	})
	template.HasResourceProperties(jsii.String("AWS::CloudWatch::Alarm"), map[string]any{
		"Threshold":         dlqConsumerErrorAlarmThreshold,
		"EvaluationPeriods": 1,
		"TreatMissingData":  "notBreaching",
		"Namespace":         "AWS/Lambda",
		"MetricName":        "Errors",
		"Dimensions": []any{
			map[string]any{"Name": "FunctionName", "Value": map[string]any{"Ref": assertions.Match_StringLikeRegexp(jsii.String(dlqFunctionID))}},
		},
	})
	template.HasResourceProperties(jsii.String("AWS::CloudWatch::Alarm"), map[string]any{
		"Threshold":          dlqVisibleAlarmThreshold,
		"EvaluationPeriods":  dlqVisibleEvaluationPeriods,
		"ComparisonOperator": "GreaterThanThreshold",
		"TreatMissingData":   "notBreaching",
		"Namespace":          "AWS/SQS",
		"MetricName":         "ApproximateNumberOfMessagesVisible",
	})
	template.HasResourceProperties(jsii.String("AWS::CloudWatch::Alarm"), map[string]any{
		"Threshold":         latencyAlarmMs,
		"EvaluationPeriods": 3,
		"TreatMissingData":  "notBreaching",
		"Namespace":         "AWS/Lambda",
		"MetricName":        "Duration",
		"ExtendedStatistic": "p99",
		"Dimensions": []any{
			map[string]any{"Name": "FunctionName", "Value": map[string]any{"Ref": assertions.Match_StringLikeRegexp(jsii.String(functionID))}},
		},
	})
}

func TestLambdaEntriesPointAtApps(t *testing.T) {
	for _, cmd := range []string{"http", "worker", "dlq"} {
		if _, err := os.Stat(filepath.Join(cmdEntry(cmd), "main.go")); err != nil {
			t.Errorf("entry %s: %v", cmd, err)
		}
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
		{"floci default", "http://localhost:4566", "", []string{"http://floci:4566", "http://floci:4566", "http://floci:4566", "http://floci:4566"}},
		{"override", "http://floci:4566", "http://other:4566", []string{"http://other:4566", "http://other:4566", "http://other:4566", "http://other:4566"}},
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

func TestFlociCapsLambdaConcurrency(t *testing.T) {
	t.Cleanup(jsii.Close)
	t.Setenv("AWS_ENDPOINT_URL", "http://localhost:4566")
	got := reservedConcurrency(t)
	want := map[string]any{
		functionID:       float64(flociEvaluateConcurrency),
		workerFunctionID: float64(flociWorkerConcurrency),
		dlqFunctionID:    float64(flociDlqConcurrency),
		relayFunctionID:  float64(flociRelayConcurrency),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestWorkerBatchIs50OnAWSAnd10OnFloci(t *testing.T) {
	t.Cleanup(jsii.Close)
	for _, tc := range []struct {
		endpoint string
		want     int
	}{{"", workerBatchSize}, {"http://localhost:4566", sqsBatchSize}} {
		t.Setenv("AWS_ENDPOINT_URL", tc.endpoint)
		template := assertions.Template_FromStack(NewStack(awscdk.NewApp(nil), "Test", nil), nil)
		template.HasResourceProperties(jsii.String("AWS::Lambda::EventSourceMapping"), map[string]any{
			"FunctionName":                   map[string]any{"Ref": assertions.Match_StringLikeRegexp(jsii.String(workerFunctionID))},
			"BatchSize":                      tc.want,
			"MaximumBatchingWindowInSeconds": workerBatchingWindow,
		})
		template.HasResourceProperties(jsii.String("AWS::SQS::Queue"), map[string]any{
			"VisibilityTimeout": lambdaTimeoutS*6 + workerBatchingWindow,
		})
	}
}

func TestRealAWSLeavesLambdaConcurrencyUnreserved(t *testing.T) {
	t.Cleanup(jsii.Close)
	t.Setenv("AWS_ENDPOINT_URL", "")
	for name, n := range reservedConcurrency(t) {
		if n != nil {
			t.Fatalf("%s reserved=%v", name, n)
		}
	}
}

func reservedConcurrency(t *testing.T) map[string]any {
	t.Helper()
	app := awscdk.NewApp(nil)
	template := assertions.Template_FromStack(NewStack(app, "Test", nil), nil)
	got := map[string]any{}
	for _, fn := range *template.FindResources(jsii.String("AWS::Lambda::Function"), nil) {
		props := (*fn)["Properties"].(map[string]any)
		// Each function's log group is "<function id>Logs<hash>".
		log := props["LoggingConfig"].(map[string]any)["LogGroup"].(map[string]any)["Ref"].(string)
		name, _, _ := strings.Cut(log, "Logs")
		got[name] = props["ReservedConcurrentExecutions"]
	}
	return got
}
