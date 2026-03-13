package abc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	abclib "github.com/Sales-Analysis/abc-helper-lib/abc"
)

const (
	defaultUploadAIPrepareTimeout    = 130 * time.Second
	defaultUploadAIPrepareMaxRetries = 2
	defaultUploadAIPrepareBackoff    = 1500 * time.Millisecond
	defaultUploadAIMappingAttempts   = 2
	maxUploadAIPrepareRetries        = 5
	maxUploadAIPrepareBackoff        = 12 * time.Second
	defaultUploadAIPrepareSampleRows = 6
	defaultUploadAIPrepareSampleCols = 6
	defaultUploadAIPrepareCellChars  = 24
	maxUploadAIPrepareSampleCols     = 60
	minUploadAIPrepareSampleCols     = 12
	uploadAIPrepareValidationRows    = 12
)

type uploadAIPrepareConfig struct {
	enabled      bool
	baseURL      string
	timeout      time.Duration
	maxRetries   int
	retryBackoff time.Duration
	mode         string
	sampleRows   int
	sampleCols   int
	maxCellChars int
}

type uploadAIPrepareProcessor struct {
	cfg    uploadAIPrepareConfig
	client *http.Client
}

type uploadAIMapping struct {
	HeaderRow   int
	DataStart   int
	SKUCol      int
	NameCol     int
	QuantityCol int
	PriceCol    int
	ValueCol    int
	SKUHeader   string
	NameHeader  string
	QtyHeader   string
	PriceHeader string
	ValueHeader string
	Confidence  float64
}

type uploadSamplePayload struct {
	Columns []uploadSampleColumn `json:"columns"`
	Rows    []uploadSampleRow    `json:"rows"`
}

type uploadSampleColumn struct {
	Col    int    `json:"col"`
	Header string `json:"header"`
}

type uploadSampleRow struct {
	Row   int                `json:"row"`
	Cells []uploadSampleCell `json:"cells"`
}

type uploadSampleCell struct {
	Col   int    `json:"col"`
	Value string `json:"value"`
}

type uploadAIPrepareHTTPError struct {
	StatusCode int
}

func (e *uploadAIPrepareHTTPError) Error() string {
	if e == nil {
		return "assistant upload preparation failed"
	}
	return fmt.Sprintf("assistant upload preparation failed with status %d", e.StatusCode)
}

type uploadAIPrepareFunc func(
	ctx context.Context,
	rows [][]string,
	requestID string,
) ([]abclib.Product, UploadPreparation, error)

var (
	uploadAIPrepareOnce     sync.Once
	uploadAIPrepareInstance *uploadAIPrepareProcessor
	prepareProductsWithAI   uploadAIPrepareFunc = prepareProductsWithAIDefault

	errUploadAIPrepareNotInitialized  = errors.New("assistant upload preparation is not initialized")
	errUploadAIPrepareDisabled        = errors.New("assistant upload preparation is disabled")
	errUploadAIPrepareBaseURLNotFound = errors.New("assistant upload preparation base url is not configured")
)

func prepareProductsWithAIDefault(
	ctx context.Context,
	rows [][]string,
	requestID string,
) ([]abclib.Product, UploadPreparation, error) {
	processor := getUploadAIPrepareProcessor()
	if processor == nil {
		return nil, UploadPreparation{}, errUploadAIPrepareNotInitialized
	}
	return processor.prepare(ctx, rows, requestID)
}

func getUploadAIPrepareProcessor() *uploadAIPrepareProcessor {
	uploadAIPrepareOnce.Do(func() {
		uploadAIPrepareInstance = newUploadAIPrepareProcessor(loadUploadAIPrepareConfig())
	})
	return uploadAIPrepareInstance
}

func newUploadAIPrepareProcessor(cfg uploadAIPrepareConfig) *uploadAIPrepareProcessor {
	if cfg.timeout <= 0 {
		cfg.timeout = defaultUploadAIPrepareTimeout
	}
	if cfg.maxRetries < 0 {
		cfg.maxRetries = 0
	}
	if cfg.maxRetries > maxUploadAIPrepareRetries {
		cfg.maxRetries = maxUploadAIPrepareRetries
	}
	if cfg.retryBackoff <= 0 {
		cfg.retryBackoff = defaultUploadAIPrepareBackoff
	}
	if cfg.retryBackoff > maxUploadAIPrepareBackoff {
		cfg.retryBackoff = maxUploadAIPrepareBackoff
	}
	if cfg.sampleRows <= 0 {
		cfg.sampleRows = defaultUploadAIPrepareSampleRows
	}
	if cfg.sampleCols <= 0 {
		cfg.sampleCols = defaultUploadAIPrepareSampleCols
	}
	if cfg.maxCellChars <= 0 {
		cfg.maxCellChars = defaultUploadAIPrepareCellChars
	}

	return &uploadAIPrepareProcessor{
		cfg: cfg,
		client: &http.Client{
			Timeout: cfg.timeout,
		},
	}
}

