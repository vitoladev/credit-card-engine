package ddb_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"engine/internal/adapter/ddb"
	"engine/internal/domain"
	"engine/internal/store"
)

// These tests need a DynamoDB endpoint (Floci), e.g.
// AWS_TEST_ENDPOINT=http://localhost:4566 go test ./internal/adapter/ddb/
func newStore(t *testing.T) *ddb.Store {
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
