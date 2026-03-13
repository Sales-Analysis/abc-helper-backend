package abc

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	abclib "github.com/Sales-Analysis/abc-helper-lib/abc"
)

const defaultUploadStreamingEnabled = true

type uploadStreamingWorkbook struct {
	archive       *zip.ReadCloser
	sheetPath     string
	sheetName     string
	sharedStrings []string
}

type worksheetRowXML struct {
	RowNumber int                `xml:"r,attr"`
	Cells     []worksheetCellXML `xml:"c"`
}

type worksheetCellXML struct {
	Ref    string           `xml:"r,attr"`
	Type   string           `xml:"t,attr"`
	Value  string           `xml:"v"`
	Inline inlineStringXML  `xml:"is"`
	Runs   []richTextRunXML `xml:"r"`
	Text   string           `xml:"t"`
}

type inlineStringXML struct {
	Text string           `xml:"t"`
	Runs []richTextRunXML `xml:"r"`
}

type richTextRunXML struct {
	Text string `xml:"t"`
}

type sharedStringItemXML struct {
	Text string           `xml:"t"`
	Runs []richTextRunXML `xml:"r"`
}

func uploadStreamingEnabled() bool {
	return envBool("ABC_UPLOAD_STREAMING_ENABLED", defaultUploadStreamingEnabled)
}

func shouldFallbackToExcelizeAfterStreaming(err error) bool {
	if err == nil {
		return false
	}
	perr := new(parseError)
	if errors.As(err, &perr) {
		return perr.code == ErrInvalidXLSX
	}
	return true
}

func openUploadStreamingWorkbook(filePath string) (*uploadStreamingWorkbook, error) {
	startedAt := time.Now()
	slog.Default().Info("abc_upload_streaming_open_started")

	archive, err := zip.OpenReader(filePath)
	if err != nil {
		return nil, &parseError{
			code: ErrInvalidXLSX,
			msg:  "invalid xlsx format",
		}
	}

	sheetPath, sheetName, resolveErr := resolveFirstWorksheetPath(archive)
	if resolveErr != nil {
		_ = archive.Close()
		return nil, &parseError{
			code: ErrInvalidXLSX,
			msg:  "invalid xlsx structure",
		}
	}

	sharedStrings, sharedErr := loadSharedStringsFromArchive(archive)
	if sharedErr != nil {
		_ = archive.Close()
		return nil, &parseError{
			code: ErrInvalidXLSX,
			msg:  "invalid xlsx structure",
		}
	}

	slog.Default().Info("abc_upload_streaming_open_done",
		slog.String("sheet", sheetName),
		slog.Int("shared_strings", len(sharedStrings)),
		slog.Duration("duration", time.Since(startedAt)),
	)
	return &uploadStreamingWorkbook{
		archive:       archive,
		sheetPath:     sheetPath,
		sheetName:     sheetName,
		sharedStrings: sharedStrings,
	}, nil
}

func (w *uploadStreamingWorkbook) close() {
	if w == nil || w.archive == nil {
		return
	}
	_ = w.archive.Close()
}

func (w *uploadStreamingWorkbook) readSample() (uploadWorkbookSample, error) {
	if w == nil {
		return uploadWorkbookSample{}, &parseError{code: ErrInvalidXLSX, msg: "invalid xlsx structure"}
	}
	startedAt := time.Now()
	rowLimit := resolveUploadSampleScanRows()
	colLimit := resolveUploadSampleScanCols()
	cellLimit := resolveUploadSampleCellChars()

	rows := make([][]string, 0, rowLimit)
	totalRows := 0
	totalCols := 0

	_, metaRows, metaCols, scanErr := w.scanRows(rowLimit, func(rowNum int, row worksheetRowXML) error {
		sampleRow, lastCol := buildSampleRowFromWorksheetRow(row.Cells, colLimit, cellLimit, w.sharedStrings)
		rows = append(rows, sampleRow)
		if rowNum > totalRows {
			totalRows = rowNum
		}
		if lastCol > totalCols {
			totalCols = lastCol
		}
		return nil
	})
	if scanErr != nil {
		return uploadWorkbookSample{}, &parseError{code: ErrInvalidXLSX, msg: "invalid xlsx structure"}
	}

	if metaRows > totalRows {
		totalRows = metaRows
	}
	if metaCols > totalCols {
		totalCols = metaCols
	}
	if len(rows) == 0 {
		return uploadWorkbookSample{}, &parseError{code: ErrNoData, msg: "xlsx has no data"}
	}
	if totalRows <= 0 || totalRows < len(rows) {
		totalRows = len(rows)
	}
	if cols := maxColumns(rows); cols > totalCols {
		totalCols = cols
	}
	if totalRows < 2 && len(rows) < 2 {
		return uploadWorkbookSample{}, &parseError{code: ErrNoData, msg: "xlsx has no data"}
	}

	slog.Default().Info("abc_upload_sample_read_done",
		slog.String("sheet", strings.TrimSpace(w.sheetName)),
		slog.Int("rows_sampled", len(rows)),
		slog.Int("total_rows", totalRows),
		slog.Int("total_cols", totalCols),
		slog.Duration("duration", time.Since(startedAt)),
	)
	return uploadWorkbookSample{
		Sheet:     w.sheetName,
		Rows:      rows,
		TotalRows: totalRows,
		TotalCols: totalCols,
	}, nil
}

