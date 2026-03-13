package abc

import (
	"context"
	"testing"
)

func TestParseProductsWithConfirmedPreparationOK(t *testing.T) {
	rows := [][]string{
		{"Код", "Наименование", "Сумма", "Кол-во"},
		{"A-1", "Товар A", "1000", "10"},
		{"B-2", "Товар B", "300", "3"},
	}
	confirmed := UploadPreparation{
		Mode:         "ai",
		HeaderRow:    1,
		DataStartRow: 2,
		Columns: &UploadPreparationColumns{
			Name:     UploadPreparationColumn{Index: 2},
			Quantity: UploadPreparationColumn{Index: 4},
			Value:    UploadPreparationColumn{Index: 3},
		},
	}

	products, preparation, err := parseProductsWithConfirmedPreparation(context.Background(), rows, confirmed)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(products) != 2 {
		t.Fatalf("expected 2 products, got %d", len(products))
	}
	if preparation.Columns == nil {
		t.Fatalf("expected preparation columns")
	}
	if preparation.Columns.Name.Index != 2 || preparation.Columns.Quantity.Index != 4 || preparation.Columns.Value.Index != 3 {
		t.Fatalf("unexpected preparation columns: %+v", preparation.Columns)
	}
}

func TestParseProductsWithConfirmedPreparationInvalidColumns(t *testing.T) {
	rows := [][]string{
		{"Код", "Наименование", "Сумма", "Кол-во"},
		{"A-1", "Товар A", "1000", "10"},
	}
	confirmed := UploadPreparation{
		Columns: &UploadPreparationColumns{
			Name:     UploadPreparationColumn{Index: 2},
			Quantity: UploadPreparationColumn{Index: 2},
			Value:    UploadPreparationColumn{Index: 3},
		},
	}

	_, _, err := parseProductsWithConfirmedPreparation(context.Background(), rows, confirmed)
	if err == nil {
		t.Fatalf("expected error for invalid confirmed columns")
	}
	perr, ok := err.(*parseError)
	if !ok {
		t.Fatalf("expected parseError, got %T", err)
	}
	if perr.code != ErrInvalidHeader {
		t.Fatalf("error code = %q, want %q", perr.code, ErrInvalidHeader)
	}
}
