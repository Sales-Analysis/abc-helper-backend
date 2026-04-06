package abc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	abclib "github.com/Sales-Analysis/abc-helper-lib/abc"
	"github.com/xuri/excelize/v2"
)

// UploadHandler processes an XLSX file upload for ABC analysis:
// - accepts multipart/form-data with a single "file" part;
// - validates extension/structure/rows and required columns;
// - runs ABC analysis using abc-helper-lib;
// - emits structured errors with codes;
// - records Prometheus metrics on success/failure and duration.
//
// @Summary      Upload ABC XLSX
// @Description  Accepts an XLSX file, validates it and returns ABC analysis results.
// @Tags         analysis
// @Accept       multipart/form-data
// @Produce      json
// @Param        file  formData  file  true  "XLSX file"
// @Success      200   {object}  UploadResponse
// @Failure      400   {object}  map[string]any
// @Router       /api/v1/abc/upload [post]
func UploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, ErrMethodNotAllowed, "method not allowed")
		return
	}

	// --- измеряем время выполнения
	start := time.Now()
	defer func() {
		UploadDuration.Observe(time.Since(start).Seconds())
	}()

	file, fh, err := parseUploadFileFromRequest(w, r)
	if err != nil {
		writeUploadError(w, r.Header.Get("X-Request-ID"), err)
		return
	}
	defer func() { _ = file.Close() }()

	resp, err := analyzeUploadFromReader(r.Context(), fh.Filename, file, r.Header.Get("X-Request-ID"))
	if err != nil {
		writeUploadError(w, r.Header.Get("X-Request-ID"), err)
		return
	}
	UploadCounter.WithLabelValues("success").Inc()
	writeOK(w, resp)
}

// UploadResponse is the JSON response for a successful ABC upload.
type UploadResponse struct {
	Status      string                 `json:"status"`
	Result      []abclib.ProductResult `json:"result"`
	Explanation string                 `json:"explanation,omitempty"`
	Preparation *UploadPreparation     `json:"preparation,omitempty"`
}

// UploadPreparation describes how the server normalized incoming XLSX rows.
type UploadPreparation struct {
	Mode         string                    `json:"mode"`
	Confidence   float64                   `json:"confidence,omitempty"`
	HeaderRow    int                       `json:"header_row,omitempty"`
	DataStartRow int                       `json:"data_start_row,omitempty"`
	Columns      *UploadPreparationColumns `json:"columns,omitempty"`
	Available    []UploadPreparationColumn `json:"available_columns,omitempty"`
}

type UploadPreparationColumns struct {
	SKU      UploadPreparationColumn `json:"sku,omitempty"`
	Name     UploadPreparationColumn `json:"name,omitempty"`
	Quantity UploadPreparationColumn `json:"quantity,omitempty"`
	Price    UploadPreparationColumn `json:"price,omitempty"`
	Value    UploadPreparationColumn `json:"value,omitempty"`
}

type UploadPreparationColumn struct {
	Index  int    `json:"index,omitempty"`
	Header string `json:"header,omitempty"`
}

func (p UploadPreparation) hasDetectedColumns() bool {
	if p.Columns == nil {
		return false
	}
	return p.Columns.SKU.Index > 0 ||
		p.Columns.Name.Index > 0 ||
		p.Columns.Quantity.Index > 0 ||
		p.Columns.Price.Index > 0 ||
		p.Columns.Value.Index > 0
}

func (p UploadPreparation) toHeaderIndex(rows [][]string) (headerIndex, int, error) {
	return p.toHeaderIndexWithBounds(rows, len(rows), maxColumns(rows))
}

