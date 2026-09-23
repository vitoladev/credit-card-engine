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
	decisions := os.Getenv("DECISIONS_TABLE")
	items := os.Getenv("BATCH_ITEMS_TABLE")
	keys := os.Getenv("IDEMPOTENCY_KEYS_TABLE")
	if decisions == "" || items == "" || keys == "" {
		return httpapi.Handler{}, errors.New("DECISIONS_TABLE, BATCH_ITEMS_TABLE, and IDEMPOTENCY_KEYS_TABLE required")
	}
	batchSize, err := batch.ParseBatchSize(os.Getenv("BATCH_SIZE"))
	if err != nil {
		return httpapi.Handler{}, err
	}
	cfg, err := awsconfig.Load(ctx)
	if err != nil {
		return httpapi.Handler{}, err
	}
	policy := rules.NewPolicy()
	b := batch.New(batch.Deps{
		Items:        ddb.NewItems(cfg, items),
		Policy:       policy,
		Emitter:      telemetry.EMF{},
		MaxCustomers: batchSize,
	})
	return httpapi.New(
		evaluate.New(policy, ddb.NewDecisions(cfg, decisions), telemetry.EMF{}),
		b,
		idempotency.New(ddb.NewKeys(cfg, keys)),
	), nil
}
