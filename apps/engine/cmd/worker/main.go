package main

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"

	"engine/internal/adapter/awsconfig"
	"engine/internal/adapter/ddb"
	"engine/internal/adapter/sqs"
	"engine/internal/adapter/telemetry"
	"engine/internal/batch"
	"engine/internal/rules"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	h, err := compose(context.Background())
	if err != nil {
		panic(err)
	}
	lambda.Start(h.Handle)
}

func compose(ctx context.Context) (sqs.Consumer, error) {
	table := os.Getenv("DECISIONS_TABLE")
	if table == "" {
		return sqs.Consumer{}, errors.New("DECISIONS_TABLE required")
	}
	cfg, err := awsconfig.Load(ctx)
	if err != nil {
		return sqs.Consumer{}, err
	}
	return sqs.Worker(batch.New(batch.Deps{
		Items:   ddb.New(cfg, table),
		Policy:  rules.NewPolicy(),
		Emitter: telemetry.EMF{},
	})), nil
}