func (w *uploadStreamingWorkbook) parseResults(
	idx headerIndex,
	dataStartRow int,
	totalRows int,
	onProgress func(done, total int),
) ([]abclib.ProductResult, parseResultsStats, error) {
	if w == nil {
		return nil, parseResultsStats{}, &parseError{code: ErrInvalidXLSX, msg: "invalid xlsx structure"}
	}
	if dataStartRow <= 0 {
		dataStartRow = 2
	}

	totalParseRows := 0
	if totalRows >= dataStartRow {
		totalParseRows = totalRows - dataStartRow + 1
	}
	if totalParseRows < 0 {
		totalParseRows = 0
	}

	estimatedCap := totalParseRows
	if estimatedCap < 0 {
		estimatedCap = 0
	}
	if estimatedCap > maxUploadProductsPrealloc {
		estimatedCap = maxUploadProductsPrealloc
	}
	type aggregatedProduct struct {
		SKU        string
		Name       string
		Quantity   int
		PriceTotal float64
	}
	productsByKey := make(map[string]*aggregatedProduct, estimatedCap)
	processed := 0
	stats := parseResultsStats{}
	maxCol := maxRequiredColumn(idx) + 1
	if maxCol <= 0 {
		maxCol = 1
	}
	skipInvalidRows := uploadSkipInvalidRowsEnabled()

	_, _, _, scanErr := w.scanRows(0, func(rowNum int, row worksheetRowXML) error {
		if rowNum < dataStartRow {
			return nil
		}
		processed++
		rowValues := buildRowValuesForColumns(row.Cells, maxCol, w.sharedStrings)
		if isRowEmpty(rowValues) {
			if onProgress != nil {
				onProgress(processed, totalParseRows)
			}
			return nil
		}

		product, parseErr := parseProductFromRow(rowValues, idx, rowNum)
		if parseErr != nil {
			if skipInvalidRows && isSkippableRowParseError(parseErr) {
				stats.SkippedRows++
				if stats.FirstSkippedErr == nil {
					stats.FirstSkippedErr = parseErr
				}
				if onProgress != nil {
					onProgress(processed, totalParseRows)
				}
				return nil
			}
			return parseErr
		}
		priceTotal := float64(product.Quantity) * product.Price
		skuKey := strings.TrimSpace(cellAt(rowValues, idx.sku))
		productKey := buildAggregatedProductKey(skuKey, product.Name)
		item, exists := productsByKey[productKey]
		if !exists {
			item = &aggregatedProduct{
				SKU:        strings.TrimSpace(product.SKU),
				Name:       strings.TrimSpace(product.Name),
				Quantity:   0,
				PriceTotal: 0,
			}
			if strings.TrimSpace(skuKey) != "" {
				item.SKU = strings.TrimSpace(skuKey)
			}
			productsByKey[productKey] = item
		}
		item.Quantity += product.Quantity
		item.PriceTotal += priceTotal

		if onProgress != nil {
			onProgress(processed, totalParseRows)
		}
		return nil
	})
	if scanErr != nil {
		return nil, stats, scanErr
	}
	if len(productsByKey) == 0 {
		if stats.FirstSkippedErr != nil {
			return nil, stats, stats.FirstSkippedErr
		}
		return nil, stats, &parseError{code: ErrNoData, msg: "xlsx has no data"}
	}

	results := make([]abclib.ProductResult, 0, len(productsByKey))
	for _, item := range productsByKey {
		priceUnit := 0.0
		if item.Quantity > 0 {
			priceUnit = item.PriceTotal / float64(item.Quantity)
		}
		results = append(results, abclib.ProductResult{
			SKU:        strings.TrimSpace(item.SKU),
			Name:       strings.TrimSpace(item.Name),
			Quantity:   item.Quantity,
			PriceUnit:  priceUnit,
			PriceTotal: item.PriceTotal,
		})
	}
	return results, stats, nil
}

