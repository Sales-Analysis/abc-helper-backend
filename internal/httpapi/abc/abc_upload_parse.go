package abc

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	abclib "github.com/Sales-Analysis/abc-helper-lib/abc"
	"github.com/xuri/excelize/v2"
)

const maxUploadProductsPrealloc = 10000

type parseResultsStats struct {
	SkippedRows     int
	FirstSkippedErr error
}

type parseError struct {
	code ErrCode
	msg  string
}

func (e *parseError) Error() string { return e.msg }

type headerIndex struct {
	sku   int
	name  int
	value int
	price int
	qty   int
}

func parseHeader(header []string) (headerIndex, error) {
	idx := headerIndex{sku: -1, name: -1, value: -1, price: -1, qty: -1}
	for i, raw := range header {
		label := normalizeHeaderLabel(raw)
		if idx.sku < 0 && headerHasPattern(label,
			"sku", "id", "code", "код", "артикул",
		) {
			idx.sku = i
			continue
		}
		if idx.name < 0 && headerHasPattern(label,
			"item", "name", "product", "товар", "наименование", "номенклатур", "названи", "продукт",
		) && isLikelyProductNameHeader(label) {
			idx.name = i
			continue
		}
		if idx.qty < 0 && isLikelyQuantityHeader(label) {
			idx.qty = i
			continue
		}
		if idx.price < 0 && headerHasPattern(label,
			"unitprice", "priceunit", "price", "ценаед", "ценаединиц", "цена", "закупоч", "розничн",
		) {
			idx.price = i
			continue
		}
		if idx.value < 0 && headerHasPattern(label,
			"value", "revenue", "amount", "total", "sum", "сумма", "выручк", "оборот", "стоимост", "итого",
		) {
			idx.value = i
		}
	}

	missing := []string{}
	if idx.name < 0 {
		missing = append(missing, "Item")
	}
	if idx.qty < 0 {
		missing = append(missing, "Quantity")
	}
	if idx.value < 0 && idx.price < 0 {
		missing = append(missing, "Value or Price")
	}
	if len(missing) > 0 {
		return idx, &parseError{
			code: ErrInvalidHeader,
			msg:  "missing required columns: " + strings.Join(missing, ", "),
		}
	}

	return idx, nil
}

func normalizeHeaderLabel(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.ReplaceAll(s, "ё", "е")

	replacer := strings.NewReplacer(
		" ", "",
		"_", "",
		"-", "",
		".", "",
		":", "",
		";", "",
		"/", "",
		"\\", "",
		"(", "",
		")", "",
		"[", "",
		"]", "",
	)
	return replacer.Replace(s)
}

func headerHasPattern(label string, patterns ...string) bool {
	if label == "" {
		return false
	}
	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}
		if label == pattern || strings.Contains(label, pattern) {
			return true
		}
	}
	return false
}

func isLikelyProductNameHeader(label string) bool {
	if label == "" {
		return false
	}

	// Product-related columns must win over organization/location columns.
	if headerHasPattern(label, "товар", "product", "номенклатур", "продукт", "item") {
		return true
	}
	if headerHasPattern(label, "организац", "сотрудник", "адрес", "точкапродаж", "магазин") {
		return false
	}
	return true
}

func isLikelyQuantityHeader(label string) bool {
	if label == "" {
		return false
	}
	if headerHasPattern(label, "штрихкод", "barcode", "ean") {
		return false
	}
	if label == "шт" || strings.HasSuffix(label, "шт") {
		return true
	}
	return headerHasPattern(label,
		"quantity", "qty", "count", "количество", "колво", "колич", "units", "unit", "обьем",
	)
}

func parseProducts(rows [][]string, idx headerIndex) ([]abclib.Product, error) {
	return parseProductsWithProgress(rows, idx, nil)
}

func parseProductsWithProgress(
	rows [][]string,
	idx headerIndex,
	onProgress func(done, total int),
) ([]abclib.Product, error) {
	return parseProductsWithProgressFromRow(rows, idx, 2, onProgress)
}

