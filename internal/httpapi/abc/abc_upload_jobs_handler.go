package abc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	abclib "github.com/Sales-Analysis/abc-helper-lib/abc"
)

const (
	uploadJobsPathPrefix = "/api/v1/abc/upload/jobs/"

	uploadJobStatusQueued      = "queued"
	uploadJobStatusRunning     = "running"
	uploadJobStatusAIPreparing = "ai_preparing"
	uploadJobStatusDone        = "done"
	uploadJobStatusError       = "error"

	uploadJobStageQueued      = "queued"
	uploadJobStageValidating  = "validating"
	uploadJobStageParsingRows = "parsing_rows"
	uploadJobStageAIPreparing = "ai_preparing"
	uploadJobStageCalculating = "calculating"
	uploadJobStageFinalizing  = "finalizing"
	uploadJobStageDone        = "done"
	uploadJobStageError       = "error"

	uploadJobTTL       = 24 * time.Hour
	uploadJobMaxItems  = 500
	uploadFileTTL      = 6 * time.Hour
	uploadFileMaxItems = 300
)

var (
	uploadJobsStore      = newUploadJobStore()
	uploadFilesStore     = newUploadFileStore()
	runUploadJobFromFile = defaultRunUploadJobFromFile
	uploadJobTimeout     = envDuration("ABC_UPLOAD_JOB_TIMEOUT", 30*time.Minute)
)

type uploadJobStore struct {
	mu   sync.Mutex
	jobs map[string]storedUploadJob
}

type uploadFileStore struct {
	mu    sync.Mutex
	files map[string]storedUploadFile
}

type storedUploadFile struct {
	FilePath  string
	Filename  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type storedUploadJob struct {
	Status      string
	Stage       string
	Progress    int
	FileID      string
	Result      []abclib.ProductResult
	Preparation *UploadPreparation
	Error       *APIError
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type uploadJobResponse struct {
	Status      string                 `json:"status"`
	Stage       string                 `json:"stage,omitempty"`
	Progress    int                    `json:"progress"`
	JobID       string                 `json:"job_id"`
	FileID      string                 `json:"file_id,omitempty"`
	Result      []abclib.ProductResult `json:"result,omitempty"`
	Preparation *UploadPreparation     `json:"preparation,omitempty"`
	Error       *APIError              `json:"error,omitempty"`
}

type uploadJobRunner func(
	ctx context.Context,
	filePath, filename, requestID string,
	confirmedPreparation *UploadPreparation,
	prepareOnly bool,
) (UploadResponse, error)

func newUploadJobStore() *uploadJobStore {
	return &uploadJobStore{
		jobs: make(map[string]storedUploadJob),
	}
}

func newUploadFileStore() *uploadFileStore {
	return &uploadFileStore{
		files: make(map[string]storedUploadFile),
	}
}

func defaultRunUploadJobFromFile(
	ctx context.Context,
	filePath, filename, requestID string,
	confirmedPreparation *UploadPreparation,
	prepareOnly bool,
) (UploadResponse, error) {
	slog.Default().Info("abc_upload_job_run_started",
		slog.String("request_id", strings.TrimSpace(requestID)),
		slog.String("filename", strings.TrimSpace(filename)),
		slog.Bool("prepare_only", prepareOnly),
	)
	if sizeErr := validateXLSXWorksheetSizeBudget(filePath); sizeErr != nil {
		slog.Default().Warn("abc_upload_job_validate_failed",
			slog.String("request_id", strings.TrimSpace(requestID)),
			slog.String("filename", strings.TrimSpace(filename)),
			slog.String("err", strings.TrimSpace(sizeErr.Error())),
		)
		return UploadResponse{}, &parseError{
			code: ErrInvalidXLSX,
			msg:  strings.TrimSpace(sizeErr.Error()),
		}
	}

	if prepareOnly {
		preparation, prepErr := prepareUploadFromFile(ctx, filename, filePath, requestID)
		if prepErr != nil {
			return UploadResponse{}, prepErr
		}
		return UploadResponse{
			Status:      "prepared",
			Preparation: &preparation,
		}, nil
	}

	return analyzeUploadFromFileWithPreparation(ctx, filename, filePath, requestID, confirmedPreparation)
}

// UploadJobCreateHandler creates async upload+analysis job.
func UploadJobCreateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, ErrMethodNotAllowed, "method not allowed")
		return
	}

	input, err := parseUploadJobCreateInput(w, r)
	if err != nil {
		writeUploadError(w, r.Header.Get("X-Request-ID"), err)
		return
	}

	jobID, err := enqueueUploadJob(
		input.TempPath,
		input.Filename,
		input.FileID,
		input.RequestID,
		input.ConfirmedPreparation,
		input.PrepareOnly,
		input.CleanupTemp,
	)
	if err != nil {
		if input.CleanupTemp {
			_ = os.Remove(input.TempPath)
		}
		slog.Default().Error("abc_upload_job_enqueue_failed",
			slog.String("request_id", strings.TrimSpace(input.RequestID)),
			slog.String("filename", strings.TrimSpace(input.Filename)),
			slog.String("err", strings.TrimSpace(err.Error())),
		)
		writeErr(w, http.StatusBadRequest, ErrInvalidXLSX, "cannot enqueue upload job")
		return
	}

	slog.Default().Info("abc_upload_job_queued",
		slog.String("request_id", strings.TrimSpace(input.RequestID)),
		slog.String("job_id", jobID),
		slog.String("file_id", strings.TrimSpace(input.FileID)),
		slog.String("filename", strings.TrimSpace(input.Filename)),
		slog.Bool("prepare_only", input.PrepareOnly),
	)

	writeOK(w, uploadJobResponse{
		Status:   uploadJobStatusQueued,
		Stage:    uploadJobStageQueued,
		Progress: 0,
		JobID:    jobID,
		FileID:   input.FileID,
	})
}

