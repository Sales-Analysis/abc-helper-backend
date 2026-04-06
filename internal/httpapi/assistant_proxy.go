package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	defaultAssistantTimeout = 15 * time.Second
	maxAssistantInputBytes  = 64 * 1024
)

type assistantProxy struct {
	baseURL string
	client  *http.Client
	timeout time.Duration
}

type assistantChatRequest struct {
	SessionID   string               `json:"session_id,omitempty"`
	Message     string               `json:"message"`
	UserContext assistantUserContext `json:"user_context"`
	Mode        string               `json:"mode,omitempty"`
}

type assistantUserContext struct {
	UserID   string   `json:"user_id,omitempty"`
	TenantID string   `json:"tenant_id,omitempty"`
	Role     string   `json:"role,omitempty"`
	Groups   []string `json:"groups,omitempty"`
	ACLTags  []string `json:"acl_tags,omitempty"`
}

type assistantChatResponse struct {
	Answer     string            `json:"answer"`
	Sources    []assistantSource `json:"sources,omitempty"`
	Confidence float64           `json:"confidence"`
	Followups  []string          `json:"followups,omitempty"`
	Model      string            `json:"model,omitempty"`
}

type assistantSource struct {
	SourceID   string  `json:"source_id"`
	SourceType string  `json:"source_type"`
	Title      string  `json:"title"`
	Section    string  `json:"section"`
	ChunkIndex int     `json:"chunk_index"`
	Score      float64 `json:"score"`
}

func newAssistantProxy(baseURL string, timeout time.Duration) *assistantProxy {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if timeout <= 0 {
		timeout = defaultAssistantTimeout
	}

	return &assistantProxy{
		baseURL: baseURL,
		timeout: timeout,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

func (p *assistantProxy) HandleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if p.baseURL == "" {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"error": "assistant integration is not configured"})
		return
	}

	reqBody, err := decodeAssistantRequest(w, r)
	if err != nil {
		status := http.StatusBadRequest
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			status = http.StatusRequestEntityTooLarge
		}
		jsonResponse(w, status, map[string]any{"error": err.Error()})
		return
	}

	start := time.Now()
	status, body, err := p.forwardChat(r.Context(), reqBody, r.Header.Get("X-Request-ID"))
	if err != nil {
		if logger != nil {
			logger.Warn("assistant_proxy_failed",
				slog.String("request_id", r.Header.Get("X-Request-ID")),
				slog.Duration("duration", time.Since(start)),
				slog.String("err", err.Error()),
			)
		}
		jsonResponse(w, http.StatusBadGateway, map[string]any{"error": "assistant is unavailable"})
		return
	}

	if logger != nil {
		logAttrs := []any{
			slog.String("request_id", r.Header.Get("X-Request-ID")),
			slog.Int("status", status),
			slog.Duration("duration", time.Since(start)),
		}
		if status < 400 {
			var parsed assistantChatResponse
			if err := json.Unmarshal(body, &parsed); err == nil {
				logAttrs = append(logAttrs,
					slog.Int("sources", len(parsed.Sources)),
					slog.Float64("confidence", parsed.Confidence),
				)
			}
			logger.Info("assistant_proxy", logAttrs...)
		} else {
			logger.Warn("assistant_proxy", logAttrs...)
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (p *assistantProxy) forwardChat(
	parent context.Context,
	req assistantChatRequest,
	requestID string,
) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(parent, p.timeout)
	defer cancel()

	payload, err := json.Marshal(req)
	if err != nil {
		return 0, nil, err
	}

	outReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat", bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	outReq.Header.Set("Content-Type", "application/json")
	if requestID != "" {
		outReq.Header.Set("X-Request-ID", requestID)
	}

	resp, err := p.client.Do(outReq)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}

	return resp.StatusCode, body, nil
}

func decodeAssistantRequest(w http.ResponseWriter, r *http.Request) (assistantChatRequest, error) {
	reader := http.MaxBytesReader(w, r.Body, maxAssistantInputBytes)
	dec := json.NewDecoder(reader)
	dec.DisallowUnknownFields()

	var req assistantChatRequest
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return assistantChatRequest{}, err
		}
		if errors.Is(err, io.EOF) {
			return assistantChatRequest{}, errors.New("request body is empty")
		}
		return assistantChatRequest{}, errors.New("invalid JSON body")
	}

	if strings.TrimSpace(req.Message) == "" {
		return assistantChatRequest{}, errors.New("message is required")
	}

	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode != "" && mode != "help" && mode != "diagnostic" && mode != "howto" {
		return assistantChatRequest{}, errors.New("mode must be one of: help, diagnostic, howto")
	}
	req.Mode = mode

	var trailing any
	if err := dec.Decode(&trailing); err == nil {
		return assistantChatRequest{}, errors.New("invalid JSON body")
	} else if !errors.Is(err, io.EOF) {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return assistantChatRequest{}, err
		}
		return assistantChatRequest{}, errors.New("invalid JSON body")
	}

	return req, nil
}