func loadUploadAIPrepareConfig() uploadAIPrepareConfig {
	mode := strings.TrimSpace(strings.ToLower(os.Getenv("ASSISTANT_UPLOAD_PREP_MODE")))
	if mode == "" {
		mode = "help"
	}
	if mode != "help" && mode != "diagnostic" && mode != "howto" {
		mode = "help"
	}

	cfg := uploadAIPrepareConfig{
		enabled:      envBool("ASSISTANT_UPLOAD_PREP_ENABLED", true),
		baseURL:      strings.TrimRight(strings.TrimSpace(os.Getenv("ASSISTANT_BASE_URL")), "/"),
		timeout:      envDuration("ASSISTANT_UPLOAD_PREP_TIMEOUT", defaultUploadAIPrepareTimeout),
		maxRetries:   envInt("ASSISTANT_UPLOAD_PREP_MAX_RETRIES", defaultUploadAIPrepareMaxRetries),
		retryBackoff: envDuration("ASSISTANT_UPLOAD_PREP_RETRY_BACKOFF", defaultUploadAIPrepareBackoff),
		mode:         mode,
		sampleRows:   envInt("ASSISTANT_UPLOAD_PREP_SAMPLE_ROWS", defaultUploadAIPrepareSampleRows),
		sampleCols:   envInt("ASSISTANT_UPLOAD_PREP_SAMPLE_COLS", defaultUploadAIPrepareSampleCols),
		maxCellChars: envInt("ASSISTANT_UPLOAD_PREP_MAX_CELL_CHARS", defaultUploadAIPrepareCellChars),
	}
	if cfg.sampleRows <= 0 {
		cfg.sampleRows = defaultUploadAIPrepareSampleRows
	}
	if cfg.sampleRows > 80 {
		cfg.sampleRows = 80
	}
	if cfg.sampleCols <= 0 {
		cfg.sampleCols = defaultUploadAIPrepareSampleCols
	}
	if cfg.sampleCols > maxUploadAIPrepareSampleCols {
		cfg.sampleCols = maxUploadAIPrepareSampleCols
	}
	if cfg.maxCellChars <= 0 {
		cfg.maxCellChars = defaultUploadAIPrepareCellChars
	}
	if cfg.maxCellChars > 120 {
		cfg.maxCellChars = 120
	}
	return cfg
}

