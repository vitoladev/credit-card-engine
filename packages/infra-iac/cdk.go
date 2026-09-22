package main

import (
	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/jsii-runtime-go"
)

func main() {
	defer jsii.Close()
	app := awscdk.NewApp(nil)
	NewStack(app, stackName, &stackProps{
		Env: &awscdk.Environment{
			Region: jsii.String("us-east-1"),
		},
	})
	app.Synth(nil)
}
