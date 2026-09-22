package main

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-lambda-go/lambda"

	"engine/internal/adapter/awsconfig"
	"engine/internal/adapter/ddb"
	"engine/internal/adapter/sqs"
	"engine/internal/evaluate"
	"engine/internal/processjob"
	"engine/internal/rules"
)

func main() {
	h, err := compose(context.Background())
	if err != nil {
		panic(err)
	}
	lambda.Start(h.Handle)
}

func compose(ctx context.Context) (sqs.Handler, error) {
	table := os.Getenv("DECISIONS_TABLE")
	if table == "" {
		return sqs.Handler{}, fmt.Errorf("DECISIONS_TABLE required")
	}
	cfg, err := awsconfig.Load(ctx)
	if err != nil {
		return sqs.Handler{}, err
	}
	ev := evaluate.New(rules.NewChain(), ddb.New(cfg, table))
	return sqs.New(processjob.New(ev)), nil
}