// UploadJobStatusHandler returns async upload job status and final result.
func UploadJobStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, ErrMethodNotAllowed, "method not allowed")
		return
	}

	jobID, ok := parseUploadJobID(r.URL.Path)
	if !ok {
		writeErr(w, http.StatusBadRequest, ErrMissingJobID, "job_id is required")
		return
	}

	job, found := uploadJobsStore.get(jobID)
	if !found {
		writeErr(w, http.StatusNotFound, ErrJobNotFound, "upload job not found")
		return
	}

	resp := uploadJobResponse{
		Status:      job.Status,
		Stage:       job.Stage,
		Progress:    job.Progress,
		JobID:       jobID,
		Preparation: job.Preparation,
		Error:       job.Error,
	}
	if job.Status == uploadJobStatusDone {
		resp.Result = append([]abclib.ProductResult(nil), job.Result...)
	}

	writeOK(w, resp)
}

func enqueueUploadJob(
	tempPath, filename, fileID, requestID string,
	confirmedPreparation *UploadPreparation,
	prepareOnly bool,
	cleanupTemp bool,
) (string, error) {
	jobID, err := newUploadJobID()
	if err != nil {
		return "", err
	}
	uploadJobsStore.create(jobID, fileID)

	filename = strings.TrimSpace(filename)
	requestID = strings.TrimSpace(requestID)
	confirmedCopy := cloneUploadPreparation(confirmedPreparation)
	queuedAt := time.Now()

	go func(
		jobID, tempPath, filename, requestID string,
		confirmedPreparation *UploadPreparation,
		prepareOnly bool,
		cleanupTemp bool,
		queuedAt time.Time,
	) {
		runStart := time.Now()
		if cleanupTemp {
			defer func() { _ = os.Remove(tempPath) }()
		}

		slog.Default().Info("abc_upload_job_started",
			slog.String("request_id", requestID),
			slog.String("job_id", jobID),
			slog.String("filename", filename),
			slog.Bool("prepare_only", prepareOnly),
			slog.Duration("queued_for", runStart.Sub(queuedAt)),
		)

		uploadJobsStore.markRunning(jobID)

		ctx, cancel := context.WithTimeout(context.Background(), uploadJobTimeout)
		defer cancel()
		ctx = withUploadProgressReporter(ctx, uploadProgressReporter{
			OnAIPreparing: func() {
				uploadJobsStore.markAIPreparing(jobID)
			},
			OnProgress: func(stage string, progress int) {
				uploadJobsStore.markProgress(jobID, progress, stage)
			},
		})
		uploadJobsStore.markProgress(jobID, 5, uploadJobStageValidating)

		resp, runErr := runUploadJobFromFile(
			ctx,
			tempPath,
			filename,
			requestID,
			confirmedPreparation,
			prepareOnly,
		)
		if runErr != nil {
			apiErr := toUploadAPIError(runErr)
			uploadJobsStore.markError(jobID, apiErr)
			slog.Default().Warn("abc_upload_job_failed",
				slog.String("request_id", requestID),
				slog.String("job_id", jobID),
				slog.String("filename", filename),
				slog.Bool("prepare_only", prepareOnly),
				slog.String("code", string(apiErr.Code)),
				slog.String("message", strings.TrimSpace(apiErr.Message)),
				slog.Duration("duration", time.Since(runStart)),
			)
			return
		}
		UploadCounter.WithLabelValues("success").Inc()
		uploadJobsStore.markDone(jobID, resp)
		slog.Default().Info("abc_upload_job_done",
			slog.String("request_id", requestID),
			slog.String("job_id", jobID),
			slog.String("filename", filename),
			slog.Bool("prepare_only", prepareOnly),
			slog.Int("results", len(resp.Result)),
			slog.Bool("has_preparation", resp.Preparation != nil),
			slog.Duration("duration", time.Since(runStart)),
		)
	}(jobID, tempPath, filename, requestID, confirmedCopy, prepareOnly, cleanupTemp, queuedAt)

	return jobID, nil
}

