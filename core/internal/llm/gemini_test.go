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

// geminiServer captures the request body and returns a controllable SSE stream.
func geminiServer(t *testing.T, body *map[string]any, sse string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, body)
		if got := r.Header.Get("x-goog-api-key"); got == "" {
			t.Errorf("x-goog-api-key is missing")
		}
		if want := "/models/gemini-test:streamGenerateContent"; r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		if got := r.URL.Query().Get("alt"); got != "sse" {
			t.Errorf("alt = %q, want sse", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
}

// Reasoning depth goes as thinkingLevel (uppercase), not OpenAI's reasoning_effort.
func TestGeminiSendsThinkingLevel(t *testing.T) {
	var body map[string]any
	srv := geminiServer(t, &body, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]},\"finishReason\":\"STOP\"}]}\n\n")
	defer srv.Close()

	c, err := NewGemini(config.Provider{Type: "gemini", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewGemini: %v", err)
	}
	if _, err := c.Stream(context.Background(), Request{
		Model:     "gemini-test",
		System:    "You are an assistant",
		Reasoning: config.ReasoningHigh,
		MaxTokens: 256,
		Tools:     []Tool{NewTool("read_file", "read a file", map[string]any{"type": "object"})},
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
	}, func(string) {}); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	gen, _ := body["generationConfig"].(map[string]any)
	if gen == nil {
		t.Fatalf("no generationConfig: %#v", body)
	}
	if gen["maxOutputTokens"] != float64(256) {
		t.Errorf("maxOutputTokens = %#v", gen["maxOutputTokens"])
	}
	tc, _ := gen["thinkingConfig"].(map[string]any)
	if tc == nil || tc["thinkingLevel"] != "HIGH" {
		t.Errorf("thinkingLevel = %#v, want HIGH", gen["thinkingConfig"])
	}
	if _, has := gen["thinkingBudget"]; has {
		t.Errorf("thinkingBudget must not be sent as well")
	}

	si, _ := body["systemInstruction"].(map[string]any)
	if si == nil {
		t.Errorf("system belongs in systemInstruction, not messages")
	}
	contents, _ := body["contents"].([]any)
	if len(contents) != 1 {
		t.Errorf("len(contents) = %d, want 1", len(contents))
	}

	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	tool0, _ := tools[0].(map[string]any)
	if _, has := tool0["functionDeclarations"]; !has {
		t.Errorf("tools should live in functionDeclarations: %#v", tool0)
	}
}

// Built-in search is a separate element in tools, not mixed into the function-declaration object.
func TestGeminiNativeSearchIsSeparateTool(t *testing.T) {
	var body map[string]any
	srv := geminiServer(t, &body, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]}}]}\n\n")
	defer srv.Close()

	c, _ := NewGemini(config.Provider{Type: "gemini", BaseURL: srv.URL, APIKey: "k"})
	if _, err := c.Stream(context.Background(), Request{
		Model:        "gemini-test",
		NativeSearch: true,
		Tools:        []Tool{NewTool("read_file", "read a file", nil)},
		Messages:     []Message{{Role: RoleUser, Content: "hi"}},
	}, func(string) {}); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	tools, _ := body["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("len(tools) = %d, want 2 (function declarations + google_search)", len(tools))
	}
	second, _ := tools[1].(map[string]any)
	if _, has := second["google_search"]; !has {
		t.Errorf("the second tool should be google_search: %#v", second)
	}
	if _, has := second["functionDeclarations"]; has {
		t.Errorf("google_search cannot share an object with functionDeclarations")
	}
}

