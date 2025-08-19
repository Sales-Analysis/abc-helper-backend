package httpapi

import (
	"encoding/json"
	"net/http"
)

// ErrCode — программный код ошибки, который клиент может разбирать без парсинга текста.
type ErrCode string

// Набор кодов ошибок, возвращаемых API при валидации/загрузке XLSX.
const (
	ErrMethodNotAllowed ErrCode = "METHOD_NOT_ALLOWED"
	ErrInvalidForm      ErrCode = "INVALID_FORM"
	ErrMissingFile      ErrCode = "MISSING_FILE"
	ErrEmptyFile        ErrCode = "EMPTY_FILE"
	ErrInvalidExt       ErrCode = "INVALID_EXTENSION"
	ErrReadError        ErrCode = "READ_ERROR"
	ErrInvalidXLSX      ErrCode = "INVALID_XLSX"
	ErrNoSheets         ErrCode = "NO_SHEETS"
	ErrNoData           ErrCode = "NO_DATA"
	ErrMissingValue     ErrCode = "MISSING_VALUE"
)

// APIError — JSON-структура ошибки в ответе API.
type APIError struct {
	Code    ErrCode `json:"code"`
	Message string  `json:"message"`
}

func writeOK(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, status int, code ErrCode, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": APIError{Code: code, Message: msg},
	})
}
