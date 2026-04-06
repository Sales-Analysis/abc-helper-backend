package abc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	abclib "github.com/Sales-Analysis/abc-helper-lib/abc"
)

const maxChartChatRequestBytes int64 = 512 * 1024

type chartChatRequest struct {
	JobID           string                   `json:"job_id"`
	Message         string                   `json:"message"`
	ExcludedItemIDs []string                 `json:"excluded_item_ids,omitempty"`
	History         []chartChatHistoryRecord `json:"history,omitempty"`
}

type chartChatHistoryRecord struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type chartChatAction struct {
	Type    string   `json:"type"`
	Label   string   `json:"label"`
	ItemIDs []string `json:"item_ids,omitempty"`
	Count   int      `json:"count,omitempty"`
}

type chartChatResponse struct {
	Status  string            `json:"status"`
	Answer  string            `json:"answer"`
	Actions []chartChatAction `json:"actions,omitempty"`
}

type chartChatItem struct {
	ID     string
	Result abclib.ProductResult
}

func ChartChatHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, ErrMethodNotAllowed, "method not allowed")
		return
	}

	req, ok := decodeChartChatRequest(w, r)
	if !ok {
		return
	}

	job, found := uploadJobsStore.get(req.JobID)
	if !found {
		writeErr(w, http.StatusNotFound, ErrJobNotFound, "upload job not found")
		return
	}
	if len(job.Result) == 0 {
		writeErr(w, http.StatusBadRequest, ErrMissingResult, "analysis result is unavailable")
		return
	}

	activeItems, excludedItems := splitChartChatItems(job.Result, req.ExcludedItemIDs)
	allItems := make([]chartChatItem, 0, len(activeItems)+len(excludedItems))
	allItems = append(allItems, activeItems...)
	allItems = append(allItems, excludedItems...)
	contextGroup := resolveChartChatGroup(req.Message, req.History)
	topicItems := findChartChatTopicItems(req.Message, allItems, contextGroup)
	if !isChartChatQuestionInScope(req.Message, req.History, topicItems) {
		writeOK(w, chartChatResponse{
			Status: "ok",
			Answer: "Я не могу выходить за рамки текущего отчёта. Спросите про товары, SKU, группы A/B/C, долю, сумму или сценарий исключений.",
		})
		return
	}

	actions := resolveChartChatActions(req.Message, activeItems, excludedItems, contextGroup)
	if len(actions) > 0 {
		writeOK(w, chartChatResponse{
			Status:  "ok",
			Answer:  buildChartChatActionAnswer(actions[0], activeItems, excludedItems),
			Actions: actions,
		})
		return
	}

	answer, err := answerChartQuestion(r.Context(), req, activeItems, excludedItems, r.Header.Get("X-Request-ID"))
	if err != nil {
		answer = buildChartChatFallbackAnswer(req.Message, activeItems, excludedItems, topicItems, contextGroup)
	}

	writeOK(w, chartChatResponse{
		Status: "ok",
		Answer: strings.TrimSpace(answer),
	})
}

func decodeChartChatRequest(w http.ResponseWriter, r *http.Request) (chartChatRequest, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxChartChatRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var req chartChatRequest
	if err := decoder.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, ErrInvalidJSON, "invalid JSON body")
		return chartChatRequest{}, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeErr(w, http.StatusBadRequest, ErrInvalidJSON, "invalid JSON body")
		return chartChatRequest{}, false
	}

	req.JobID = strings.TrimSpace(req.JobID)
	req.Message = strings.TrimSpace(req.Message)
	if req.JobID == "" {
		writeErr(w, http.StatusBadRequest, ErrMissingJobID, "job_id is required")
		return chartChatRequest{}, false
	}
	if req.Message == "" {
		writeErr(w, http.StatusBadRequest, ErrInvalidForm, "message is required")
		return chartChatRequest{}, false
	}

	req.ExcludedItemIDs = uniqueStrings(req.ExcludedItemIDs)
	req.History = normalizeChartChatHistory(req.History)
	return req, true
}

