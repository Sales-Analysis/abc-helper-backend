package abc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	abclib "github.com/Sales-Analysis/abc-helper-lib/abc"
	"github.com/xuri/excelize/v2"
)

type errResp struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type okResp struct {
	Status      string             `json:"status"`
	Result      []productResult    `json:"result"`
	Preparation *UploadPreparation `json:"preparation,omitempty"`
}

type productResult struct {
	SKU              string
	Name             string
	Quantity         int
	PriceUnit        float64
	PriceTotal       float64
	ShareTotal       float64
	ShareAccumulated float64
	Group            string
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

func parseOK(t *testing.T, rr *httptest.ResponseRecorder) okResp {
	t.Helper()
	var ok okResp
	if err := json.Unmarshal(rr.Body.Bytes(), &ok); err != nil {
		t.Fatalf("unmarshal ok response: %v; body=%q", err, rr.Body.String())
	}
	return ok
}

func assertFloat(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.0001 {
		t.Fatalf("want %.4f, got %.4f", want, got)
	}
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
		{1, "A", 100.0, 10, "", "", ""},
	})
	rr := doUpload(t, "ok.xlsx", content)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rr.Code, rr.Body.String())
	}
	ok := parseOK(t, rr)
	if ok.Status != "ok" {
		t.Fatalf("want status ok, got %v", ok.Status)
	}
	if len(ok.Result) != 1 {
		t.Fatalf("want 1 result, got %d", len(ok.Result))
	}
}

func TestUpload_TooLarge(t *testing.T) {
	oldMaxUploadBytes := maxUploadBytes
	oldMaxMultipartMemory := maxMultipartMemory
	t.Cleanup(func() {
		maxUploadBytes = oldMaxUploadBytes
		maxMultipartMemory = oldMaxMultipartMemory
	})
	maxUploadBytes = 1 << 20
	maxMultipartMemory = 1 << 20

	// тело больше лимита upload → ParseMultipartForm вернёт ошибку → INVALID_FORM
	// создаём тело больше текущего лимита upload
	huge := bytes.Repeat([]byte("x"), int(maxUploadBytes)+1)
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
	// валидный файл: несколько строк для ABC анализа
	content := makeXLSX(t, [][]any{
		{"#", "Item", "Value", "Quantity"},
		{1, "Product A", 800.0, 8},
		{2, "Product B", 150.0, 3},
		{3, "Product C", 50.0, 5},
	})
	rr := doUpload(t, "ok.xlsx", content)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rr.Code, rr.Body.String())
	}
	ok := parseOK(t, rr)
	if ok.Status != "ok" {
		t.Fatalf("want status ok, got %v", ok.Status)
	}
	if len(ok.Result) != 3 {
		t.Fatalf("want 3 results, got %d", len(ok.Result))
	}
	first := ok.Result[0]
	if first.SKU != "1" || first.Name != "Product A" || first.Quantity != 8 {
		t.Fatalf("unexpected first result: %+v", first)
	}
	assertFloat(t, first.PriceUnit, 100)
	assertFloat(t, first.PriceTotal, 800)
	assertFloat(t, first.ShareTotal, 80)
	assertFloat(t, first.ShareAccumulated, 80)
	if first.Group != "A" {
		t.Fatalf("want group A, got %s", first.Group)
	}

	second := ok.Result[1]
	assertFloat(t, second.ShareAccumulated, 95)
	if second.Group != "B" {
		t.Fatalf("want group B, got %s", second.Group)
	}

	third := ok.Result[2]
	assertFloat(t, third.ShareAccumulated, 100)
	if third.Group != "C" {
		t.Fatalf("want group C, got %s", third.Group)
	}
}

func TestUpload_InvalidHeader(t *testing.T) {
	content := makeXLSX(t, [][]any{
		{"#", "Item", "Value"},
		{1, "A", 100.0},
	})
	rr := doUpload(t, "bad.xlsx", content)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
	e := parseErr(t, rr)
	if e.Error.Code != "INVALID_HEADER" {
		t.Fatalf("want code INVALID_HEADER, got %s", e.Error.Code)
	}
}

func TestUpload_InvalidQuantity(t *testing.T) {
	content := makeXLSX(t, [][]any{
		{"#", "Item", "Value", "Quantity"},
		{1, "A", 100.0, 1.5},
	})
	rr := doUpload(t, "bad.xlsx", content)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
	e := parseErr(t, rr)
	if e.Error.Code != "INVALID_QUANTITY" {
		t.Fatalf("want code INVALID_QUANTITY, got %s", e.Error.Code)
	}
}

