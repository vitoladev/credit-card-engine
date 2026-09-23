package httpapi_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"engine/internal/adapter/ddb"
	"engine/internal/batch"
	"engine/internal/flocitest"
)

type violation struct {
	Index *int   `json:"index"`
	Field string `json:"field"`
	Code  string `json:"code"`
}

type errorBody struct {
	Error      string      `json:"error"`
	Violations []violation `json:"violations"`
}

func customerJSON(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	c := map[string]any{
		"name": "Ana", "cpf": "390.533.447-05", "credit_score": 720,
		"current_invoice_cents": 80_000, "credit_limit_cents": 500_000,
		"late_payments": 0, "monthly_spend_cents": []int64{100_000, 110_000, 90_000},
	}
	mutate(c)
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEvaluateValidation(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantError  string
		want       []violation
	}{
		{name: "malformed json", body: `{"name":`, wantStatus: 400, wantError: "invalid_json"},
		{
			name:       "wrong check digits",
			body:       customerJSON(t, func(c map[string]any) { c["cpf"] = "39053344706" }),
			wantStatus: 422, wantError: "invalid_customer",
			want: []violation{{Field: "cpf", Code: "invalid_check_digits"}},
		},
		{
			name:       "repeated digits",
			body:       customerJSON(t, func(c map[string]any) { c["cpf"] = "11111111111" }),
			wantStatus: 422, wantError: "invalid_customer",
			want: []violation{{Field: "cpf", Code: "repeated_digits"}},
		},
		{
			name:       "negative credit score",
			body:       customerJSON(t, func(c map[string]any) { c["credit_score"] = -1 }),
			wantStatus: 422, wantError: "invalid_customer",
			want: []violation{{Field: "credit_score", Code: "negative"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			resp := h.do("POST", "/evaluations", tc.body)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
			}
			var got errorBody
			if err := json.Unmarshal([]byte(resp.Body), &got); err != nil {
				t.Fatal(err)
			}
			if got.Error != tc.wantError || !slices.Equal(got.Violations, tc.want) {
				t.Fatalf("body=%s", resp.Body)
			}
			if n := flocitest.Rows(t, h.cfg, h.tables.All()...); n != 0 {
				t.Fatalf("stored %d rows for an invalid request", n)
			}
		})
	}
}

func TestBatchWithInvalidCustomerPublishesNothing(t *testing.T) {
	h := newHarness(t)
	valid := customerJSON(t, func(map[string]any) {})
	invalid := customerJSON(t, func(c map[string]any) {
		c["cpf"] = "39053344706"
		c["credit_limit_cents"] = -1
	})
	resp := h.do("POST", "/evaluations/batch", `{"customers":[`+valid+`,`+invalid+`,`+valid+`]}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var got errorBody
	if err := json.Unmarshal([]byte(resp.Body), &got); err != nil {
		t.Fatal(err)
	}
	one := 1
	want := []violation{
		{Index: &one, Field: "cpf", Code: "invalid_check_digits"},
		{Index: &one, Field: "credit_limit_cents", Code: "negative"},
	}
	if got.Error != "invalid_customer" || len(got.Violations) != len(want) {
		t.Fatalf("body=%s", resp.Body)
	}
	for i, v := range got.Violations {
		if v.Index == nil || *v.Index != *want[i].Index || v.Field != want[i].Field || v.Code != want[i].Code {
			t.Fatalf("violation %d = %+v want %+v", i, v, want[i])
		}
	}
	if n := flocitest.Rows(t, h.cfg, h.tables.All()...); n != 0 || len(h.attempts()) != 0 {
		t.Fatalf("stored %d rows or published an invalid batch", n)
	}
}

func TestBatchStoresNormalizedCPF(t *testing.T) {
	h := newHarness(t)
	resp := h.do("POST", "/evaluations/batch", `[`+customerJSON(t, func(map[string]any) {})+`]`)
	h.want(resp, http.StatusAccepted, "")
	var acc batch.Accepted
	if err := json.Unmarshal([]byte(resp.Body), &acc); err != nil {
		t.Fatal(err)
	}
	it, err := ddb.New(h.cfg, ddb.Tables(h.tables)).Item(t.Context(), acc.BatchID, acc.ItemIDs[0])
	if err != nil || it.Customer.CPF != "39053344705" {
		t.Fatalf("item=%+v err=%v", it, err)
	}
}
