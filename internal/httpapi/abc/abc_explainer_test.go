package abc

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	abclib "github.com/Sales-Analysis/abc-helper-lib/abc"
)

func TestBuildAnalysisExplanationPrompt(t *testing.T) {
	results := []abclib.ProductResult{
		{SKU: "1", Name: "A", Quantity: 10, PriceTotal: 1000, ShareTotal: 50, Group: "A"},
		{SKU: "2", Name: "B", Quantity: 7, PriceTotal: 700, ShareTotal: 35, Group: "B"},
		{SKU: "3", Name: "C", Quantity: 3, PriceTotal: 300, ShareTotal: 15, Group: "C"},
	}

	prompt := buildAnalysisExplanationPrompt(results, 2)
	if !strings.Contains(prompt, "Сводка по группам") {
		t.Fatalf("prompt must contain group summary")
	}
	if !strings.Contains(prompt, "Топ-2") {
		t.Fatalf("prompt must contain top-N section")
	}
	if !strings.Contains(prompt, "SKU=1") {
		t.Fatalf("prompt must include top sku details")
	}
}

func TestCallAssistantForExplanation(t *testing.T) {
	cfg := analysisExplainerConfig{
		enabled: true,
		baseURL: "http://assistant.local",
		timeout: 3 * time.Second,
		mode:    "help",
	}

	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.URL.String(); got != "http://assistant.local/v1/chat" {
				t.Fatalf("unexpected URL: %s", got)
			}
			if got := req.Header.Get("X-Request-ID"); got != "rid-1" {
				t.Fatalf("expected X-Request-ID forward, got %q", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(`{"answer":"Короткое пояснение."}`)),
			}, nil
		}),
	}

	answer, err := callAssistantForExplanation(context.Background(), client, cfg, "rid-1", "prompt")
	if err != nil {
		t.Fatalf("callAssistantForExplanation err: %v", err)
	}
	if strings.TrimSpace(answer) != "Короткое пояснение." {
		t.Fatalf("unexpected answer: %q", answer)
	}
}

func TestCallAssistantForExplanationHTTPError(t *testing.T) {
	cfg := analysisExplainerConfig{
		enabled: true,
		baseURL: "http://assistant.local",
		timeout: 3 * time.Second,
		mode:    "help",
	}

	client := &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(`{"error":"upstream failed"}`)),
			}, nil
		}),
	}

	_, err := callAssistantForExplanation(context.Background(), client, cfg, "rid-2", "prompt")
	if err == nil {
		t.Fatalf("expected error on non-2xx response")
	}
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
