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
	defaultExplanationMaxTokens   = 900
	minWaitTimeoutDelta           = 5 * time.Second
)

type analysisExplainerConfig struct {
	enabled         bool
	baseURL         string
	timeout         time.Duration
	waitTimeout     time.Duration
	mode            string
	topN            int
	maxTokens       int
	workerQueueSize int
}

type explanationJob struct {
	result    []abclib.ProductResult
	requestID string
	done      chan explanationOutcome
}

type explanationOutcome struct {
	answer    string
	truncated bool
	err       error
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
	answer, truncated, err := worker.Explain(ctx, result, requestID)
	answer = strings.TrimSpace(answer)
	if err == nil && answer != "" && !truncated {
		return answer, nil
	}

	if err != nil {
		log.Printf("assistant explanation fallback: request_id=%s err=%v", requestID, err)
	}
	if truncated {
		log.Printf("assistant explanation fallback: request_id=%s err=truncated answer", requestID)
	}

	fallback := strings.TrimSpace(buildAnalysisExplanationFallback(result, worker.cfg.topN))
	if fallback == "" {
		if err != nil {
			return "", err
		}
		return answer, nil
	}
	return fallback, nil
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
) (string, bool, error) {
	if w == nil || !w.cfg.enabled || w.cfg.baseURL == "" || len(result) == 0 {
		return "", false, nil
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
		return "", false, waitCtx.Err()
	}

	select {
	case out := <-job.done:
		return out.answer, out.truncated, out.err
	case <-waitCtx.Done():
		return "", false, waitCtx.Err()
	}
}

