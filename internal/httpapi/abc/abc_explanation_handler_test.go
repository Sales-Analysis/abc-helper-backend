package abc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExplanationHandlerMethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/abc/explanation", nil)
	rec := httptest.NewRecorder()

	ExplanationHandler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestExplanationHandlerInvalidJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/abc/explanation", strings.NewReader(`{"result":`))
	rec := httptest.NewRecorder()

	ExplanationHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var body struct {
		Error APIError `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != ErrInvalidJSON {
		t.Fatalf("error code = %q, want %q", body.Error.Code, ErrInvalidJSON)
	}
}

func TestExplanationHandlerMissingResult(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/abc/explanation", strings.NewReader(`{"result":[]}`))
	rec := httptest.NewRecorder()

	ExplanationHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var body struct {
		Error APIError `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != ErrMissingResult {
		t.Fatalf("error code = %q, want %q", body.Error.Code, ErrMissingResult)
	}
}