func (w *uploadStreamingWorkbook) scanRows(
	rowLimit int,
	onRow func(rowNum int, row worksheetRowXML) error,
) (rowsScanned int, totalRows int, totalCols int, err error) {
	sheetFile := findArchiveFile(w.archive, w.sheetPath)
	if sheetFile == nil {
		return 0, 0, 0, errors.New("worksheet xml not found")
	}
	rc, openErr := sheetFile.Open()
	if openErr != nil {
		return 0, 0, 0, openErr
	}
	defer func() { _ = rc.Close() }()

	decoder := xml.NewDecoder(rc)
	nextRowNum := 1
	for {
		tok, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			return rowsScanned, totalRows, totalCols, tokenErr
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}

		switch start.Name.Local {
		case "dimension":
			ref := xmlAttrValue(start.Attr, "ref")
			rows, cols := parseSheetDimension(ref)
			if rows > totalRows {
				totalRows = rows
			}
			if cols > totalCols {
				totalCols = cols
			}
		case "row":
			var row worksheetRowXML
			if decodeErr := decoder.DecodeElement(&row, &start); decodeErr != nil {
				return rowsScanned, totalRows, totalCols, decodeErr
			}

			rowNum := row.RowNumber
			if rowNum <= 0 {
				rowNum = nextRowNum
			}
			if rowNum >= nextRowNum {
				nextRowNum = rowNum + 1
			} else {
				nextRowNum++
			}

			if rowNum > totalRows {
				totalRows = rowNum
			}
			rowMaxCol := worksheetRowMaxColumn(row.Cells)
			if rowMaxCol > totalCols {
				totalCols = rowMaxCol
			}

			rowsScanned++
			if onRow != nil {
				if callbackErr := onRow(rowNum, row); callbackErr != nil {
					return rowsScanned, totalRows, totalCols, callbackErr
				}
			}
			if rowLimit > 0 && rowsScanned >= rowLimit {
				return rowsScanned, totalRows, totalCols, nil
			}
		}
	}

	return rowsScanned, totalRows, totalCols, nil
}

func resolveFirstWorksheetPath(archive *zip.ReadCloser) (string, string, error) {
	if archive == nil {
		return "", "", errors.New("empty archive")
	}

	workbook := findArchiveFile(archive, "xl/workbook.xml")
	if workbook != nil {
		relationID, sheetName, ridErr := findFirstWorkbookSheetRelation(workbook)
		if ridErr == nil && strings.TrimSpace(relationID) != "" {
			relationships := findArchiveFile(archive, "xl/_rels/workbook.xml.rels")
			if relationships != nil {
				if sheetPath, relErr := resolveWorksheetTargetByRelation(relationships, relationID); relErr == nil {
					if findArchiveFile(archive, sheetPath) != nil {
						return sheetPath, sheetName, nil
					}
				}
			}
		}
	}

	candidates := make([]string, 0, len(archive.File))
	for _, f := range archive.File {
		name := normalizeArchivePath(f.Name)
		if strings.HasPrefix(name, "xl/worksheets/") && strings.HasSuffix(name, ".xml") {
			candidates = append(candidates, name)
		}
	}
	if len(candidates) == 0 {
		return "", "", errors.New("no worksheet files")
	}
	sort.Strings(candidates)
	sheetPath := candidates[0]
	return sheetPath, path.Base(sheetPath), nil
}

func findFirstWorkbookSheetRelation(workbook *zip.File) (string, string, error) {
	rc, err := workbook.Open()
	if err != nil {
		return "", "", err
	}
	defer func() { _ = rc.Close() }()

	decoder := xml.NewDecoder(rc)
	for {
		tok, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			return "", "", tokenErr
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "sheet" {
			continue
		}
		relationID := ""
		sheetName := strings.TrimSpace(xmlAttrValue(start.Attr, "name"))
		for _, attr := range start.Attr {
			if attr.Name.Local == "id" {
				relationID = strings.TrimSpace(attr.Value)
				break
			}
		}
		if relationID != "" {
			return relationID, sheetName, nil
		}
	}

	return "", "", errors.New("workbook has no sheets")
}