func TestUpload_InvalidNumber(t *testing.T) {
	content := makeXLSX(t, [][]any{
		{"#", "Item", "Value", "Quantity"},
		{1, "A", "oops", 10},
	})
	rr := doUpload(t, "bad.xlsx", content)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
	e := parseErr(t, rr)
	if e.Error.Code != "INVALID_NUMBER" {
		t.Fatalf("want code INVALID_NUMBER, got %s", e.Error.Code)
	}
}

func TestUpload_AIPrepareFallback_OK(t *testing.T) {
	orig := prepareProductsWithAI
	t.Cleanup(func() {
		prepareProductsWithAI = orig
	})

	called := false
	prepareProductsWithAI = func(
		_ context.Context,
		rows [][]string,
		requestID string,
	) ([]abclib.Product, UploadPreparation, error) {
		called = true
		if len(rows) == 0 {
			t.Fatalf("expected non-empty rows")
		}
		if requestID != "" {
			t.Fatalf("unexpected request id in test: %q", requestID)
		}
		idx := headerIndex{sku: 0, name: 1, value: 2, qty: 3, price: -1}
		return []abclib.Product{
			{SKU: "A-1", Name: "Товар A", Quantity: 2, Price: 100},
		}, buildUploadPreparation(rows, 1, 2, idx, "ai", 0.91), nil
	}

	content := makeXLSX(t, [][]any{
		{"Поле1", "Поле2", "Поле3", "Поле4"},
		{"A-1", "Товар A", "200,00", 2},
	})

	rr := doUpload(t, "fallback.xlsx", content)
	if !called {
		t.Fatalf("expected AI preparation fallback to be called")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rr.Code, rr.Body.String())
	}

	ok := parseOK(t, rr)
	if ok.Preparation == nil || ok.Preparation.Mode != "ai" {
		t.Fatalf("want preparation mode ai, got %+v", ok.Preparation)
	}
	if len(ok.Result) != 1 {
		t.Fatalf("want 1 result, got %d", len(ok.Result))
	}
	if ok.Result[0].Name != "Товар A" {
		t.Fatalf("unexpected result: %+v", ok.Result[0])
	}
}

func TestUpload_AIPrepareFallback_FailedReturnsAssistantPrepError(t *testing.T) {
	orig := prepareProductsWithAI
	t.Cleanup(func() {
		prepareProductsWithAI = orig
	})

	called := false
	prepareProductsWithAI = func(
		_ context.Context,
		_ [][]string,
		_ string,
	) ([]abclib.Product, UploadPreparation, error) {
		called = true
		return nil, UploadPreparation{}, errors.New("assistant unavailable")
	}

	content := makeXLSX(t, [][]any{
		{"Поле1", "Поле2", "Поле3"},
		{"A-1", "Товар A", "200,00"},
	})
	rr := doUpload(t, "fallback.xlsx", content)

	if !called {
		t.Fatalf("expected AI preparation fallback to be called")
	}
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d; body=%s", rr.Code, rr.Body.String())
	}
	e := parseErr(t, rr)
	if e.Error.Code != "ASSISTANT_UPLOAD_PREPARATION_FAILED" {
		t.Fatalf("want code ASSISTANT_UPLOAD_PREPARATION_FAILED, got %s", e.Error.Code)
	}
}

func TestUpload_AIPrepareFallback_TransientErrorFallsBackToOriginalParseError(t *testing.T) {
	orig := prepareProductsWithAI
	t.Cleanup(func() {
		prepareProductsWithAI = orig
	})

	prepareProductsWithAI = func(
		_ context.Context,
		_ [][]string,
		_ string,
	) ([]abclib.Product, UploadPreparation, error) {
		return nil, UploadPreparation{}, context.DeadlineExceeded
	}

	content := makeXLSX(t, [][]any{
		{"Поле1", "Поле2", "Поле3"},
		{"A-1", "Товар A", "200,00"},
	})
	rr := doUpload(t, "fallback.xlsx", content)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d; body=%s", rr.Code, rr.Body.String())
	}
	e := parseErr(t, rr)
	if e.Error.Code != "INVALID_HEADER" {
		t.Fatalf("want code INVALID_HEADER, got %s", e.Error.Code)
	}
}

