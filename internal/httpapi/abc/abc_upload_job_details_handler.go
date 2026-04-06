package abc

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

const maxUploadDetailUniqueValues = 12

type uploadJobDetailField struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type uploadJobDetailsResponse struct {
	Status string                 `json:"status"`
	Fields []uploadJobDetailField `json:"fields,omitempty"`
}

type uploadJobDetailSpec struct {
	Index int
	Label string
}

type uploadJobDetailAccumulator struct {
	values   []string
	seen     map[string]struct{}
	overflow int
}

func UploadJobDetailsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, ErrMethodNotAllowed, "method not allowed")
		return
	}

	jobID := strings.TrimSpace(r.URL.Query().Get("job_id"))
	if jobID == "" {
		writeErr(w, http.StatusBadRequest, ErrMissingJobID, "job_id is required")
		return
	}

	sku := strings.TrimSpace(r.URL.Query().Get("sku"))
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if sku == "" && name == "" {
		writeErr(w, http.StatusBadRequest, ErrInvalidForm, "sku or name is required")
		return
	}

	job, found := uploadJobsStore.get(jobID)
	if !found {
		writeErr(w, http.StatusNotFound, ErrJobNotFound, "upload job not found")
		return
	}
	if job.FileID == "" {
		writeErr(w, http.StatusNotFound, ErrUploadFileNotFound, "upload file is not attached to the job")
		return
	}
	if job.Preparation == nil || !job.Preparation.hasDetectedColumns() {
		writeErr(w, http.StatusBadRequest, ErrInvalidHeader, "job preparation is unavailable")
		return
	}

	storedFile, found := uploadFilesStore.get(job.FileID)
	if !found {
		writeErr(w, http.StatusNotFound, ErrUploadFileNotFound, "upload file not found by file_id")
		return
	}

	fields, err := loadUploadJobDetails(storedFile.FilePath, *job.Preparation, sku, name)
	if err != nil {
		perr := new(parseError)
		if errors.As(err, &perr) {
			status := http.StatusBadRequest
			if perr.code == ErrUploadFileNotFound || perr.code == ErrJobNotFound {
				status = http.StatusNotFound
			}
			writeErr(w, status, perr.code, strings.TrimSpace(perr.msg))
			return
		}
		writeErr(w, http.StatusBadRequest, ErrInvalidXLSX, "cannot load upload item details")
		return
	}

	writeOK(w, uploadJobDetailsResponse{
		Status: "ok",
		Fields: fields,
	})
}

func loadUploadJobDetails(
	filePath string,
	preparation UploadPreparation,
	targetSKU string,
	targetName string,
) ([]uploadJobDetailField, error) {
	idx, headerRow, dataStartRow, err := resolveUploadPreparationBounds(preparation)
	if err != nil {
		return nil, err
	}

	xls, err := excelize.OpenFile(filePath)
	if err != nil {
		return nil, &parseError{code: ErrInvalidXLSX, msg: "invalid xlsx structure"}
	}
	defer func() { _ = xls.Close() }()

	sheets := xls.GetSheetList()
	if len(sheets) == 0 {
		return nil, &parseError{code: ErrNoSheets, msg: "xlsx has no worksheets"}
	}

	stream, err := xls.Rows(sheets[0])
	if err != nil {
		return nil, &parseError{code: ErrInvalidXLSX, msg: "invalid xlsx structure"}
	}
	defer func() { _ = stream.Close() }()

	header := []string{}
	specs := []uploadJobDetailSpec{}
	accumulators := make(map[int]*uploadJobDetailAccumulator)
	rowNum := 0
	foundMatch := false

	for stream.Next() {
		rowNum++
		row, rowErr := stream.Columns()
		if rowErr != nil {
			return nil, &parseError{code: ErrInvalidXLSX, msg: "invalid xlsx structure"}
		}
		if rowNum == headerRow {
			header = append([]string(nil), row...)
			specs = buildUploadJobDetailSpecs(header, idx)
			continue
		}
		if rowNum < dataStartRow || isRowEmpty(row) {
			continue
		}

		product, parseErr := parseProductFromRow(row, idx, rowNum)
		if parseErr != nil {
			continue
		}
		rawSKU := strings.TrimSpace(cellAt(row, idx.sku))
		if !matchesUploadDetailTarget(rawSKU, product.Name, targetSKU, targetName) {
			continue
		}

		foundMatch = true
		if len(specs) == 0 {
			specs = buildUploadJobDetailSpecs(header, idx)
		}
		mergeUploadJobDetailFields(accumulators, specs, row)
	}

	if !foundMatch {
		return nil, &parseError{code: ErrNoData, msg: "item details not found in uploaded file"}
	}
	return finalizeUploadJobDetailFields(specs, accumulators), nil
}