// Streaming: reasoning summary goes to reasoning, text to content, functionCall becomes ToolCall.
func TestGeminiStreamParsing(t *testing.T) {
	sse := "" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"thinking\",\"thought\":true}]}}]}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"answer\"}]}}]}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"read_file\",\"args\":{\"path\":\"a.txt\"}}}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":11,\"candidatesTokenCount\":7}}\n\n"

	var body map[string]any
	srv := geminiServer(t, &body, sse)
	defer srv.Close()

	c, _ := NewGemini(config.Provider{Type: "gemini", BaseURL: srv.URL, APIKey: "k"})
	var deltas, thoughts []string
	res, err := c.Stream(context.Background(), Request{
		Model:    "gemini-test",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		OnReasoning: func(s string) {
			thoughts = append(thoughts, s)
		},
	}, func(s string) { deltas = append(deltas, s) })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if res.Content != "answer" {
		t.Errorf("Content = %q", res.Content)
	}
	if res.Reasoning != "thinking" {
		t.Errorf("Reasoning = %q", res.Reasoning)
	}
	if len(deltas) != 1 || deltas[0] != "answer" {
		t.Errorf("onDelta got %#v", deltas)
	}
	if len(thoughts) != 1 || thoughts[0] != "thinking" {
		t.Errorf("OnReasoning got %#v", thoughts)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %#v", res.ToolCalls)
	}
	if res.ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("name = %q", res.ToolCalls[0].Function.Name)
	}
	if res.ToolCalls[0].Function.Arguments != `{"path":"a.txt"}` {
		t.Errorf("arguments = %q", res.ToolCalls[0].Function.Arguments)
	}
	if res.Usage.Input != 11 || res.Usage.Output != 7 {
		t.Errorf("usage = %+v", res.Usage)
	}
	if res.StopReason != "stop" {
		t.Errorf("StopReason = %q", res.StopReason)
	}
}

// Tool results come back as functionResponse + name: Gemini has no call id.
func TestGeminiToolResultUsesName(t *testing.T) {
	var body map[string]any
	srv := geminiServer(t, &body, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]}}]}\n\n")
	defer srv.Close()

	c, _ := NewGemini(config.Provider{Type: "gemini", BaseURL: srv.URL, APIKey: "k"})
	_, err := c.Stream(context.Background(), Request{
		Model: "gemini-test",
		Messages: []Message{
			{Role: RoleUser, Content: "read a file"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{
				ID:       "gemini-1-read_file",
				Type:     "function",
				Function: FunctionCall{Name: "read_file", Arguments: `{"path":"a.txt"}`},
			}}},
			{Role: RoleTool, ToolCallID: "gemini-1-read_file", Content: "file contents"},
		},
	}, func(string) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	contents, _ := body["contents"].([]any)
	if len(contents) != 3 {
		t.Fatalf("len(contents) = %d, want 3", len(contents))
	}
	last, _ := contents[2].(map[string]any)
	parts, _ := last["parts"].([]any)
	if len(parts) != 1 {
		t.Fatalf("parts = %#v", parts)
	}
	part0, _ := parts[0].(map[string]any)
	fr, _ := part0["functionResponse"].(map[string]any)
	if fr == nil {
		t.Fatalf("expected functionResponse: %#v", part0)
	}
	if fr["name"] != "read_file" {
		t.Errorf("name = %#v, want read_file (looked up from the assistant toolCalls)", fr["name"])
	}
	if _, isObj := fr["response"].(map[string]any); !isObj {
		t.Errorf("response must be an object: %#v", fr["response"])
	}

	// The assistant message needs its functionCall (that is how Gemini rebuilds tool-call history).
	assistant, _ := contents[1].(map[string]any)
	if assistant["role"] != "model" {
		t.Errorf("the assistant role should be model, got %#v", assistant["role"])
	}
}

func TestGeminiStopReasonMapping(t *testing.T) {
	cases := map[string]string{
		"STOP":       "stop",
		"MAX_TOKENS": "length",
		"SAFETY":     "content_filter",
	}
	for in, want := range cases {
		if got := geminiStopReason(in); got != want {
			t.Errorf("geminiStopReason(%q) = %q, want %q", in, got, want)
		}
	}
}