type uploadJobCreateInput struct {
	TempPath             string
	Filename             string
	FileID               string
	CleanupTemp          bool
	PrepareOnly          bool
	ConfirmedPreparation *UploadPreparation
	RequestID            string
}

func parseUploadJobCreateInput(w http.ResponseWriter, r *http.Request) (uploadJobCreateInput, error) {
	if r == nil {
		return uploadJobCreateInput{}, &parseError{code: ErrInvalidForm, msg: "invalid form: empty request"}
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxMultipartMemory); err != nil {
		return uploadJobCreateInput{}, &parseError{
			code: ErrInvalidForm,
			msg:  "invalid form: " + err.Error(),
		}
	}

	prepareOnly := parseUploadPrepareOnly(r)
	confirmedPreparation, err := parseUploadConfirmedPreparation(r)
	if err != nil {
		return uploadJobCreateInput{}, err
	}
	if prepareOnly && confirmedPreparation != nil {
		return uploadJobCreateInput{}, &parseError{
			code: ErrInvalidForm,
			msg:  "preparation is not allowed with prepare_only",
		}
	}

	fileID := strings.TrimSpace(r.FormValue("file_id"))
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if fileID != "" {
		if _, _, fileErr := r.FormFile("file"); fileErr == nil {
			return uploadJobCreateInput{}, &parseError{
				code: ErrInvalidForm,
				msg:  "send either file or file_id",
			}
		}
		stored, found := uploadFilesStore.get(fileID)
		if !found {
			slog.Default().Warn("abc_upload_job_file_not_found",
				slog.String("request_id", requestID),
				slog.String("file_id", fileID),
			)
			return uploadJobCreateInput{}, &parseError{
				code: ErrUploadFileNotFound,
				msg:  "upload file not found by file_id",
			}
		}
		if sizeErr := validateXLSXWorksheetSizeBudget(stored.FilePath); sizeErr != nil {
			slog.Default().Warn("abc_upload_job_reused_file_rejected",
				slog.String("request_id", requestID),
				slog.String("file_id", fileID),
				slog.String("filename", strings.TrimSpace(stored.Filename)),
				slog.String("err", strings.TrimSpace(sizeErr.Error())),
			)
			return uploadJobCreateInput{}, &parseError{
				code: ErrInvalidXLSX,
				msg:  strings.TrimSpace(sizeErr.Error()),
			}
		}
		slog.Default().Info("abc_upload_job_reused_file_accepted",
			slog.String("request_id", requestID),
			slog.String("file_id", fileID),
			slog.String("filename", strings.TrimSpace(stored.Filename)),
			slog.Bool("prepare_only", prepareOnly),
		)
		return uploadJobCreateInput{
			TempPath:             stored.FilePath,
			Filename:             stored.Filename,
			FileID:               fileID,
			CleanupTemp:          false,
			PrepareOnly:          prepareOnly,
			ConfirmedPreparation: confirmedPreparation,
			RequestID:            requestID,
		}, nil
	}

	file, fileHeader, formErr := r.FormFile("file")
	if formErr != nil {
		return uploadJobCreateInput{}, &parseError{
			code: ErrMissingFile,
			msg:  "file is required",
		}
	}
	defer func() { _ = file.Close() }()
	if fileHeader.Size == 0 {
		slog.Default().Warn("abc_upload_job_empty_file",
			slog.String("request_id", requestID),
			slog.String("filename", strings.TrimSpace(fileHeader.Filename)),
		)
		return uploadJobCreateInput{}, &parseError{
			code: ErrEmptyFile,
			msg:  "file is empty",
		}
	}

	tempPath, saveErr := saveUploadedFileToTemp(file)
	if saveErr != nil {
		return uploadJobCreateInput{}, &parseError{
			code: ErrReadError,
			msg:  "cannot read file",
		}
	}
	if sizeErr := validateXLSXWorksheetSizeBudget(tempPath); sizeErr != nil {
		_ = os.Remove(tempPath)
		slog.Default().Warn("abc_upload_job_file_rejected",
			slog.String("request_id", requestID),
			slog.String("filename", strings.TrimSpace(fileHeader.Filename)),
			slog.Int64("size_bytes", fileHeader.Size),
			slog.String("err", strings.TrimSpace(sizeErr.Error())),
		)
		return uploadJobCreateInput{}, &parseError{
			code: ErrInvalidXLSX,
			msg:  strings.TrimSpace(sizeErr.Error()),
		}
	}

	input := uploadJobCreateInput{
		TempPath:             tempPath,
		Filename:             fileHeader.Filename,
		CleanupTemp:          true,
		PrepareOnly:          prepareOnly,
		ConfirmedPreparation: confirmedPreparation,
		RequestID:            requestID,
	}
	createdFileID, putErr := uploadFilesStore.put(tempPath, fileHeader.Filename)
	if putErr != nil {
		_ = os.Remove(tempPath)
		slog.Default().Error("abc_upload_job_store_file_failed",
			slog.String("request_id", requestID),
			slog.String("filename", strings.TrimSpace(fileHeader.Filename)),
			slog.Int64("size_bytes", fileHeader.Size),
			slog.String("err", strings.TrimSpace(putErr.Error())),
		)
		return uploadJobCreateInput{}, &parseError{
			code: ErrInvalidXLSX,
			msg:  "cannot store uploaded file",
		}
	}
	input.FileID = createdFileID
	input.CleanupTemp = false
	slog.Default().Info("abc_upload_job_file_accepted",
		slog.String("request_id", requestID),
		slog.String("filename", strings.TrimSpace(fileHeader.Filename)),
		slog.Int64("size_bytes", fileHeader.Size),
		slog.String("file_id", strings.TrimSpace(input.FileID)),
		slog.Bool("prepare_only", prepareOnly),
	)
	return input, nil
}