func (w *analysisExplanationWorker) run() {
	for job := range w.jobs {
		jobCtx, cancel := context.WithTimeout(context.Background(), w.cfg.timeout)
		message := buildAnalysisExplanationPrompt(job.result, w.cfg.topN)
		answer, truncated, err := callAssistantForExplanation(jobCtx, http.DefaultClient, w.cfg, job.requestID, message)
		cancel()

		if err == nil {
			answer = strings.TrimSpace(answer)
		}

		job.done <- explanationOutcome{answer: answer, truncated: truncated, err: err}
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
		maxTokens:       envInt("ASSISTANT_ANALYSIS_EXPLANATION_MAX_TOKENS", defaultExplanationMaxTokens),
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
	if cfg.maxTokens <= 0 {
		cfg.maxTokens = defaultExplanationMaxTokens
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
) (string, bool, error) {
	if client == nil {
		return "", false, errors.New("assistant client is nil")
	}

	payload := map[string]any{
		"session_id": "abc-upload-analysis",
		"mode":       cfg.mode,
		"message":    message,
		"max_tokens": cfg.maxTokens,
		"bypass_rag": true,
		"user_context": map[string]any{
			"role": "analyst",
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", false, err
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
		return "", false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(requestID) != "" {
		req.Header.Set("X-Request-ID", requestID)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", false, err
	}
	if resp.StatusCode >= 400 {
		return "", false, fmt.Errorf("assistant explanation failed with status %d", resp.StatusCode)
	}

	var parsed struct {
		Answer    string `json:"answer"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", false, err
	}
	if strings.TrimSpace(parsed.Answer) == "" {
		return "", parsed.Truncated, errors.New("assistant explanation is empty")
	}
	return parsed.Answer, parsed.Truncated, nil
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
	b.WriteString("Сформируй законченное пояснение по результату ABC-анализа на русском языке.\n")
	b.WriteString(
		"Требования: без выдуманных данных, без обрыва на полуслове, " +
			"с короткой выжимкой в начале и подробностями ниже.\n\n",
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
	b.WriteString("\nВерни Markdown строго в таком виде:\n")
	b.WriteString("### Выжимка\n")
	b.WriteString("1 короткий абзац на 2-3 предложения с главным выводом по отчету.\n\n")
	b.WriteString("### Подробно\n")
	b.WriteString("- По каждой реально присутствующей группе отдельный пункт в формате '- **Группа X**: ...'.\n")
	b.WriteString("- Для каждой группы укажи выручку, долю, и 1-2 ключевые позиции с цифрами.\n\n")
	b.WriteString("### Что делать дальше\n")
	b.WriteString("- 2-4 коротких практических пункта.\n")
	return b.String()
}

func buildAnalysisExplanationFallback(result []abclib.ProductResult, topN int) string {
	if len(result) == 0 {
		return ""
	}
	if topN <= 0 {
		topN = 5
	}

	type groupSummary struct {
		count int
		total float64
		top   []abclib.ProductResult
	}

	groups := map[string]*groupSummary{}
	var grandTotal float64
	sorted := append([]abclib.ProductResult(nil), result...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].PriceTotal > sorted[j].PriceTotal
	})

	for _, item := range sorted {
		groupKey := strings.ToUpper(strings.TrimSpace(item.Group))
		if groupKey == "" {
			groupKey = "OTHER"
		}
		summary := groups[groupKey]
		if summary == nil {
			summary = &groupSummary{}
			groups[groupKey] = summary
		}
		summary.count++
		summary.total += item.PriceTotal
		if len(summary.top) < 2 {
			summary.top = append(summary.top, item)
		}
		grandTotal += item.PriceTotal
	}

	type orderedGroup struct {
		key string
		*groupSummary
	}
	ordered := make([]orderedGroup, 0, len(groups))
	for _, key := range []string{"A", "B", "C"} {
		if summary := groups[key]; summary != nil {
			ordered = append(ordered, orderedGroup{key: key, groupSummary: summary})
			delete(groups, key)
		}
	}
	rest := make([]orderedGroup, 0, len(groups))
	for key, summary := range groups {
		rest = append(rest, orderedGroup{key: key, groupSummary: summary})
	}
	sort.SliceStable(rest, func(i, j int) bool {
		return rest[i].total > rest[j].total
	})
	ordered = append(ordered, rest...)

	topLabelCount := topN
	if topLabelCount > len(sorted) {
		topLabelCount = len(sorted)
	}
	var b strings.Builder
	b.WriteString("### Выжимка\n")
	if len(ordered) > 0 {
		leader := ordered[0]
		leaderShare := 0.0
		if grandTotal > 0 {
			leaderShare = leader.total / grandTotal * 100
		}
		b.WriteString(fmt.Sprintf(
			"Основная выручка сейчас сосредоточена в группе %s: %.2f руб. (**%s%%** от общей выручки). ",
			leader.key,
			leader.total,
			formatPercentForExplanation(leaderShare),
		))
		if topLabelCount > 0 {
			b.WriteString(fmt.Sprintf(
				"Наиболее заметный вклад в отчет дают верхние %d позиций по выручке, поэтому их стоит рассматривать в первую очередь.\n\n",
				topLabelCount,
			))
		} else {
			b.WriteString("\n\n")
		}
	}

	b.WriteString("### Подробно\n")
	for _, group := range ordered {
		share := 0.0
		if grandTotal > 0 {
			share = group.total / grandTotal * 100
		}
		b.WriteString(fmt.Sprintf(
			"- **Группа %s**: %d позиций, выручка **%.2f** руб., доля **%s%%**.",
			group.key,
			group.count,
			group.total,
			formatPercentForExplanation(share),
		))
		if len(group.top) > 0 {
			b.WriteString(" Ключевые позиции: ")
			for idx, item := range group.top {
				if idx > 0 {
					b.WriteString("; ")
				}
				label := strings.TrimSpace(item.Name)
				if label == "" {
					label = strings.TrimSpace(item.SKU)
				}
				if label == "" {
					label = "позиция без названия"
				}
				b.WriteString(fmt.Sprintf(
					"%s (SKU=%s, %.2f руб., %s%%)",
					label,
					strings.TrimSpace(item.SKU),
					item.PriceTotal,
					formatPercentForExplanation(item.ShareTotal),
				))
			}
			b.WriteString(".")
		}
		b.WriteString("\n")
	}

	b.WriteString("\n### Что делать дальше\n")
	if len(ordered) > 0 {
		b.WriteString(fmt.Sprintf("- Перепроверьте верхние позиции группы %s: они дают основной вклад в выручку.\n", ordered[0].key))
	}
	if len(ordered) > 1 {
		b.WriteString("- Сравните соседние группы по доле и подумайте, какие товары можно усилить для роста средней группы.\n")
	}
	b.WriteString("- Используйте ленту и плитки для проверки конкретных SKU, которые тянут группу вверх или вниз.")

	return strings.TrimSpace(b.String())
}

func formatPercentForExplanation(value float64) string {
	text := fmt.Sprintf("%.2f", value)
	text = strings.TrimRight(text, "0")
	text = strings.TrimRight(text, ".")
	if text == "" {
		return "0"
	}
	return text
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
