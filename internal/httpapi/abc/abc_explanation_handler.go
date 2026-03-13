package abc

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	abclib "github.com/Sales-Analysis/abc-helper-lib/abc"
)

const maxExplanationRequestBytes int64 = 2 * 1024 * 1024

type explanationRequest struct {
	Result []abclib.ProductResult `json:"result"`
}

type explanationResponse struct {
	Status      string `json:"status"`
	Explanation string `json:"explanation,omitempty"`
}

// ExplanationHandler generates assistant explanation for an already computed ABC result.
func ExplanationHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, ErrMethodNotAllowed, "method not allowed")
		return
	}

	req, ok := decodeExplanationRequest(w, r)
	if !ok {
		return
	}

	explanation, err := explainAnalysis(r.Context(), req.Result, r.Header.Get("X-Request-ID"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, ErrAssistantFailed, "assistant explanation failed")
		return
	}

	writeOK(w, explanationResponse{
		Status:      "ok",
		Explanation: strings.TrimSpace(explanation),
	})
}

func decodeExplanationRequest(w http.ResponseWriter, r *http.Request) (explanationRequest, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxExplanationRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var req explanationRequest
	if err := decoder.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, ErrInvalidJSON, "invalid JSON body")
		return explanationRequest{}, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeErr(w, http.StatusBadRequest, ErrInvalidJSON, "invalid JSON body")
		return explanationRequest{}, false
	}
	if len(req.Result) == 0 {
		writeErr(w, http.StatusBadRequest, ErrMissingResult, "result is required")
		return explanationRequest{}, false
	}

	return req, true
}