func (p *uploadAIPrepareProcessor) prepare(
	ctx context.Context,
	rows [][]string,
	requestID string,
) ([]abclib.Product, UploadPreparation, error) {
	if p == nil || !p.cfg.enabled {
		return nil, UploadPreparation{}, errUploadAIPrepareDisabled
	}
	if strings.TrimSpace(p.cfg.baseURL) == "" {
		return nil, UploadPreparation{}, errUploadAIPrepareBaseURLNotFound
	}
	if len(rows) < 2 {
		return nil, UploadPreparation{}, errors.New("xlsx has no data")
	}

	prompt := buildUploadAIPreparePrompt(rows, p.cfg)
	var lastErr error
	for mappingAttempt := 1; mappingAttempt <= defaultUploadAIMappingAttempts; mappingAttempt++ {
		reportUploadProgress(ctx, uploadJobStageAIPreparing, 40+mappingAttempt)
		answer, err := callAssistantForUploadPrepareWithRetry(ctx, p.client, p.cfg, requestID, prompt)
		if err != nil {
			return nil, UploadPreparation{}, err
		}

		mapping, err := decodeUploadAIMapping(answer)
		if err != nil {
			lastErr = err
			if mappingAttempt < defaultUploadAIMappingAttempts {
				prompt = buildUploadAIPreparePromptWithFeedback(rows, p.cfg, err.Error())
				continue
			}
			return nil, UploadPreparation{}, err
		}
		idx, dataStart, err := mapping.toHeaderIndex(rows)
		if err != nil {
			lastErr = err
			if mappingAttempt < defaultUploadAIMappingAttempts {
				prompt = buildUploadAIPreparePromptWithFeedback(rows, p.cfg, err.Error())
				continue
			}
			return nil, UploadPreparation{}, err
		}

		if validationErr := validateAIMappingRows(rows, idx, mapping.HeaderRow, dataStart); validationErr != nil {
			lastErr = validationErr
			if mappingAttempt < defaultUploadAIMappingAttempts {
				prompt = buildUploadAIPreparePromptWithFeedback(rows, p.cfg, validationErr.Error())
				continue
			}
			return nil, UploadPreparation{}, &parseError{
				code: ErrInvalidHeader,
				msg:  "assistant selected invalid columns: " + strings.TrimSpace(validationErr.Error()),
			}
		}

		start := dataStart - 1
		if start < 0 {
			start = 0
		}
		if start > len(rows) {
			start = len(rows)
		}
		products, err := parseProductsWithProgressFromRow(
			rows[start:],
			idx,
			dataStart,
			newRowProgressReporter(ctx, uploadJobStageParsingRows, 58, 80),
		)
		if err != nil {
			lastErr = err
			if mappingAttempt < defaultUploadAIMappingAttempts && isAIMappingLikelyWrongParseError(err) {
				prompt = buildUploadAIPreparePromptWithFeedback(rows, p.cfg, err.Error())
				continue
			}
			return nil, UploadPreparation{}, err
		}
		reportUploadProgress(ctx, uploadJobStageParsingRows, 82)

		headerRow := mapping.HeaderRow
		if headerRow <= 0 {
			headerRow = 1
		}
		preparation := buildUploadPreparation(
			rows,
			headerRow,
			dataStart,
			idx,
			"ai",
			mapping.Confidence,
		)
		return products, preparation, nil
	}

	if lastErr != nil {
		return nil, UploadPreparation{}, lastErr
	}
	return nil, UploadPreparation{}, errors.New("assistant upload preparation failed")
}

func callAssistantForUploadPrepare(
	ctx context.Context,
	client *http.Client,
	cfg uploadAIPrepareConfig,
	requestID, message string,
) (string, error) {
	if client == nil {
		return "", errors.New("assistant client is nil")
	}

	payload := map[string]any{
		"session_id":      "abc-upload-preparation",
		"mode":            cfg.mode,
		"message":         message,
		"bypass_rag":      true,
		"response_format": "json",
		"user_context": map[string]any{
			"role": "analyst",
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	reqCtx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(
		reqCtx,
		http.MethodPost,
		cfg.baseURL+"/v1/chat",
		bytes.NewReader(body),
	)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(requestID) != "" {
		req.Header.Set("X-Request-ID", requestID)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", &uploadAIPrepareHTTPError{StatusCode: resp.StatusCode}
	}

	var parsed struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", err
	}
	answer := strings.TrimSpace(parsed.Answer)
	if answer == "" {
		return "", errors.New("assistant upload preparation is empty")
	}
	return answer, nil
}

func callAssistantForUploadPrepareWithRetry(
	ctx context.Context,
	client *http.Client,
	cfg uploadAIPrepareConfig,
	requestID, message string,
) (string, error) {
	attempts := cfg.maxRetries + 1
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		answer, err := callAssistantForUploadPrepare(ctx, client, cfg, requestID, message)
		if err == nil {
			return answer, nil
		}
		lastErr = err

		if attempt >= attempts || !isUploadAIPrepareTransient(err) {
			break
		}

		if waitErr := waitUploadAIPrepareBackoff(ctx, cfg.retryBackoff, attempt); waitErr != nil {
			return "", waitErr
		}
	}

	return "", lastErr
}

func waitUploadAIPrepareBackoff(ctx context.Context, base time.Duration, attempt int) error {
	if base <= 0 {
		base = defaultUploadAIPrepareBackoff
	}
	if attempt < 1 {
		attempt = 1
	}

	wait := base * time.Duration(1<<(attempt-1))
	if wait > maxUploadAIPrepareBackoff {
		wait = maxUploadAIPrepareBackoff
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isUploadAIPrepareTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	var httpErr *uploadAIPrepareHTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusRequestTimeout,
			http.StatusTooManyRequests,
			http.StatusInternalServerError,
			http.StatusBadGateway,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}

	return false
}

