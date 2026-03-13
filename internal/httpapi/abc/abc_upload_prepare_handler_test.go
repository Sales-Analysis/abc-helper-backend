package abc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	abclib "github.com/Sales-Analysis/abc-helper-lib/abc"
)

func TestUploadPrepareHandlerMethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/abc/upload/prepare", nil)
	rec := httptest.NewRecorder()

	UploadPrepareHandler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestUploadPrepareHandlerHeaderPreparation(t *testing.T) {
	content := makeXLSX(t, [][]any{
		{"Артикул", "Наименование", "Сумма", "Кол-во"},
		{"A-1", "Товар A", "1000", "10"},
	})
	req := newUploadJobCreateRequest(t, "sample.xlsx", content)
	req.URL.Path = "/api/v1/abc/upload/prepare"
	rec := httptest.NewRecorder()

	UploadPrepareHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		Status      string            `json:"status"`
		Preparation UploadPreparation `json:"preparation"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Status != "prepared" {
		t.Fatalf("status = %q, want %q", body.Status, "prepared")
	}
	if body.Preparation.Mode != "header" {
		t.Fatalf("mode = %q, want %q", body.Preparation.Mode, "header")
	}
	if body.Preparation.Columns == nil {
		t.Fatalf("expected columns in preparation")
	}
	if body.Preparation.Columns.Name.Index != 2 {
		t.Fatalf("name index = %d, want 2", body.Preparation.Columns.Name.Index)
	}
	if body.Preparation.Columns.Quantity.Index != 4 {
		t.Fatalf("quantity index = %d, want 4", body.Preparation.Columns.Quantity.Index)
	}
	if body.Preparation.Columns.Value.Index != 3 {
		t.Fatalf("value index = %d, want 3", body.Preparation.Columns.Value.Index)
	}
}

func TestUploadPrepareHandlerAIPreparation(t *testing.T) {
	orig := prepareProductsWithAI
	t.Cleanup(func() {
		prepareProductsWithAI = orig
	})

	prepareProductsWithAI = func(
		_ context.Context,
		rows [][]string,
		_ string,
	) ([]abclib.Product, UploadPreparation, error) {
		idx := headerIndex{sku: 0, name: 1, value: 2, qty: 3, price: -1}
		return []abclib.Product{{SKU: "A-1", Name: "Товар A", Quantity: 10, Price: 100}}, buildUploadPreparation(rows, 1, 2, idx, "ai", 0.91), nil
	}

	content := makeXLSX(t, [][]any{
		{"Колонка 1", "Колонка 2", "Колонка 3", "Колонка 4"},
		{"A-1", "Товар A", "1000", "10"},
	})
	req := newUploadJobCreateRequest(t, "sample.xlsx", content)
	req.URL.Path = "/api/v1/abc/upload/prepare"
	rec := httptest.NewRecorder()

	UploadPrepareHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		Preparation UploadPreparation `json:"preparation"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Preparation.Mode != "ai" {
		t.Fatalf("mode = %q, want %q", body.Preparation.Mode, "ai")
	}
	if body.Preparation.Columns == nil || body.Preparation.Columns.Name.Index != 2 {
		t.Fatalf("unexpected columns: %+v", body.Preparation.Columns)
	}
}