func (p UploadPreparation) toHeaderIndexWithBounds(
	rows [][]string,
	totalRows int,
	totalCols int,
) (headerIndex, int, error) {
	if len(rows) == 0 {
		return headerIndex{}, 0, errors.New("xlsx has no data")
	}
	if p.Columns == nil {
		return headerIndex{}, 0, errors.New("preparation columns are not provided")
	}

	headerRow := p.HeaderRow
	if headerRow <= 0 {
		headerRow = 1
	}
	dataStart := p.DataStartRow
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
		return headerIndex{}, 0, errors.New("prepared data start is out of range")
	}

	idx := headerIndex{
		sku:   toZeroBasedColumn(p.Columns.SKU.Index),
		name:  toZeroBasedColumn(p.Columns.Name.Index),
		qty:   toZeroBasedColumn(p.Columns.Quantity.Index),
		price: toZeroBasedColumn(p.Columns.Price.Index),
		value: toZeroBasedColumn(p.Columns.Value.Index),
	}
	if idx.sku < 0 && strings.TrimSpace(p.Columns.SKU.Header) != "" {
		idx.sku = findColumnByHeaderName(rows, headerRow, p.Columns.SKU.Header)
	}
	if idx.name < 0 && strings.TrimSpace(p.Columns.Name.Header) != "" {
		idx.name = findColumnByHeaderName(rows, headerRow, p.Columns.Name.Header)
	}
	if idx.qty < 0 && strings.TrimSpace(p.Columns.Quantity.Header) != "" {
		idx.qty = findColumnByHeaderName(rows, headerRow, p.Columns.Quantity.Header)
	}
	if idx.price < 0 && strings.TrimSpace(p.Columns.Price.Header) != "" {
		idx.price = findColumnByHeaderName(rows, headerRow, p.Columns.Price.Header)
	}
	if idx.value < 0 && strings.TrimSpace(p.Columns.Value.Header) != "" {
		idx.value = findColumnByHeaderName(rows, headerRow, p.Columns.Value.Header)
	}

	if idx.name < 0 {
		return headerIndex{}, 0, errors.New("product name column is not detected")
	}
	if idx.qty < 0 {
		return headerIndex{}, 0, errors.New("quantity column is not detected")
	}
	if idx.price < 0 && idx.value < 0 {
		return headerIndex{}, 0, errors.New("price/value column is not detected")
	}

	if totalCols <= 0 {
		totalCols = maxColumns(rows)
	}
	if totalCols <= 0 {
		return headerIndex{}, 0, errors.New("xlsx has no columns")
	}
	for _, col := range []int{idx.sku, idx.name, idx.qty, idx.price, idx.value} {
		if col >= totalCols {
			return headerIndex{}, 0, errors.New("prepared columns are out of range")
		}
	}
	return idx, dataStart, nil
}

func buildUploadPreparation(
	rows [][]string,
	headerRow int,
	dataStart int,
	idx headerIndex,
	mode string,
	confidence float64,
) UploadPreparation {
	if headerRow <= 0 {
		headerRow = 1
	}
	if dataStart <= headerRow {
		dataStart = headerRow + 1
	}
	headerCells := []string{}
	if headerRow-1 >= 0 && headerRow-1 < len(rows) {
		headerCells = rows[headerRow-1]
	}
	preparation := UploadPreparation{
		Mode:         strings.TrimSpace(mode),
		HeaderRow:    headerRow,
		DataStartRow: dataStart,
		Columns: &UploadPreparationColumns{
			SKU:      toUploadPreparationColumn(headerCells, idx.sku),
			Name:     toUploadPreparationColumn(headerCells, idx.name),
			Quantity: toUploadPreparationColumn(headerCells, idx.qty),
			Price:    toUploadPreparationColumn(headerCells, idx.price),
			Value:    toUploadPreparationColumn(headerCells, idx.value),
		},
	}
	if len(headerCells) > 0 {
		available := make([]UploadPreparationColumn, 0, len(headerCells))
		for i := range headerCells {
			available = append(available, toUploadPreparationColumn(headerCells, i))
		}
		preparation.Available = available
	}
	if confidence > 0 {
		preparation.Confidence = confidence
	}
	return preparation
}