func validateAIMappingRows(rows [][]string, idx headerIndex, headerRow int, dataStart int) error {
	if len(rows) == 0 {
		return errors.New("xlsx has no data")
	}

	if idx.name < 0 || idx.qty < 0 || (idx.price < 0 && idx.value < 0) {
		return errors.New("assistant mapping is incomplete")
	}

	if headerRow <= 0 {
		headerRow = 1
	}

	if headerRow-1 >= 0 && headerRow-1 < len(rows) {
		header := rows[headerRow-1]
		nameHeader := normalizeHeaderLabel(cellAt(header, idx.name))
		if !isLikelyProductNameHeader(nameHeader) {
			return fmt.Errorf("name column %d looks like non-product header", idx.name+1)
		}
		qtyHeader := normalizeHeaderLabel(cellAt(header, idx.qty))
		if headerHasPattern(qtyHeader, "штрихкод", "barcode", "ean") {
			return fmt.Errorf("quantity column %d looks like barcode header", idx.qty+1)
		}
	}

	start := dataStart - 1
	if start < headerRow {
		start = headerRow
	}
	if start < 1 {
		start = 1
	}
	sampled := 0
	nameSeen := false
	qtySeen := false
	priceSeen := false
	valueSeen := false

	for rowIdx := start; rowIdx < len(rows) && sampled < uploadAIPrepareValidationRows; rowIdx++ {
		row := rows[rowIdx]
		if isRowEmpty(row) {
			continue
		}
		sampled++
		rowNum := rowIdx + 1

		if strings.TrimSpace(cellAt(row, idx.name)) != "" {
			nameSeen = true
		}

		if qtyRaw := strings.TrimSpace(cellAt(row, idx.qty)); qtyRaw != "" {
			qtySeen = true
			qty, err := parseFloat(qtyRaw)
			if err != nil || qty <= 0 || math.Mod(qty, 1.0) != 0 {
				return fmt.Errorf("quantity column %d has non-integer value at row %d", idx.qty+1, rowNum)
			}
		}

		if idx.price >= 0 {
			if priceRaw := strings.TrimSpace(cellAt(row, idx.price)); priceRaw != "" {
				priceSeen = true
				price, err := parseFloat(priceRaw)
				if err != nil || price <= 0 {
					return fmt.Errorf("price column %d has invalid number at row %d", idx.price+1, rowNum)
				}
			}
		}
		if idx.value >= 0 {
			if valueRaw := strings.TrimSpace(cellAt(row, idx.value)); valueRaw != "" {
				valueSeen = true
				value, err := parseFloat(valueRaw)
				if err != nil || value <= 0 {
					return fmt.Errorf("value column %d has invalid number at row %d", idx.value+1, rowNum)
				}
			}
		}
	}
	if sampled == 0 {
		return nil
	}

	if !nameSeen {
		return fmt.Errorf("name column %d has no non-empty values in sampled rows", idx.name+1)
	}
	if !qtySeen {
		return fmt.Errorf("quantity column %d has no numeric values in sampled rows", idx.qty+1)
	}
	if idx.price >= 0 && idx.value >= 0 {
		if !priceSeen && !valueSeen {
			return fmt.Errorf("price/value columns have no numeric values in sampled rows")
		}
	} else if idx.price >= 0 {
		if !priceSeen {
			return fmt.Errorf("price column %d has no numeric values in sampled rows", idx.price+1)
		}
	} else if !valueSeen {
		return fmt.Errorf("value column %d has no numeric values in sampled rows", idx.value+1)
	}

	return nil
}

func isAIMappingLikelyWrongParseError(err error) bool {
	perr := new(parseError)
	if !errors.As(err, &perr) {
		return false
	}
	switch perr.code {
	case ErrMissingValue, ErrInvalidNumber, ErrInvalidQuantity, ErrInvalidValue:
		return true
	default:
		return false
	}
}

func buildUploadAIPreparePrompt(rows [][]string, cfg uploadAIPrepareConfig) string {
	return buildUploadAIPreparePromptWithFeedback(rows, cfg, "")
}

