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

func responsesServer(t *testing.T, body *map[string]any, sse string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, body)
		if want := "/responses"; r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
}

// In Responses, reasoning depth is a nested reasoning:{effort}, not a top-level reasoning_effort.
func TestResponsesSendsNestedReasoning(t *testing.T) {
	var body map[string]any
	srv := responsesServer(t, &body, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	defer srv.Close()

	c, err := NewOpenAIResponses(config.Provider{Type: "openai-responses", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Stream(context.Background(), Request{
		Model:     "gpt-test",
		System:    "You are an assistant",
		Reasoning: config.ReasoningHigh,
		MaxTokens: 512,
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
	}, func(string) {}); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if _, has := body["reasoning_effort"]; has {
		t.Errorf("Responses must not carry a top-level reasoning_effort")
	}
	reasoning, _ := body["reasoning"].(map[string]any)
	if reasoning == nil || reasoning["effort"] != "high" {
		t.Errorf("reasoning = %#v, want {effort:high}", body["reasoning"])
	}
	if body["instructions"] != "You are an assistant" {
		t.Errorf("instructions = %#v", body["instructions"])
	}
	if body["max_output_tokens"] != float64(512) {
		t.Errorf("max_output_tokens = %#v", body["max_output_tokens"])
	}
	if body["store"] != false {
		t.Errorf("store should be false")
	}
}

// In Responses, tool calls and results are standalone input items, not the assistant's tool_calls.
func TestResponsesToolItems(t *testing.T) {
	var body map[string]any
	srv := responsesServer(t, &body, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	defer srv.Close()

	c, _ := NewOpenAIResponses(config.Provider{Type: "openai-responses", BaseURL: srv.URL, APIKey: "k"})
	_, err := c.Stream(context.Background(), Request{
		Model: "gpt-test",
		Messages: []Message{
			{Role: RoleUser, Content: "read a file"},
			{Role: RoleAssistant, Content: "let me look", ToolCalls: []ToolCall{{
				ID: "call_1", Type: "function",
				Function: FunctionCall{Name: "read_file", Arguments: `{"path":"a.txt"}`},
			}}},
			{Role: RoleTool, ToolCallID: "call_1", Content: "file contents"},
		},
	}, func(string) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	input, _ := body["input"].([]any)
	if len(input) != 4 {
		t.Fatalf("len(input) = %d, want 4 (user + assistant + function_call + output): %#v", len(input), input)
	}
	fc, _ := input[2].(map[string]any)
	if fc["type"] != "function_call" || fc["call_id"] != "call_1" || fc["name"] != "read_file" {
		t.Errorf("function_call item = %#v", fc)
	}
	if _, has := fc["role"]; has {
		t.Errorf("function_call must not carry a role")
	}
	out, _ := input[3].(map[string]any)
	if out["type"] != "function_call_output" || out["call_id"] != "call_1" {
		t.Errorf("function_call_output = %#v", out)
	}
	if out["output"] != "file contents" {
		t.Errorf("output = %#v", out["output"])
	}
}

func TestResponsesNativeSearchTool(t *testing.T) {
	var body map[string]any
	srv := responsesServer(t, &body, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	defer srv.Close()

	c, _ := NewOpenAIResponses(config.Provider{Type: "openai-responses", BaseURL: srv.URL, APIKey: "k"})
	if _, err := c.Stream(context.Background(), Request{
		Model:        "gpt-test",
		NativeSearch: true,
		Tools:        []Tool{NewTool("read_file", "read a file", nil)},
		Messages:     []Message{{Role: RoleUser, Content: "hi"}},
	}, func(string) {}); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	tools, _ := body["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("len(tools) = %d, want 2", len(tools))
	}
	last, _ := tools[1].(map[string]any)
	if last["type"] != "web_search" {
		t.Errorf("the second tool should be web_search: %#v", last)
	}
	if _, has := last["name"]; has {
		t.Errorf("web_search carries no name")
	}
}

// Streaming: text, reasoning summary, fragmented function_call arguments, usage.
func TestResponsesStreamParsing(t *testing.T) {
	sse := "" +
		"event: response.reasoning_summary_text.delta\n" +
		"data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"thinking\"}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n" +
		"event: response.output_item.added\n" +
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"id\":\"item_1\",\"call_id\":\"call_9\",\"name\":\"read_file\",\"arguments\":\"\"}}\n\n" +
		"event: response.function_call_arguments.delta\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"item_1\",\"delta\":\"{\\\"path\\\":\"}\n\n" +
		"event: response.function_call_arguments.delta\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"item_1\",\"delta\":\"\\\"a.txt\\\"}\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":21,\"output_tokens\":8}}}\n\n"

	var body map[string]any
	srv := responsesServer(t, &body, sse)
	defer srv.Close()

	c, _ := NewOpenAIResponses(config.Provider{Type: "openai-responses", BaseURL: srv.URL, APIKey: "k"})
	var deltas []string
	res, err := c.Stream(context.Background(), Request{
		Model:       "gpt-test",
		Messages:    []Message{{Role: RoleUser, Content: "hi"}},
		OnReasoning: func(string) {},
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
		t.Errorf("onDelta = %#v", deltas)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %#v", res.ToolCalls)
	}
	tc := res.ToolCalls[0]
	if tc.ID != "call_9" {
		t.Errorf("ID should be the call_id (needed to echo back next turn): %q", tc.ID)
	}
	if tc.Function.Arguments != `{"path":"a.txt"}` {
		t.Errorf("arguments were not accumulated correctly: %q", tc.Function.Arguments)
	}
	if res.Usage.Input != 21 || res.Usage.Output != 8 {
		t.Errorf("usage = %+v", res.Usage)
	}
	if res.StopReason != "tool_calls" {
		t.Errorf("with tool calls present StopReason should be tool_calls, got %q", res.StopReason)
	}
}

func TestResponsesIncompleteIsLength(t *testing.T) {
	got := responsesStopReason(&responsesResult{
		Status: "incomplete",
		IncompleteDetails: &struct {
			Reason string `json:"reason"`
		}{Reason: "max_output_tokens"},
	})
	if got != "length" {
		t.Errorf("got %q, want length", got)
	}
}