func toUploadPreparationColumn(header []string, idx int) UploadPreparationColumn {
	if idx < 0 {
		return UploadPreparationColumn{}
	}
	return UploadPreparationColumn{
		Index:  idx + 1,
		Header: strings.TrimSpace(cellAt(header, idx)),
	}
}

type uploadProgressContextKey struct{}

type uploadProgressReporter struct {
	OnAIPreparing func()
	OnProgress    func(stage string, progress int)
}

const defaultUploadSkipInvalidRows = true

func uploadSkipInvalidRowsEnabled() bool {
	return envBool("ABC_UPLOAD_SKIP_INVALID_ROWS", defaultUploadSkipInvalidRows)
}

func withUploadProgressReporter(ctx context.Context, reporter uploadProgressReporter) context.Context {
	return context.WithValue(ctx, uploadProgressContextKey{}, reporter)
}

func reportUploadAIPreparing(ctx context.Context) {
	if ctx == nil {
		return
	}
	reporter, ok := ctx.Value(uploadProgressContextKey{}).(uploadProgressReporter)
	if !ok || reporter.OnAIPreparing == nil {
		return
	}
	reporter.OnAIPreparing()
}

func reportUploadProgress(ctx context.Context, stage string, progress int) {
	if ctx == nil {
		return
	}
	reporter, ok := ctx.Value(uploadProgressContextKey{}).(uploadProgressReporter)
	if !ok || reporter.OnProgress == nil {
		return
	}
	reporter.OnProgress(strings.TrimSpace(stage), progress)
}

func mapProgressToRange(done, total, start, end int) int {
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	if total <= 0 {
		return end
	}
	if done < 0 {
		done = 0
	}
	if done > total {
		done = total
	}

	span := end - start
	return start + (span*done)/total
}

func newRowProgressReporter(
	ctx context.Context,
	stage string,
	start,
	end int,
) func(done, total int) {
	lastProgress := -1
	return func(done, total int) {
		progress := mapProgressToRange(done, total, start, end)
		if progress <= lastProgress {
			return
		}
		lastProgress = progress
		reportUploadProgress(ctx, stage, progress)
	}
}

func parseUploadFileFromRequest(
	w http.ResponseWriter,
	r *http.Request,
) (multipartFile io.ReadCloser, fh *uploadFileHeader, err error) {
	// ограничиваем тело и парсим multipart
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if parseErr := r.ParseMultipartForm(maxMultipartMemory); parseErr != nil {
		return nil, nil, &parseError{
			code: ErrInvalidForm,
			msg:  "invalid form: " + parseErr.Error(),
		}
	}

	file, fileHeader, formErr := r.FormFile("file")
	if formErr != nil {
		return nil, nil, &parseError{
			code: ErrMissingFile,
			msg:  "file is required",
		}
	}
	if fileHeader.Size == 0 {
		_ = file.Close()
		return nil, nil, &parseError{
			code: ErrEmptyFile,
			msg:  "file is empty",
		}
	}
	return file, &uploadFileHeader{
		Filename: fileHeader.Filename,
		Size:     fileHeader.Size,
	}, nil
}

type multipartFile interface {
	io.ReadCloser
}

type uploadFileHeader struct {
	Filename string
	Size     int64
}

func analyzeUploadFromReader(
	ctx context.Context,
	filename string,
	reader io.Reader,
	requestID string,
) (UploadResponse, error) {
	return analyzeUploadFromReaderWithPreparation(ctx, filename, reader, requestID, nil)
}

func analyzeUploadFromReaderWithPreparation(
	ctx context.Context,
	filename string,
	reader io.Reader,
	requestID string,
	confirmedPreparation *UploadPreparation,
) (UploadResponse, error) {
	if err := validateExtension(filename); err != nil {
		return UploadResponse{}, &parseError{
			code: ErrInvalidExt,
			msg:  "only .xlsx allowed",
		}
	}

	xls, err := openXLSX(reader)
	if err != nil {
		return UploadResponse{}, &parseError{
			code: ErrInvalidXLSX,
			msg:  "invalid xlsx format",
		}
	}
	defer func() { _ = xls.Close() }()

	return analyzeUploadFromWorkbookWithOptions(ctx, xls, requestID, confirmedPreparation, false)
}