func buildUploadAIPreparePromptWithFeedback(
	rows [][]string,
	cfg uploadAIPrepareConfig,
	feedback string,
) string {
	samplePayload := collectUploadSamplePayload(rows, cfg.sampleRows, cfg.sampleCols, cfg.maxCellChars)
	samplesJSON, _ := json.Marshal(samplePayload)

	var b strings.Builder
	b.WriteString(
		"Ты помощник по подготовке Excel-файлов для ABC-анализа.\n" +
			"Определи структуру таблицы и верни только JSON (без markdown):\n" +
			"{\"header_row\":1,\"data_start_row\":2," +
			"\"columns\":{\"sku\":1,\"name\":2,\"quantity\":4,\"price\":0,\"value\":3}," +
			"\"confidence\":0.0}\n\n" +
			"Формат входных примеров:\n" +
			"- columns: список колонок с исходными индексами col (1-based) и заголовками.\n" +
			"- rows: пример строк, где у каждой ячейки есть исходный col и value.\n\n" +
			"Правила:\n" +
			"1) Номера колонок в ответе должны быть исходными индексами col из примеров (1-based).\n" +
			"2) Если колонка отсутствует, верни 0.\n" +
			"3) Обязательны колонки name и quantity, а также хотя бы одна из price/value.\n" +
			"4) Для quantity и price/value выбирай колонки, где в sample rows видны числовые значения.\n" +
			"5) Не выбирай текстовые колонки (например точка продаж, адрес, штрихкод) в quantity/price/value.\n" +
			"6) Используй только данные из примера ниже, ничего не придумывай.\n\n",
	)
	if trimmedFeedback := strings.TrimSpace(feedback); trimmedFeedback != "" {
		b.WriteString("Важно: предыдущая попытка была невалидна. Причина: ")
		b.WriteString(trimmedFeedback)
		b.WriteString("\nСкорректируй mapping и верни новый JSON.\n\n")
	}
	b.WriteString("Пример данных:\n")
	b.Write(samplesJSON)
	return b.String()
}

func collectUploadSamplePayload(
	rows [][]string,
	rowLimit, colLimit, cellLimit int,
) uploadSamplePayload {
	if rowLimit <= 0 {
		return uploadSamplePayload{}
	}
	if colLimit <= 0 {
		colLimit = defaultUploadAIPrepareSampleCols
	}
	if colLimit < minUploadAIPrepareSampleCols {
		colLimit = minUploadAIPrepareSampleCols
	}
	if colLimit > maxUploadAIPrepareSampleCols {
		colLimit = maxUploadAIPrepareSampleCols
	}
	if cellLimit <= 0 {
		cellLimit = defaultUploadAIPrepareCellChars
	}

	selectedCols := selectUploadSampleColumns(rows, colLimit)
	payload := uploadSamplePayload{
		Columns: make([]uploadSampleColumn, 0, len(selectedCols)),
		Rows:    make([]uploadSampleRow, 0, rowLimit),
	}
	if len(selectedCols) == 0 {
		return payload
	}

	headerRow := []string{}
	if len(rows) > 0 {
		headerRow = rows[0]
	}
	for _, col := range selectedCols {
		payload.Columns = append(payload.Columns, uploadSampleColumn{
			Col:    col + 1,
			Header: normalizeSampleCell(cellAt(headerRow, col), cellLimit),
		})
	}

	for i := 1; i < len(rows); i++ {
		if len(payload.Rows) >= rowLimit {
			break
		}
		row := rows[i]
		if isRowEmpty(row) {
			continue
		}

		sampleCells := make([]uploadSampleCell, 0, len(selectedCols))
		for _, col := range selectedCols {
			value := normalizeSampleCell(cellAt(row, col), cellLimit)
			if strings.TrimSpace(value) == "" {
				continue
			}
			sampleCells = append(sampleCells, uploadSampleCell{
				Col:   col + 1,
				Value: value,
			})
		}
		if len(sampleCells) == 0 {
			continue
		}
		payload.Rows = append(payload.Rows, uploadSampleRow{
			Row:   i + 1,
			Cells: sampleCells,
		})
	}
	return payload
}

