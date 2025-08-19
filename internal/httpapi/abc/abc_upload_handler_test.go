package abc

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

type errResp struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func doUpload(t *testing.T, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if content != nil {
		if _, err := fw.Write(content); err != nil {
			t.Fatalf("write content: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/abc/upload", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rr := httptest.NewRecorder()

	UploadHandler(rr, req)
	return rr
}

func parseErr(t *testing.T, rr *httptest.ResponseRecorder) errResp {
	t.Helper()
	var e errResp
	if err := json.Unmarshal(rr.Body.Bytes(), &e); err != nil {
		t.Fatalf("unmarshal error response: %v; body=%q", err, rr.Body.String())
	}
	return e
}

func makeXLSX(t *testing.T, rows [][]any) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	const sheet = "ABC"
	if err := f.SetSheetName(f.GetSheetName(0), sheet); err != nil { // ✅ check err
		t.Fatalf("SetSheetName: %v", err)
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

func TestUpload_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/abc/upload", nil)
	rr := httptest.NewRecorder()

	UploadHandler(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rr.Code)
	}
	e := parseErr(t, rr)
	if e.Error.Code != "METHOD_NOT_ALLOWED" {
		t.Fatalf("want code METHOD_NOT_ALLOWED, got %s", e.Error.Code)
	}
}

func TestUpload_MissingFile(t *testing.T) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	// без поля "file"
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/abc/upload", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rr := httptest.NewRecorder()

	UploadHandler(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
	e := parseErr(t, rr)
	if e.Error.Code != "MISSING_FILE" {
		t.Fatalf("want code MISSING_FILE, got %s", e.Error.Code)
	}
}

func TestUpload_EmptyFile(t *testing.T) {
	// пустой файл: ничего не пишем в part → fh.Size == 0
	rr := doUpload(t, "empty.xlsx", nil)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
	e := parseErr(t, rr)
	if e.Error.Code != "EMPTY_FILE" {
		t.Fatalf("want code EMPTY_FILE, got %s", e.Error.Code)
	}
}

func TestUpload_InvalidExtension(t *testing.T) {
	// валидный контент, но расширение не .xlsx → INVALID_EXTENSION
	content := makeXLSX(t, [][]any{
		{"#", "Item", "Value"},
		{1, "A", 100.0},
	})
	rr := doUpload(t, "sample.xls", content)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
	e := parseErr(t, rr)
	if e.Error.Code != "INVALID_EXTENSION" {
		t.Fatalf("want code INVALID_EXTENSION, got %s", e.Error.Code)
	}
}

func TestUpload_InvalidXLSX(t *testing.T) {
	rr := doUpload(t, "broken.xlsx", []byte("not an xlsx"))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
	e := parseErr(t, rr)
	if e.Error.Code != "INVALID_XLSX" {
		t.Fatalf("want code INVALID_XLSX, got %s", e.Error.Code)
	}
}

func TestUpload_NoData(t *testing.T) {
	// только заголовок, данных нет → NO_DATA
	content := makeXLSX(t, [][]any{
		{"#", "Item", "Value", "Quantity", "Share", "CumShare", "Class"},
	})
	rr := doUpload(t, "nodata.xlsx", content)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
	e := parseErr(t, rr)
	if e.Error.Code != "NO_DATA" {
		t.Fatalf("want code NO_DATA, got %s", e.Error.Code)
	}
}

func TestUpload_MissingValue(t *testing.T) {
	// строка с пропущенной ячейкой (пустая середина) → MISSING_VALUE
	content := makeXLSX(t, [][]any{
		{"#", "Item", "Value", "Quantity", "Share", "CumShare", "Class"},
		{1, "", 100.0, 10, 0.6, 0.6, "A"}, // пропущено значение во 2-й колонке
	})
	rr := doUpload(t, "miss.xlsx", content)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d; body=%s", rr.Code, rr.Body.String())
	}
	e := parseErr(t, rr)
	if e.Error.Code != "MISSING_VALUE" {
		t.Fatalf("want code MISSING_VALUE, got %s", e.Error.Code)
	}
	if !strings.Contains(e.Error.Message, "row 2") {
		t.Fatalf("want message to mention row 2, got %q", e.Error.Message)
	}
}

func TestUpload_SkipEmptyRow_OK(t *testing.T) {
	// пустая строка (все значения пустые) должна быть пропущена → OK
	content := makeXLSX(t, [][]any{
		{"#", "Item", "Value", "Quantity", "Share", "CumShare", "Class"},
		// полностью пустая строка
		{"", "", "", "", "", "", ""},
		// валидная строка
		{1, "A", 100.0, 10, 0.6, 0.6, "A"},
	})
	rr := doUpload(t, "ok.xlsx", content)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rr.Code, rr.Body.String())
	}
	var ok map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &ok); err != nil {
		t.Fatalf("unmarshal ok: %v", err)
	}
	if ok["status"] != "ok" {
		t.Fatalf("want status ok, got %v", ok)
	}
}

func TestUpload_TooLarge(t *testing.T) {
	// тело больше 5MiB → ParseMultipartForm вернёт ошибку → INVALID_FORM
	// создаём "файл" ~6MiB
	huge := bytes.Repeat([]byte("x"), 6<<20)
	rr := doUpload(t, "big.xlsx", huge)

	if rr.Code != http.StatusBadRequest && rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("want 4xx, got %d; body=%s", rr.Code, rr.Body.String())
	}
	e := parseErr(t, rr)
	if e.Error.Code != "INVALID_FORM" && e.Error.Code != "READ_ERROR" {
		t.Fatalf("want code INVALID_FORM/READ_ERROR, got %s", e.Error.Code)
	}
}

func TestUpload_OK(t *testing.T) {
	// валидный файл: заголовок + полная строка
	content := makeXLSX(t, [][]any{
		{"#", "Item", "Value", "Quantity", "Share", "CumShare", "Class"},
		{1, "A", 100.0, 10, 0.6, 0.6, "A"},
	})
	rr := doUpload(t, "ok.xlsx", content)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rr.Code, rr.Body.String())
	}
	var ok map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &ok); err != nil {
		t.Fatalf("unmarshal ok: %v", err)
	}
	if ok["status"] != "ok" {
		t.Fatalf("want status ok, got %v", ok)
	}
}