func analyzeUploadFromFileWithPreparation(
	ctx context.Context,
	filename string,
	filePath string,
	requestID string,
	confirmedPreparation *UploadPreparation,
) (UploadResponse, error) {
	if err := validateExtension(filename); err != nil {
		return UploadResponse{}, &parseError{
			code: ErrInvalidExt,
			msg:  "only .xlsx allowed",
		}
	}

	if uploadStreamingEnabled() {
		resp, streamErr := analyzeUploadFromFileStreaming(ctx, filePath, requestID, confirmedPreparation)
		if streamErr == nil {
			return resp, nil
		}
		if !shouldFallbackToExcelizeAfterStreaming(streamErr) {
			return UploadResponse{}, streamErr
		}
		slog.Default().Warn("abc_upload_streaming_analyze_fallback",
			slog.String("request_id", strings.TrimSpace(requestID)),
			slog.String("filename", strings.TrimSpace(filename)),
			slog.String("err", strings.TrimSpace(streamErr.Error())),
		)
	}

	xls, err := openXLSXFile(filePath)
	if err != nil {
		return UploadResponse{}, &parseError{
			code: ErrInvalidXLSX,
			msg:  "invalid xlsx format",
		}
	}
	defer func() { _ = xls.Close() }()

	return analyzeUploadFromWorkbookWithOptions(
		ctx,
		xls,
		requestID,
		confirmedPreparation,
		uploadSkipInvalidRowsEnabled(),
	)
}

func analyzeUploadFromWorkbook(
	ctx context.Context,
	xls *excelize.File,
	requestID string,
	confirmedPreparation *UploadPreparation,
) (UploadResponse, error) {
	return analyzeUploadFromWorkbookWithOptions(ctx, xls, requestID, confirmedPreparation, false)
}

func analyzeUploadFromWorkbookWithOptions(
	ctx context.Context,
	xls *excelize.File,
	requestID string,
	confirmedPreparation *UploadPreparation,
	skipInvalidRows bool,
) (UploadResponse, error) {
	sample, err := readUploadWorkbookSample(xls)
	if err != nil {
		switch err.Error() {
		case string(ErrNoSheets):
			return UploadResponse{}, &parseError{
				code: ErrNoSheets,
				msg:  "xlsx has no sheets",
			}
		case string(ErrNoData):
			return UploadResponse{}, &parseError{
				code: ErrNoData,
				msg:  "xlsx has no data",
			}
		default:
			return UploadResponse{}, &parseError{
				code: ErrInvalidXLSX,
				msg:  "invalid xlsx structure",
			}
		}
	}
	reportUploadProgress(ctx, uploadJobStageValidating, 15)

	var (
		idx         headerIndex
		dataStart   int
		results     []abclib.ProductResult
		preparation UploadPreparation
	)

	reportUploadProgress(ctx, uploadJobStageParsingRows, 20)
	if confirmedPreparation != nil {
		idx, dataStart, preparation, err = resolveConfirmedPreparationFromSample(
			sample.Rows,
			sample.TotalRows,
			sample.TotalCols,
			*confirmedPreparation,
		)
	} else {
		idx, dataStart, preparation, err = resolveFallbackPreparationFromSample(
			ctx,
			requestID,
			sample.Rows,
			sample.TotalRows,
			sample.TotalCols,
		)
	}
	if err != nil {
		return UploadResponse{}, err
	}
	results, stats, err := parseResultsFromSheetWithProgressAndOptions(
		xls,
		sample.Sheet,
		idx,
		dataStart,
		sample.TotalRows,
		newRowProgressReporter(ctx, uploadJobStageParsingRows, 28, 82),
		skipInvalidRows,
	)
	if err != nil {
		return UploadResponse{}, err
	}
	if stats.SkippedRows > 0 {
		firstSkipped := ""
		if stats.FirstSkippedErr != nil {
			firstSkipped = strings.TrimSpace(stats.FirstSkippedErr.Error())
		}
		slog.Default().Warn("abc_upload_rows_skipped",
			slog.String("request_id", strings.TrimSpace(requestID)),
			slog.Int("skipped_rows", stats.SkippedRows),
			slog.String("first_error", firstSkipped),
			slog.Bool("skip_invalid_rows", skipInvalidRows),
		)
	}
	reportUploadProgress(ctx, uploadJobStageParsingRows, 84)

	reportUploadProgress(ctx, uploadJobStageCalculating, 88)
	results = calculateABCInPlace(results)
	reportUploadProgress(ctx, uploadJobStageFinalizing, 97)

	resp := UploadResponse{Status: "ok", Result: results}
	if preparation.Mode != "" || preparation.hasDetectedColumns() {
		resp.Preparation = &preparation
	}
	return resp, nil
}

