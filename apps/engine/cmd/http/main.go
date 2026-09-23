package main

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"

	"engine/internal/adapter/awsconfig"
	"engine/internal/adapter/ddb"
	"engine/internal/adapter/httpapi"
	"engine/internal/adapter/telemetry"
	"engine/internal/batch"
	"engine/internal/evaluate"
	"engine/internal/idempotency"
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

func compose(ctx context.Context) (httpapi.Handler, error) {
	table := os.Getenv("DECISIONS_TABLE")
	if table == "" {
		return httpapi.Handler{}, errors.New("DECISIONS_TABLE required")
	}
	batchSize, err := batch.ParseBatchSize(os.Getenv("BATCH_SIZE"))
	if err != nil {
		return httpapi.Handler{}, err
	}
	cfg, err := awsconfig.Load(ctx)
	if err != nil {
		return httpapi.Handler{}, err
	}
	st := ddb.New(cfg, table)
	policy := rules.NewPolicy()
	b := batch.New(batch.Deps{
		Items:        st,
		Policy:       policy,
		Emitter:      telemetry.EMF{},
		MaxCustomers: batchSize,
	})
	return httpapi.New(evaluate.New(policy, st, telemetry.EMF{}), b, idempotency.New(ddb.NewKeys(st))), nil
}
