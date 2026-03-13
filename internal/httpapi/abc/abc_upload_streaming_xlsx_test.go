package abc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestPrepareUploadFromFileStreaming(t *testing.T) {
	t.Parallel()

	filePath := createStreamingTestWorkbook(t, [][]string{
		{"SKU", "Товар", "Количество", "Сумма"},
		{"A-1", "Шоколад", "2", "200"},
		{"A-2", "Чай", "3", "300"},
	})

	preparation, err := prepareUploadFromFileStreaming(context.Background(), filePath, "req-prepare")
	if err != nil {
		t.Fatalf("prepareUploadFromFileStreaming returned error: %v", err)
	}
	if preparation.Columns == nil {
		t.Fatalf("expected preparation columns, got nil")
	}
	if preparation.Columns.Name.Index != 2 {
		t.Fatalf("expected name column 2, got %d", preparation.Columns.Name.Index)
	}
	if preparation.Columns.Quantity.Index != 3 {
		t.Fatalf("expected quantity column 3, got %d", preparation.Columns.Quantity.Index)
	}
	if preparation.Columns.Value.Index != 4 {
		t.Fatalf("expected value column 4, got %d", preparation.Columns.Value.Index)
	}
}

func TestAnalyzeUploadFromFileStreaming(t *testing.T) {
	t.Parallel()

	filePath := createStreamingTestWorkbook(t, [][]string{
		{"SKU", "Name", "Quantity", "Price"},
		{"A-1", "Milk", "2", "100"},
		{"A-1", "Milk", "1", "100"},
		{"B-1", "Bread", "3", "50"},
	})

	resp, err := analyzeUploadFromFileStreaming(context.Background(), filePath, "req-analyze", nil)
	if err != nil {
		t.Fatalf("analyzeUploadFromFileStreaming returned error: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("expected status ok, got %s", resp.Status)
	}
	if len(resp.Result) != 2 {
		t.Fatalf("expected 2 aggregated rows, got %d", len(resp.Result))
	}
}

func TestAnalyzeUploadFromFileStreaming_SkipsInvalidRows(t *testing.T) {
	t.Setenv("ABC_UPLOAD_SKIP_INVALID_ROWS", "true")
	filePath := createStreamingTestWorkbook(t, [][]string{
		{"SKU", "Name", "Quantity", "Value"},
		{"A-1", "Milk", "2", "200"},
		{"B-1", "Bread", "3", "oops"},
		{"A-1", "Milk", "1", "100"},
	})

	resp, err := analyzeUploadFromFileStreaming(context.Background(), filePath, "req-skip", nil)
	if err != nil {
		t.Fatalf("analyzeUploadFromFileStreaming returned error: %v", err)
	}
	if len(resp.Result) != 1 {
		t.Fatalf("expected only valid aggregated row, got %d", len(resp.Result))
	}
	if resp.Result[0].Name != "Milk" {
		t.Fatalf("unexpected surviving row: %+v", resp.Result[0])
	}
	if resp.Result[0].Quantity != 3 {
		t.Fatalf("expected quantity 3 for surviving row, got %d", resp.Result[0].Quantity)
	}
}

func createStreamingTestWorkbook(t *testing.T, rows [][]string) string {
	t.Helper()
	if len(rows) == 0 {
		t.Fatal("rows must not be empty")
	}

	workbook := excelize.NewFile()
	defer func() { _ = workbook.Close() }()

	sheet := workbook.GetSheetName(0)
	for rowIdx, row := range rows {
		for colIdx, value := range row {
			cell, err := excelize.CoordinatesToCellName(colIdx+1, rowIdx+1)
			if err != nil {
				t.Fatalf("CoordinatesToCellName failed: %v", err)
			}
			if err := workbook.SetCellValue(sheet, cell, value); err != nil {
				t.Fatalf("SetCellValue failed: %v", err)
			}
		}
	}

	dir := t.TempDir()
	filePath := filepath.Join(dir, "streaming.xlsx")
	if err := workbook.SaveAs(filePath); err != nil {
		t.Fatalf("SaveAs failed: %v", err)
	}
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("saved workbook not found: %v", err)
	}
	return filePath
}
