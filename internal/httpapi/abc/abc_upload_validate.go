// Package abc provides handlers, validation helpers, errors and metrics for ABC analysis XLSX uploads.
package abc

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xuri/excelize/v2"
)

const (
	defaultMaxUploadBytes        int64 = 200 << 20 // 200 MiB
	defaultMultipartMemory       int64 = 32 << 20  // 32 MiB
	defaultMaxUploadBytesMB            = 200
	defaultMultipartMemoryMB           = 32
	defaultXLSXUnzipSizeMB             = 1024
	defaultXLSXUnzipXMLSizeMB          = 4
	defaultMaxWorksheetXMLMB           = 512
	minWorksheetXMLMB                  = 64
	worksheetXMLMemoryDivisor          = 4
	worksheetXMLGoMemoryDivisor        = 4
	defaultUploadSampleScanCols        = 512
	maxUploadSampleScanCols            = 4096
	defaultUploadSampleCellChars       = 96
	maxUploadSampleCellChars           = 256
	defaultUploadSampleScanRows        = 240
	maxUploadSampleScanRows            = 5000
)

var (
	maxUploadBytes      int64 = defaultMaxUploadBytes
	maxMultipartMemory  int64 = defaultMultipartMemory
	xlsxUnzipSizeLimit  int64 = int64(defaultXLSXUnzipSizeMB) << 20
	xlsxUnzipXMLLimit   int64 = int64(defaultXLSXUnzipXMLSizeMB) << 20
	maxWorksheetXMLB    int64 = int64(defaultMaxWorksheetXMLMB) << 20
	uploadLimitsLogOnce sync.Once
)

func init() {
	uploadLimitMB := envInt("ABC_UPLOAD_MAX_MB", defaultMaxUploadBytesMB)
	if uploadLimitMB > 0 {
		maxUploadBytes = int64(uploadLimitMB) << 20
	}

	multipartMemoryMB := envInt("ABC_UPLOAD_MULTIPART_MEMORY_MB", defaultMultipartMemoryMB)
	if multipartMemoryMB > 0 {
		maxMultipartMemory = int64(multipartMemoryMB) << 20
	}

	if maxMultipartMemory > maxUploadBytes {
		maxMultipartMemory = maxUploadBytes
	}

	unzipSizeMB := envInt("ABC_UPLOAD_XLSX_UNZIP_SIZE_MB", defaultXLSXUnzipSizeMB)
	if unzipSizeMB > 0 {
		xlsxUnzipSizeLimit = int64(unzipSizeMB) << 20
	}
	unzipXMLMB := envInt("ABC_UPLOAD_XLSX_UNZIP_XML_MB", defaultXLSXUnzipXMLSizeMB)
	if unzipXMLMB > 0 {
		xlsxUnzipXMLLimit = int64(unzipXMLMB) << 20
	}
	if xlsxUnzipXMLLimit > xlsxUnzipSizeLimit {
		xlsxUnzipXMLLimit = xlsxUnzipSizeLimit
	}

	maxWorksheetXMLMB := envInt("ABC_UPLOAD_MAX_WORKSHEET_XML_MB", defaultMaxWorksheetXMLMB)
	if maxWorksheetXMLMB > 0 {
		maxWorksheetXMLB = int64(maxWorksheetXMLMB) << 20
	}
	maxWorksheetXMLB = clampWorksheetXMLByContainerLimit(maxWorksheetXMLB)
	maxWorksheetXMLB = clampWorksheetXMLByGoMemoryLimit(maxWorksheetXMLB)
}

func logUploadLimits() {
	uploadLimitsLogOnce.Do(func() {
		containerLimitMB := int64(0)
		if containerLimit := detectContainerMemoryLimitBytes(); containerLimit > 0 {
			containerLimitMB = containerLimit >> 20
		}
		goLimitMB := int64(0)
		if goLimit := detectGoMemoryLimitBytes(); goLimit > 0 {
			goLimitMB = goLimit >> 20
		}

		slog.Default().Info("abc_upload_limits",
			slog.Int64("max_upload_mb", maxUploadBytes>>20),
			slog.Int64("multipart_memory_mb", maxMultipartMemory>>20),
			slog.Int64("xlsx_unzip_size_mb", xlsxUnzipSizeLimit>>20),
			slog.Int64("xlsx_unzip_xml_mb", xlsxUnzipXMLLimit>>20),
			slog.Int64("worksheet_xml_limit_mb", maxWorksheetXMLB>>20),
			slog.Int64("container_memory_limit_mb", containerLimitMB),
			slog.Int64("go_memory_limit_mb", goLimitMB),
			slog.String("gomemlimit", strings.TrimSpace(os.Getenv("GOMEMLIMIT"))),
		)
	})
}