func normalizeChartChatHistory(history []chartChatHistoryRecord) []chartChatHistoryRecord {
	if len(history) == 0 {
		return nil
	}

	normalized := make([]chartChatHistoryRecord, 0, len(history))
	for _, item := range history {
		role := strings.ToLower(strings.TrimSpace(item.Role))
		text := strings.TrimSpace(item.Text)
		if text == "" {
			continue
		}
		if role != "user" && role != "assistant" {
			continue
		}
		normalized = append(normalized, chartChatHistoryRecord{
			Role: role,
			Text: text,
		})
	}

	if len(normalized) > 6 {
		normalized = normalized[len(normalized)-6:]
	}
	return normalized
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func splitChartChatItems(
	base []abclib.ProductResult,
	excludedIDs []string,
) ([]chartChatItem, []chartChatItem) {
	excludedSet := make(map[string]struct{}, len(excludedIDs))
	for _, itemID := range excludedIDs {
		excludedSet[itemID] = struct{}{}
	}

	activeBase := make([]abclib.ProductResult, 0, len(base))
	excluded := make([]chartChatItem, 0, len(excludedIDs))
	for _, item := range base {
		itemID := chartChatItemID(item)
		if _, ok := excludedSet[itemID]; ok {
			excluded = append(excluded, chartChatItem{
				ID:     itemID,
				Result: item,
			})
			continue
		}
		activeBase = append(activeBase, item)
	}

	recalculated := recalculateChartChatResults(activeBase)
	active := make([]chartChatItem, 0, len(recalculated))
	for _, item := range recalculated {
		active = append(active, chartChatItem{
			ID:     chartChatItemID(item),
			Result: item,
		})
	}
	return active, excluded
}

func recalculateChartChatResults(base []abclib.ProductResult) []abclib.ProductResult {
	if len(base) == 0 {
		return nil
	}

	results := append([]abclib.ProductResult(nil), base...)
	grandTotal := 0.0
	for idx := range results {
		grandTotal += results[idx].PriceTotal
	}
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].PriceTotal > results[j].PriceTotal
	})

	accumulated := 0.0
	for idx := range results {
		share := 0.0
		if grandTotal > 0 {
			share = (results[idx].PriceTotal / grandTotal) * 100
		}
		accumulated += share
		results[idx].ShareTotal = share
		results[idx].ShareAccumulated = accumulated
		switch {
		case accumulated <= 80:
			results[idx].Group = "A"
		case accumulated <= 95:
			results[idx].Group = "B"
		default:
			results[idx].Group = "C"
		}
	}
	return results
}

func chartChatItemID(item abclib.ProductResult) string {
	sku := strings.ToLower(strings.TrimSpace(item.SKU))
	name := strings.ToLower(strings.TrimSpace(item.Name))
	if sku == "" && name == "" {
		return "unknown"
	}
	return sku + "|" + name
}

func resolveChartChatActions(
	message string,
	activeItems []chartChatItem,
	excludedItems []chartChatItem,
	contextGroup string,
) []chartChatAction {
	intent := detectChartChatIntent(message)
	switch intent {
	case "exclude":
		matches := matchChartChatItems(message, activeItems, contextGroup)
		if len(matches) == 0 {
			return nil
		}
		return []chartChatAction{{
			Type:    "exclude_items",
			Label:   formatChartChatActionLabel("Исключить", len(matches)),
			ItemIDs: collectChartChatItemIDs(matches),
			Count:   len(matches),
		}}
	case "restore_all":
		if len(excludedItems) == 0 {
			return nil
		}
		return []chartChatAction{{
			Type:  "restore_all",
			Label: fmt.Sprintf("Вернуть все (%d)", len(excludedItems)),
			Count: len(excludedItems),
		}}
	case "restore":
		matches := matchChartChatItems(message, excludedItems, contextGroup)
		if len(matches) == 0 {
			if len(excludedItems) > 0 && isGenericRestoreRequest(message) {
				return []chartChatAction{{
					Type:  "restore_all",
					Label: fmt.Sprintf("Вернуть все (%d)", len(excludedItems)),
					Count: len(excludedItems),
				}}
			}
			return nil
		}
		return []chartChatAction{{
			Type:    "restore_items",
			Label:   formatChartChatActionLabel("Вернуть", len(matches)),
			ItemIDs: collectChartChatItemIDs(matches),
			Count:   len(matches),
		}}
	default:
		return nil
	}
}

