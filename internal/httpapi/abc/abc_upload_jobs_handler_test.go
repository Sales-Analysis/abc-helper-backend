package abc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	abclib "github.com/Sales-Analysis/abc-helper-lib/abc"
)

func TestUploadJobCreateHandlerMethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/abc/upload/jobs", nil)
	rec := httptest.NewRecorder()

	UploadJobCreateHandler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestUploadJobHandlersCreateAndPollDone(t *testing.T) {
	resetUploadJobsState()
	orig := runUploadJobFromFile
	runUploadJobFromFile = func(
		_ context.Context,
		_ string,
		_ string,
		_ string,
		_ *UploadPreparation,
		_ bool,
	) (UploadResponse, error) {
		return UploadResponse{
			Status: "ok",
			Result: []abclib.ProductResult{
				{SKU: "A-1", Name: "Товар A", Quantity: 10, PriceUnit: 100, PriceTotal: 1000, ShareTotal: 100, ShareAccumulated: 100, Group: "A"},
			},
			Preparation: &UploadPreparation{Mode: "ai"},
		}, nil
	}
	t.Cleanup(func() {
		runUploadJobFromFile = orig
		resetUploadJobsState()
	})

	content := makeXLSX(t, [][]any{
		{"Field1", "Field2", "Field3", "Field4"},
		{"A-1", "Товар A", "1000", "10"},
	})
	req := newUploadJobCreateRequest(t, "sample.xlsx", content)
	rec := httptest.NewRecorder()
	UploadJobCreateHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var createBody uploadJobResponse
	if err := json.NewDecoder(rec.Body).Decode(&createBody); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if createBody.Progress != 0 || createBody.Stage != uploadJobStageQueued {
		t.Fatalf("unexpected create progress/stage: progress=%d stage=%q", createBody.Progress, createBody.Stage)
	}
	if strings.TrimSpace(createBody.JobID) == "" {
		t.Fatalf("job_id must be set")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		statusReq := httptest.NewRequest(
			http.MethodGet,
			"/api/v1/abc/upload/jobs/"+createBody.JobID,
			nil,
		)
		statusRec := httptest.NewRecorder()
		UploadJobStatusHandler(statusRec, statusReq)

		if statusRec.Code != http.StatusOK {
			t.Fatalf("status code = %d, want %d", statusRec.Code, http.StatusOK)
		}

		var statusBody uploadJobResponse
		if err := json.NewDecoder(statusRec.Body).Decode(&statusBody); err != nil {
			t.Fatalf("decode status response: %v", err)
		}

		if statusBody.Status == uploadJobStatusDone {
			if statusBody.Progress != 100 || statusBody.Stage != uploadJobStageDone {
				t.Fatalf("unexpected done progress/stage: progress=%d stage=%q", statusBody.Progress, statusBody.Stage)
			}
			if len(statusBody.Result) != 1 {
				t.Fatalf("expected 1 result, got %d", len(statusBody.Result))
			}
			if statusBody.Preparation == nil || statusBody.Preparation.Mode != "ai" {
				t.Fatalf("expected preparation mode ai, got %+v", statusBody.Preparation)
			}
			return
		}
		if statusBody.Status == uploadJobStatusError {
			t.Fatalf("job unexpectedly failed: %+v", statusBody.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("job did not complete before timeout")
}

func TestUploadJobHandlersCreateAndPollError(t *testing.T) {
	resetUploadJobsState()
	orig := runUploadJobFromFile
	runUploadJobFromFile = func(
		_ context.Context,
		_ string,
		_ string,
		_ string,
		_ *UploadPreparation,
		_ bool,
	) (UploadResponse, error) {
		return UploadResponse{}, &parseError{
			code: ErrUploadPrepFailed,
			msg:  "assistant upload preparation failed",
		}
	}
	t.Cleanup(func() {
		runUploadJobFromFile = orig
		resetUploadJobsState()
	})

	content := makeXLSX(t, [][]any{
		{"Field1", "Field2", "Field3", "Field4"},
		{"A-1", "Товар A", "1000", "10"},
	})
	req := newUploadJobCreateRequest(t, "sample.xlsx", content)
	rec := httptest.NewRecorder()
	UploadJobCreateHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var createBody uploadJobResponse
	if err := json.NewDecoder(rec.Body).Decode(&createBody); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		statusReq := httptest.NewRequest(
			http.MethodGet,
			"/api/v1/abc/upload/jobs/"+createBody.JobID,
			nil,
		)
		statusRec := httptest.NewRecorder()
		UploadJobStatusHandler(statusRec, statusReq)

		if statusRec.Code != http.StatusOK {
			t.Fatalf("status code = %d, want %d", statusRec.Code, http.StatusOK)
		}

		var statusBody uploadJobResponse
		if err := json.NewDecoder(statusRec.Body).Decode(&statusBody); err != nil {
			t.Fatalf("decode status response: %v", err)
		}

		if statusBody.Status == uploadJobStatusError {
			if statusBody.Error == nil {
				t.Fatalf("error payload must be set")
			}
			if statusBody.Error.Code != ErrUploadPrepFailed {
				t.Fatalf("error code = %q, want %q", statusBody.Error.Code, ErrUploadPrepFailed)
			}
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("job did not fail before timeout")
}

func TestUploadJobStatusHandlerNotFound(t *testing.T) {
	resetUploadJobsState()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/abc/upload/jobs/unknown", nil)
	rec := httptest.NewRecorder()

	UploadJobStatusHandler(rec, req)

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

func TestUploadJobCreateHandlerReadError(t *testing.T) {
	resetUploadJobsState()
	orig := runUploadJobFromFile
	runUploadJobFromFile = func(
		_ context.Context,
		_ string,
		_ string,
		_ string,
		_ *UploadPreparation,
		_ bool,
	) (UploadResponse, error) {
		return UploadResponse{}, errors.New("boom")
	}
	t.Cleanup(func() {
		runUploadJobFromFile = orig
		resetUploadJobsState()
	})

	content := makeXLSX(t, [][]any{
		{"Код", "Наименование", "Сумма", "Количество"},
		{"A-1", "Товар A", "100", "1"},
	})
	req := newUploadJobCreateRequest(t, "sample.xlsx", content)
	rec := httptest.NewRecorder()
	UploadJobCreateHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestUploadJobCreateHandlerRejectsOversizedWorksheetXML(t *testing.T) {
	resetUploadJobsState()
	oldLimit := maxWorksheetXMLB
	maxWorksheetXMLB = 1
	t.Cleanup(func() {
		maxWorksheetXMLB = oldLimit
		resetUploadJobsState()
	})

	content := makeXLSX(t, [][]any{
		{"Код", "Наименование", "Сумма", "Количество"},
		{"A-1", "Товар A", "100", "1"},
	})
	req := newUploadJobCreateRequest(t, "sample.xlsx", content)
	rec := httptest.NewRecorder()
	UploadJobCreateHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	var body struct {
		Error APIError `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != ErrInvalidXLSX {
		t.Fatalf("error code = %q, want %q", body.Error.Code, ErrInvalidXLSX)
	}
}

func TestUploadJobCreateHandlerPassesConfirmedPreparation(t *testing.T) {
	resetUploadJobsState()
	orig := runUploadJobFromFile
	receivedPreparation := make(chan *UploadPreparation, 8)
	runUploadJobFromFile = func(
		_ context.Context,
		_ string,
		_ string,
		_ string,
		preparation *UploadPreparation,
		_ bool,
	) (UploadResponse, error) {
		receivedPreparation <- preparation
		return UploadResponse{
			Status: "ok",
			Result: []abclib.ProductResult{
				{SKU: "A-1", Name: "Товар A", Quantity: 1, PriceUnit: 100, PriceTotal: 100, ShareTotal: 100, ShareAccumulated: 100, Group: "A"},
			},
		}, nil
	}
	t.Cleanup(func() {
		runUploadJobFromFile = orig
		resetUploadJobsState()
	})

	content := makeXLSX(t, [][]any{
		{"Код", "Наименование", "Сумма", "Количество"},
		{"A-1", "Товар A", "100", "1"},
	})
	preparationJSON := `{"mode":"ai","header_row":1,"data_start_row":2,"columns":{"name":{"index":2},"quantity":{"index":4},"value":{"index":3}}}`

	req := newUploadJobCreateRequestWithPreparation(t, "sample.xlsx", content, preparationJSON)
	rec := httptest.NewRecorder()
	UploadJobCreateHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case preparation := <-receivedPreparation:
			if preparation == nil {
				continue
			}
			if preparation.Columns == nil {
				t.Fatalf("expected preparation columns")
			}
			if preparation.Columns.Name.Index != 2 || preparation.Columns.Quantity.Index != 4 || preparation.Columns.Value.Index != 3 {
				t.Fatalf("unexpected preparation columns: %+v", preparation.Columns)
			}
			return
		case <-deadline:
			t.Fatalf("runner did not receive confirmed preparation")
		}
	}
}

func TestUploadJobCreateHandlerInvalidPreparationJSON(t *testing.T) {
	content := makeXLSX(t, [][]any{
		{"Field1", "Field2", "Field3", "Field4"},
		{"A-1", "Товар A", "1000", "10"},
	})

	req := newUploadJobCreateRequestWithPreparation(t, "sample.xlsx", content, "{invalid")
	rec := httptest.NewRecorder()
	UploadJobCreateHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
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

func TestUploadJobCreateHandlerPrepareOnlyFlag(t *testing.T) {
	resetUploadJobsState()
	orig := runUploadJobFromFile
	receivedPrepareOnly := make(chan bool, 4)
	runUploadJobFromFile = func(
		_ context.Context,
		_ string,
		_ string,
		_ string,
		_ *UploadPreparation,
		prepareOnly bool,
	) (UploadResponse, error) {
		receivedPrepareOnly <- prepareOnly
		return UploadResponse{
			Status: "prepared",
			Preparation: &UploadPreparation{
				Mode:    "header",
				Columns: &UploadPreparationColumns{Name: UploadPreparationColumn{Index: 2}},
			},
		}, nil
	}
	t.Cleanup(func() {
		runUploadJobFromFile = orig
		resetUploadJobsState()
	})

	content := makeXLSX(t, [][]any{
		{"Код", "Наименование", "Сумма", "Количество"},
		{"A-1", "Товар A", "100", "1"},
	})
	req := newUploadJobCreateRequestWithOptions(t, "sample.xlsx", content, uploadJobCreateRequestOptions{
		PrepareOnly: true,
	})
	rec := httptest.NewRecorder()
	UploadJobCreateHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var createBody uploadJobResponse
	if err := json.NewDecoder(rec.Body).Decode(&createBody); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if strings.TrimSpace(createBody.FileID) == "" {
		t.Fatalf("expected file_id for prepare_only upload")
	}

	select {
	case prepareOnly := <-receivedPrepareOnly:
		if !prepareOnly {
			t.Fatalf("expected prepareOnly=true")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("runner did not receive prepare_only flag")
	}
}

func TestUploadJobCreateHandlerPrepareOnlyWithPreparationRejected(t *testing.T) {
	content := makeXLSX(t, [][]any{
		{"Field1", "Field2", "Field3", "Field4"},
		{"A-1", "Товар A", "1000", "10"},
	})
	preparationJSON := `{"mode":"ai","columns":{"name":{"index":2},"quantity":{"index":4},"value":{"index":3}}}`
	req := newUploadJobCreateRequestWithOptions(t, "sample.xlsx", content, uploadJobCreateRequestOptions{
		PreparationJSON: preparationJSON,
		PrepareOnly:     true,
	})
	rec := httptest.NewRecorder()
	UploadJobCreateHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	var body struct {
		Error APIError `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != ErrInvalidForm {
		t.Fatalf("error code = %q, want %q", body.Error.Code, ErrInvalidForm)
	}
}

func TestUploadJobCreateHandlerReuseUploadedFileByFileID(t *testing.T) {
	resetUploadJobsState()
	orig := runUploadJobFromFile
	callCount := 0
	receivedPrepareOnly := make(chan bool, 4)
	runUploadJobFromFile = func(
		_ context.Context,
		_ string,
		_ string,
		_ string,
		_ *UploadPreparation,
		prepareOnly bool,
	) (UploadResponse, error) {
		callCount++
		receivedPrepareOnly <- prepareOnly
		if prepareOnly {
			return UploadResponse{
				Status: "prepared",
				Preparation: &UploadPreparation{
					Mode:    "ai",
					Columns: &UploadPreparationColumns{Name: UploadPreparationColumn{Index: 2}},
				},
			}, nil
		}
		return UploadResponse{
			Status: "ok",
			Result: []abclib.ProductResult{
				{SKU: "A-1", Name: "Товар A", Quantity: 1, PriceUnit: 100, PriceTotal: 100, ShareTotal: 100, ShareAccumulated: 100, Group: "A"},
			},
		}, nil
	}
	t.Cleanup(func() {
		runUploadJobFromFile = orig
		resetUploadJobsState()
	})

	content := makeXLSX(t, [][]any{
		{"Код", "Наименование", "Сумма", "Количество"},
		{"A-1", "Товар A", "100", "1"},
	})

	prepareReq := newUploadJobCreateRequestWithOptions(t, "sample.xlsx", content, uploadJobCreateRequestOptions{
		PrepareOnly: true,
	})
	prepareRec := httptest.NewRecorder()
	UploadJobCreateHandler(prepareRec, prepareReq)
	if prepareRec.Code != http.StatusOK {
		t.Fatalf("prepare status = %d, want %d; body=%s", prepareRec.Code, http.StatusOK, prepareRec.Body.String())
	}

	var prepareBody uploadJobResponse
	if err := json.NewDecoder(prepareRec.Body).Decode(&prepareBody); err != nil {
		t.Fatalf("decode prepare response: %v", err)
	}
	fileID := strings.TrimSpace(prepareBody.FileID)
	if fileID == "" {
		t.Fatalf("expected non-empty file_id")
	}

	analyzeReq := newUploadJobCreateRequestWithOptions(t, "", nil, uploadJobCreateRequestOptions{
		FileID:          fileID,
		PreparationJSON: `{"mode":"ai","columns":{"name":{"index":2},"quantity":{"index":4},"value":{"index":3}}}`,
	})
	analyzeRec := httptest.NewRecorder()
	UploadJobCreateHandler(analyzeRec, analyzeReq)
	if analyzeRec.Code != http.StatusOK {
		t.Fatalf("analyze status = %d, want %d; body=%s", analyzeRec.Code, http.StatusOK, analyzeRec.Body.String())
	}

	deadline := time.After(2 * time.Second)
	seenPrepare := false
	seenAnalyze := false
	for !(seenPrepare && seenAnalyze) {
		select {
		case prepareOnly := <-receivedPrepareOnly:
			if prepareOnly {
				seenPrepare = true
			} else {
				seenAnalyze = true
			}
		case <-deadline:
			t.Fatalf("did not observe both prepare_only and analyze executions, callCount=%d", callCount)
		}
	}
}

func TestUploadJobCreateHandlerFileIDNotFound(t *testing.T) {
	resetUploadJobsState()
	req := newUploadJobCreateRequestWithOptions(t, "", nil, uploadJobCreateRequestOptions{
		FileID: "missing-file-id",
	})
	rec := httptest.NewRecorder()
	UploadJobCreateHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	var body struct {
		Error APIError `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != ErrUploadFileNotFound {
		t.Fatalf("error code = %q, want %q", body.Error.Code, ErrUploadFileNotFound)
	}
}

func TestUploadJobStoreMarkAIPreparing(t *testing.T) {
	resetUploadJobsState()

	const jobID = "job-ai-prep"
	uploadJobsStore.create(jobID, "")
	uploadJobsStore.markRunning(jobID)
	uploadJobsStore.markAIPreparing(jobID)

	job, found := uploadJobsStore.get(jobID)
	if !found {
		t.Fatalf("job must exist")
	}
	if job.Status != uploadJobStatusAIPreparing {
		t.Fatalf("status = %q, want %q", job.Status, uploadJobStatusAIPreparing)
	}

	uploadJobsStore.markError(jobID, &APIError{Code: ErrInvalidXLSX, Message: "x"})
	uploadJobsStore.markAIPreparing(jobID)
	job, _ = uploadJobsStore.get(jobID)
	if job.Status != uploadJobStatusError {
		t.Fatalf("status should stay %q after terminal state, got %q", uploadJobStatusError, job.Status)
	}
}

func newUploadJobCreateRequest(t *testing.T, filename string, content []byte) *http.Request {
	return newUploadJobCreateRequestWithOptions(t, filename, content, uploadJobCreateRequestOptions{})
}

func newUploadJobCreateRequestWithPreparation(
	t *testing.T,
	filename string,
	content []byte,
	preparationJSON string,
) *http.Request {
	return newUploadJobCreateRequestWithOptions(t, filename, content, uploadJobCreateRequestOptions{
		PreparationJSON: preparationJSON,
	})
}

type uploadJobCreateRequestOptions struct {
	PreparationJSON string
	PrepareOnly     bool
	FileID          string
	OmitFile        bool
}

func newUploadJobCreateRequestWithOptions(
	t *testing.T,
	filename string,
	content []byte,
	opts uploadJobCreateRequestOptions,
) *http.Request {
	t.Helper()

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if !opts.OmitFile && strings.TrimSpace(opts.FileID) == "" {
		fw, err := w.CreateFormFile("file", filename)
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, err := fw.Write(content); err != nil {
			t.Fatalf("write content: %v", err)
		}
	}
	if strings.TrimSpace(opts.PreparationJSON) != "" {
		if err := w.WriteField("preparation", opts.PreparationJSON); err != nil {
			t.Fatalf("write preparation field: %v", err)
		}
	}
	if strings.TrimSpace(opts.FileID) != "" {
		if err := w.WriteField("file_id", strings.TrimSpace(opts.FileID)); err != nil {
			t.Fatalf("write file_id field: %v", err)
		}
	}
	if opts.PrepareOnly {
		if err := w.WriteField("prepare_only", "1"); err != nil {
			t.Fatalf("write prepare_only field: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/abc/upload/jobs", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

func resetUploadJobsState() {
	uploadJobsStore = newUploadJobStore()
	uploadFilesStore = newUploadFileStore()
}