func resolveWorksheetTargetByRelation(relationships *zip.File, relationID string) (string, error) {
	relationID = strings.TrimSpace(relationID)
	if relationID == "" {
		return "", errors.New("empty relationship id")
	}

	rc, err := relationships.Open()
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }()

	decoder := xml.NewDecoder(rc)
	for {
		tok, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			return "", tokenErr
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "Relationship" {
			continue
		}

		id := strings.TrimSpace(xmlAttrValue(start.Attr, "Id"))
		target := strings.TrimSpace(xmlAttrValue(start.Attr, "Target"))
		if id != relationID || target == "" {
			continue
		}
		normalized := normalizeWorkbookTarget(target)
		if normalized == "" {
			continue
		}
		return normalized, nil
	}

	return "", errors.New("worksheet relation target not found")
}

func normalizeWorkbookTarget(target string) string {
	target = strings.TrimSpace(strings.ReplaceAll(target, "\\", "/"))
	if target == "" {
		return ""
	}
	if strings.HasPrefix(target, "/") {
		target = strings.TrimPrefix(target, "/")
		return normalizeArchivePath(target)
	}
	cleaned := path.Clean(path.Join("xl", target))
	cleaned = normalizeArchivePath(cleaned)
	if strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return ""
	}
	return cleaned
}

func findArchiveFile(archive *zip.ReadCloser, filePath string) *zip.File {
	if archive == nil {
		return nil
	}
	target := normalizeArchivePath(filePath)
	for _, f := range archive.File {
		if normalizeArchivePath(f.Name) == target {
			return f
		}
	}
	return nil
}

func normalizeArchivePath(raw string) string {
	normalized := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	normalized = path.Clean(normalized)
	normalized = strings.TrimPrefix(normalized, "./")
	normalized = strings.TrimPrefix(normalized, "/")
	return normalized
}

func loadSharedStringsFromArchive(archive *zip.ReadCloser) ([]string, error) {
	shared := findArchiveFile(archive, "xl/sharedStrings.xml")
	if shared == nil {
		return nil, nil
	}

	rc, err := shared.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()

	decoder := xml.NewDecoder(rc)
	values := make([]string, 0, 1024)
	for {
		tok, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			return nil, tokenErr
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "si" {
			continue
		}

		var item sharedStringItemXML
		if decodeErr := decoder.DecodeElement(&item, &start); decodeErr != nil {
			return nil, decodeErr
		}
		values = append(values, flattenRichText(item.Text, item.Runs))
	}

	return values, nil
}

func buildSampleRowFromWorksheetRow(
	cells []worksheetCellXML,
	colLimit int,
	cellLimit int,
	sharedStrings []string,
) ([]string, int) {
	if colLimit <= 0 {
		colLimit = defaultUploadSampleScanCols
	}
	if cellLimit <= 0 {
		cellLimit = defaultUploadSampleCellChars
	}
	row := make([]string, colLimit)
	lastNonEmpty := -1
	maxCol := 0
	nextCol := 0

	for _, cell := range cells {
		col := nextCol
		if parsedCol, ok := worksheetCellColumnIndex(cell.Ref); ok {
			col = parsedCol
		}
		if col < 0 {
			continue
		}
		nextCol = col + 1
		if col+1 > maxCol {
			maxCol = col + 1
		}
		if col >= colLimit {
			continue
		}

		value := compactUploadSampleCell(worksheetCellValue(cell, sharedStrings), cellLimit)
		if strings.TrimSpace(value) == "" {
			continue
		}
		row[col] = value
		if col > lastNonEmpty {
			lastNonEmpty = col
		}
	}

	if lastNonEmpty < 0 {
		return nil, maxCol
	}
	return row[:lastNonEmpty+1], maxCol
}

func buildRowValuesForColumns(
	cells []worksheetCellXML,
	requiredCols int,
	sharedStrings []string,
) []string {
	if requiredCols <= 0 {
		requiredCols = 1
	}
	row := make([]string, requiredCols)
	nextCol := 0
	for _, cell := range cells {
		col := nextCol
		if parsedCol, ok := worksheetCellColumnIndex(cell.Ref); ok {
			col = parsedCol
		}
		if col < 0 {
			continue
		}
		nextCol = col + 1
		if col >= requiredCols {
			continue
		}
		row[col] = worksheetCellValue(cell, sharedStrings)
	}
	return row
}

