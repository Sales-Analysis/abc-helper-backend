package abc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	abclib "github.com/Sales-Analysis/abc-helper-lib/abc"
)

func TestChartChatHandlerExcludeBySKU(t *testing.T) {
	uploadJobsStore = newUploadJobStore()
	jobID := "job-chat-exclude"
	uploadJobsStore.create(jobID, "")
	uploadJobsStore.markDone(jobID, UploadResponse{
		Result: []abclib.ProductResult{
			{SKU: "060116126", Name: "Сим-Карта МТС", PriceTotal: 1000, ShareTotal: 60, ShareAccumulated: 60, Group: "A"},
			{SKU: "060115097", Name: "Смартфон Xiaomi", PriceTotal: 500, ShareTotal: 30, ShareAccumulated: 90, Group: "B"},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/abc/chart-chat", bytes.NewBufferString(`{
		"job_id":"job-chat-exclude",
		"message":"исключи sku 060116126"
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	ChartChatHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body chartChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Actions) != 1 {
		t.Fatalf("actions = %d, want 1", len(body.Actions))
	}
	if body.Actions[0].Type != "exclude_items" {
		t.Fatalf("action type = %q, want exclude_items", body.Actions[0].Type)
	}
	if len(body.Actions[0].ItemIDs) != 1 || body.Actions[0].ItemIDs[0] != "060116126|сим-карта мтс" {
		t.Fatalf("unexpected item ids: %#v", body.Actions[0].ItemIDs)
	}
}

func TestChartChatHandlerRestoreAll(t *testing.T) {
	uploadJobsStore = newUploadJobStore()
	jobID := "job-chat-restore"
	uploadJobsStore.create(jobID, "")
	uploadJobsStore.markDone(jobID, UploadResponse{
		Result: []abclib.ProductResult{
			{SKU: "060116126", Name: "Сим-Карта МТС", PriceTotal: 1000, ShareTotal: 60, ShareAccumulated: 60, Group: "A"},
			{SKU: "060115097", Name: "Смартфон Xiaomi", PriceTotal: 500, ShareTotal: 30, ShareAccumulated: 90, Group: "B"},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/abc/chart-chat", bytes.NewBufferString(`{
		"job_id":"job-chat-restore",
		"message":"верни всё обратно",
		"excluded_item_ids":["060116126|сим-карта мтс","060115097|смартфон xiaomi"]
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	ChartChatHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body chartChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Actions) != 1 {
		t.Fatalf("actions = %d, want 1", len(body.Actions))
	}
	if body.Actions[0].Type != "restore_all" {
		t.Fatalf("action type = %q, want restore_all", body.Actions[0].Type)
	}
	if body.Actions[0].Count != 2 {
		t.Fatalf("restore count = %d, want 2", body.Actions[0].Count)
	}
}

func TestChartChatHandlerOutOfScopeQuestion(t *testing.T) {
	uploadJobsStore = newUploadJobStore()
	jobID := "job-chat-scope"
	uploadJobsStore.create(jobID, "")
	uploadJobsStore.markDone(jobID, UploadResponse{
		Result: []abclib.ProductResult{
			{SKU: "060116126", Name: "Сим-Карта МТС", PriceTotal: 1000, ShareTotal: 60, ShareAccumulated: 60, Group: "A"},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/abc/chart-chat", bytes.NewBufferString(`{
		"job_id":"job-chat-scope",
		"message":"как к байдену относишься?"
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	ChartChatHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body chartChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(body.Answer, "не могу выходить за рамки") {
		t.Fatalf("unexpected answer: %q", body.Answer)
	}
}

func TestChartChatHandlerItemQuestion(t *testing.T) {
	uploadJobsStore = newUploadJobStore()
	jobID := "job-chat-item"
	uploadJobsStore.create(jobID, "")
	uploadJobsStore.markDone(jobID, UploadResponse{
		Result: []abclib.ProductResult{
			{SKU: "TOP-1", Name: "Флагман", Quantity: 2, PriceTotal: 800, ShareTotal: 80, ShareAccumulated: 80, Group: "A"},
			{SKU: "060121084", Name: "Мобильный телефон Maxvi P33 black", Quantity: 1, PriceTotal: 150, ShareTotal: 15, ShareAccumulated: 95, Group: "B"},
			{SKU: "LOW-1", Name: "Мелочь", Quantity: 1, PriceTotal: 50, ShareTotal: 5, ShareAccumulated: 100, Group: "C"},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/abc/chart-chat", bytes.NewBufferString(`{
		"job_id":"job-chat-item",
		"message":"а что скажешь про Мобильный телефон Maxvi P33 black"
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	ChartChatHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body chartChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(body.Answer, "Maxvi P33 black") {
		t.Fatalf("expected item in answer, got %q", body.Answer)
	}
	if !strings.Contains(body.Answer, "Группа B") {
		t.Fatalf("expected group in answer, got %q", body.Answer)
	}
}

func TestChartChatHandlerContextualGroupLowestPercentQuestion(t *testing.T) {
	uploadJobsStore = newUploadJobStore()
	jobID := "job-chat-context-group"
	uploadJobsStore.create(jobID, "")
	uploadJobsStore.markDone(jobID, UploadResponse{
		Result: []abclib.ProductResult{
			{SKU: "A-1", Name: "Товар A1", Quantity: 1, PriceTotal: 800, ShareTotal: 80, ShareAccumulated: 80, Group: "A"},
			{SKU: "B-1", Name: "Товар B1", Quantity: 1, PriceTotal: 100, ShareTotal: 10, ShareAccumulated: 90, Group: "B"},
			{SKU: "B-2", Name: "Товар B2", Quantity: 1, PriceTotal: 50, ShareTotal: 5, ShareAccumulated: 95, Group: "B"},
			{SKU: "C-1", Name: "Товар C1", Quantity: 1, PriceTotal: 50, ShareTotal: 5, ShareAccumulated: 100, Group: "C"},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/abc/chart-chat", bytes.NewBufferString(`{
		"job_id":"job-chat-context-group",
		"message":"какой самый низкий процент в этой группе?",
		"history":[
			{"role":"assistant","text":"Группа A сейчас занимает 80.00% активной выручки."},
			{"role":"user","text":"почему группа A сейчас самая крупная?"}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	ChartChatHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body chartChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(body.Answer, "В группе A самый низкий процент") {
		t.Fatalf("unexpected answer: %q", body.Answer)
	}
}

func TestChartChatHandlerABCKeywordQuestion(t *testing.T) {
	uploadJobsStore = newUploadJobStore()
	jobID := "job-chat-abc-keyword"
	uploadJobsStore.create(jobID, "")
	uploadJobsStore.markDone(jobID, UploadResponse{
		Result: []abclib.ProductResult{
			{SKU: "A-1", Name: "Товар A1", Quantity: 1, PriceTotal: 700, ShareTotal: 70, ShareAccumulated: 70, Group: "A"},
			{SKU: "A-2", Name: "Товар A2", Quantity: 1, PriceTotal: 200, ShareTotal: 20, ShareAccumulated: 90, Group: "B"},
			{SKU: "A-3", Name: "Товар A3", Quantity: 1, PriceTotal: 100, ShareTotal: 10, ShareAccumulated: 100, Group: "C"},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/abc/chart-chat", bytes.NewBufferString(`{
		"job_id":"job-chat-abc-keyword",
		"message":"я хотел увидеть крупные значения в ABC"
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	ChartChatHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body chartChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if strings.Contains(body.Answer, "не могу выходить за рамки") {
		t.Fatalf("unexpected out-of-scope answer: %q", body.Answer)
	}
}
