package httpapi

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"log/slog"
	"os"

	"github.com/Sales-Analysis/abc-helper-backend/internal/version"
	"github.com/xuri/excelize/v2"
)

func genSampleXLSX(t *testing.T) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	const sheet = "ABC"
	if err := f.SetSheetName(f.GetSheetName(0), sheet); err != nil {
		t.Fatalf("SetSheetName: %v", err)
	}

	rows := [][]any{
		{"#", "Item", "Value", "Quantity", "Share", "CumShare", "Class"},
		{1, "Product A", 12000.0, 24, 0.60, 0.60, "A"},
		{2, "Product B", 5000.0, 10, 0.25, 0.85, "B"},
	}
	for r := range rows {
		for c := range rows[r] {
			cell, _ := excelize.CoordinatesToCellName(c+1, r+1)
			if err := f.SetCellValue(sheet, cell, rows[r][c]); err != nil {
				t.Fatalf("set cell: %v", err)
			}
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("write xlsx: %v", err)
	}
	return buf.Bytes()
}

func TestUploadEndpoint_WithRealXLSX(t *testing.T) {
	// поднимаем весь роутер как http.Handler
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	handler := Router(version.Info{Version: "test", Commit: "test", BuiltAt: "now"}, log)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	// собираем multipart/form-data с файлом
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	fw, _ := w.CreateFormFile("file", "abc_sample.xlsx")
	_, _ = fw.Write(genSampleXLSX(t))
	_ = w.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/abc/upload", body)
	req.Header.Set("Content-Type", w.FormDataContentType())

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", res.StatusCode)
	}
}
