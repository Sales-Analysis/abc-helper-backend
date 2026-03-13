package abc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	abclib "github.com/Sales-Analysis/abc-helper-lib/abc"
)

const (
	defaultExplanationTimeout     = 120 * time.Second
	defaultExplanationWaitTimeout = 130 * time.Second
	defaultExplanationQueueSize   = 16
	minWaitTimeoutDelta           = 5 * time.Second
)

type analysisExplainerConfig struct {
	enabled         bool
	baseURL         string
	timeout         time.Duration
	waitTimeout     time.Duration
	mode            string
	topN            int
	workerQueueSize int
}

type explanationJob struct {
	result    []abclib.ProductResult
	requestID string
	done      chan explanationOutcome
}

type explanationOutcome struct {
	answer string
	err    error
}

type analysisExplanationWorker struct {
	cfg  analysisExplainerConfig
	jobs chan explanationJob
}

var (
	explanationWorkerOnce sync.Once
	explanationWorker     *analysisExplanationWorker
)

func explainAnalysis(ctx context.Context, result []abclib.ProductResult, requestID string) (string, error) {
	worker := getExplanationWorker()
	answer, err := worker.Explain(ctx, result, requestID)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(answer), nil
}

func getExplanationWorker() *analysisExplanationWorker {
	explanationWorkerOnce.Do(func() {
		explanationWorker = newAnalysisExplanationWorker(loadAnalysisExplainerConfig())
	})
	return explanationWorker
}

func newAnalysisExplanationWorker(cfg analysisExplainerConfig) *analysisExplanationWorker {
	if cfg.workerQueueSize <= 0 {
		cfg.workerQueueSize = defaultExplanationQueueSize
	}
	if cfg.waitTimeout <= 0 {
		cfg.waitTimeout = defaultExplanationWaitTimeout
	}

	worker := &analysisExplanationWorker{cfg: cfg}
	if cfg.enabled && cfg.baseURL != "" {
		worker.jobs = make(chan explanationJob, cfg.workerQueueSize)
		go worker.run()
	}

	return worker
}

func (w *analysisExplanationWorker) Explain(
	ctx context.Context,
	result []abclib.ProductResult,
	requestID string,
) (string, error) {
	if w == nil || !w.cfg.enabled || w.cfg.baseURL == "" || len(result) == 0 {
		return "", nil
	}

	waitCtx, cancel := context.WithTimeout(ctx, w.cfg.waitTimeout)
	defer cancel()

	job := explanationJob{
		result:    append([]abclib.ProductResult(nil), result...),
		requestID: requestID,
		done:      make(chan explanationOutcome, 1),
	}

	select {
	case w.jobs <- job:
	case <-waitCtx.Done():
		return "", waitCtx.Err()
	}

	select {
	case out := <-job.done:
		return out.answer, out.err
	case <-waitCtx.Done():
		return "", waitCtx.Err()
	}
}

func (w *analysisExplanationWorker) run() {
	for job := range w.jobs {
		jobCtx, cancel := context.WithTimeout(context.Background(), w.cfg.timeout)
		message := buildAnalysisExplanationPrompt(job.result, w.cfg.topN)
		answer, err := callAssistantForExplanation(jobCtx, http.DefaultClient, w.cfg, job.requestID, message)
		cancel()

		if err == nil {
			answer = strings.TrimSpace(answer)
		}

		job.done <- explanationOutcome{answer: answer, err: err}
	}
}

