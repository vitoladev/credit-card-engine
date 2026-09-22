package ddb_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/smithy-go/middleware"

	"engine/internal/adapter/ddb"
	"engine/internal/domain"
	"engine/internal/store"
)

// These tests need a DynamoDB endpoint (Floci), e.g.
// AWS_TEST_ENDPOINT=http://localhost:4566 go test ./internal/adapter/ddb/
func newStore(t *testing.T) *ddb.Store {
	return newStoreWith(t, nil)
}

func newStoreFailingPutsAfter(t *testing.T, succeed int32) *ddb.Store {
	t.Helper()
	var n atomic.Int32
	return newStoreWith(t, func(cfg *aws.Config) {
		cfg.APIOptions = append(cfg.APIOptions, func(stack *middleware.Stack) error {
			return stack.Initialize.Add(middleware.InitializeMiddlewareFunc("failPuts", func(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
				input, ok := in.Parameters.(*dynamodb.BatchWriteItemInput)
				if !ok {
					return next.HandleInitialize(ctx, in)
				}
				puts := 0
				for _, reqs := range input.RequestItems {
					for _, r := range reqs {
						if r.PutRequest != nil {
							puts++
						}
					}
				}
				if puts == 0 {
					return next.HandleInitialize(ctx, in)
				}
				if n.Add(1) > succeed {
					return middleware.InitializeOutput{}, middleware.Metadata{}, errors.New("injected put failure")
				}
				return next.HandleInitialize(ctx, in)
			}), middleware.After)
		})
	})
}

func newStoreWith(t *testing.T, opt func(*aws.Config)) *ddb.Store {
	t.Helper()
	endpoint := os.Getenv("AWS_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("AWS_TEST_ENDPOINT not set")
	}
	cfg, err := config.LoadDefaultConfig(t.Context(),
		config.WithRegion("us-east-1"),
		config.WithBaseEndpoint(endpoint),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if opt != nil {
		opt(&cfg)
	}
	client := dynamodb.NewFromConfig(cfg)
	table := "ddb-test-" + randomID()
	_, err = client.CreateTable(t.Context(), &dynamodb.CreateTableInput{
		TableName:   aws.String(table),
		BillingMode: types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: types.KeyTypeRange},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = client.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(table)})
	})
	return ddb.New(cfg, table)
}