func clampWorksheetXMLByContainerLimit(currentLimit int64) int64 {
	if currentLimit <= 0 {
		return currentLimit
	}
	containerLimit := detectContainerMemoryLimitBytes()
	if containerLimit <= 0 {
		return currentLimit
	}
	safeLimit := containerLimit / worksheetXMLMemoryDivisor
	minLimit := int64(minWorksheetXMLMB) << 20
	if safeLimit < minLimit {
		safeLimit = minLimit
	}
	if safeLimit >= currentLimit {
		return currentLimit
	}
	return safeLimit
}

func detectContainerMemoryLimitBytes() int64 {
	paths := []string{
		"/sys/fs/cgroup/memory.max",
		"/sys/fs/cgroup/memory/memory.limit_in_bytes",
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		value := strings.TrimSpace(string(raw))
		if value == "" || strings.EqualFold(value, "max") {
			continue
		}
		n, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || n <= 0 {
			continue
		}
		// cgroup may expose a huge "unlimited" value.
		if n >= (1 << 60) {
			continue
		}
		return n
	}
	return 0
}

func clampWorksheetXMLByGoMemoryLimit(currentLimit int64) int64 {
	if currentLimit <= 0 {
		return currentLimit
	}
	goLimit := detectGoMemoryLimitBytes()
	if goLimit <= 0 {
		return currentLimit
	}
	safeLimit := goLimit / worksheetXMLGoMemoryDivisor
	minLimit := int64(minWorksheetXMLMB) << 20
	if safeLimit < minLimit {
		safeLimit = minLimit
	}
	if safeLimit >= currentLimit {
		return currentLimit
	}
	return safeLimit
}

func detectGoMemoryLimitBytes() int64 {
	raw := strings.TrimSpace(os.Getenv("GOMEMLIMIT"))
	if raw == "" {
		return 0
	}
	limit, ok := parseMemoryLimitBytes(raw)
	if !ok || limit <= 0 {
		return 0
	}
	return limit
}

func parseMemoryLimitBytes(raw string) (int64, bool) {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return 0, false
	}
	normalized = strings.ReplaceAll(normalized, "_", "")
	if value, err := strconv.ParseInt(normalized, 10, 64); err == nil {
		if value <= 0 {
			return 0, false
		}
		return value, true
	}

	cut := 0
	for cut < len(normalized) {
		ch := normalized[cut]
		if ch < '0' || ch > '9' {
			break
		}
		cut++
	}
	if cut == 0 || cut >= len(normalized) {
		return 0, false
	}

	value, err := strconv.ParseInt(normalized[:cut], 10, 64)
	if err != nil || value <= 0 {
		return 0, false
	}
	unit := strings.ToLower(strings.TrimSpace(normalized[cut:]))
	multiplier, ok := memoryUnitMultiplier(unit)
	if !ok {
		return 0, false
	}

	const maxInt64 = int64(^uint64(0) >> 1)
	if value > maxInt64/multiplier {
		return 0, false
	}
	return value * multiplier, true
}

func memoryUnitMultiplier(unit string) (int64, bool) {
	switch unit {
	case "b":
		return 1, true
	case "k", "kb":
		return 1000, true
	case "ki", "kib":
		return 1 << 10, true
	case "m", "mb":
		return 1000 * 1000, true
	case "mi", "mib":
		return 1 << 20, true
	case "g", "gb":
		return 1000 * 1000 * 1000, true
	case "gi", "gib":
		return 1 << 30, true
	case "t", "tb":
		return 1000 * 1000 * 1000 * 1000, true
	case "ti", "tib":
		return 1 << 40, true
	default:
		return 0, false
	}
}

// validateExtension checks that the file extension is .xlsx.
func validateExtension(filename string) error {
	if ext := strings.ToLower(filepath.Ext(filename)); ext != ".xlsx" {
		return errors.New(string(ErrInvalidExt))
	}
	return nil
}

// openXLSX opens a workbook from stream and ensures it is a valid XLSX.
func openXLSX(reader io.Reader) (*excelize.File, error) {
	logUploadLimits()
	startedAt := time.Now()
	slog.Default().Info("abc_upload_open_xlsx_started")
	opts := excelize.Options{
		RawCellValue:      true,
		UnzipSizeLimit:    xlsxUnzipSizeLimit,
		UnzipXMLSizeLimit: xlsxUnzipXMLLimit,
	}
	f, err := excelize.OpenReader(reader, opts)
	if err != nil {
		slog.Default().Warn("abc_upload_open_xlsx_failed",
			slog.String("err", strings.TrimSpace(err.Error())),
			slog.Duration("duration", time.Since(startedAt)),
		)
		return nil, errors.New(string(ErrInvalidXLSX))
	}
	slog.Default().Info("abc_upload_open_xlsx_done",
		slog.Duration("duration", time.Since(startedAt)),
	)
	return f, nil
}

