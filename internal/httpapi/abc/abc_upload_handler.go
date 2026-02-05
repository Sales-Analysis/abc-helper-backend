package abc

import (
	"errors"
	"io"
	"net/http"
	"time"

	abclib "github.com/Sales-Analysis/abc-helper-lib/abc"
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

	headerIdx, err := parseHeader(rows[0])
	if err != nil {
		if perr := new(parseError); errors.As(err, &perr) {
			writeErr(w, http.StatusBadRequest, perr.code, perr.msg)
		} else {
			writeErr(w, http.StatusBadRequest, ErrInvalidXLSX, "invalid xlsx header")
		}
		return
	}

	products, err := parseProducts(rows[1:], headerIdx)
	if err != nil {
		if perr := new(parseError); errors.As(err, &perr) {
			writeErr(w, http.StatusBadRequest, perr.code, perr.msg)
		} else {
			writeErr(w, http.StatusBadRequest, ErrInvalidXLSX, "invalid xlsx rows")
		}
		return
	}

	analysis := abclib.New()
	analysis.Calculate(products)

	UploadCounter.WithLabelValues("success").Inc()
	writeOK(w, UploadResponse{Status: "ok", Result: analysis.Result})
}

// UploadResponse is the JSON response for a successful ABC upload.
type UploadResponse struct {
	Status string                 `json:"status"`
	Result []abclib.ProductResult `json:"result"`
}
