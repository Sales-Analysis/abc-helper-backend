// Package abc contains handlers, validation logic, errors and metrics for ABC analysis endpoints.
package abc

import (
	"encoding/json"
	"net/http"
)

// ErrCode is a machine-readable error code returned by the API.
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
	ErrInvalidHeader    ErrCode = "INVALID_HEADER"
	ErrInvalidNumber    ErrCode = "INVALID_NUMBER"
	ErrInvalidQuantity  ErrCode = "INVALID_QUANTITY"
	ErrInvalidValue     ErrCode = "INVALID_VALUE"
	ErrNoSheets         ErrCode = "NO_SHEETS"
	ErrNoData           ErrCode = "NO_DATA"
	ErrMissingValue     ErrCode = "MISSING_VALUE"
)

// APIError is the JSON shape for error responses.
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