func parseProductsWithProgressFromRow(
	rows [][]string,
	idx headerIndex,
	firstRowNum int,
	onProgress func(done, total int),
) ([]abclib.Product, error) {
	products := make([]abclib.Product, 0, len(rows))
	totalRows := len(rows)
	if firstRowNum <= 0 {
		firstRowNum = 1
	}
	for i, row := range rows {
		rowNum := firstRowNum + i
		if isRowEmpty(row) {
			if onProgress != nil {
				onProgress(i+1, totalRows)
			}
			continue
		}

		product, err := parseProductFromRow(row, idx, rowNum)
		if err != nil {
			return nil, err
		}
		products = append(products, product)
		if onProgress != nil {
			onProgress(i+1, totalRows)
		}
	}

	if len(products) == 0 {
		return nil, &parseError{code: ErrNoData, msg: "xlsx has no data"}
	}
	return products, nil
}

func parseProductsFromSheetWithProgress(
	f *excelize.File,
	sheet string,
	idx headerIndex,
	dataStartRow int,
	totalRows int,
	onProgress func(done, total int),
) ([]abclib.Product, error) {
	if f == nil {
		return nil, &parseError{code: ErrInvalidXLSX, msg: "invalid xlsx structure"}
	}
	if dataStartRow <= 0 {
		dataStartRow = 2
	}

	stream, err := f.Rows(sheet)
	if err != nil {
		return nil, &parseError{code: ErrInvalidXLSX, msg: "invalid xlsx structure"}
	}
	defer func() { _ = stream.Close() }()

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
	products := make([]abclib.Product, 0, estimatedCap)
	rowNum := 0
	processed := 0

	for stream.Next() {
		rowNum++
		row, rowErr := stream.Columns()
		if rowErr != nil {
			return nil, &parseError{code: ErrInvalidXLSX, msg: "invalid xlsx structure"}
		}
		if rowNum < dataStartRow {
			continue
		}
		processed++
		if isRowEmpty(row) {
			if onProgress != nil {
				onProgress(processed, totalParseRows)
			}
			continue
		}

		product, parseErr := parseProductFromRow(row, idx, rowNum)
		if parseErr != nil {
			return nil, parseErr
		}
		products = append(products, product)
		if onProgress != nil {
			onProgress(processed, totalParseRows)
		}
	}

	if err := stream.Error(); err != nil {
		return nil, &parseError{code: ErrInvalidXLSX, msg: "invalid xlsx structure"}
	}
	if len(products) == 0 {
		return nil, &parseError{code: ErrNoData, msg: "xlsx has no data"}
	}
	return products, nil
}

func parseResultsFromSheetWithProgress(
	f *excelize.File,
	sheet string,
	idx headerIndex,
	dataStartRow int,
	totalRows int,
	onProgress func(done, total int),
) ([]abclib.ProductResult, error) {
	results, _, err := parseResultsFromSheetWithProgressAndOptions(
		f,
		sheet,
		idx,
		dataStartRow,
		totalRows,
		onProgress,
		false,
	)
	return results, err
}