func openXLSXFile(path string) (*excelize.File, error) {
	logUploadLimits()
	startedAt := time.Now()
	slog.Default().Info("abc_upload_open_xlsx_file_started",
		slog.String("file", filepath.Base(strings.TrimSpace(path))),
	)
	opts := excelize.Options{
		RawCellValue:      true,
		UnzipSizeLimit:    xlsxUnzipSizeLimit,
		UnzipXMLSizeLimit: xlsxUnzipXMLLimit,
	}
	f, err := excelize.OpenFile(path, opts)
	if err != nil {
		slog.Default().Warn("abc_upload_open_xlsx_file_failed",
			slog.String("file", filepath.Base(strings.TrimSpace(path))),
			slog.String("err", strings.TrimSpace(err.Error())),
			slog.Duration("duration", time.Since(startedAt)),
		)
		return nil, errors.New(string(ErrInvalidXLSX))
	}
	slog.Default().Info("abc_upload_open_xlsx_file_done",
		slog.String("file", filepath.Base(strings.TrimSpace(path))),
		slog.Duration("duration", time.Since(startedAt)),
	)
	return f, nil
}

type uploadWorkbookSample struct {
	Sheet     string
	Rows      [][]string
	TotalRows int
	TotalCols int
}

func readUploadWorkbookSample(f *excelize.File) (uploadWorkbookSample, error) {
	if f == nil {
		return uploadWorkbookSample{}, errors.New(string(ErrInvalidXLSX))
	}
	startedAt := time.Now()
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return uploadWorkbookSample{}, errors.New(string(ErrNoSheets))
	}
	sheet := sheets[0]

	totalRows, totalCols := estimateSheetBounds(f, sheet)

	scanRows := resolveUploadSampleScanRows()
	if totalRows > 1 && scanRows > totalRows {
		scanRows = totalRows
	}
	rows, err := collectSheetSampleRows(f, sheet, scanRows)
	if err != nil {
		return uploadWorkbookSample{}, err
	}
	if len(rows) == 0 {
		return uploadWorkbookSample{}, errors.New(string(ErrNoData))
	}

	if totalRows <= 0 || totalRows < len(rows) {
		totalRows = len(rows)
	}
	if cols := maxColumns(rows); totalCols <= 0 || totalCols < cols {
		totalCols = cols
	}
	if totalRows < 2 && len(rows) < 2 {
		return uploadWorkbookSample{}, errors.New(string(ErrNoData))
	}
	slog.Default().Info("abc_upload_sample_read_done",
		slog.String("sheet", sheet),
		slog.Int("rows_sampled", len(rows)),
		slog.Int("total_rows", totalRows),
		slog.Int("total_cols", totalCols),
		slog.Duration("duration", time.Since(startedAt)),
	)
	return uploadWorkbookSample{
		Sheet:     sheet,
		Rows:      rows,
		TotalRows: totalRows,
		TotalCols: totalCols,
	}, nil
}

func resolveUploadSampleScanRows() int {
	sampleRows := envInt("ASSISTANT_UPLOAD_PREP_SAMPLE_ROWS", defaultUploadAIPrepareSampleRows)
	if sampleRows <= 0 {
		sampleRows = defaultUploadAIPrepareSampleRows
	}
	if sampleRows > 120 {
		sampleRows = 120
	}
	scanRows := sampleRows * 40
	if scanRows < defaultUploadSampleScanRows {
		scanRows = defaultUploadSampleScanRows
	}
	if scanRows > maxUploadSampleScanRows {
		scanRows = maxUploadSampleScanRows
	}
	return scanRows
}

func collectSheetSampleRows(f *excelize.File, sheet string, rowLimit int) ([][]string, error) {
	stream, err := f.Rows(sheet)
	if err != nil {
		return nil, errors.New(string(ErrInvalidXLSX))
	}
	defer func() { _ = stream.Close() }()

	if rowLimit <= 0 {
		rowLimit = defaultUploadSampleScanRows
	}
	colLimit := resolveUploadSampleScanCols()
	cellLimit := resolveUploadSampleCellChars()

	rows := make([][]string, 0, rowLimit)
	rowNum := 0
	for stream.Next() {
		rowNum++
		row, rowErr := stream.Columns()
		if rowErr != nil {
			return nil, errors.New(string(ErrInvalidXLSX))
		}
		rows = append(rows, compactUploadSampleRow(row, colLimit, cellLimit))
		if rowNum >= rowLimit {
			break
		}
	}
	if err := stream.Error(); err != nil {
		return nil, errors.New(string(ErrInvalidXLSX))
	}
	return rows, nil
}

