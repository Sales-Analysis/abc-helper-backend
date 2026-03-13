package abc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDecodeUploadAIMapping_FlatColumnsOnRoot(t *testing.T) {
	answer := `{"header_row":1,"data_start_row":2,"name":2,"quantity":4,"value":3,"confidence":0.88}`
	mapping, err := decodeUploadAIMapping(answer)
	if err != nil {
		t.Fatalf("decode mapping: %v", err)
	}

	if mapping.HeaderRow != 1 || mapping.DataStart != 2 {
		t.Fatalf("unexpected rows: header=%d data_start=%d", mapping.HeaderRow, mapping.DataStart)
	}
	if mapping.NameCol != 2 || mapping.QuantityCol != 4 || mapping.ValueCol != 3 {
		t.Fatalf("unexpected cols: %+v", mapping)
	}
	if mapping.Confidence <= 0 {
		t.Fatalf("expected confidence > 0, got %.3f", mapping.Confidence)
	}
}

func TestDecodeUploadAIMapping_ColumnNamesAndLetters(t *testing.T) {
	answer := `{"header_row":1,"data_start_row":2,"columns":{"name":"Наименование","quantity":"D","value":"C"}}`
	mapping, err := decodeUploadAIMapping(answer)
	if err != nil {
		t.Fatalf("decode mapping: %v", err)
	}

	rows := [][]string{
		{"Код", "Наименование товара", "Сумма", "Кол-во"},
		{"A-1", "Товар A", "1000", "10"},
	}
	idx, dataStart, err := mapping.toHeaderIndex(rows)
	if err != nil {
		t.Fatalf("toHeaderIndex: %v", err)
	}
	if dataStart != 2 {
		t.Fatalf("expected dataStart=2, got %d", dataStart)
	}
	if idx.name != 1 {
		t.Fatalf("expected name col=1, got %d", idx.name)
	}
	if idx.qty != 3 {
		t.Fatalf("expected qty col=3, got %d", idx.qty)
	}
	if idx.value != 2 {
		t.Fatalf("expected value col=2, got %d", idx.value)
	}
}

func TestCallAssistantForUploadPrepareWithRetry_RetriesOnTransientHTTPError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		if current == 1 {
			http.Error(w, `{"error":"temporary"}`, http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"answer":"{\"header_row\":1,\"data_start_row\":2,\"columns\":{\"name\":2,\"quantity\":4,\"value\":3}}` + `"}`))
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 3 * time.Second}
	cfg := uploadAIPrepareConfig{
		baseURL:      srv.URL,
		timeout:      3 * time.Second,
		maxRetries:   2,
		retryBackoff: 10 * time.Millisecond,
		mode:         "help",
	}

	answer, err := callAssistantForUploadPrepareWithRetry(
		context.Background(),
		client,
		cfg,
		"",
		"ping",
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if answer == "" {
		t.Fatalf("expected non-empty answer")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected 2 attempts, got %d", got)
	}
}

func TestIsUploadAIPrepareTransient(t *testing.T) {
	if !isUploadAIPrepareTransient(context.DeadlineExceeded) {
		t.Fatalf("deadline exceeded must be transient")
	}
	if !isUploadAIPrepareTransient(&uploadAIPrepareHTTPError{StatusCode: http.StatusGatewayTimeout}) {
		t.Fatalf("504 must be transient")
	}
	if isUploadAIPrepareTransient(&uploadAIPrepareHTTPError{StatusCode: http.StatusBadRequest}) {
		t.Fatalf("400 must not be transient")
	}
}

func TestCollectUploadSamplePayload_UsesWideRelevantColumns(t *testing.T) {
	header := make([]string, 45)
	header[0] = "Наименование организации (дилер)"
	header[2] = "Точка продаж"
	header[18] = "Наименование товара (дилер)"
	header[20] = "Штрихкод EAN (дилер)"
	header[37] = "Цена продажи"
	header[39] = "Кол-во"
	header[40] = "Сумма продажи"

	row2 := make([]string, 45)
	row2[0] = "ИП Ромашка"
	row2[2] = "МТС Площадь"
	row2[18] = "SIM-карта"
	row2[20] = "4601234567890"
	row2[37] = "199.99"
	row2[39] = "2"
	row2[40] = "399.98"

	payload := collectUploadSamplePayload(
		[][]string{header, row2},
		6,
		6,
		40,
	)
	if len(payload.Columns) == 0 {
		t.Fatalf("expected non-empty columns in payload")
	}

	hasCol := func(col int) bool {
		for _, c := range payload.Columns {
			if c.Col == col {
				return true
			}
		}
		return false
	}

	// Critical wide columns must be present (1-based indices).
	if !hasCol(19) || !hasCol(38) || !hasCol(40) || !hasCol(41) {
		t.Fatalf("payload columns do not include expected wide columns: %+v", payload.Columns)
	}
}

func TestValidateAIMappingRows_DetectsNonNumericQuantityColumn(t *testing.T) {
	rows := [][]string{
		{"Наименование товара", "Точка продаж", "Кол-во", "Сумма продажи"},
		{"SIM-карта", "МТС Площадь", "n/a", "399.98"},
	}
	idx := headerIndex{name: 0, qty: 1, value: 3, price: -1, sku: -1}

	err := validateAIMappingRows(rows, idx, 1, 2)
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if want := "quantity column 2"; err != nil && !strings.Contains(err.Error(), want) {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidateAIMappingRows_OK(t *testing.T) {
	rows := [][]string{
		{"Наименование товара", "Кол-во", "Цена продажи"},
		{"SIM-карта", "2", "199.99"},
	}
	idx := headerIndex{name: 0, qty: 1, value: -1, price: 2, sku: -1}

	if err := validateAIMappingRows(rows, idx, 1, 2); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}