func writeUploadError(w http.ResponseWriter, requestID string, err error) {
	requestID = strings.TrimSpace(requestID)
	if err == nil {
		slog.Default().Error("abc_upload_failed_unexpected",
			slog.String("request_id", requestID),
			slog.String("err", "nil error"),
		)
		writeErr(w, http.StatusBadRequest, ErrInvalidXLSX, "invalid xlsx data")
		return
	}
	if perr := new(parseError); errors.As(err, &perr) {
		slog.Default().Warn("abc_upload_failed",
			slog.String("request_id", requestID),
			slog.String("code", string(perr.code)),
			slog.String("message", strings.TrimSpace(perr.msg)),
		)
		writeErr(w, http.StatusBadRequest, perr.code, perr.msg)
		return
	}
	slog.Default().Error("abc_upload_failed_unexpected",
		slog.String("request_id", requestID),
		slog.String("err", strings.TrimSpace(err.Error())),
	)
	writeErr(w, http.StatusBadRequest, ErrInvalidXLSX, "invalid xlsx data")
}

func resolveConfirmedPreparationFromSample(
	rows [][]string,
	totalRows int,
	totalCols int,
	confirmedPreparation UploadPreparation,
) (headerIndex, int, UploadPreparation, error) {
	idx, dataStart, err := confirmedPreparation.toHeaderIndexWithBounds(rows, totalRows, totalCols)
	if err != nil {
		return headerIndex{}, 0, UploadPreparation{}, &parseError{
			code: ErrInvalidHeader,
			msg:  "invalid confirmed columns: " + strings.TrimSpace(err.Error()),
		}
	}
	if validationErr := validateAIMappingRows(rows, idx, confirmedPreparation.HeaderRow, dataStart); validationErr != nil {
		return headerIndex{}, 0, UploadPreparation{}, &parseError{
			code: ErrInvalidHeader,
			msg:  "invalid confirmed columns: " + strings.TrimSpace(validationErr.Error()),
		}
	}

	mode := strings.TrimSpace(confirmedPreparation.Mode)
	if mode == "" {
		mode = "confirmed"
	}
	normalizedPreparation := buildUploadPreparation(
		rows,
		confirmedPreparation.HeaderRow,
		dataStart,
		idx,
		mode,
		confirmedPreparation.Confidence,
	)
	return idx, dataStart, normalizedPreparation, nil
}