func resolveUploadSampleScanCols() int {
	colLimit := envInt("ABC_UPLOAD_SAMPLE_SCAN_COLS", defaultUploadSampleScanCols)
	if colLimit <= 0 {
		colLimit = defaultUploadSampleScanCols
	}
	if colLimit > maxUploadSampleScanCols {
		colLimit = maxUploadSampleScanCols
	}
	return colLimit
}

func resolveUploadSampleCellChars() int {
	cellLimit := envInt("ABC_UPLOAD_SAMPLE_CELL_CHARS", defaultUploadSampleCellChars)
	if cellLimit <= 0 {
		cellLimit = defaultUploadSampleCellChars
	}
	if cellLimit > maxUploadSampleCellChars {
		cellLimit = maxUploadSampleCellChars
	}
	return cellLimit
}

func compactUploadSampleRow(row []string, colLimit, cellLimit int) []string {
	if len(row) == 0 {
		return nil
	}
	if colLimit <= 0 {
		colLimit = defaultUploadSampleScanCols
	}
	if colLimit > len(row) {
		colLimit = len(row)
	}
	compact := make([]string, colLimit)
	lastNonEmpty := -1
	for i := 0; i < colLimit; i++ {
		cell := compactUploadSampleCell(row[i], cellLimit)
		compact[i] = cell
		if strings.TrimSpace(cell) != "" {
			lastNonEmpty = i
		}
	}
	if lastNonEmpty < 0 {
		return nil
	}
	return compact[:lastNonEmpty+1]
}

func compactUploadSampleCell(raw string, cellLimit int) string {
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

func validateXLSXWorksheetSizeBudget(filePath string) error {
	logUploadLimits()
	if strings.TrimSpace(filePath) == "" || maxWorksheetXMLB <= 0 {
		return nil
	}

	archive, err := zip.OpenReader(filePath)
	if err != nil {
		slog.Default().Warn("abc_upload_worksheet_budget_invalid_archive",
			slog.String("file", filepath.Base(strings.TrimSpace(filePath))),
			slog.String("err", strings.TrimSpace(err.Error())),
		)
		return errors.New("invalid xlsx format")
	}
	defer func() { _ = archive.Close() }()

	var largestWorksheet int64
	for _, f := range archive.File {
		name := strings.ToLower(strings.TrimSpace(f.Name))
		if !strings.HasPrefix(name, "xl/worksheets/") || !strings.HasSuffix(name, ".xml") {
			continue
		}
		size := int64(f.UncompressedSize64)
		if size > largestWorksheet {
			largestWorksheet = size
		}
	}
	slog.Default().Info("abc_upload_worksheet_budget_checked",
		slog.String("file", filepath.Base(strings.TrimSpace(filePath))),
		slog.Int64("largest_worksheet_mb", largestWorksheet>>20),
		slog.Int64("limit_mb", maxWorksheetXMLB>>20),
	)
	if largestWorksheet > maxWorksheetXMLB {
		slog.Default().Warn("abc_upload_worksheet_budget_exceeded",
			slog.String("file", filepath.Base(strings.TrimSpace(filePath))),
			slog.Int64("largest_worksheet_mb", largestWorksheet>>20),
			slog.Int64("limit_mb", maxWorksheetXMLB>>20),
		)
		return fmt.Errorf(
			"xlsx worksheet xml is too large: %d MB (limit %d MB)",
			largestWorksheet>>20,
			maxWorksheetXMLB>>20,
		)
	}
	return nil
}

func estimateSheetBounds(f *excelize.File, sheet string) (int, int) {
	if f == nil || strings.TrimSpace(sheet) == "" {
		return 0, 0
	}
	dim, err := f.GetSheetDimension(sheet)
	if err != nil {
		return 0, 0
	}
	return parseSheetDimension(dim)
}

func parseSheetDimension(raw string) (rows, cols int) {
	dim := strings.TrimSpace(raw)
	if dim == "" {
		return 0, 0
	}

	endRef := dim
	if idx := strings.LastIndex(endRef, ":"); idx >= 0 && idx+1 < len(endRef) {
		endRef = endRef[idx+1:]
	}
	if idx := strings.LastIndex(endRef, "!"); idx >= 0 && idx+1 < len(endRef) {
		endRef = endRef[idx+1:]
	}
	endRef = strings.TrimSpace(strings.ReplaceAll(endRef, "$", ""))
	if endRef == "" {
		return 0, 0
	}

	col, row, err := excelize.CellNameToCoordinates(endRef)
	if err != nil {
		return 0, 0
	}
	if col <= 0 || row <= 0 {
		return 0, 0
	}
	return row, col
}