func selectUploadSampleColumns(rows [][]string, colLimit int) []int {
	maxCols := maxColumns(rows)
	if maxCols <= 0 {
		return nil
	}

	selected := make([]int, 0, colLimit)
	seen := make(map[int]struct{}, colLimit)
	addCol := func(col int) {
		if col < 0 || col >= maxCols {
			return
		}
		if _, exists := seen[col]; exists {
			return
		}
		seen[col] = struct{}{}
		selected = append(selected, col)
	}

	// Keep a bit of left context in all files.
	for i := 0; i < maxCols && i < 3; i++ {
		addCol(i)
	}

	if len(rows) > 0 {
		header := rows[0]
		for col := 0; col < maxCols; col++ {
			label := normalizeHeaderLabel(cellAt(header, col))
			if label == "" {
				continue
			}
			if headerHasPattern(label, "sku", "id", "code", "код", "артикул") ||
				(headerHasPattern(label,
					"item", "name", "product", "товар", "наименование", "номенклатур", "названи", "продукт",
				) && isLikelyProductNameHeader(label)) ||
				isLikelyQuantityHeader(label) ||
				headerHasPattern(label,
					"unitprice", "priceunit", "price", "ценаед", "ценаединиц", "цена", "закупоч", "розничн",
				) ||
				headerHasPattern(label,
					"value", "revenue", "amount", "total", "sum", "сумма", "выручк", "оборот", "стоимост", "итого",
				) {
				addCol(col)
			}
		}
	}

	// Fill with non-empty columns up to limit for extra context.
	if len(selected) < colLimit && len(rows) > 0 {
		header := rows[0]
		for col := 0; col < maxCols && len(selected) < colLimit; col++ {
			if strings.TrimSpace(cellAt(header, col)) == "" {
				continue
			}
			addCol(col)
		}
	}

	// Fallback for tiny/empty headers.
	if len(selected) == 0 {
		for col := 0; col < maxCols && len(selected) < colLimit; col++ {
			addCol(col)
		}
	}

	return selected
}

func normalizeSampleCell(raw string, cellLimit int) string {
	cleaned := strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
	if cellLimit <= 0 {
		return cleaned
	}
	runes := []rune(cleaned)
	if len(runes) <= cellLimit {
		return cleaned
	}
	return string(runes[:cellLimit])
}

func decodeUploadAIMapping(answer string) (uploadAIMapping, error) {
	jsonBlock := extractFirstJSONObject(answer)
	if jsonBlock == "" {
		return uploadAIMapping{}, errors.New("assistant upload preparation response does not contain JSON object")
	}

	var root map[string]any
	if err := json.Unmarshal([]byte(jsonBlock), &root); err != nil {
		return uploadAIMapping{}, err
	}

	headerRow, err := readIntByKeys(root,
		"header_row",
		"headerRow",
		"header",
		"header_index",
		"header_row_index",
	)
	if err != nil {
		return uploadAIMapping{}, fmt.Errorf("invalid header_row: %w", err)
	}
	dataStart, err := readIntByKeys(root,
		"data_start_row",
		"dataStartRow",
		"data_start",
		"start_row",
		"first_data_row",
		"data_row",
	)
	if err != nil {
		return uploadAIMapping{}, fmt.Errorf("invalid data_start_row: %w", err)
	}
	confidence, err := readFloatByKeys(root, "confidence", "score")
	if err != nil {
		return uploadAIMapping{}, fmt.Errorf("invalid confidence: %w", err)
	}

	columns := firstMapByKeys(root, "columns", "mapping", "column_map", "field_map")

	readColumn := func(label string, keys []string) (int, string, error) {
		col, header, readErr := readColumnSpec(columns, root, keys)
		if readErr != nil {
			return 0, "", fmt.Errorf("invalid %s column: %w", label, readErr)
		}
		return col, header, nil
	}

	skuCol, skuHeader, err := readColumn("sku", []string{
		"sku", "id", "code", "article", "артикул", "код",
		"sku_col", "sku_column", "sku_index",
	})
	if err != nil {
		return uploadAIMapping{}, err
	}
	nameCol, nameHeader, err := readColumn("name", []string{
		"name", "item", "product", "title", "наименование", "номенклатура", "товар",
		"name_col", "name_column", "item_col",
	})
	if err != nil {
		return uploadAIMapping{}, err
	}
	quantityCol, quantityHeader, err := readColumn("quantity", []string{
		"quantity", "qty", "count", "количество", "колво", "шт",
		"quantity_col", "qty_col", "quantity_index",
	})
	if err != nil {
		return uploadAIMapping{}, err
	}
	priceCol, priceHeader, err := readColumn("price", []string{
		"price", "unit_price", "price_unit", "unitprice", "цена",
		"price_col", "price_index",
	})
	if err != nil {
		return uploadAIMapping{}, err
	}
	valueCol, valueHeader, err := readColumn("value", []string{
		"value", "amount", "sum", "total", "revenue", "сумма", "выручка", "стоимость",
		"value_col", "amount_col", "sum_col", "revenue_col",
	})
	if err != nil {
		return uploadAIMapping{}, err
	}

	return uploadAIMapping{
		HeaderRow:   headerRow,
		DataStart:   dataStart,
		SKUCol:      skuCol,
		NameCol:     nameCol,
		QuantityCol: quantityCol,
		PriceCol:    priceCol,
		ValueCol:    valueCol,
		SKUHeader:   skuHeader,
		NameHeader:  nameHeader,
		QtyHeader:   quantityHeader,
		PriceHeader: priceHeader,
		ValueHeader: valueHeader,
		Confidence:  confidence,
	}, nil
}

