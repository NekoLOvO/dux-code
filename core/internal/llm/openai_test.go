package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/NekoLOvO/dux-code/core/internal/config"
)

// captured holds the request body so field names can be asserted.
func captureStream(t *testing.T, body *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
}

// OpenAI's newer models accept only max_completion_tokens; max_tokens is a 400.
func TestOfficialOpenAISendsMaxCompletionTokens(t *testing.T) {
	if !isOfficialOpenAI("https://api.openai.com/v1") {
		t.Fatal("api.openai.com should be recognized as the official endpoint")
	}
	var body map[string]any
	srv := captureStream(t, &body)
	defer srv.Close()

	c, err := NewOpenAI(config.Provider{Type: "openai", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	// The httptest address is not openai.com, so this checks the field-selection logic itself.
	c.useMaxCompletionTokens = true
	if _, err := c.Stream(context.Background(), Request{
		Model:     "gpt-test",
		MaxTokens: 1234,
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
	}, func(string) {}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if body["max_completion_tokens"] != float64(1234) {
		t.Errorf("max_completion_tokens = %#v, want 1234", body["max_completion_tokens"])
	}
	if _, ok := body["max_tokens"]; ok {
		t.Error("max_tokens must not be sent as well: some endpoints reject it outright")
	}
}

// Compatible endpoints (deepseek / gemini / openrouter / local) still accept only max_tokens.
func TestCompatEndpointSendsMaxTokens(t *testing.T) {
	if isOfficialOpenAI("https://api.deepseek.com/v1") {
		t.Fatal("deepseek must not be treated as official OpenAI")
	}
	var body map[string]any
	srv := captureStream(t, &body)
	defer srv.Close()

	c, err := NewOpenAI(config.Provider{Type: "openai", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	if c.useMaxCompletionTokens {
		t.Fatal("an httptest endpoint must not use max_completion_tokens")
	}
	if _, err := c.Stream(context.Background(), Request{
		Model:     "m",
		MaxTokens: 2048,
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
	}, func(string) {}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if body["max_tokens"] != float64(2048) {
		t.Errorf("max_tokens = %#v, want 2048", body["max_tokens"])
	}
	if _, ok := body["max_completion_tokens"]; ok {
		t.Error("compat endpoints must not receive max_completion_tokens")
	}
}

// Reasoning tokens are billed either way, so they must at least arrive (the names differ, so both
// are accepted).
func TestOpenAIReadsReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"reasoning\":\"ing\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c, err := NewOpenAI(config.Provider{Type: "openai", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	var got []string
	res, err := c.Stream(context.Background(), Request{
		Model:       "m",
		Messages:    []Message{{Role: RoleUser, Content: "hi"}},
		OnReasoning: func(s string) { got = append(got, s) },
	}, func(string) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if res.Reasoning != "thinking" {
		t.Errorf("Reasoning = %q, want thinking", res.Reasoning)
	}
	if res.Content != "answer" {
		t.Errorf("content must not be polluted by reasoning: %q", res.Content)
	}
	if len(got) != 2 || got[0] != "think" || got[1] != "ing" {
		t.Errorf("onReasoning got %#v", got)
	}
}