func isChartChatQuestionInScope(
	message string,
	history []chartChatHistoryRecord,
	topicItems []chartChatItem,
) bool {
	if detectChartChatIntent(message) != "" {
		return true
	}
	if len(topicItems) > 0 {
		return true
	}

	normalized := normalizeChartChatText(message)
	if normalized == "" {
		return false
	}

	for _, keyword := range []string{
		"отчет", "отчёт", "анализ", "график", "группа", "группе", "доля", "выручк", "sku",
		"товар", "позици", "сумм", "колич", "накоплен", "исключ", "сценар", "процент",
		"abc", "значени",
		"минималь", "максималь", "низк", "высок",
		"лента", "плитк", "бар",
	} {
		if strings.Contains(normalized, keyword) {
			return true
		}
	}
	return len(history) > 0 && hasChartChatContinuationMarker(normalized)
}

func hasChartChatContinuationMarker(message string) bool {
	for _, marker := range []string{
		"этой группе", "этого товара", "этой позиции", "этот товар", "этой категории",
		"самый низкий", "самый высокий", "минималь", "максималь", "какой процент",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func findChartChatTopicItems(message string, items []chartChatItem, contextGroup string) []chartChatItem {
	if len(items) == 0 {
		return nil
	}

	groupFilter := contextGroup
	pool := filterChartChatItemsByGroup(items, groupFilter)
	if len(pool) == 0 {
		return nil
	}

	normalizedMessage := normalizeChartChatText(message)
	if normalizedMessage == "" {
		return nil
	}

	skuMatches := make([]chartChatItem, 0)
	for _, item := range pool {
		sku := normalizeChartChatText(item.Result.SKU)
		if sku != "" && strings.Contains(normalizedMessage, sku) {
			skuMatches = append(skuMatches, item)
		}
	}
	if len(skuMatches) > 0 {
		return limitChartChatItemsByValue(skuMatches, 5)
	}

	query := extractChartChatSearchQuery(message)
	tokens := buildChartChatSearchTokens(query)
	if len(tokens) == 0 {
		return nil
	}

	type scoredItem struct {
		item  chartChatItem
		score int
	}

	scored := make([]scoredItem, 0, len(pool))
	for _, item := range pool {
		haystack := normalizeChartChatText(item.Result.SKU + " " + item.Result.Name)
		score := 0
		for _, token := range tokens {
			if strings.Contains(haystack, token) {
				score++
			}
		}
		minScore := 1
		if len(tokens) >= 2 {
			minScore = 2
		}
		if score >= minScore {
			scored = append(scored, scoredItem{item: item, score: score})
		}
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].item.Result.PriceTotal > scored[j].item.Result.PriceTotal
	})

	out := make([]chartChatItem, 0, len(scored))
	for _, item := range scored {
		out = append(out, item.item)
	}
	return limitChartChatItemsByValue(out, 5)
}

func extractChartChatSearchQuery(message string) string {
	normalized := normalizeChartChatText(message)
	replacer := strings.NewReplacer(
		"а что скажешь про", " ",
		"что скажешь про", " ",
		"что ты думаешь про", " ",
		"расскажи про", " ",
		"расскажи о", " ",
		"что известно про", " ",
		"что видно по", " ",
		"хотелось бы узнать про", " ",
		"хочу узнать про", " ",
		"про товар", " ",
		"про товары", " ",
		"по товару", " ",
		"по товарам", " ",
		"из группы", " ",
		"группы", " ",
		"группа", " ",
		"группу", " ",
		"отчет", " ",
		"отчёт", " ",
		"анализ", " ",
		"график", " ",
	)
	return strings.TrimSpace(replacer.Replace(normalized))
}