func TestUpload_AIPrepareFallback_ParseErrorReturnedAsIs(t *testing.T) {
	orig := prepareProductsWithAI
	t.Cleanup(func() {
		prepareProductsWithAI = orig
	})

	prepareProductsWithAI = func(
		_ context.Context,
		_ [][]string,
		_ string,
	) ([]abclib.Product, UploadPreparation, error) {
		return nil, UploadPreparation{}, &parseError{
			code: ErrInvalidNumber,
			msg:  "row 2 has invalid number in column 3",
		}
	}

	content := makeXLSX(t, [][]any{
		{"Поле1", "Поле2", "Поле3"},
		{"A-1", "Товар A", "n/a"},
	})
	rr := doUpload(t, "fallback.xlsx", content)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d; body=%s", rr.Code, rr.Body.String())
	}
	e := parseErr(t, rr)
	if e.Error.Code != "INVALID_NUMBER" {
		t.Fatalf("want code INVALID_NUMBER, got %s", e.Error.Code)
	}
	if !strings.Contains(e.Error.Message, "row 2 has invalid number in column 3") {
		t.Fatalf("unexpected error message: %q", e.Error.Message)
	}
}

func TestUpload_OK_MessyNumericValues(t *testing.T) {
	content := makeXLSX(t, [][]any{
		{"#", "Item", "Value", "Quantity"},
		{"A-1", "Product A", "1 200,50 ₽", "10 шт"},
	})
	rr := doUpload(t, "ok.xlsx", content)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rr.Code, rr.Body.String())
	}
	ok := parseOK(t, rr)
	if len(ok.Result) != 1 {
		t.Fatalf("want 1 result, got %d", len(ok.Result))
	}
	assertFloat(t, ok.Result[0].PriceUnit, 120.05)
	assertFloat(t, ok.Result[0].PriceTotal, 1200.5)
}

func TestUpload_OK_RussianHeaders(t *testing.T) {
	content := makeXLSX(t, [][]any{
		{"Артикул", "Наименование", "Сумма", "Кол-во"},
		{"A-1", "Товар A", "1 000,00", "10"},
		{"B-2", "Товар B", "300,00", "3"},
	})
	rr := doUpload(t, "ru.xlsx", content)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rr.Code, rr.Body.String())
	}
	ok := parseOK(t, rr)
	if len(ok.Result) != 2 {
		t.Fatalf("want 2 results, got %d", len(ok.Result))
	}
	if ok.Result[0].Name == "" || ok.Result[0].Quantity <= 0 {
		t.Fatalf("unexpected first result: %+v", ok.Result[0])
	}
}

func TestUpload_OK_RussianHeaders_WithExtraWords(t *testing.T) {
	content := makeXLSX(t, [][]any{
		{"Артикул товара", "Наименование товара", "Сумма продаж, руб.", "Количество, шт"},
		{"A-1", "Товар A", "1 000,00", "10"},
		{"B-2", "Товар B", "300,00", "3"},
	})
	rr := doUpload(t, "ru-complex.xlsx", content)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rr.Code, rr.Body.String())
	}
	ok := parseOK(t, rr)
	if len(ok.Result) != 2 {
		t.Fatalf("want 2 results, got %d", len(ok.Result))
	}
	if ok.Result[0].Name != "Товар A" {
		t.Fatalf("unexpected first result: %+v", ok.Result[0])
	}
}

func TestUpload_OK_AggregatesDuplicateProducts(t *testing.T) {
	content := makeXLSX(t, [][]any{
		{"SKU", "Item", "Value", "Quantity"},
		{"A-1", "Product A", "100", "2"},
		{"A-1", "Product A", "150", "3"},
		{"B-1", "Product B", "50", "1"},
	})
	rr := doUpload(t, "dups.xlsx", content)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%s", rr.Code, rr.Body.String())
	}
	ok := parseOK(t, rr)
	if len(ok.Result) != 2 {
		t.Fatalf("want 2 aggregated products, got %d", len(ok.Result))
	}
	if ok.Result[0].SKU != "A-1" || ok.Result[0].Quantity != 5 {
		t.Fatalf("unexpected first aggregated result: %+v", ok.Result[0])
	}
	assertFloat(t, ok.Result[0].PriceTotal, 250)
}
