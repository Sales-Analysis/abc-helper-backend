package abc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	abclib "github.com/Sales-Analysis/abc-helper-lib/abc"
)

const (
	explanationJobsPathPrefix = "/api/v1/abc/explanation/jobs/"

	explanationJobStatusQueued  = "queued"
	explanationJobStatusRunning = "running"
	explanationJobStatusDone    = "done"
	explanationJobStatusError   = "error"

	explanationJobTTL      = 24 * time.Hour
	explanationJobMaxItems = 1000
)

var (
	explanationJobsStore = newExplanationJobStore()
	runExplanationAsync  = explainAnalysis
)

type explanationJobStore struct {
	mu   sync.Mutex
	jobs map[string]storedExplanationJob
}

type storedExplanationJob struct {
	Status      string
	Explanation string
	Error       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type explanationJobResponse struct {
	Status      string    `json:"status"`
	JobID       string    `json:"job_id"`
	Explanation string    `json:"explanation,omitempty"`
	Error       *APIError `json:"error,omitempty"`
}

func newExplanationJobStore() *explanationJobStore {
	return &explanationJobStore{
		jobs: make(map[string]storedExplanationJob),
	}
}

func enqueueExplanationJob(result []abclib.ProductResult, requestID string) (string, error) {
	if len(result) == 0 {
		return "", errors.New("result is required")
	}

	jobID, err := newExplanationJobID()
	if err != nil {
		return "", err
	}

	resultCopy := append([]abclib.ProductResult(nil), result...)
	requestID = strings.TrimSpace(requestID)
	explanationJobsStore.create(jobID)

	go func(jobID string, result []abclib.ProductResult, requestID string) {
		explanationJobsStore.markRunning(jobID)

		answer, err := runExplanationAsync(context.Background(), result, requestID)
		if err != nil {
			log.Printf("assistant explanation job failed: job_id=%s err=%v", jobID, err)
			explanationJobsStore.markError(jobID, err.Error())
			return
		}

		answer = strings.TrimSpace(answer)
		if answer == "" {
			log.Printf("assistant explanation job failed: job_id=%s err=empty answer", jobID)
			explanationJobsStore.markError(jobID, "assistant explanation is empty")
			return
		}

		explanationJobsStore.markDone(jobID, answer)
	}(jobID, resultCopy, requestID)

	return jobID, nil
}

func (s *explanationJobStore) create(jobID string) {
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.pruneLocked(now)
	s.jobs[jobID] = storedExplanationJob{
		Status:    explanationJobStatusQueued,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func (s *explanationJobStore) markRunning(jobID string) {
	s.update(jobID, func(job storedExplanationJob, now time.Time) storedExplanationJob {
		job.Status = explanationJobStatusRunning
		job.UpdatedAt = now
		return job
	})
}

func (s *explanationJobStore) markDone(jobID, explanation string) {
	s.update(jobID, func(job storedExplanationJob, now time.Time) storedExplanationJob {
		job.Status = explanationJobStatusDone
		job.Explanation = explanation
		job.Error = ""
		job.UpdatedAt = now
		return job
	})
}

func (s *explanationJobStore) markError(jobID, message string) {
	s.update(jobID, func(job storedExplanationJob, now time.Time) storedExplanationJob {
		job.Status = explanationJobStatusError
		job.Explanation = ""
		job.Error = strings.TrimSpace(message)
		job.UpdatedAt = now
		return job
	})
}

func (s *explanationJobStore) update(jobID string, mutate func(storedExplanationJob, time.Time) storedExplanationJob) {
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

func (s *explanationJobStore) get(jobID string) (storedExplanationJob, bool) {
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.pruneLocked(now)
	job, ok := s.jobs[jobID]
	return job, ok
}

func (s *explanationJobStore) pruneLocked(now time.Time) {
	for jobID, job := range s.jobs {
		if now.Sub(job.UpdatedAt) > explanationJobTTL {
			delete(s.jobs, jobID)
		}
	}

	if len(s.jobs) <= explanationJobMaxItems {
		return
	}

	type completedJob struct {
		ID        string
		UpdatedAt time.Time
	}
	completed := make([]completedJob, 0, len(s.jobs))
	for jobID, job := range s.jobs {
		if job.Status == explanationJobStatusDone || job.Status == explanationJobStatusError {
			completed = append(completed, completedJob{ID: jobID, UpdatedAt: job.UpdatedAt})
		}
	}
	sort.Slice(completed, func(i, j int) bool {
		return completed[i].UpdatedAt.Before(completed[j].UpdatedAt)
	})

	overflow := len(s.jobs) - explanationJobMaxItems
	for i := 0; i < overflow && i < len(completed); i++ {
		delete(s.jobs, completed[i].ID)
	}
}

func newExplanationJobID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// ExplanationJobCreateHandler creates async explanation job.
func ExplanationJobCreateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, ErrMethodNotAllowed, "method not allowed")
		return
	}

	req, ok := decodeExplanationRequest(w, r)
	if !ok {
		return
	}

	jobID, err := enqueueExplanationJob(req.Result, r.Header.Get("X-Request-ID"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, ErrAssistantFailed, "assistant explanation failed")
		return
	}

	writeOK(w, explanationJobResponse{
		Status: explanationJobStatusQueued,
		JobID:  jobID,
	})
}

// ExplanationJobStatusHandler returns async explanation job status and result.
func ExplanationJobStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, ErrMethodNotAllowed, "method not allowed")
		return
	}

	jobID, ok := parseExplanationJobID(r.URL.Path)
	if !ok {
		writeErr(w, http.StatusBadRequest, ErrMissingJobID, "job_id is required")
		return
	}

	job, found := explanationJobsStore.get(jobID)
	if !found {
		writeErr(w, http.StatusNotFound, ErrJobNotFound, "explanation job not found")
		return
	}

	resp := explanationJobResponse{
		Status: job.Status,
		JobID:  jobID,
	}
	if job.Status == explanationJobStatusDone {
		resp.Explanation = strings.TrimSpace(job.Explanation)
	}
	if job.Status == explanationJobStatusError {
		resp.Error = &APIError{
			Code:    ErrAssistantFailed,
			Message: strings.TrimSpace(job.Error),
		}
	}

	writeOK(w, resp)
}

func parseExplanationJobID(path string) (string, bool) {
	if !strings.HasPrefix(path, explanationJobsPathPrefix) {
		return "", false
	}

	jobID := strings.TrimSpace(strings.TrimPrefix(path, explanationJobsPathPrefix))
	if jobID == "" {
		return "", false
	}
	if strings.Contains(jobID, "/") {
		return "", false
	}
	return jobID, true
}