func parseResultsFromSheetWithProgressAndOptions(
	f *excelize.File,
	sheet string,
	idx headerIndex,
	dataStartRow int,
	totalRows int,
	onProgress func(done, total int),
	skipInvalidRows bool,
) ([]abclib.ProductResult, parseResultsStats, error) {
	if f == nil {
		return nil, parseResultsStats{}, &parseError{code: ErrInvalidXLSX, msg: "invalid xlsx structure"}
	}
	if dataStartRow <= 0 {
		dataStartRow = 2
	}

	stream, err := f.Rows(sheet)
	if err != nil {
		return nil, parseResultsStats{}, &parseError{code: ErrInvalidXLSX, msg: "invalid xlsx structure"}
	}
	defer func() { _ = stream.Close() }()

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
	rowNum := 0
	processed := 0
	stats := parseResultsStats{}

	for stream.Next() {
		rowNum++
		row, rowErr := stream.Columns()
		if rowErr != nil {
			return nil, stats, &parseError{code: ErrInvalidXLSX, msg: "invalid xlsx structure"}
		}
		if rowNum < dataStartRow {
			continue
		}
		processed++
		if isRowEmpty(row) {
			if onProgress != nil {
				onProgress(processed, totalParseRows)
			}
			continue
		}

		product, parseErr := parseProductFromRow(row, idx, rowNum)
		if parseErr != nil {
			if skipInvalidRows && isSkippableRowParseError(parseErr) {
				stats.SkippedRows++
				if stats.FirstSkippedErr == nil {
					stats.FirstSkippedErr = parseErr
				}
				if onProgress != nil {
					onProgress(processed, totalParseRows)
				}
				continue
			}
			return nil, stats, parseErr
		}
		priceTotal := float64(product.Quantity) * product.Price
		skuKey := strings.TrimSpace(cellAt(row, idx.sku))
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
	}

	if err := stream.Error(); err != nil {
		return nil, stats, &parseError{code: ErrInvalidXLSX, msg: "invalid xlsx structure"}
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

func buildAggregatedProductKey(rawSKU, rawName string) string {
	sku := strings.TrimSpace(rawSKU)
	name := strings.TrimSpace(rawName)
	if sku != "" {
		return "sku:" + sku + "|name:" + name
	}
	return "name:" + name
}

func calculateABCInPlace(results []abclib.ProductResult) []abclib.ProductResult {
	if len(results) == 0 {
		return results
	}

	grandTotal := 0.0
	for i := range results {
		grandTotal += results[i].PriceTotal
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].PriceTotal > results[j].PriceTotal
	})

	accumulated := 0.0
	for i := range results {
		share := 0.0
		if grandTotal > 0 {
			share = (results[i].PriceTotal / grandTotal) * 100
		}
		accumulated += share
		results[i].ShareTotal = share
		results[i].ShareAccumulated = accumulated
		switch {
		case accumulated <= 80:
			results[i].Group = "A"
		case accumulated <= 95:
			results[i].Group = "B"
		default:
			results[i].Group = "C"
		}
	}
	return results
}

func parseProductFromRow(row []string, idx headerIndex, rowNum int) (abclib.Product, error) {
	name := strings.TrimSpace(cellAt(row, idx.name))
	if name == "" {
		return abclib.Product{}, missingValueError(rowNum, idx.name)
	}

	qtyStr := cellAt(row, idx.qty)
	if strings.TrimSpace(qtyStr) == "" {
		return abclib.Product{}, missingValueError(rowNum, idx.qty)
	}
	qty, err := parseQuantity(qtyStr, rowNum, idx.qty)
	if err != nil {
		return abclib.Product{}, err
	}

	priceUnit, err := parsePriceUnit(row, idx, rowNum, qty)
	if err != nil {
		return abclib.Product{}, err
	}

	sku := ""
	if idx.sku >= 0 {
		sku = strings.TrimSpace(cellAt(row, idx.sku))
	}
	if sku == "" {
		if rowNum <= 1 {
			sku = strconv.Itoa(rowNum)
		} else {
			sku = strconv.Itoa(rowNum - 1)
		}
	}

	return abclib.Product{
		SKU:      sku,
		Name:     name,
		Quantity: qty,
		Price:    priceUnit,
	}, nil
}

func parsePriceUnit(row []string, idx headerIndex, rowNum int, qty int) (float64, error) {
	if idx.price >= 0 {
		priceStr := strings.TrimSpace(cellAt(row, idx.price))
		if priceStr != "" {
			priceUnit, err := parsePositiveFloat(priceStr, rowNum, idx.price)
			if err != nil {
				return 0, err
			}
			return priceUnit, nil
		}
	}

	if idx.value >= 0 {
		valueStr := strings.TrimSpace(cellAt(row, idx.value))
		if valueStr != "" {
			total, err := parsePositiveFloat(valueStr, rowNum, idx.value)
			if err != nil {
				return 0, err
			}
			return total / float64(qty), nil
		}
	}

	col := idx.price
	if col < 0 {
		col = idx.value
	}
	return 0, missingValueError(rowNum, col)
}

