package main

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"

	"engine/internal/adapter/awsconfig"
	"engine/internal/adapter/ddb"
	"engine/internal/adapter/dlq"
	"engine/internal/adapter/telemetry"
	"engine/internal/markfailed"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	h, err := compose(context.Background())
	if err != nil {
		panic(err)
	}
	lambda.Start(h.Handle)
}

func compose(ctx context.Context) (dlq.Handler, error) {
	table := os.Getenv("DECISIONS_TABLE")
	if table == "" {
		return dlq.Handler{}, errors.New("DECISIONS_TABLE required")
	}
	cfg, err := awsconfig.Load(ctx)
	if err != nil {
		return dlq.Handler{}, err
	}
	return dlq.New(markfailed.New(telemetry.Observe(ddb.New(cfg, table)))), nil
}
