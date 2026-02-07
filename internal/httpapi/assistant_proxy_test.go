package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAssistantProxyNotConfigured(t *testing.T) {
	proxy := newAssistantProxy("", 5*time.Second)

	req := httptest.NewRequest(http.MethodPost, "/assistant/chat", bytes.NewBufferString(`{"message":"hi"}`))
	rec := httptest.NewRecorder()

	proxy.HandleChat(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestAssistantProxyForwardsRequestAndResponse(t *testing.T) {
	var gotRequestID string
	var gotBody assistantChatRequest
	proxy := newAssistantProxy("http://assistant.local", 5*time.Second)
	proxy.client = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if got := r.URL.String(); got != "http://assistant.local/v1/chat" {
				t.Fatalf("unexpected upstream URL: %s", got)
			}
			gotRequestID = r.Header.Get("X-Request-ID")
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Fatalf("decode upstream body: %v", err)
			}
			respBody, _ := json.Marshal(assistantChatResponse{
				Answer: "ok",
				Sources: []assistantSource{
					{SourceID: "docs/x.md", Score: 0.9},
				},
				Confidence: 0.9,
			})
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader(respBody)),
			}, nil
		}),
	}

	req := httptest.NewRequest(http.MethodPost, "/assistant/chat", bytes.NewBufferString(`{
		"session_id":"s-1",
		"message":"How to reindex?",
		"mode":"howto",
		"user_context":{"tenant_id":"t1","role":"admin","groups":["ops"]}
	}`))
	req.Header.Set("X-Request-ID", "rid-123")
	rec := httptest.NewRecorder()

	proxy.HandleChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotRequestID != "rid-123" {
		t.Fatalf("expected request-id to be forwarded, got %q", gotRequestID)
	}
	if gotBody.Message != "How to reindex?" || gotBody.Mode != "howto" {
		t.Fatalf("unexpected upstream body: %#v", gotBody)
	}
	if gotBody.UserContext.Role != "admin" || gotBody.UserContext.TenantID != "t1" {
		t.Fatalf("unexpected user context: %#v", gotBody.UserContext)
	}

	var out assistantChatResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Answer != "ok" || len(out.Sources) != 1 {
		t.Fatalf("unexpected proxied response: %#v", out)
	}
}

func TestAssistantProxyInvalidBody(t *testing.T) {
	proxy := newAssistantProxy("http://example.com", 5*time.Second)

	req := httptest.NewRequest(http.MethodPost, "/assistant/chat", bytes.NewBufferString(`{"mode":"help"}`))
	rec := httptest.NewRecorder()
	proxy.HandleChat(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestForwardChatPassesStatusAndBody(t *testing.T) {
	proxy := newAssistantProxy("http://assistant.local", 5*time.Second)
	proxy.client = &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":"bad request"}`)),
			}, nil
		}),
	}

	status, body, err := proxy.forwardChat(context.Background(), assistantChatRequest{
		Message: "test",
	}, "rid-1")
	if err != nil {
		t.Fatalf("forwardChat err: %v", err)
	}
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", status)
	}
	if string(body) != `{"error":"bad request"}` {
		t.Fatalf("unexpected body: %s", string(body))
	}
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
