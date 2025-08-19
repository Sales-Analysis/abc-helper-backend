package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

// @Summary      Upload ABC XLSX
// @Description  Accepts an XLSX file, validates it and returns OK if valid.
// @Tags         analysis
// @Accept       multipart/form-data
// @Produce      json
// @Param        file  formData  file  true  "XLSX file"
// @Success      200   {object}  map[string]string
// @Failure      400   {object}  map[string]any
// @Router       /api/v1/abc/upload [post]
func abcUploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, ErrMethodNotAllowed, "method not allowed")
		return
	}

	// --- измеряем время выполнения
	start := time.Now()
	defer func() {
		uploadDuration.Observe(time.Since(start).Seconds())
	}()

	// ограничиваем тело и парсим multipart
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		writeErr(w, http.StatusBadRequest, ErrInvalidForm, "invalid form: "+err.Error())
		return
	}

	file, fh, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, ErrMissingFile, "file is required")
		return
	}
	defer func() { _ = file.Close() }()

	if fh.Size == 0 {
		writeErr(w, http.StatusBadRequest, ErrEmptyFile, "file is empty")
		return
	}

	if err := validateExtension(fh.Filename); err != nil {
		writeErr(w, http.StatusBadRequest, ErrInvalidExt, "only .xlsx allowed")
		return
	}

	data, err := io.ReadAll(file)
	if err != nil {
		writeErr(w, http.StatusBadRequest, ErrReadError, "cannot read file")
		return
	}

	xls, err := openXLSX(data)
	if err != nil {
		writeErr(w, http.StatusBadRequest, ErrInvalidXLSX, "invalid xlsx format")
		return
	}
	defer func() { _ = xls.Close() }()

	_, rows, err := validateWorkbook(xls)
	if err != nil {
		switch err.Error() {
		case string(ErrNoSheets):
			writeErr(w, http.StatusBadRequest, ErrNoSheets, "xlsx has no sheets")
		case string(ErrNoData):
			writeErr(w, http.StatusBadRequest, ErrNoData, "xlsx has no data")
		default:
			writeErr(w, http.StatusBadRequest, ErrInvalidXLSX, "invalid xlsx structure")
		}
		return
	}

	if err := validateRows(rows); err != nil {
		// формат из validateRows: "MISSING_VALUE:row:col"
		if len(err.Error()) > 0 && err.Error()[:13] == string(ErrMissingValue) {
			var row, col int
			_, _ = fmt.Sscanf(err.Error(), "MISSING_VALUE:%d:%d", &row, &col)
			writeErr(w, http.StatusBadRequest, ErrMissingValue,
				fmt.Sprintf("row %d has missing value in column %d", row, col))
			return
		}
		writeErr(w, http.StatusBadRequest, ErrInvalidXLSX, "invalid xlsx rows")
		return
	}

	uploadCounter.WithLabelValues("success").Inc()
	writeOK(w, map[string]string{"status": "ok"})
}
