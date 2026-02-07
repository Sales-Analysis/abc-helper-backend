// Integration test for the ABC upload endpoint using a real HTTP server.
package abc_test

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"log/slog"
	"os"

	"github.com/Sales-Analysis/abc-helper-backend/internal/config"
	httpapi "github.com/Sales-Analysis/abc-helper-backend/internal/httpapi"
	"github.com/Sales-Analysis/abc-helper-backend/internal/version"
	"github.com/xuri/excelize/v2"
)

// genSampleXLSX создаёт небольшой валидный XLSX в памяти.
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
	}

	for r := range rows {
		for c := range rows[r] {
			cell, _ := excelize.CoordinatesToCellName(c+1, r+1)
			if err := f.SetCellValue(sheet, cell, rows[r][c]); err != nil {
				t.Fatalf("SetCellValue: %v", err)
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
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// Собираем http.Handler всего сервиса.
	handler := httpapi.Router(
		version.Info{Version: "test", Commit: "test", BuiltAt: "now"},
		log,
		config.Config{},
	)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Готовим multipart/form-data с файлом
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	fw, err := w.CreateFormFile("file", "abc_sample.xlsx")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write(genSampleXLSX(t)); err != nil {
		t.Fatalf("write file content: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/abc/upload", body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do request: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", res.StatusCode)
	}
}