func resolveFallbackPreparationFromSample(
	ctx context.Context,
	requestID string,
	rows [][]string,
	totalRows int,
	totalCols int,
) (headerIndex, int, UploadPreparation, error) {
	if len(rows) == 0 {
		return headerIndex{}, 0, UploadPreparation{}, &parseError{
			code: ErrNoData,
			msg:  "xlsx has no data",
		}
	}

	headerIdx, err := parseHeader(rows[0])
	if err == nil {
		return headerIdx, 2, buildUploadPreparation(rows, 1, 2, headerIdx, "header", 0), nil
	}
	if !shouldTryAIPrepare(err) {
		return headerIndex{}, 0, UploadPreparation{}, err
	}

	reportUploadAIPreparing(ctx)
	reportUploadProgress(ctx, uploadJobStageAIPreparing, 40)
	_, preparation, prepErr := prepareProductsWithAI(ctx, rows, requestID)
	if prepErr == nil {
		idx, dataStart, idxErr := preparation.toHeaderIndexWithBounds(rows, totalRows, totalCols)
		if idxErr != nil {
			return headerIndex{}, 0, UploadPreparation{}, &parseError{
				code: ErrInvalidHeader,
				msg:  "assistant selected invalid columns: " + strings.TrimSpace(idxErr.Error()),
			}
		}
		if validationErr := validateAIMappingRows(rows, idx, preparation.HeaderRow, dataStart); validationErr != nil {
			return headerIndex{}, 0, UploadPreparation{}, &parseError{
				code: ErrInvalidHeader,
				msg:  "assistant selected invalid columns: " + strings.TrimSpace(validationErr.Error()),
			}
		}
		return idx, dataStart, preparation, nil
	}
	if isUploadAIPrepareUnavailable(prepErr) || isUploadAIPrepareTransient(prepErr) {
		return headerIndex{}, 0, UploadPreparation{}, err
	}
	aiParseErr := new(parseError)
	if errors.As(prepErr, &aiParseErr) {
		return headerIndex{}, 0, UploadPreparation{}, aiParseErr
	}

	return headerIndex{}, 0, UploadPreparation{}, &parseError{
		code: ErrUploadPrepFailed,
		msg:  "assistant upload preparation failed: " + strings.TrimSpace(prepErr.Error()),
	}
}

func parseProductsWithFallback(
	ctx context.Context,
	requestID string,
	rows [][]string,
) ([]abclib.Product, UploadPreparation, error) {
	idx, dataStart, preparation, err := resolveFallbackPreparationFromSample(
		ctx,
		requestID,
		rows,
		len(rows),
		maxColumns(rows),
	)
	if err != nil {
		return nil, UploadPreparation{}, err
	}

	start := dataStart - 1
	if start < 0 {
		start = 0
	}
	if start > len(rows) {
		start = len(rows)
	}
	products, parseErr := parseProductsWithProgressFromRow(
		rows[start:],
		idx,
		dataStart,
		newRowProgressReporter(ctx, uploadJobStageParsingRows, 24, 72),
	)
	if parseErr != nil {
		return nil, UploadPreparation{}, parseErr
	}
	reportUploadProgress(ctx, uploadJobStageParsingRows, 80)
	return products, preparation, nil
}

func parseProductsWithConfirmedPreparation(
	ctx context.Context,
	rows [][]string,
	confirmedPreparation UploadPreparation,
) ([]abclib.Product, UploadPreparation, error) {
	idx, dataStart, normalizedPreparation, err := resolveConfirmedPreparationFromSample(
		rows,
		len(rows),
		maxColumns(rows),
		confirmedPreparation,
	)
	if err != nil {
		return nil, UploadPreparation{}, err
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
		newRowProgressReporter(ctx, uploadJobStageParsingRows, 28, 78),
	)
	if err != nil {
		return nil, UploadPreparation{}, err
	}
	reportUploadProgress(ctx, uploadJobStageParsingRows, 82)
	return products, normalizedPreparation, nil
}

func isUploadAIPrepareUnavailable(err error) bool {
	return errors.Is(err, errUploadAIPrepareNotInitialized) ||
		errors.Is(err, errUploadAIPrepareDisabled) ||
		errors.Is(err, errUploadAIPrepareBaseURLNotFound)
}

func shouldTryAIPrepare(err error) bool {
	perr := new(parseError)
	if !errors.As(err, &perr) {
		return false
	}
	switch perr.code {
	case ErrInvalidHeader, ErrNoData, ErrMissingValue, ErrInvalidNumber, ErrInvalidQuantity, ErrInvalidValue:
		return true
	default:
		return false
	}
}
