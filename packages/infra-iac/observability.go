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
	api.MetricServerError(&awscloudwatch.MetricOptions{
		Statistic: jsii.String("Sum"),
		Period:    minute,
	}).CreateAlarm(stack, jsii.String("Api5xx"), &awscloudwatch.CreateAlarmOptions{
		AlarmDescription:  jsii.String("API Gateway 5xx ≥ 1 in 1 minute"),
		Threshold:         jsii.Number(api5xxAlarmThreshold),
		EvaluationPeriods: jsii.Number(1),
		TreatMissingData:  awscloudwatch.TreatMissingData_NOT_BREACHING,
	})
	worker.MetricErrors(&awscloudwatch.MetricOptions{
		Statistic: jsii.String("Sum"),
		Period:    minute,
	}).CreateAlarm(stack, jsii.String("WorkerErrors"), &awscloudwatch.CreateAlarmOptions{
		AlarmDescription:  jsii.String("Worker Lambda errors ≥ 1 in 1 minute"),
		Threshold:         jsii.Number(workerErrorAlarmThreshold),
		EvaluationPeriods: jsii.Number(1),
		TreatMissingData:  awscloudwatch.TreatMissingData_NOT_BREACHING,
	})
	dlqConsumer.MetricErrors(&awscloudwatch.MetricOptions{
		Statistic: jsii.String("Sum"),
		Period:    minute,
	}).CreateAlarm(stack, jsii.String("DlqConsumerErrors"), &awscloudwatch.CreateAlarmOptions{
		AlarmDescription:  jsii.String("DLQ consumer Lambda errors ≥ 1 in 1 minute"),
		Threshold:         jsii.Number(dlqConsumerErrorAlarmThreshold),
		EvaluationPeriods: jsii.Number(1),
		TreatMissingData:  awscloudwatch.TreatMissingData_NOT_BREACHING,
	})
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
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("Evaluations per minute"),
			Left:  &[]awscloudwatch.IMetric{evaluations},
			Width: jsii.Number(12),
		}),
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("Approval rate"),
			Left:  &[]awscloudwatch.IMetric{approvalRate},
			Width: jsii.Number(12),
		}),
	)
	dash.AddWidgets(
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("DenyByReason"),
			Left:  &[]awscloudwatch.IMetric{denyByReason},
			Width: jsii.Number(12),
		}),
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("DecisionLatencyMs p99"),
			Left:  &[]awscloudwatch.IMetric{engineMetric("DecisionLatencyMs", "p99")},
			Width: jsii.Number(12),
		}),
	)
	dash.AddWidgets(
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("HTTP Lambda p99 duration"),
			Left:  &[]awscloudwatch.IMetric{fn.MetricDuration(&awscloudwatch.MetricOptions{Statistic: jsii.String("p99"), Period: minute})},
			Width: jsii.Number(12),
		}),
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("API 5xx"),
			Left:  &[]awscloudwatch.IMetric{api.MetricServerError(&awscloudwatch.MetricOptions{Statistic: jsii.String("Sum"), Period: minute})},
			Width: jsii.Number(12),
		}),
	)
	dash.AddWidgets(
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("DLQ visible messages"),
			Left:  &[]awscloudwatch.IMetric{dlq.MetricApproximateNumberOfMessagesVisible(&awscloudwatch.MetricOptions{Period: minute})},
			Width: jsii.Number(12),
		}),
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("ItemsFailed"),
			Left:  &[]awscloudwatch.IMetric{engineMetric("ItemsFailed", "Sum")},
			Width: jsii.Number(12),
		}),
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