func parseUploadPrepareOnly(r *http.Request) bool {
	if r == nil {
		return false
	}
	raw := strings.TrimSpace(strings.ToLower(r.FormValue("prepare_only")))
	switch raw {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func parseUploadConfirmedPreparation(r *http.Request) (*UploadPreparation, error) {
	if r == nil {
		return nil, nil
	}

	raw := strings.TrimSpace(r.FormValue("preparation"))
	if raw == "" {
		return nil, nil
	}

	var preparation UploadPreparation
	if err := json.Unmarshal([]byte(raw), &preparation); err != nil {
		return nil, &parseError{
			code: ErrInvalidJSON,
			msg:  "invalid preparation json",
		}
	}
	if preparation.Columns == nil {
		return nil, &parseError{
			code: ErrInvalidHeader,
			msg:  "preparation columns are required",
		}
	}

	return &preparation, nil
}

func (s *uploadFileStore) put(filePath, filename string) (string, error) {
	if s == nil {
		return "", errors.New("upload file store is not initialized")
	}
	now := time.Now()
	fileID, err := newUploadJobID()
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	s.files[fileID] = storedUploadFile{
		FilePath:  filePath,
		Filename:  strings.TrimSpace(filename),
		CreatedAt: now,
		UpdatedAt: now,
	}
	return fileID, nil
}

func (s *uploadFileStore) get(fileID string) (storedUploadFile, bool) {
	if s == nil {
		return storedUploadFile{}, false
	}
	now := time.Now()
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return storedUploadFile{}, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	file, found := s.files[fileID]
	if !found {
		return storedUploadFile{}, false
	}
	file.UpdatedAt = now
	s.files[fileID] = file
	return file, true
}

func (s *uploadFileStore) pruneLocked(now time.Time) {
	if s == nil {
		return
	}

	for fileID, file := range s.files {
		if now.Sub(file.UpdatedAt) > uploadFileTTL {
			_ = os.Remove(file.FilePath)
			delete(s.files, fileID)
		}
	}
	if len(s.files) <= uploadFileMaxItems {
		return
	}

	type fileRef struct {
		ID        string
		UpdatedAt time.Time
	}
	items := make([]fileRef, 0, len(s.files))
	for fileID, file := range s.files {
		items = append(items, fileRef{ID: fileID, UpdatedAt: file.UpdatedAt})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt.Before(items[j].UpdatedAt)
	})

	overflow := len(s.files) - uploadFileMaxItems
	for i := 0; i < overflow && i < len(items); i++ {
		file := s.files[items[i].ID]
		_ = os.Remove(file.FilePath)
		delete(s.files, items[i].ID)
	}
}

func cloneUploadPreparation(preparation *UploadPreparation) *UploadPreparation {
	if preparation == nil {
		return nil
	}

	copyPrep := *preparation
	if preparation.Columns != nil {
		copyCols := *preparation.Columns
		copyPrep.Columns = &copyCols
	}
	if len(preparation.Available) > 0 {
		copyPrep.Available = append([]UploadPreparationColumn(nil), preparation.Available...)
	}
	return &copyPrep
}

func toUploadAPIError(err error) *APIError {
	if err == nil {
		return &APIError{Code: ErrInvalidXLSX, Message: "unknown upload error"}
	}
	perr := new(parseError)
	if errors.As(err, &perr) {
		return &APIError{Code: perr.code, Message: strings.TrimSpace(perr.msg)}
	}
	return &APIError{Code: ErrInvalidXLSX, Message: strings.TrimSpace(err.Error())}
}

func saveUploadedFileToTemp(file io.Reader) (string, error) {
	tmpFile, err := os.CreateTemp("", "abc-upload-*.xlsx")
	if err != nil {
		return "", err
	}
	defer func() { _ = tmpFile.Close() }()

	if _, err := io.Copy(tmpFile, file); err != nil {
		_ = os.Remove(tmpFile.Name())
		return "", err
	}
	return tmpFile.Name(), nil
}

func parseUploadJobID(path string) (string, bool) {
	if !strings.HasPrefix(path, uploadJobsPathPrefix) {
		return "", false
	}
	jobID := strings.TrimSpace(strings.TrimPrefix(path, uploadJobsPathPrefix))
	if jobID == "" || strings.Contains(jobID, "/") {
		return "", false
	}
	return jobID, true
}

func (s *uploadJobStore) create(jobID string, fileID string) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pruneLocked(now)
	s.jobs[jobID] = storedUploadJob{
		Status:    uploadJobStatusQueued,
		Stage:     uploadJobStageQueued,
		Progress:  0,
		FileID:    strings.TrimSpace(fileID),
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func (s *uploadJobStore) markRunning(jobID string) {
	s.update(jobID, func(job storedUploadJob, now time.Time) storedUploadJob {
		job.Status = uploadJobStatusRunning
		if job.Stage == "" || job.Stage == uploadJobStageQueued {
			job.Stage = uploadJobStageValidating
		}
		if job.Progress < 1 {
			job.Progress = 1
		}
		job.UpdatedAt = now
		return job
	})
}

func (s *uploadJobStore) markAIPreparing(jobID string) {
	s.update(jobID, func(job storedUploadJob, now time.Time) storedUploadJob {
		if job.Status == uploadJobStatusDone || job.Status == uploadJobStatusError {
			return job
		}
		job.Status = uploadJobStatusAIPreparing
		job.Stage = uploadJobStageAIPreparing
		if job.Progress < 40 {
			job.Progress = 40
		}
		job.UpdatedAt = now
		return job
	})
}

func (s *uploadJobStore) markProgress(jobID string, progress int, stage string) {
	s.update(jobID, func(job storedUploadJob, now time.Time) storedUploadJob {
		if job.Status == uploadJobStatusDone || job.Status == uploadJobStatusError {
			return job
		}

		if progress < 0 {
			progress = 0
		}
		if progress > 99 {
			progress = 99
		}
		if progress > job.Progress {
			job.Progress = progress
		}

		stage = strings.TrimSpace(stage)
		if stage != "" {
			job.Stage = stage
			if stage == uploadJobStageAIPreparing {
				job.Status = uploadJobStatusAIPreparing
			} else {
				job.Status = uploadJobStatusRunning
			}
		}
		job.UpdatedAt = now
		return job
	})
}

func (s *uploadJobStore) markDone(jobID string, resp UploadResponse) {
	s.update(jobID, func(job storedUploadJob, now time.Time) storedUploadJob {
		job.Status = uploadJobStatusDone
		job.Stage = uploadJobStageDone
		job.Progress = 100
		job.Result = append([]abclib.ProductResult(nil), resp.Result...)
		if resp.Preparation != nil {
			prepCopy := *resp.Preparation
			job.Preparation = &prepCopy
		} else {
			job.Preparation = nil
		}
		job.Error = nil
		job.UpdatedAt = now
		return job
	})
}

func (s *uploadJobStore) markError(jobID string, apiErr *APIError) {
	s.update(jobID, func(job storedUploadJob, now time.Time) storedUploadJob {
		job.Status = uploadJobStatusError
		job.Stage = uploadJobStageError
		job.Result = nil
		job.Preparation = nil
		job.Error = apiErr
		job.UpdatedAt = now
		return job
	})
}

func (s *uploadJobStore) update(jobID string, mutate func(storedUploadJob, time.Time) storedUploadJob) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[jobID]
	if !ok {
		return
	}
	s.jobs[jobID] = mutate(job, now)
	s.pruneLocked(now)
}

func (s *uploadJobStore) get(jobID string) (storedUploadJob, bool) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pruneLocked(now)
	job, ok := s.jobs[jobID]
	return job, ok
}

func (s *uploadJobStore) pruneLocked(now time.Time) {
	for jobID, job := range s.jobs {
		if now.Sub(job.UpdatedAt) > uploadJobTTL {
			delete(s.jobs, jobID)
		}
	}

	if len(s.jobs) <= uploadJobMaxItems {
		return
	}

	type completedJob struct {
		ID        string
		UpdatedAt time.Time
	}
	completed := make([]completedJob, 0, len(s.jobs))
	for jobID, job := range s.jobs {
		if job.Status == uploadJobStatusDone || job.Status == uploadJobStatusError {
			completed = append(completed, completedJob{ID: jobID, UpdatedAt: job.UpdatedAt})
		}
	}
	sort.Slice(completed, func(i, j int) bool {
		return completed[i].UpdatedAt.Before(completed[j].UpdatedAt)
	})

	overflow := len(s.jobs) - uploadJobMaxItems
	for i := 0; i < overflow && i < len(completed); i++ {
		delete(s.jobs, completed[i].ID)
	}
}

func newUploadJobID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