func randomID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func TestDecisionRoundTrip(t *testing.T) {
	st := newStore(t)
	r := domain.Result{Name: "Ana", CPFMasked: "***05", Decision: domain.Approved, RevolvingAmountCents: 250_000, Reasons: []string{"eligible"}}
	if err := st.Save(t.Context(), "d1", domain.Customer{Name: "Ana", CPF: "39053344705"}, r); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(t.Context(), "d1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Decision != r.Decision || got.RevolvingAmountCents != r.RevolvingAmountCents || got.CPFMasked != r.CPFMasked {
		t.Fatalf("%+v", got)
	}
	if _, err := st.Get(t.Context(), "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestBatchTransitions(t *testing.T) {
	st := newStore(t)
	// 1000 items of ~2 KB push the Query past one 1 MB page, and the write
	// past one BatchWriteItem chunk.
	customers := make([]domain.Customer, 1000)
	for i := range customers {
		customers[i] = domain.Customer{Name: strings.Repeat("n", 2000), CPF: "39053344705"}
	}
	if err := st.Create(t.Context(), "b1", customers); err != nil {
		t.Fatal(err)
	}
	b, err := st.Report(t.Context(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if b.Counters != (store.Counters{Queued: 1000}) || len(b.Items) != 1000 {
		t.Fatalf("counters=%+v items=%d", b.Counters, len(b.Items))
	}
	for i, it := range b.Items {
		if it.Index != i || it.Status != store.Queued || it.Attempts != 1 || it.Customer.CPF != "39053344705" {
			t.Fatalf("item %d: index=%d status=%s attempts=%d", i, it.Index, it.Status, it.Attempts)
		}
	}

	r := domain.Result{Decision: domain.Approved, RevolvingAmountCents: 100, Reasons: []string{"eligible"}}
	if err := st.Decide(t.Context(), "b1", 7, 1, r); err != nil {
		t.Fatal(err)
	}
	if err := st.Decide(t.Context(), "b1", 7, 1, r); !errors.Is(err, store.ErrInvalidTransition) {
		t.Fatalf("repeat decide err=%v", err)
	}
	if err := st.Decide(t.Context(), "b1", 8, 2, r); !errors.Is(err, store.ErrInvalidTransition) {
		t.Fatalf("wrong attempt err=%v", err)
	}
	if err := st.Decide(t.Context(), "b1", 5000, 1, r); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing item err=%v", err)
	}

	b, err = st.Report(t.Context(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if b.Counters != (store.Counters{Queued: 999, Decided: 1}) {
		t.Fatalf("counters=%+v", b.Counters)
	}
	if it := b.Items[7]; it.Status != store.Decided || it.Result.RevolvingAmountCents != 100 {
		t.Fatalf("%+v", it.Status)
	}
	if _, err := st.Report(t.Context(), "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestFailRetryCancelTransitions(t *testing.T) {
	st := newStore(t)
	ctx := t.Context()
	customers := []domain.Customer{{Name: "Ana", CPF: "39053344705"}, {Name: "Bruno", CPF: "12345678909"}, {Name: "Carla", CPF: "98765432100"}}
	if err := st.Create(ctx, "b1", customers); err != nil {
		t.Fatal(err)
	}
	wantErr := func(name string, err, want error) {
		t.Helper()
		if !errors.Is(err, want) || (want == nil && err != nil) {
			t.Fatalf("%s: err=%v want %v", name, err, want)
		}
	}
	wantCounters := func(want store.Counters) {
		t.Helper()
		b, err := st.Report(ctx, "b1")
		if err != nil {
			t.Fatal(err)
		}
		if b.Counters != want {
			t.Fatalf("counters=%+v want %+v", b.Counters, want)
		}
	}

	wantErr("fail wrong attempt", st.Fail(ctx, "b1", 0, 2), store.ErrInvalidTransition)
	wantErr("fail missing", st.Fail(ctx, "b1", 9, 1), store.ErrNotFound)
	wantErr("fail 0", st.Fail(ctx, "b1", 0, 1), nil)
	wantErr("fail 0 again", st.Fail(ctx, "b1", 0, 1), store.ErrInvalidTransition)
	wantErr("fail 1", st.Fail(ctx, "b1", 1, 1), nil)
	wantErr("decide failed item", st.Decide(ctx, "b1", 0, 1, domain.Result{}), store.ErrInvalidTransition)
	wantCounters(store.Counters{Queued: 1, Failed: 2})

	failed, err := st.Failed(ctx, "b1")
	if err != nil || len(failed) != 2 || failed[0].Index != 0 || failed[1].Index != 1 || failed[1].Customer.CPF != "12345678909" {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	if _, err := st.Failed(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failed of missing batch: err=%v", err)
	}

	// Two operators retry the same attempt: only one wins.
	wantErr("retry 0", st.Retry(ctx, "b1", 0, 1), nil)
	wantErr("retry 0 again", st.Retry(ctx, "b1", 0, 1), store.ErrInvalidTransition)
	wantErr("retry queued", st.Retry(ctx, "b1", 2, 1), store.ErrInvalidTransition)
	wantErr("retry missing", st.Retry(ctx, "b1", 9, 1), store.ErrNotFound)
	it, err := st.Item(ctx, "b1", 0)
	if err != nil || it.Status != store.Queued || it.Attempts != 2 || it.Customer.Name != "Ana" {
		t.Fatalf("item=%+v err=%v", it, err)
	}
	if _, err := st.Item(ctx, "b1", 9); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("item missing: err=%v", err)
	}
	wantCounters(store.Counters{Queued: 2, Failed: 1})

	for attempt := 2; attempt < store.MaxAttempts; attempt++ {
		wantErr("fail", st.Fail(ctx, "b1", 0, attempt), nil)
		wantErr("retry", st.Retry(ctx, "b1", 0, attempt), nil)
	}
	wantErr("fail at max", st.Fail(ctx, "b1", 0, store.MaxAttempts), nil)
	wantErr("retry at max", st.Retry(ctx, "b1", 0, store.MaxAttempts), store.ErrMaxAttempts)

	wantErr("cancel 1", st.Cancel(ctx, "b1", 1), nil)
	wantErr("cancel 1 again", st.Cancel(ctx, "b1", 1), nil)
	wantErr("cancel queued", st.Cancel(ctx, "b1", 2), store.ErrInvalidTransition)
	wantErr("cancel missing", st.Cancel(ctx, "b1", 9), store.ErrNotFound)
	wantErr("retry cancelled", st.Retry(ctx, "b1", 1, 1), store.ErrInvalidTransition)
	wantCounters(store.Counters{Queued: 1, Failed: 1, Cancelled: 1})
}

func TestRetryManyMovesFailedItems(t *testing.T) {
	st := newStore(t)
	ctx := t.Context()
	customers := []domain.Customer{{Name: "Ana", CPF: "39053344705"}, {Name: "Bruno", CPF: "12345678909"}, {Name: "Carla", CPF: "98765432100"}}
	if err := st.Create(ctx, "b1", customers); err != nil {
		t.Fatal(err)
	}
	if err := st.Fail(ctx, "b1", 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.Fail(ctx, "b1", 1, 1); err != nil {
		t.Fatal(err)
	}
	failed, err := st.Failed(ctx, "b1")
	if err != nil || len(failed) != 2 {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	queued, err := st.RetryMany(ctx, "b1", append(failed, store.Item{Index: 2, Attempts: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 2 || queued[0].Index != 0 || queued[1].Index != 1 {
		t.Fatalf("queued=%+v", queued)
	}
	b, err := st.Report(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	if b.Counters != (store.Counters{Queued: 3}) {
		t.Fatalf("counters=%+v", b.Counters)
	}
	if b.Items[0].Attempts != 2 || b.Items[1].Attempts != 2 || b.Items[2].Attempts != 1 {
		t.Fatalf("attempts=%d,%d,%d", b.Items[0].Attempts, b.Items[1].Attempts, b.Items[2].Attempts)
	}
}

func TestRetryManyMovesAFullChunk(t *testing.T) {
	st := newStore(t)
	ctx := t.Context()
	n := 51
	customers := make([]domain.Customer, n)
	for i := range customers {
		customers[i] = domain.Customer{Name: "Ana", CPF: "39053344705"}
	}
	if err := st.Create(ctx, "b1", customers); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		if err := st.Fail(ctx, "b1", i, 1); err != nil {
			t.Fatal(err)
		}
	}
	failed, err := st.Failed(ctx, "b1")
	if err != nil || len(failed) != n {
		t.Fatalf("failed=%d err=%v", len(failed), err)
	}
	queued, err := st.RetryMany(ctx, "b1", failed)
	if err != nil || len(queued) != n {
		t.Fatalf("queued=%d err=%v", len(queued), err)
	}
	b, err := st.Report(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	if b.Counters != (store.Counters{Queued: n}) {
		t.Fatalf("counters=%+v", b.Counters)
	}
}

func TestCreateRemovesPartialRowsOnWriteFailure(t *testing.T) {
	st := newStoreFailingPutsAfter(t, 1)
	customers := make([]domain.Customer, 30)
	for i := range customers {
		customers[i] = domain.Customer{Name: "Ana", CPF: "39053344705"}
	}
	if err := st.Create(t.Context(), "b1", customers); err == nil {
		t.Fatal("expected write failure")
	}
	if _, err := st.Report(t.Context(), "b1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("leftover batch: err=%v", err)
	}
}