func readColumnSpec(
	nested map[string]any,
	root map[string]any,
	keys []string,
) (int, string, error) {
	for _, key := range keys {
		if nested != nil {
			if val, ok := nested[key]; ok {
				col, header, err := anyToColumnSpec(val)
				if err != nil {
					return 0, "", err
				}
				if col > 0 || header != "" {
					return col, header, nil
				}
			}
		}
		if val, ok := root[key]; ok {
			col, header, err := anyToColumnSpec(val)
			if err != nil {
				return 0, "", err
			}
			if col > 0 || header != "" {
				return col, header, nil
			}
		}
	}
	return 0, "", nil
}

func anyToColumnSpec(v any) (int, string, error) {
	switch value := v.(type) {
	case nil:
		return 0, "", nil
	case float64, int, int64:
		n, err := anyToInt(value)
		return n, "", err
	case string:
		s := strings.TrimSpace(value)
		if s == "" {
			return 0, "", nil
		}
		if n, err := strconv.Atoi(s); err == nil {
			return n, "", nil
		}
		if n, ok := excelColumnNameToIndex(s); ok {
			return n, "", nil
		}
		return 0, s, nil
	case map[string]any:
		if idx, ok, err := readIndexFromMap(value); err != nil {
			return 0, "", err
		} else if ok {
			return idx, "", nil
		}
		if name := readNameFromMap(value); name != "" {
			return 0, name, nil
		}
		return 0, "", nil
	default:
		return 0, "", fmt.Errorf("unsupported value type %T", v)
	}
}

func readIndexFromMap(m map[string]any) (int, bool, error) {
	for _, key := range []string{"index", "column", "col", "position", "number"} {
		val, ok := m[key]
		if !ok {
			continue
		}
		n, err := anyToInt(val)
		if err != nil {
			return 0, true, err
		}
		return n, true, nil
	}
	return 0, false, nil
}

func readNameFromMap(m map[string]any) string {
	for _, key := range []string{"name", "header", "title", "column_name"} {
		val, ok := m[key]
		if !ok {
			continue
		}
		s, ok := val.(string)
		if !ok {
			continue
		}
		s = strings.TrimSpace(s)
		if s != "" {
			return s
		}
	}
	return ""
}

func firstMapByKeys(root map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		val, ok := root[key]
		if !ok {
			continue
		}
		if m, ok := val.(map[string]any); ok {
			return m
		}
	}
	return nil
}

func readIntByKeys(m map[string]any, keys ...string) (int, error) {
	for _, key := range keys {
		val, ok := m[key]
		if !ok {
			continue
		}
		return anyToInt(val)
	}
	return 0, nil
}

func readFloatByKeys(m map[string]any, keys ...string) (float64, error) {
	for _, key := range keys {
		val, ok := m[key]
		if !ok {
			continue
		}
		return anyToFloat(val)
	}
	return 0, nil
}

func excelColumnNameToIndex(raw string) (int, bool) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return 0, false
	}
	idx := 0
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return 0, false
		}
		idx = idx*26 + int(r-'A'+1)
	}
	if idx <= 0 {
		return 0, false
	}
	return idx, true
}

func extractFirstJSONObject(text string) string {
	s := strings.TrimSpace(text)
	if s == "" {
		return ""
	}

	inString := false
	escaped := false
	depth := 0
	start := -1

	for i, r := range s {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && inString {
			escaped = true
			continue
		}
		if r == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}

		if r == '{' {
			if depth == 0 {
				start = i
			}
			depth++
			continue
		}
		if r == '}' {
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 && start >= 0 {
				candidate := strings.TrimSpace(s[start : i+1])
				if json.Valid([]byte(candidate)) {
					return candidate
				}
				start = -1
			}
		}
	}

	return ""
}

func anyToInt(v any) (int, error) {
	switch value := v.(type) {
	case nil:
		return 0, nil
	case float64:
		return int(value), nil
	case int:
		return value, nil
	case int64:
		return int(value), nil
	case string:
		s := strings.TrimSpace(value)
		if s == "" {
			return 0, nil
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, err
		}
		return n, nil
	default:
		return 0, fmt.Errorf("unsupported value type %T", v)
	}
}

