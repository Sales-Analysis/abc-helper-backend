package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/Sales-Analysis/abc-helper-backend/internal/version"
)

var buildInfo version.Info

// @Summary Hello world
// @Tags    root
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router  / [get]
func helloHandler(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]any{
		"message": "hello world",
		"time":    time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// @Summary Liveness
// @Tags    health
// @Produce json
// @Success 200 {object} map[string]string
// @Router  /healthz [get]
func healthzHandler(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

// @Summary Readiness
// @Tags    health
// @Produce json
// @Success 200 {object} map[string]string
// @Router  /ready [get]
func readyHandler(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]string{"ready": "true"})
}

// @Summary Build info
// @Tags    meta
// @Produce json
// @Success 200 {object} version.Info
// @Router  /version [get]
func versionHandler(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, http.StatusOK, buildInfo)
}

// nolint:unparam
func jsonResponse(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// abcUploadHandler принимает XLSX-файл и валидирует его.
// Возвращает 200 {"status":"ok"} если файл валиден,
// иначе 400 {"error": {"code": "...", "message": "..."}} с понятным кодом.
//
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
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{
			"error": map[string]string{"code": "METHOD_NOT_ALLOWED", "message": "method not allowed"},
		})
		return
	}

	const maxUpload = 5 << 20 // 5 MiB
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	if err := r.ParseMultipartForm(maxUpload); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"code": "INVALID_FORM", "message": "invalid form: " + err.Error()},
		})
		return
	}

	file, fh, err := r.FormFile("file")
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"code": "MISSING_FILE", "message": "file is required"},
		})
		return
	}
	defer func() { _ = file.Close() }()

	if fh.Size == 0 {
		jsonResponse(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"code": "EMPTY_FILE", "message": "file is empty"},
		})
		return
	}

	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if ext != ".xlsx" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"code": "INVALID_EXTENSION", "message": "only .xlsx allowed"},
		})
		return
	}

	data, err := io.ReadAll(file)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"code": "READ_ERROR", "message": "cannot read file"},
		})
		return
	}

	xls, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"code": "INVALID_XLSX", "message": "invalid xlsx format"},
		})
		return
	}
	defer func() { _ = xls.Close() }()

	sheets := xls.GetSheetList()
	if len(sheets) == 0 {
		jsonResponse(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"code": "NO_SHEETS", "message": "xlsx has no sheets"},
		})
		return
	}
	rows, err := xls.GetRows(sheets[0])
	if err != nil || len(rows) < 2 {
		jsonResponse(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"code": "NO_DATA", "message": "xlsx has no data"},
		})
		return
	}

	// 🔍 Проверка строк
	for i, row := range rows[1:] {
		allEmpty := true
		for _, cell := range row {
			if strings.TrimSpace(cell) != "" {
				allEmpty = false
				break
			}
		}
		if allEmpty {
			continue
		}
		for j, cell := range row {
			if strings.TrimSpace(cell) == "" {
				jsonResponse(w, http.StatusBadRequest, map[string]any{
					"error": map[string]string{
						"code":    "MISSING_VALUE",
						"message": fmt.Sprintf("row %d has missing value in column %d", i+2, j+1),
					},
				})
				return
			}
		}
	}

	jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}
