package main

import (
	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsapigatewayv2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscloudwatch"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslambda"
	"github.com/aws/aws-cdk-go/awscdk/v2/awssqs"
	"github.com/aws/jsii-runtime-go"
)

func wireObservability(stack awscdk.Stack, fn, worker, dlqConsumer awslambda.IFunction, api awsapigatewayv2.HttpApi, dlq awssqs.IQueue) {
	minute := awscdk.Duration_Minutes(jsii.Number(1))
	alarmOnSum(stack, "Api5xx", "API Gateway 5xx ≥ 1 in 1 minute", api.MetricServerError, api5xxAlarmThreshold)
	alarmOnSum(stack, "WorkerErrors", "Worker Lambda errors ≥ 1 in 1 minute", worker.MetricErrors, workerErrorAlarmThreshold)
	alarmOnSum(stack, "DlqConsumerErrors", "DLQ consumer Lambda errors ≥ 1 in 1 minute", dlqConsumer.MetricErrors, dlqConsumerErrorAlarmThreshold)
	dlq.MetricApproximateNumberOfMessagesVisible(&awscloudwatch.MetricOptions{
		Period: minute,
	}).CreateAlarm(stack, jsii.String("DlqVisible"), &awscloudwatch.CreateAlarmOptions{
		AlarmDescription:   jsii.String("DLQ ApproximateNumberOfMessagesVisible > 0 for 5 minutes"),
		Threshold:          jsii.Number(dlqVisibleAlarmThreshold),
		ComparisonOperator: awscloudwatch.ComparisonOperator_GREATER_THAN_THRESHOLD,
		EvaluationPeriods:  jsii.Number(dlqVisibleEvaluationPeriods),
		TreatMissingData:   awscloudwatch.TreatMissingData_NOT_BREACHING,
	})
	fn.MetricDuration(&awscloudwatch.MetricOptions{
		Statistic: jsii.String("p99"),
		Period:    minute,
	}).CreateAlarm(stack, jsii.String("EvaluateLatency"), &awscloudwatch.CreateAlarmOptions{
		AlarmDescription:  jsii.String("Evaluate p99 duration over 800ms (SLO is 1s)"),
		Threshold:         jsii.Number(latencyAlarmMs),
		EvaluationPeriods: jsii.Number(3),
		TreatMissingData:  awscloudwatch.TreatMissingData_NOT_BREACHING,
	})

	approved := engineMetric("Approved", "Sum")
	denied := engineMetric("Denied", "Sum")
	evaluations := awscloudwatch.NewMathExpression(&awscloudwatch.MathExpressionProps{
		Expression: jsii.String("approved + denied"),
		UsingMetrics: &map[string]awscloudwatch.IMetric{
			"approved": approved,
			"denied":   denied,
		},
		Label:  jsii.String("Evaluations per minute"),
		Period: minute,
	})
	approvalRate := awscloudwatch.NewMathExpression(&awscloudwatch.MathExpressionProps{
		Expression: jsii.String("approved / (approved + denied)"),
		UsingMetrics: &map[string]awscloudwatch.IMetric{
			"approved": approved,
			"denied":   denied,
		},
		Label:  jsii.String("Approval rate"),
		Period: minute,
	})
	denyByReason := awscloudwatch.NewSearchExpression(&awscloudwatch.SearchExpressionProps{
		Expression: jsii.String(`SEARCH('{` + metricsNamespace + `,reason} MetricName="DenyByReason"', 'Sum', 60)`),
		Label:      jsii.String("Deny by reason"),
		Period:     minute,
	})

	dash := awscloudwatch.NewDashboard(stack, jsii.String("Dashboard"), &awscloudwatch.DashboardProps{
		DashboardName: jsii.String(dashboardName),
	})
	dash.AddWidgets(
		graph("Evaluations per minute", evaluations),
		graph("Approval rate", approvalRate),
	)
	dash.AddWidgets(
		graph("DenyByReason", denyByReason),
		graph("DecisionLatencyMs p99", engineMetric("DecisionLatencyMs", "p99")),
	)
	dash.AddWidgets(
		graph("HTTP Lambda p99 duration", fn.MetricDuration(&awscloudwatch.MetricOptions{Statistic: jsii.String("p99"), Period: minute})),
		graph("API 5xx", api.MetricServerError(&awscloudwatch.MetricOptions{Statistic: jsii.String("Sum"), Period: minute})),
	)
	dash.AddWidgets(
		graph("DLQ visible messages", dlq.MetricApproximateNumberOfMessagesVisible(&awscloudwatch.MetricOptions{Period: minute})),
		graph("ItemsFailed", engineMetric("ItemsFailed", "Sum")),
	)
}

func engineMetric(name, stat string) awscloudwatch.Metric {
	return awscloudwatch.NewMetric(&awscloudwatch.MetricProps{
		Namespace:  jsii.String(metricsNamespace),
		MetricName: jsii.String(name),
		Statistic:  jsii.String(stat),
		Period:     awscdk.Duration_Minutes(jsii.Number(1)),
	})
}

// alarmOnSum alarms when a metric's per-minute Sum reaches threshold.
func alarmOnSum(stack awscdk.Stack, id, description string, metric func(*awscloudwatch.MetricOptions) awscloudwatch.Metric, threshold float64) {
	metric(&awscloudwatch.MetricOptions{
		Statistic: jsii.String("Sum"),
		Period:    awscdk.Duration_Minutes(jsii.Number(1)),
	}).CreateAlarm(stack, jsii.String(id), &awscloudwatch.CreateAlarmOptions{
		AlarmDescription:  jsii.String(description),
		Threshold:         jsii.Number(threshold),
		EvaluationPeriods: jsii.Number(1),
		TreatMissingData:  awscloudwatch.TreatMissingData_NOT_BREACHING,
	})
}

// graph is a half-width dashboard graph of one metric.
func graph(title string, metric awscloudwatch.IMetric) awscloudwatch.GraphWidget {
	return awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
		Title: jsii.String(title),
		Left:  &[]awscloudwatch.IMetric{metric},
		Width: jsii.Number(12),
	})
}