func buildChartChatSearchTokens(query string) []string {
	if query == "" {
		return nil
	}

	stopwords := map[string]struct{}{
		"про": {}, "по": {}, "а": {}, "и": {}, "или": {}, "в": {}, "во": {}, "на": {}, "из": {},
		"что": {}, "скажешь": {}, "думаешь": {}, "узнать": {}, "хочу": {}, "хотелось": {}, "бы": {},
		"сейчас": {}, "этот": {}, "эта": {}, "это": {}, "мне": {}, "о": {}, "об": {},
	}

	raw := strings.Fields(query)
	out := make([]string, 0, len(raw))
	for _, token := range raw {
		token = strings.TrimSpace(token)
		if len(token) <= 1 {
			continue
		}
		if _, skip := stopwords[token]; skip {
			continue
		}
		out = append(out, token)
	}
	return uniqueStrings(out)
}

func limitChartChatItemsByValue(items []chartChatItem, limit int) []chartChatItem {
	if len(items) <= limit {
		return items
	}
	return items[:limit]
}

func detectChartChatIntent(message string) string {
	normalized := normalizeChartChatText(message)
	switch {
	case normalized == "":
		return ""
	case strings.Contains(normalized, "верни все"),
		strings.Contains(normalized, "верни всё"),
		strings.Contains(normalized, "восстанови все"),
		strings.Contains(normalized, "восстанови всё"),
		strings.Contains(normalized, "сбрось исключ"),
		strings.Contains(normalized, "вернуть все"):
		return "restore_all"
	case strings.Contains(normalized, "верни"),
		strings.Contains(normalized, "вернуть"),
		strings.Contains(normalized, "восстанов"),
		strings.Contains(normalized, "добавь обратно"):
		return "restore"
	case strings.Contains(normalized, "убери"),
		strings.Contains(normalized, "убрать"),
		strings.Contains(normalized, "исключ"),
		strings.Contains(normalized, "удали"),
		strings.Contains(normalized, "удалить"),
		strings.Contains(normalized, "скрой"),
		strings.Contains(normalized, "скрыть"):
		return "exclude"
	default:
		return ""
	}
}

func isGenericRestoreRequest(message string) bool {
	normalized := normalizeChartChatText(message)
	return strings.Contains(normalized, "обратно") ||
		strings.Contains(normalized, "назад") ||
		strings.Contains(normalized, "верни") ||
		strings.Contains(normalized, "восстанов")
}

func matchChartChatItems(message string, items []chartChatItem, contextGroup string) []chartChatItem {
	if len(items) == 0 {
		return nil
	}

	groupFilter := contextGroup
	pool := filterChartChatItemsByGroup(items, groupFilter)
	if len(pool) == 0 {
		return nil
	}

	normalizedMessage := normalizeChartChatText(message)
	if normalizedMessage == "" {
		return nil
	}

	skuMatches := make([]chartChatItem, 0)
	for _, item := range pool {
		sku := normalizeChartChatText(item.Result.SKU)
		if sku != "" && strings.Contains(normalizedMessage, sku) {
			skuMatches = append(skuMatches, item)
		}
	}
	if len(skuMatches) > 0 {
		return skuMatches
	}

	phrases := extractChartChatTargetPhrases(message)
	if len(phrases) > 0 {
		matched := make([]chartChatItem, 0)
		for _, item := range pool {
			haystack := normalizeChartChatText(item.Result.SKU + " " + item.Result.Name)
			for _, phrase := range phrases {
				if phrase != "" && strings.Contains(haystack, phrase) {
					matched = append(matched, item)
					break
				}
			}
		}
		if len(matched) > 0 {
			return matched
		}
	}

	if groupFilter != "" && len(phrases) == 0 {
		return pool
	}
	return nil
}

func detectChartChatGroup(message string) string {
	normalized := normalizeChartChatText(message)
	switch {
	case strings.Contains(normalized, "группа a"), strings.Contains(normalized, "группы a"), strings.Contains(normalized, "группу a"):
		return "A"
	case strings.Contains(normalized, "группа b"), strings.Contains(normalized, "группы b"), strings.Contains(normalized, "группу b"):
		return "B"
	case strings.Contains(normalized, "группа c"), strings.Contains(normalized, "группы c"), strings.Contains(normalized, "группу c"):
		return "C"
	default:
		return ""
	}
}

