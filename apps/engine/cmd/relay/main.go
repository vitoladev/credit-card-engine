package main

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"

	"engine/internal/adapter/awsconfig"
	"engine/internal/adapter/ddb"
	"engine/internal/adapter/sqspub"
	"engine/internal/batch"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	h, err := compose(context.Background())
	if err != nil {
		panic(err)
	}
	lambda.Start(h.Handle)
}

// compose wires the relay: the table's stream in, the work queue out. Relay
// reads no rows and evaluates nothing, so the module gets only a Publisher.
func compose(ctx context.Context) (ddb.Relay, error) {
	queueURL := os.Getenv("QUEUE_URL")
	if queueURL == "" {
		return ddb.Relay{}, errors.New("QUEUE_URL required")
	}
	cfg, err := awsconfig.Load(ctx)
	if err != nil {
		return ddb.Relay{}, err
	}
	return ddb.NewRelay(batch.New(batch.Deps{Publisher: sqspub.New(cfg, queueURL)})), nil
}
