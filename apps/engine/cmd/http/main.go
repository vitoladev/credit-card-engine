package main

import (
	"context"
	"errors"
	"os"

	"github.com/aws/aws-lambda-go/lambda"

	"engine/internal/adapter/awsconfig"
	"engine/internal/adapter/ddb"
	"engine/internal/adapter/httpapi"
	"engine/internal/adapter/sqspub"
	"engine/internal/evaluate"
	"engine/internal/recovery"
	"engine/internal/report"
	"engine/internal/rules"
	"engine/internal/submit"
)

func main() {
	h, err := compose(context.Background())
	if err != nil {
		panic(err)
	}
	lambda.Start(h.Handle)
}

func compose(ctx context.Context) (httpapi.Handler, error) {
	table := os.Getenv("DECISIONS_TABLE")
	queueURL := os.Getenv("QUEUE_URL")
	if table == "" || queueURL == "" {
		return httpapi.Handler{}, errors.New("DECISIONS_TABLE and QUEUE_URL required")
	}
	batchSize, err := submit.ParseBatchSize(os.Getenv("BATCH_SIZE"))
	if err != nil {
		return httpapi.Handler{}, err
	}
	cfg, err := awsconfig.Load(ctx)
	if err != nil {
		return httpapi.Handler{}, err
	}
	st := ddb.New(cfg, table)
	pub := sqspub.New(cfg, queueURL)
	return httpapi.New(evaluate.New(rules.NewPolicy()), st, submit.New(st, pub, batchSize), report.New(st), recovery.New(st, pub)), nil
}