func anyToFloat(v any) (float64, error) {
	switch value := v.(type) {
	case nil:
		return 0, nil
	case float64:
		return value, nil
	case int:
		return float64(value), nil
	case int64:
		return float64(value), nil
	case string:
		s := strings.TrimSpace(value)
		if s == "" {
			return 0, nil
		}
		return strconv.ParseFloat(s, 64)
	default:
		return 0, fmt.Errorf("unsupported value type %T", v)
	}
}

func (m uploadAIMapping) toHeaderIndex(rows [][]string) (headerIndex, int, error) {
	return m.toHeaderIndexWithBounds(rows, len(rows), maxColumns(rows))
}

func (m uploadAIMapping) toHeaderIndexWithBounds(
	rows [][]string,
	totalRows int,
	totalCols int,
) (headerIndex, int, error) {
	if len(rows) == 0 {
		return headerIndex{}, 0, errors.New("xlsx has no data")
	}

	headerRow := m.HeaderRow
	if headerRow <= 0 {
		headerRow = 1
	}
	dataStart := m.DataStart
	if dataStart <= 0 {
		dataStart = headerRow + 1
	}
	if dataStart <= headerRow {
		dataStart = headerRow + 1
	}
	if totalRows <= 0 {
		totalRows = len(rows)
	}
	if totalRows > 0 && dataStart > totalRows {
		return headerIndex{}, 0, errors.New("assistant detected data start out of range")
	}

	idx := headerIndex{
		sku:   toZeroBasedColumn(m.SKUCol),
		name:  toZeroBasedColumn(m.NameCol),
		value: toZeroBasedColumn(m.ValueCol),
		price: toZeroBasedColumn(m.PriceCol),
		qty:   toZeroBasedColumn(m.QuantityCol),
	}
	if idx.sku < 0 {
		idx.sku = findColumnByHeaderName(rows, headerRow, m.SKUHeader)
	}
	if idx.name < 0 {
		idx.name = findColumnByHeaderName(rows, headerRow, m.NameHeader)
	}
	if idx.qty < 0 {
		idx.qty = findColumnByHeaderName(rows, headerRow, m.QtyHeader)
	}
	if idx.price < 0 {
		idx.price = findColumnByHeaderName(rows, headerRow, m.PriceHeader)
	}
	if idx.value < 0 {
		idx.value = findColumnByHeaderName(rows, headerRow, m.ValueHeader)
	}

	if idx.name < 0 {
		return headerIndex{}, 0, errors.New("assistant did not detect product name column")
	}
	if idx.qty < 0 {
		return headerIndex{}, 0, errors.New("assistant did not detect quantity column")
	}
	if idx.price < 0 && idx.value < 0 {
		return headerIndex{}, 0, errors.New("assistant did not detect price/value column")
	}

	if totalCols <= 0 {
		totalCols = maxColumns(rows)
	}
	if totalCols <= 0 {
		return headerIndex{}, 0, errors.New("xlsx has no columns")
	}
	for _, col := range []int{idx.sku, idx.name, idx.qty, idx.price, idx.value} {
		if col >= totalCols {
			return headerIndex{}, 0, errors.New("assistant returned out of range column index")
		}
	}

	return idx, dataStart, nil
}

func toZeroBasedColumn(col int) int {
	if col <= 0 {
		return -1
	}
	return col - 1
}

func maxColumns(rows [][]string) int {
	max := 0
	for _, row := range rows {
		if len(row) > max {
			max = len(row)
		}
	}
	return max
}

func findColumnByHeaderName(rows [][]string, headerRow int, raw string) int {
	target := normalizeHeaderLabel(raw)
	if target == "" || len(rows) == 0 {
		return -1
	}

	start := 0
	end := len(rows) - 1
	if headerRow > 0 && headerRow <= len(rows) {
		start = headerRow - 1
		end = start
	}

	for r := start; r <= end; r++ {
		row := rows[r]
		for c, cell := range row {
			label := normalizeHeaderLabel(cell)
			if label == "" {
				continue
			}
			if label == target || strings.Contains(label, target) || strings.Contains(target, label) {
				return c
			}
		}
	}

	// If explicit header row did not match, scan first few rows heuristically.
	limit := len(rows)
	if limit > 5 {
		limit = 5
	}
	for r := 0; r < limit; r++ {
		row := rows[r]
		for c, cell := range row {
			label := normalizeHeaderLabel(cell)
			if label == "" {
				continue
			}
			if label == target || strings.Contains(label, target) || strings.Contains(target, label) {
				return c
			}
		}
	}

	return -1
}