func worksheetCellValue(cell worksheetCellXML, sharedStrings []string) string {
	cellType := strings.TrimSpace(cell.Type)
	switch cellType {
	case "s":
		idx, err := strconv.Atoi(strings.TrimSpace(cell.Value))
		if err != nil || idx < 0 || idx >= len(sharedStrings) {
			return ""
		}
		return sharedStrings[idx]
	case "inlineStr":
		inline := flattenRichText(cell.Inline.Text, cell.Inline.Runs)
		if inline != "" {
			return inline
		}
		return flattenRichText(cell.Text, cell.Runs)
	default:
		if cell.Value != "" {
			return cell.Value
		}
		inline := flattenRichText(cell.Inline.Text, cell.Inline.Runs)
		if inline != "" {
			return inline
		}
		return flattenRichText(cell.Text, cell.Runs)
	}
}

func flattenRichText(text string, runs []richTextRunXML) string {
	if text != "" {
		return text
	}
	if len(runs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, run := range runs {
		b.WriteString(run.Text)
	}
	return b.String()
}

func xmlAttrValue(attrs []xml.Attr, local string) string {
	local = strings.TrimSpace(local)
	if local == "" {
		return ""
	}
	for _, attr := range attrs {
		if strings.EqualFold(attr.Name.Local, local) {
			return strings.TrimSpace(attr.Value)
		}
	}
	return ""
}

func worksheetCellColumnIndex(cellRef string) (int, bool) {
	ref := strings.TrimSpace(cellRef)
	if ref == "" {
		return 0, false
	}

	col := 0
	seenLetter := false
	for _, r := range ref {
		switch {
		case r >= 'A' && r <= 'Z':
			seenLetter = true
			col = col*26 + int(r-'A'+1)
		case r >= 'a' && r <= 'z':
			seenLetter = true
			col = col*26 + int(r-'a'+1)
		default:
			if seenLetter {
				return col - 1, true
			}
			return 0, false
		}
	}
	if !seenLetter {
		return 0, false
	}
	return col - 1, true
}

func worksheetRowMaxColumn(cells []worksheetCellXML) int {
	maxCol := 0
	nextCol := 0
	for _, cell := range cells {
		col := nextCol
		if parsedCol, ok := worksheetCellColumnIndex(cell.Ref); ok {
			col = parsedCol
		}
		if col < 0 {
			continue
		}
		nextCol = col + 1
		if col+1 > maxCol {
			maxCol = col + 1
		}
	}
	return maxCol
}

func maxRequiredColumn(idx headerIndex) int {
	maxCol := idx.name
	for _, candidate := range []int{idx.sku, idx.qty, idx.price, idx.value} {
		if candidate > maxCol {
			maxCol = candidate
		}
	}
	return maxCol
}

func analyzeUploadFromFileStreaming(
	ctx context.Context,
	filePath string,
	requestID string,
	confirmedPreparation *UploadPreparation,
) (UploadResponse, error) {
	workbook, err := openUploadStreamingWorkbook(filePath)
	if err != nil {
		return UploadResponse{}, err
	}
	defer workbook.close()

	sample, err := workbook.readSample()
	if err != nil {
		return UploadResponse{}, err
	}
	reportUploadProgress(ctx, uploadJobStageValidating, 15)
	reportUploadProgress(ctx, uploadJobStageParsingRows, 20)

	var (
		idx         headerIndex
		dataStart   int
		preparation UploadPreparation
	)
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

	results, stats, err := workbook.parseResults(
		idx,
		dataStart,
		sample.TotalRows,
		newRowProgressReporter(ctx, uploadJobStageParsingRows, 28, 82),
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
			slog.Bool("skip_invalid_rows", uploadSkipInvalidRowsEnabled()),
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

func prepareUploadFromFileStreaming(
	ctx context.Context,
	filePath string,
	requestID string,
) (UploadPreparation, error) {
	workbook, err := openUploadStreamingWorkbook(filePath)
	if err != nil {
		return UploadPreparation{}, err
	}
	defer workbook.close()

	sample, err := workbook.readSample()
	if err != nil {
		return UploadPreparation{}, err
	}
	reportUploadProgress(ctx, uploadJobStageValidating, 16)
	reportUploadProgress(ctx, uploadJobStageParsingRows, 20)

	_, _, preparation, err := resolveFallbackPreparationFromSample(
		ctx,
		requestID,
		sample.Rows,
		sample.TotalRows,
		sample.TotalCols,
	)
	if err != nil {
		return UploadPreparation{}, err
	}
	if !preparation.hasDetectedColumns() {
		return UploadPreparation{}, &parseError{code: ErrInvalidHeader, msg: "cannot detect required columns"}
	}
	reportUploadProgress(ctx, uploadJobStageFinalizing, 96)
	return preparation, nil
}