func parseQuantity(raw string, rowNum int, col int) (int, error) {
	value, err := parseFloat(raw)
	if err != nil {
		return 0, invalidNumberError(rowNum, col)
	}
	if value <= 0 || math.Mod(value, 1.0) != 0 {
		return 0, invalidQuantityError(rowNum, col)
	}
	return int(value), nil
}

func parsePositiveFloat(raw string, rowNum int, col int) (float64, error) {
	value, err := parseFloat(raw)
	if err != nil {
		return 0, invalidNumberError(rowNum, col)
	}
	if value <= 0 {
		return 0, invalidValueError(rowNum, col)
	}
	return value, nil
}

func parseFloat(raw string) (float64, error) {
	s := normalizeNumericString(raw)
	return strconv.ParseFloat(s, 64)
}

func normalizeNumericString(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}

	s = strings.ReplaceAll(s, "\u00a0", "")
	s = strings.ReplaceAll(s, "\u202f", "")
	s = strings.ReplaceAll(s, " ", "")

	negativeByBrackets := strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")")
	if negativeByBrackets {
		s = strings.TrimPrefix(s, "(")
		s = strings.TrimSuffix(s, ")")
	}

	var b strings.Builder
	b.Grow(len(s))
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == ',':
			b.WriteRune(r)
		case r == '-' && i == 0:
			b.WriteRune(r)
		}
	}

	normalized := b.String()
	normalized = normalizeDecimalSeparators(normalized)
	if negativeByBrackets && normalized != "" && !strings.HasPrefix(normalized, "-") {
		normalized = "-" + normalized
	}
	return normalized
}

func normalizeDecimalSeparators(s string) string {
	if s == "" {
		return s
	}

	negative := strings.HasPrefix(s, "-")
	if negative {
		s = strings.TrimPrefix(s, "-")
	}

	hasComma := strings.Contains(s, ",")
	hasDot := strings.Contains(s, ".")

	switch {
	case hasComma && hasDot:
		lastComma := strings.LastIndex(s, ",")
		lastDot := strings.LastIndex(s, ".")
		if lastComma > lastDot {
			s = strings.ReplaceAll(s, ".", "")
			idx := strings.LastIndex(s, ",")
			if idx >= 0 {
				s = strings.ReplaceAll(s[:idx], ",", "") + "." + strings.ReplaceAll(s[idx+1:], ",", "")
			}
		} else {
			s = strings.ReplaceAll(s, ",", "")
		}
	case hasComma:
		if strings.Count(s, ",") == 1 {
			s = strings.ReplaceAll(s, ",", ".")
		} else {
			idx := strings.LastIndex(s, ",")
			s = strings.ReplaceAll(s[:idx], ",", "") + "." + strings.ReplaceAll(s[idx+1:], ",", "")
		}
	case hasDot:
		if strings.Count(s, ".") > 1 {
			idx := strings.LastIndex(s, ".")
			s = strings.ReplaceAll(s[:idx], ".", "") + "." + strings.ReplaceAll(s[idx+1:], ".", "")
		}
	}

	if negative && s != "" {
		s = "-" + s
	}
	return s
}

func isRowEmpty(row []string) bool {
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

func cellAt(row []string, idx int) string {
	if idx < 0 || idx >= len(row) {
		return ""
	}
	return row[idx]
}

func missingValueError(row, col int) error {
	return &parseError{
		code: ErrMissingValue,
		msg:  fmt.Sprintf("row %d has missing value in column %d", row, col+1),
	}
}

func invalidNumberError(row, col int) error {
	return &parseError{
		code: ErrInvalidNumber,
		msg:  fmt.Sprintf("row %d has invalid number in column %d", row, col+1),
	}
}

func invalidQuantityError(row, col int) error {
	return &parseError{
		code: ErrInvalidQuantity,
		msg:  fmt.Sprintf("row %d has invalid quantity in column %d", row, col+1),
	}
}

func invalidValueError(row, col int) error {
	return &parseError{
		code: ErrInvalidValue,
		msg:  fmt.Sprintf("row %d has invalid value in column %d", row, col+1),
	}
}

func isSkippableRowParseError(err error) bool {
	if err == nil {
		return false
	}
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
