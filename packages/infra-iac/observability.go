package main

import (
	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscloudwatch"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslambda"
	"github.com/aws/jsii-runtime-go"
)

func wireAlarms(stack awscdk.Stack, fn awslambda.IFunction) {
	fn.MetricErrors(nil).CreateAlarm(stack, jsii.String("EvaluateErrors"), &awscloudwatch.CreateAlarmOptions{
		AlarmDescription:  jsii.String("Evaluate Lambda errors > 0 in 1 minute"),
		Threshold:         jsii.Number(1),
		EvaluationPeriods: jsii.Number(1),
		TreatMissingData:  awscloudwatch.TreatMissingData_NOT_BREACHING,
	})
	fn.MetricDuration(&awscloudwatch.MetricOptions{
		Statistic: jsii.String("p99"),
		Period:    awscdk.Duration_Minutes(jsii.Number(1)),
	}).CreateAlarm(stack, jsii.String("EvaluateLatency"), &awscloudwatch.CreateAlarmOptions{
		AlarmDescription:  jsii.String("Evaluate p99 duration over 800ms (SLO is 1s)"),
		Threshold:         jsii.Number(latencyAlarmMs),
		EvaluationPeriods: jsii.Number(3),
		TreatMissingData:  awscloudwatch.TreatMissingData_NOT_BREACHING,
	})
}