func resolveChartChatGroup(message string, history []chartChatHistoryRecord) string {
	if group := detectChartChatGroup(message); group != "" {
		return group
	}

	normalized := normalizeChartChatText(message)
	if !hasChartChatContinuationMarker(normalized) {
		return ""
	}

	for idx := len(history) - 1; idx >= 0; idx-- {
		if group := detectChartChatGroup(history[idx].Text); group != "" {
			return group
		}
	}
	return ""
}

func filterChartChatItemsByGroup(items []chartChatItem, group string) []chartChatItem {
	if group == "" {
		return items
	}
	out := make([]chartChatItem, 0, len(items))
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item.Result.Group), group) {
			out = append(out, item)
		}
	}
	return out
}

func extractChartChatTargetPhrases(message string) []string {
	normalized := strings.TrimSpace(message)
	if normalized == "" {
		return nil
	}

	replacer := strings.NewReplacer(
		"«", "\"",
		"»", "\"",
		"“", "\"",
		"”", "\"",
		"из анализа", " ",
		"из группы", " ",
		"из графика", " ",
		"обратно", " ",
	)
	normalized = replacer.Replace(normalized)
	lower := normalizeChartChatText(normalized)

	phrases := make([]string, 0)
	quoted := extractQuotedSegments(normalized)
	for _, item := range quoted {
		if candidate := normalizeChartChatText(item); candidate != "" {
			phrases = append(phrases, candidate)
		}
	}
	if len(phrases) > 0 {
		return uniqueStrings(phrases)
	}

	for _, marker := range []string{"убери", "убрать", "исключи", "исключить", "удали", "удалить", "скрой", "скрыть", "верни", "вернуть", "восстанови", "восстановить"} {
		idx := strings.Index(lower, marker)
		if idx < 0 {
			continue
		}
		tail := strings.TrimSpace(lower[idx+len(marker):])
		tail = strings.TrimPrefix(tail, "из")
		tail = strings.TrimSpace(strings.TrimPrefix(tail, "группы"))
		tail = strings.TrimSpace(strings.TrimPrefix(tail, "группа"))
		tail = strings.TrimSpace(strings.TrimPrefix(tail, "группу"))
		if strings.HasPrefix(tail, "a") || strings.HasPrefix(tail, "b") || strings.HasPrefix(tail, "c") {
			tail = strings.TrimSpace(tail[1:])
		}
		tail = strings.TrimSpace(strings.TrimPrefix(tail, "товары"))
		tail = strings.TrimSpace(strings.TrimPrefix(tail, "товар"))
		tail = strings.TrimSpace(strings.TrimPrefix(tail, "позиции"))
		tail = strings.TrimSpace(strings.TrimPrefix(tail, "позицию"))
		tail = strings.TrimSpace(strings.TrimPrefix(tail, "sku"))
		if tail == "" {
			continue
		}
		for _, part := range splitChartChatPhraseTail(tail) {
			if candidate := normalizeChartChatText(part); candidate != "" {
				phrases = append(phrases, candidate)
			}
		}
	}

	return uniqueStrings(phrases)
}

func extractQuotedSegments(message string) []string {
	parts := strings.Split(message, "\"")
	if len(parts) < 3 {
		return nil
	}
	out := make([]string, 0, len(parts)/2)
	for idx := 1; idx < len(parts); idx += 2 {
		segment := strings.TrimSpace(parts[idx])
		if segment != "" {
			out = append(out, segment)
		}
	}
	return out
}