func loadAnalysisExplainerConfig() analysisExplainerConfig {
	timeout := envDuration("ASSISTANT_ANALYSIS_EXPLANATION_TIMEOUT", defaultExplanationTimeout)
	if timeout <= 0 {
		timeout = defaultExplanationTimeout
	}
	waitTimeout := envDuration(
		"ASSISTANT_ANALYSIS_EXPLANATION_WAIT_TIMEOUT",
		defaultExplanationWaitTimeout,
	)
	if waitTimeout <= 0 {
		waitTimeout = defaultExplanationWaitTimeout
	}
	// Wait timeout must be longer than worker request timeout.
	if waitTimeout <= timeout {
		waitTimeout = timeout + minWaitTimeoutDelta
	}

	cfg := analysisExplainerConfig{
		enabled:         envBool("ASSISTANT_ANALYSIS_EXPLANATION_ENABLED", true),
		baseURL:         strings.TrimRight(strings.TrimSpace(os.Getenv("ASSISTANT_BASE_URL")), "/"),
		timeout:         timeout,
		waitTimeout:     waitTimeout,
		mode:            strings.TrimSpace(strings.ToLower(os.Getenv("ASSISTANT_ANALYSIS_EXPLANATION_MODE"))),
		topN:            envInt("ASSISTANT_ANALYSIS_EXPLANATION_TOP_N", 5),
		workerQueueSize: envInt("ASSISTANT_ANALYSIS_EXPLANATION_WORKER_QUEUE_SIZE", defaultExplanationQueueSize),
	}

	if cfg.mode == "" {
		cfg.mode = "help"
	}
	if cfg.mode != "help" && cfg.mode != "diagnostic" && cfg.mode != "howto" {
		cfg.mode = "help"
	}
	if cfg.topN <= 0 {
		cfg.topN = 5
	}
	if cfg.topN > 20 {
		cfg.topN = 20
	}
	if cfg.waitTimeout <= cfg.timeout {
		cfg.waitTimeout = cfg.timeout + minWaitTimeoutDelta
	}
	if cfg.workerQueueSize <= 0 {
		cfg.workerQueueSize = defaultExplanationQueueSize
	}

	return cfg
}

func callAssistantForExplanation(
	ctx context.Context,
	client *http.Client,
	cfg analysisExplainerConfig,
	requestID, message string,
) (string, error) {
	if client == nil {
		return "", errors.New("assistant client is nil")
	}

	payload := map[string]any{
		"session_id": "abc-upload-analysis",
		"mode":       cfg.mode,
		"message":    message,
		"bypass_rag": true,
		"user_context": map[string]any{
			"role": "analyst",
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	reqCtx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(
		reqCtx,
		http.MethodPost,
		cfg.baseURL+"/v1/chat",
		bytes.NewReader(body),
	)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(requestID) != "" {
		req.Header.Set("X-Request-ID", requestID)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("assistant explanation failed with status %d", resp.StatusCode)
	}

	var parsed struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", err
	}
	if strings.TrimSpace(parsed.Answer) == "" {
		return "", errors.New("assistant explanation is empty")
	}
	return parsed.Answer, nil
}

func buildAnalysisExplanationPrompt(result []abclib.ProductResult, topN int) string {
	if len(result) == 0 {
		return "Сформируй краткое пояснение: данные анализа пустые."
	}
	if topN <= 0 {
		topN = 5
	}

	type groupSummary struct {
		count int
		total float64
	}
	groups := map[string]groupSummary{}
	var grandTotal float64
	for _, item := range result {
		group := strings.ToUpper(strings.TrimSpace(item.Group))
		s := groups[group]
		s.count++
		s.total += item.PriceTotal
		groups[group] = s
		grandTotal += item.PriceTotal
	}

	sorted := append([]abclib.ProductResult(nil), result...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].PriceTotal > sorted[j].PriceTotal
	})

	if topN > len(sorted) {
		topN = len(sorted)
	}

	var b strings.Builder
	b.WriteString("Дай короткое бизнес-пояснение по результату ABC-анализа на русском языке.\n")
	b.WriteString(
		"Требования: 4-7 предложений, без выдуманных данных, " +
			"с акцентом на группы A/B/C и что делать дальше.\n\n",
	)
	b.WriteString("Сводка по группам:\n")
	for _, key := range []string{"A", "B", "C"} {
		g := groups[key]
		b.WriteString(fmt.Sprintf("- %s: позиций=%d, выручка=%.2f\n", key, g.count, g.total))
	}
	b.WriteString(fmt.Sprintf("Итоговая выручка: %.2f\n\n", grandTotal))

	b.WriteString(fmt.Sprintf("Топ-%d позиций по выручке:\n", topN))
	for i := 0; i < topN; i++ {
		item := sorted[i]
		b.WriteString(
			fmt.Sprintf(
				"%d) SKU=%s, Name=%s, Group=%s, Quantity=%d, Revenue=%.2f, Share=%.2f%%\n",
				i+1,
				item.SKU,
				item.Name,
				item.Group,
				item.Quantity,
				item.PriceTotal,
				item.ShareTotal,
			),
		)
	}
	return b.String()
}

func envBool(key string, def bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return def
	}
	switch v {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return def
	}
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	log.Printf("invalid duration in %s=%q, using default %s", key, v, def.String())
	return def
}
