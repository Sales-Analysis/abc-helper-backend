package abc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	abclib "github.com/Sales-Analysis/abc-helper-lib/abc"
)

func TestExplanationJobCreateHandlerMethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/abc/explanation/jobs", nil)
	rec := httptest.NewRecorder()

	ExplanationJobCreateHandler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestExplanationJobHandlersCreateAndPollDone(t *testing.T) {
	resetExplanationJobsState()
	orig := runExplanationAsync
	runExplanationAsync = func(_ context.Context, _ []abclib.ProductResult, _ string) (string, error) {
		return "Готово", nil
	}
	t.Cleanup(func() {
		runExplanationAsync = orig
		resetExplanationJobsState()
	})

	createReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/abc/explanation/jobs",
		strings.NewReader(`{"result":[{"SKU":"1","Name":"A","Quantity":1,"PriceUnit":10,"PriceTotal":10,"ShareTotal":1,"ShareAccumulated":1,"Group":"A"}]}`),
	)
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()

	ExplanationJobCreateHandler(createRec, createReq)

	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d", createRec.Code, http.StatusOK)
	}

	var createBody explanationJobResponse
	if err := json.NewDecoder(createRec.Body).Decode(&createBody); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if strings.TrimSpace(createBody.JobID) == "" {
		t.Fatalf("job_id must be set")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		statusReq := httptest.NewRequest(
			http.MethodGet,
			"/api/v1/abc/explanation/jobs/"+createBody.JobID,
			nil,
		)
		statusRec := httptest.NewRecorder()
		ExplanationJobStatusHandler(statusRec, statusReq)

		if statusRec.Code != http.StatusOK {
			t.Fatalf("status endpoint code = %d, want %d", statusRec.Code, http.StatusOK)
		}

		var statusBody explanationJobResponse
		if err := json.NewDecoder(statusRec.Body).Decode(&statusBody); err != nil {
			t.Fatalf("decode status response: %v", err)
		}

		if statusBody.Status == explanationJobStatusDone {
			if statusBody.Explanation != "Готово" {
				t.Fatalf("unexpected explanation: %q", statusBody.Explanation)
			}
			return
		}
		if statusBody.Status == explanationJobStatusError {
			t.Fatalf("job unexpectedly failed: %+v", statusBody.Error)
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("job did not complete before timeout")
}

func TestExplanationJobHandlersCreateAndPollError(t *testing.T) {
	resetExplanationJobsState()
	orig := runExplanationAsync
	runExplanationAsync = func(_ context.Context, _ []abclib.ProductResult, _ string) (string, error) {
		return "", errors.New("boom")
	}
	t.Cleanup(func() {
		runExplanationAsync = orig
		resetExplanationJobsState()
	})

	createReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/abc/explanation/jobs",
		strings.NewReader(`{"result":[{"SKU":"1","Name":"A","Quantity":1,"PriceUnit":10,"PriceTotal":10,"ShareTotal":1,"ShareAccumulated":1,"Group":"A"}]}`),
	)
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	ExplanationJobCreateHandler(createRec, createReq)

	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d", createRec.Code, http.StatusOK)
	}

	var createBody explanationJobResponse
	if err := json.NewDecoder(createRec.Body).Decode(&createBody); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		statusReq := httptest.NewRequest(
			http.MethodGet,
			"/api/v1/abc/explanation/jobs/"+createBody.JobID,
			nil,
		)
		statusRec := httptest.NewRecorder()
		ExplanationJobStatusHandler(statusRec, statusReq)

		if statusRec.Code != http.StatusOK {
			t.Fatalf("status endpoint code = %d, want %d", statusRec.Code, http.StatusOK)
		}

		var statusBody explanationJobResponse
		if err := json.NewDecoder(statusRec.Body).Decode(&statusBody); err != nil {
			t.Fatalf("decode status response: %v", err)
		}

		if statusBody.Status == explanationJobStatusError {
			if statusBody.Error == nil {
				t.Fatalf("error payload is required")
			}
			if statusBody.Error.Code != ErrAssistantFailed {
				t.Fatalf("error code = %q, want %q", statusBody.Error.Code, ErrAssistantFailed)
			}
			if !strings.Contains(strings.ToLower(statusBody.Error.Message), "boom") {
				t.Fatalf("error message = %q, want contains boom", statusBody.Error.Message)
			}
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("job did not fail before timeout")
}

func TestExplanationJobStatusHandlerMissingID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/abc/explanation/jobs/", nil)
	rec := httptest.NewRecorder()

	ExplanationJobStatusHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var body struct {
		Error APIError `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != ErrMissingJobID {
		t.Fatalf("error code = %q, want %q", body.Error.Code, ErrMissingJobID)
	}
}

func TestExplanationJobStatusHandlerNotFound(t *testing.T) {
	resetExplanationJobsState()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/abc/explanation/jobs/unknown", nil)
	rec := httptest.NewRecorder()

	ExplanationJobStatusHandler(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}

	var body struct {
		Error APIError `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != ErrJobNotFound {
		t.Fatalf("error code = %q, want %q", body.Error.Code, ErrJobNotFound)
	}
}

func resetExplanationJobsState() {
	explanationJobsStore = newExplanationJobStore()
}