func resolveUploadPreparationBounds(preparation UploadPreparation) (headerIndex, int, int, error) {
	if preparation.Columns == nil {
		return headerIndex{}, 0, 0, &parseError{code: ErrInvalidHeader, msg: "preparation columns are not provided"}
	}

	headerRow := preparation.HeaderRow
	if headerRow <= 0 {
		headerRow = 1
	}
	dataStartRow := preparation.DataStartRow
	if dataStartRow <= headerRow {
		dataStartRow = headerRow + 1
	}

	idx := headerIndex{
		sku:   toZeroBasedColumn(preparation.Columns.SKU.Index),
		name:  toZeroBasedColumn(preparation.Columns.Name.Index),
		qty:   toZeroBasedColumn(preparation.Columns.Quantity.Index),
		price: toZeroBasedColumn(preparation.Columns.Price.Index),
		value: toZeroBasedColumn(preparation.Columns.Value.Index),
	}
	if idx.name < 0 {
		return headerIndex{}, 0, 0, &parseError{code: ErrInvalidHeader, msg: "product name column is not detected"}
	}
	if idx.qty < 0 {
		return headerIndex{}, 0, 0, &parseError{code: ErrInvalidHeader, msg: "quantity column is not detected"}
	}
	if idx.price < 0 && idx.value < 0 {
		return headerIndex{}, 0, 0, &parseError{code: ErrInvalidHeader, msg: "price/value column is not detected"}
	}
	return idx, headerRow, dataStartRow, nil
}

func buildUploadJobDetailSpecs(header []string, idx headerIndex) []uploadJobDetailSpec {
	maxCols := len(header)
	if maxCols <= 0 {
		maxCols = maxRequiredColumn(idx) + 1
	}

	excluded := map[int]struct{}{
		idx.sku:   {},
		idx.name:  {},
		idx.qty:   {},
		idx.price: {},
		idx.value: {},
	}
	specs := make([]uploadJobDetailSpec, 0, maxCols)
	for col := 0; col < maxCols; col++ {
		if _, skip := excluded[col]; skip {
			continue
		}
		label := strings.TrimSpace(cellAt(header, col))
		if label == "" {
			colName, err := excelize.ColumnNumberToName(col + 1)
			if err != nil || strings.TrimSpace(colName) == "" {
				colName = "?"
			}
			label = "Колонка " + colName
		}
		specs = append(specs, uploadJobDetailSpec{Index: col, Label: label})
	}
	return specs
}

func matchesUploadDetailTarget(rawSKU string, rowName string, targetSKU string, targetName string) bool {
	sku := strings.TrimSpace(rawSKU)
	name := strings.TrimSpace(rowName)
	targetSKU = strings.TrimSpace(targetSKU)
	targetName = strings.TrimSpace(targetName)

	if targetName != "" && strings.EqualFold(name, targetName) {
		return targetSKU == "" || sku == "" || strings.EqualFold(sku, targetSKU)
	}
	if targetSKU != "" && sku != "" && strings.EqualFold(sku, targetSKU) {
		return targetName == "" || strings.EqualFold(name, targetName)
	}
	return false
}

func mergeUploadJobDetailFields(
	accumulators map[int]*uploadJobDetailAccumulator,
	specs []uploadJobDetailSpec,
	row []string,
) {
	for _, spec := range specs {
		value := strings.TrimSpace(cellAt(row, spec.Index))
		if value == "" {
			continue
		}
		accumulator := accumulators[spec.Index]
		if accumulator == nil {
			accumulator = &uploadJobDetailAccumulator{
				values: make([]string, 0, 1),
				seen:   make(map[string]struct{}),
			}
			accumulators[spec.Index] = accumulator
		}
		accumulator.add(value)
	}
}

func finalizeUploadJobDetailFields(
	specs []uploadJobDetailSpec,
	accumulators map[int]*uploadJobDetailAccumulator,
) []uploadJobDetailField {
	fields := make([]uploadJobDetailField, 0, len(specs))
	for _, spec := range specs {
		accumulator := accumulators[spec.Index]
		if accumulator == nil {
			continue
		}
		value := accumulator.value()
		if value == "" {
			continue
		}
		fields = append(fields, uploadJobDetailField{
			Label: spec.Label,
			Value: value,
		})
	}
	return fields
}

func (a *uploadJobDetailAccumulator) add(value string) {
	if a == nil {
		return
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	if _, exists := a.seen[value]; exists {
		return
	}
	a.seen[value] = struct{}{}
	if len(a.values) >= maxUploadDetailUniqueValues {
		a.overflow++
		return
	}
	a.values = append(a.values, value)
}

func (a *uploadJobDetailAccumulator) value() string {
	if a == nil || len(a.values) == 0 {
		return ""
	}
	value := strings.Join(a.values, " | ")
	if a.overflow > 0 {
		value += " | +" + strconv.Itoa(a.overflow) + " еще"
	}
	return value
}