func splitChartChatPhraseTail(tail string) []string {
	for _, separator := range []string{",", ";", " и "} {
		if strings.Contains(tail, separator) {
			parts := strings.Split(tail, separator)
			out := make([]string, 0, len(parts))
			for _, item := range parts {
				trimmed := strings.TrimSpace(item)
				if trimmed != "" {
					out = append(out, trimmed)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	return []string{tail}
}

func normalizeChartChatText(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.NewReplacer(
		"ё", "е",
		"\n", " ",
		"\t", " ",
		"«", " ",
		"»", " ",
		"\"", " ",
		"'", " ",
	).Replace(normalized)
	return strings.Join(strings.Fields(normalized), " ")
}

func collectChartChatItemIDs(items []chartChatItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}

func formatChartChatActionLabel(prefix string, count int) string {
	if count <= 0 {
		return prefix
	}
	return fmt.Sprintf("%s %d %s", prefix, count, pluralizeChartChatItems(count))
}

func pluralizeChartChatItems(count int) string {
	n10 := count % 10
	n100 := count % 100
	switch {
	case n10 == 1 && n100 != 11:
		return "товар"
	case n10 >= 2 && n10 <= 4 && (n100 < 12 || n100 > 14):
		return "товара"
	default:
		return "товаров"
	}
}

func buildChartChatActionAnswer(
	action chartChatAction,
	activeItems []chartChatItem,
	excludedItems []chartChatItem,
) string {
	switch action.Type {
	case "exclude_items":
		return fmt.Sprintf(
			"Нашёл %d %s в текущем анализе. Могу исключить их из сценария и пересчитать группы A/B/C.",
			action.Count,
			pluralizeChartChatItems(action.Count),
		)
	case "restore_items":
		return fmt.Sprintf(
			"Нашёл %d ранее исключённых %s. Могу вернуть их обратно в анализ и пересчитать график.",
			action.Count,
			pluralizeChartChatItems(action.Count),
		)
	case "restore_all":
		count := len(excludedItems)
		return fmt.Sprintf(
			"Сейчас исключено %d %s. Могу вернуть их все обратно в анализ.",
			count,
			pluralizeChartChatItems(count),
		)
	default:
		return "Нашёл подходящее действие по графику."
	}
}

func answerChartQuestion(
	ctx context.Context,
	req chartChatRequest,
	activeItems []chartChatItem,
	excludedItems []chartChatItem,
	requestID string,
) (string, error) {
	cfg := loadAnalysisExplainerConfig()
	if !cfg.enabled || cfg.baseURL == "" {
		return "", errors.New("assistant chart chat is not configured")
	}

	prompt := buildChartChatPrompt(req.Message, req.History, activeItems, excludedItems)
	return callAssistantForChartChat(ctx, cfg, requestID, prompt)
}

func buildChartChatPrompt(
	message string,
	history []chartChatHistoryRecord,
	activeItems []chartChatItem,
	excludedItems []chartChatItem,
) string {
	active := append([]chartChatItem(nil), activeItems...)
	sort.SliceStable(active, func(i, j int) bool {
		return active[i].Result.PriceTotal > active[j].Result.PriceTotal
	})

	groupCount := map[string]int{}
	groupRevenue := map[string]float64{}
	totalRevenue := 0.0
	for _, item := range active {
		group := strings.ToUpper(strings.TrimSpace(item.Result.Group))
		groupCount[group]++
		groupRevenue[group] += item.Result.PriceTotal
		totalRevenue += item.Result.PriceTotal
	}

	var b strings.Builder
	b.WriteString("Ты помощник по ABC-графику. Отвечай только по данным ниже и не используй внешние знания.\n")
	b.WriteString("Если данных недостаточно, прямо скажи об этом. Пиши кратко, по-русски, без таблиц.\n\n")
	b.WriteString(fmt.Sprintf("Активных товаров: %d\n", len(active)))
	b.WriteString(fmt.Sprintf("Исключённых товаров: %d\n", len(excludedItems)))
	b.WriteString(fmt.Sprintf("Текущая активная выручка: %.2f\n", totalRevenue))
	b.WriteString("Сводка по группам:\n")
	for _, key := range []string{"A", "B", "C"} {
		b.WriteString(fmt.Sprintf(
			"- %s: товаров=%d, выручка=%.2f\n",
			key,
			groupCount[key],
			groupRevenue[key],
		))
	}

	topN := minChartChatInt(len(active), 12)
	if topN > 0 {
		b.WriteString("\nТоп активных товаров:\n")
		for idx := 0; idx < topN; idx++ {
			item := active[idx].Result
			b.WriteString(fmt.Sprintf(
				"%d) SKU=%s; Name=%s; Group=%s; Revenue=%.2f; Share=%.4f%%\n",
				idx+1,
				item.SKU,
				item.Name,
				item.Group,
				item.PriceTotal,
				item.ShareTotal,
			))
		}
	}

	if len(excludedItems) > 0 {
		excluded := append([]chartChatItem(nil), excludedItems...)
		sort.SliceStable(excluded, func(i, j int) bool {
			return excluded[i].Result.PriceTotal > excluded[j].Result.PriceTotal
		})
		limit := minChartChatInt(len(excluded), 10)
		b.WriteString("\nКрупнейшие исключённые товары:\n")
		for idx := 0; idx < limit; idx++ {
			item := excluded[idx].Result
			b.WriteString(fmt.Sprintf(
				"%d) SKU=%s; Name=%s; Group=%s; Revenue=%.2f; Share=%.4f%%\n",
				idx+1,
				item.SKU,
				item.Name,
				item.Group,
				item.PriceTotal,
				item.ShareTotal,
			))
		}
	}

	if len(history) > 0 {
		b.WriteString("\nНедавний контекст диалога:\n")
		for _, item := range history {
			b.WriteString(fmt.Sprintf("- %s: %s\n", item.Role, item.Text))
		}
	}

	b.WriteString("\nВопрос пользователя:\n")
	b.WriteString(strings.TrimSpace(message))
	return b.String()
}

func minChartChatInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func callAssistantForChartChat(
	parent context.Context,
	cfg analysisExplainerConfig,
	requestID, message string,
) (string, error) {
	payload := map[string]any{
		"session_id": "abc-chart-chat",
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

	reqCtx, cancel := context.WithTimeout(parent, minChartChatDuration(cfg.timeout, 45*time.Second))
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
	if requestID != "" {
		req.Header.Set("X-Request-ID", requestID)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("assistant chart chat failed with status %d", resp.StatusCode)
	}

	var parsed struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", err
	}

	answer := strings.TrimSpace(parsed.Answer)
	if answer == "" {
		return "", errors.New("assistant chart chat is empty")
	}
	return answer, nil
}

func buildChartChatFallbackAnswer(
	message string,
	activeItems []chartChatItem,
	excludedItems []chartChatItem,
	topicItems []chartChatItem,
	contextGroup string,
) string {
	if len(activeItems) == 0 {
		if len(excludedItems) > 0 {
			return "Сейчас все позиции исключены из сценария. Верните товары обратно, чтобы снова анализировать группы A/B/C."
		}
		return "В активном сценарии пока нет данных для ответа."
	}

	normalized := normalizeChartChatText(message)
	groupRevenue := map[string]float64{}
	groupItems := map[string][]chartChatItem{}
	totalRevenue := 0.0
	for _, item := range activeItems {
		group := strings.ToUpper(strings.TrimSpace(item.Result.Group))
		groupRevenue[group] += item.Result.PriceTotal
		groupItems[group] = append(groupItems[group], item)
		totalRevenue += item.Result.PriceTotal
	}
	for _, group := range []string{"A", "B", "C"} {
		sort.SliceStable(groupItems[group], func(i, j int) bool {
			return groupItems[group][i].Result.PriceTotal > groupItems[group][j].Result.PriceTotal
		})
	}

	groupShare := func(group string) float64 {
		if totalRevenue <= 0 {
			return 0
		}
		return (groupRevenue[group] / totalRevenue) * 100
	}
	describeTop := func(items []chartChatItem, limit int) string {
		if len(items) == 0 {
			return "крупных товаров не найдено"
		}
		if len(items) > limit {
			items = items[:limit]
		}
		parts := make([]string, 0, len(items))
		for _, item := range items {
			name := strings.TrimSpace(item.Result.Name)
			if name == "" {
				name = strings.TrimSpace(item.Result.SKU)
			}
			parts = append(parts, fmt.Sprintf("%s (%0.4f%%)", name, item.Result.ShareTotal))
		}
		return strings.Join(parts, ", ")
	}
	describeItem := func(item chartChatItem, excluded bool) string {
		name := strings.TrimSpace(item.Result.Name)
		if name == "" {
			name = strings.TrimSpace(item.Result.SKU)
		}
		state := "активен"
		if excluded {
			state = "сейчас исключён из сценария"
		}
		return fmt.Sprintf(
			"По текущему отчёту %s %s. Группа %s, доля %0.4f%%, сумма %.2f, количество %d.",
			name,
			state,
			item.Result.Group,
			item.Result.ShareTotal,
			item.Result.PriceTotal,
			item.Result.Quantity,
		)
	}

	if len(topicItems) == 1 {
		item := topicItems[0]
		excluded := false
		for _, excludedItem := range excludedItems {
			if excludedItem.ID == item.ID {
				excluded = true
				break
			}
		}
		return describeItem(item, excluded)
	}
	if len(topicItems) > 1 {
		return fmt.Sprintf(
			"По запросу нашёл %d подходящих позиций: %s. Уточните SKU или полное наименование, если нужен один конкретный товар.",
			len(topicItems),
			describeTop(topicItems, 4),
		)
	}

	if group := contextGroup; group != "" {
		items := groupItems[group]
		if len(items) == 0 {
			return fmt.Sprintf("В текущем сценарии в группе %s активных товаров нет.", group)
		}
		if strings.Contains(normalized, "самый низкий") ||
			strings.Contains(normalized, "минималь") ||
			(strings.Contains(normalized, "низк") && strings.Contains(normalized, "процент")) {
			item := items[len(items)-1]
			name := strings.TrimSpace(item.Result.Name)
			if name == "" {
				name = strings.TrimSpace(item.Result.SKU)
			}
			return fmt.Sprintf(
				"В группе %s самый низкий процент сейчас у позиции %s: %0.4f%%, сумма %.2f, SKU %s.",
				group,
				name,
				item.Result.ShareTotal,
				item.Result.PriceTotal,
				item.Result.SKU,
			)
		}
		if strings.Contains(normalized, "самый высокий") ||
			strings.Contains(normalized, "максималь") ||
			(strings.Contains(normalized, "высок") && strings.Contains(normalized, "процент")) {
			item := items[0]
			name := strings.TrimSpace(item.Result.Name)
			if name == "" {
				name = strings.TrimSpace(item.Result.SKU)
			}
			return fmt.Sprintf(
				"В группе %s самый высокий процент сейчас у позиции %s: %0.4f%%, сумма %.2f, SKU %s.",
				group,
				name,
				item.Result.ShareTotal,
				item.Result.PriceTotal,
				item.Result.SKU,
			)
		}
		if strings.Contains(normalized, "почему") {
			return fmt.Sprintf(
				"Группа %s сейчас занимает %0.2f%% активной выручки. Сильнее всего на неё влияют: %s.",
				group,
				groupShare(group),
				describeTop(items, 3),
			)
		}
		return fmt.Sprintf(
			"В группе %s сейчас %d активных товаров и %0.2f%% активной выручки. Самые заметные позиции: %s.",
			group,
			len(items),
			groupShare(group),
			describeTop(items, 4),
		)
	}

	if strings.Contains(normalized, "какие товары") ||
		strings.Contains(normalized, "сильнее всего влияют") ||
		strings.Contains(normalized, "влияют") ||
		strings.Contains(normalized, "тянут") ||
		strings.Contains(normalized, "крупн") {
		return fmt.Sprintf(
			"Сильнее всего на текущий анализ влияют верхние позиции по выручке: %s. Группы сейчас распределены так: A %0.2f%%, B %0.2f%%, C %0.2f%%.",
			describeTop(activeItems, 5),
			groupShare("A"),
			groupShare("B"),
			groupShare("C"),
		)
	}

	if len(excludedItems) > 0 && (strings.Contains(normalized, "сценар") || strings.Contains(normalized, "исключ")) {
		return fmt.Sprintf(
			"Сейчас в сценарии исключено %d товаров. По активной части анализа группы выглядят так: A %0.2f%%, B %0.2f%%, C %0.2f%%.",
			len(excludedItems),
			groupShare("A"),
			groupShare("B"),
			groupShare("C"),
		)
	}

	return fmt.Sprintf(
		"По текущему графику активных товаров %d. Группы распределены так: A %0.2f%%, B %0.2f%%, C %0.2f%%. Крупнейшие позиции сейчас: %s.",
		len(activeItems),
		groupShare("A"),
		groupShare("B"),
		groupShare("C"),
		describeTop(activeItems, 4),
	)
}

func minChartChatDuration(left, right time.Duration) time.Duration {
	if left <= 0 {
		return right
	}
	if left < right {
		return left
	}
	return right
}
