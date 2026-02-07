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
	"time"

	abclib "github.com/Sales-Analysis/abc-helper-lib/abc"
)

type analysisExplainerConfig struct {
	enabled bool
	baseURL string
	timeout time.Duration
	mode    string
	topN    int
}

func explainAnalysis(ctx context.Context, result []abclib.ProductResult, requestID string) (string, error) {
	cfg := loadAnalysisExplainerConfig()
	if !cfg.enabled || cfg.baseURL == "" || len(result) == 0 {
		return "", nil
	}

	message := buildAnalysisExplanationPrompt(result, cfg.topN)
	answer, err := callAssistantForExplanation(ctx, http.DefaultClient, cfg, requestID, message)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(answer), nil
}

func loadAnalysisExplainerConfig() analysisExplainerConfig {
	cfg := analysisExplainerConfig{
		enabled: envBool("ASSISTANT_ANALYSIS_EXPLANATION_ENABLED", true),
		baseURL: strings.TrimRight(strings.TrimSpace(os.Getenv("ASSISTANT_BASE_URL")), "/"),
		timeout: envDuration("ASSISTANT_TIMEOUT", 15*time.Second),
		mode:    strings.TrimSpace(strings.ToLower(os.Getenv("ASSISTANT_ANALYSIS_EXPLANATION_MODE"))),
		topN:    envInt("ASSISTANT_ANALYSIS_EXPLANATION_TOP_N", 5),
	}
	if cfg.timeout <= 0 {
		cfg.timeout = 15 * time.Second
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

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, cfg.baseURL+"/v1/chat", bytes.NewReader(body))
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
		b.WriteString(fmt.Sprintf("%d) SKU=%s, Name=%s, Group=%s, Quantity=%d, Revenue=%.2f, Share=%.2f%%\n",
			i+1, item.SKU, item.Name, item.Group, item.Quantity, item.PriceTotal, item.ShareTotal))
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
