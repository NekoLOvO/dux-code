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

func TestAnthropicMessagesGroupsToolResults(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "take a look"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "t1", Type: "function", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`}},
			{ID: "t2", Type: "function", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"b.go"}`}},
		}},
		{Role: RoleTool, ToolCallID: "t1", Content: "package a"},
		{Role: RoleTool, ToolCallID: "t2", Content: "package b"},
		{Role: RoleAssistant, Content: "done looking"},
	}

	got := anthropicMessages(msgs)

	// Anthropic requires alternating user / assistant, so consecutive tool_results must merge into one
	// user message.
	if len(got) != 4 {
		t.Fatalf("want 4 messages, got %d: %+v", len(got), got)
	}
	if got[0].Role != "user" || got[1].Role != "assistant" || got[2].Role != "user" || got[3].Role != "assistant" {
		t.Fatalf("roles must alternate, got %q %q %q %q", got[0].Role, got[1].Role, got[2].Role, got[3].Role)
	}
	if len(got[2].Content) != 2 {
		t.Fatalf("want 2 tool_result blocks merged, got %d", len(got[2].Content))
	}
	for i, b := range got[2].Content {
		if b.Type != "tool_result" {
			t.Errorf("block %d: want tool_result, got %q", i, b.Type)
		}
	}
	if got[2].Content[0].ToolUseID != "t1" || got[2].Content[1].ToolUseID != "t2" {
		t.Errorf("tool_use_id mismatch: %q %q", got[2].Content[0].ToolUseID, got[2].Content[1].ToolUseID)
	}
}

func TestAnthropicAssistantPutsTextBeforeToolUse(t *testing.T) {
	got := anthropicMessages([]Message{{
		Role:    RoleAssistant,
		Content: "I will read the file",
		ToolCalls: []ToolCall{
			{ID: "t1", Type: "function", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`}},
		},
	}})
	if len(got) != 1 || len(got[0].Content) != 2 {
		t.Fatalf("want 1 message with 2 blocks, got %+v", got)
	}
	if got[0].Content[0].Type != "text" || got[0].Content[1].Type != "tool_use" {
		t.Errorf("text must come before tool_use, got %q then %q", got[0].Content[0].Type, got[0].Content[1].Type)
	}
	if string(got[0].Content[1].Input) != `{"path":"a.go"}` {
		t.Errorf("input should pass through unchanged, got %s", got[0].Content[1].Input)
	}
}

func TestAnthropicEmptyToolResultGetsPlaceholder(t *testing.T) {
	got := anthropicMessages([]Message{
		{Role: RoleTool, ToolCallID: "t1", Content: "   \n"},
	})
	if len(got) != 1 || got[0].Content[0].Content == "" {
		t.Fatalf("empty tool output needs a placeholder, got %+v", got)
	}
}

func TestInputJSONFallsBackToEmptyObject(t *testing.T) {
	cases := map[string]string{
		"":         "{}",
		"   ":      "{}",
		"not json": "{}",
		`{"a":1}`:  `{"a":1}`,
		`[]`:       `[]`,
	}
	for in, want := range cases {
		if got := string(inputJSON(in)); got != want {
			t.Errorf("inputJSON(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnthropicStopReason(t *testing.T) {
	cases := map[string]string{
		"end_turn":      "stop",
		"stop_sequence": "stop",
		"max_tokens":    "length",
		"tool_use":      "tool_calls",
		"refusal":       "content_filter",
	}
	for in, want := range cases {
		if got := stopReason(in); got != want {
			t.Errorf("stopReason(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnthropicOmitsEmptyAssistantMessage(t *testing.T) {
	// An assistant message with neither text nor tool calls is rejected by Anthropic.
	got := anthropicMessages([]Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: ""},
	})
	if len(got) != 1 {
		t.Fatalf("want 1 message, got %d: %+v", len(got), got)
	}
}

func TestAnthropicStreamParsesSSE(t *testing.T) {
	var gotPath, gotKey, gotVersion string
	var body map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, ev := range []string{
			`event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":12,"output_tokens":1}}}

`,
			`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

`,
			`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}

`,
			`event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file","input":{}}}

`,
			`event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}

`,
			`event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"a.go\"}"}}

`,
			`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":30}}

`,
			`event: message_stop
data: {"type":"message_stop"}

`,
		} {
			_, _ = io.WriteString(w, ev)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer srv.Close()

	c, err := NewAnthropic(config.Provider{
		Type:    "anthropic",
		BaseURL: srv.URL,
		APIKey:  "sk-ant-test",
		Models:  map[string]config.ModelEntry{},
	})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}

	var deltas []string
	res, err := c.Stream(context.Background(), Request{
		Model:     "claude-test",
		System:    "You are an assistant",
		MaxTokens: 1024,
		Messages:  []Message{{Role: RoleUser, Content: "read a.go"}},
	}, func(s string) { deltas = append(deltas, s) })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if gotPath != "/messages" {
		t.Errorf("path = %q, want /messages", gotPath)
	}
	if gotKey != "sk-ant-test" {
		t.Errorf("x-api-key = %q", gotKey)
	}
	if gotVersion != anthropicVersion {
		t.Errorf("anthropic-version = %q, want %q", gotVersion, anthropicVersion)
	}

	// system must be a top-level field, never mixed into messages.
	if body["system"] != "You are an assistant" {
		t.Errorf("system should be a top-level string, got %#v", body["system"])
	}
	if msgs, ok := body["messages"].([]any); ok && len(msgs) == 1 {
		if m, ok := msgs[0].(map[string]any); ok && m["role"] == "system" {
			t.Error("system must not appear inside messages")
		}
	}
	if body["max_tokens"] != float64(1024) {
		t.Errorf("max_tokens = %#v, want 1024", body["max_tokens"])
	}

	if res.Content != "hi" {
		t.Errorf("Content = %q, want hi", res.Content)
	}
	if len(deltas) != 1 || deltas[0] != "hi" {
		t.Errorf("onDelta got %#v", deltas)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("want 1 tool call, got %d", len(res.ToolCalls))
	}
	tc := res.ToolCalls[0]
	if tc.ID != "toolu_1" || tc.Function.Name != "read_file" {
		t.Errorf("tool call = %+v", tc)
	}
	if tc.Function.Arguments != `{"path":"a.go"}` {
		t.Errorf("arguments were joined wrong: %q", tc.Function.Arguments)
	}
	if res.StopReason != "tool_calls" {
		t.Errorf("StopReason = %q, want tool_calls", res.StopReason)
	}
	if res.Usage.Input != 12 || res.Usage.Output != 30 {
		t.Errorf("usage = %+v, want in=12 out=30", res.Usage)
	}
}

func TestAnthropicStreamReportsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	}))
	defer srv.Close()

	c, err := NewAnthropic(config.Provider{
		Type:    "anthropic",
		BaseURL: srv.URL,
		APIKey:  "bad",
		Models:  map[string]config.ModelEntry{},
	})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}
	if _, err := c.Stream(context.Background(), Request{Model: "claude-test", MaxTokens: 16}, func(string) {}); err == nil {
		t.Fatal("a bad key should return an error")
	}
}

// thinking_delta used to be dropped entirely: reasoning tokens are billed but nothing was visible.
func TestAnthropicStreamsThinking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []string{
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"check the requirements first\"}}\n\n",
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":9}}\n\n",
		} {
			_, _ = io.WriteString(w, ev)
		}
	}))
	defer srv.Close()

	c, err := NewAnthropic(config.Provider{Type: "anthropic", BaseURL: srv.URL, APIKey: "k", Models: map[string]config.ModelEntry{}})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}
	var got []string
	res, err := c.Stream(context.Background(), Request{
		Model:       "claude-test",
		MaxTokens:   64,
		Messages:    []Message{{Role: RoleUser, Content: "hi"}},
		OnReasoning: func(s string) { got = append(got, s) },
	}, func(string) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if res.Reasoning != "check the requirements first" {
		t.Errorf("Reasoning = %q", res.Reasoning)
	}
	if res.Content != "ok" {
		t.Errorf("reasoning must not leak into the answer: %q", res.Content)
	}
	if len(got) != 1 {
		t.Errorf("onReasoning got %#v", got)
	}
}

// Built-in search is a server tool: only type and name, no input_schema.
func TestAnthropicNativeSearchTool(t *testing.T) {
	tools := anthropicTools(nil, true)
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	tool := tools[0]
	if tool.Type != "web_search_20250305" {
		t.Errorf("type = %q", tool.Type)
	}
	if tool.Name != "web_search" {
		t.Errorf("name = %q", tool.Name)
	}
	if tool.InputSchema != nil {
		t.Errorf("a server tool must not carry input_schema")
	}
	if tool.MaxUses <= 0 {
		t.Errorf("without a cap the model may keep searching: max_uses = %d", tool.MaxUses)
	}

	// Absent entirely when off.
	if got := anthropicTools(nil, false); len(got) != 0 {
		t.Errorf("%d tools remain after disabling it", len(got))
	}
}

// In the tool loop, "thinking" is not just text for humans but a credential to send back: without the
// signature, Anthropic returns 400 on the next turn.
func TestAnthropicKeepsThinkingSignature(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []string{
			`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

`,
			`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"look at the file first"}}

`,
			`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-abc"}}

`,
			`event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"sig-abc"}}

`,
			`event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_9","name":"read_file","input":{}}}

`,
			`event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}

`,
			`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}

`,
		} {
			_, _ = io.WriteString(w, ev)
		}
	}))
	defer srv.Close()

	c, err := NewAnthropic(config.Provider{Type: "anthropic", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}
	res, err := c.Stream(context.Background(), Request{
		Model:    "claude-opus-4-7",
		Messages: []Message{{Role: RoleUser, Content: "read a.go"}},
	}, func(string) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if len(res.ThinkingBlocks) != 1 {
		t.Fatalf("want 1 thinking block, got %d: %+v", len(res.ThinkingBlocks), res.ThinkingBlocks)
	}
	tb := res.ThinkingBlocks[0]
	if tb.Text != "look at the file first" || tb.Signature != "sig-abc" {
		t.Errorf("thinking block = %+v", tb)
	}
	if res.Reasoning != "look at the file first" {
		t.Errorf("Reasoning = %q", res.Reasoning)
	}
}

// When sending, thinking goes first in the assistant message and the field is named thinking.
func TestAnthropicSendsThinkingBeforeText(t *testing.T) {
	got := anthropicMessages([]Message{{
		Role:     RoleAssistant,
		Content:  "I want to read the file",
		Thinking: []ThinkingBlock{{Text: "look at the file first", Signature: "sig-abc"}},
		ToolCalls: []ToolCall{{
			ID:       "toolu_9",
			Function: FunctionCall{Name: "read_file", Arguments: "{}"},
		}},
	}})
	if len(got) != 1 || len(got[0].Content) != 3 {
		t.Fatalf("want 3 blocks (thinking/text/tool_use), got %+v", got)
	}
	b := got[0].Content[0]
	if b.Type != "thinking" {
		t.Fatalf("the first block should be thinking, got %q", b.Type)
	}
	if b.Thinking != "look at the file first" {
		t.Errorf("thinking text = %q", b.Thinking)
	}
	if b.Signature != "sig-abc" {
		t.Errorf("signature = %q", b.Signature)
	}
	if got[0].Content[1].Type != "text" || got[0].Content[2].Type != "tool_use" {
		t.Errorf("wrong order: %+v", got[0].Content)
	}
}

// With thinking on, Anthropic allows temperature 1 only; 0.7 is an instant 400.
// Our approach is to omit it (use the default), so the key must be absent.
func TestAnthropicDropsTemperatureWhenThinking(t *testing.T) {
	cases := []struct {
		name string
		req  Request
		want bool // whether temperature should appear
	}{
		{
			name: "auto enables thinking",
			req:  Request{Model: "claude-opus-4-7", Temperature: 0.7, ReasoningAuto: true},
			want: false,
		},
		{
			name: "an explicit level enables thinking",
			req:  Request{Model: "claude-opus-4-7", Temperature: 0.7, Reasoning: "high"},
			want: false,
		},
		{
			name: "temperature is allowed with thinking off",
			req:  Request{Model: "claude-opus-4-7", Temperature: 0.7, Reasoning: "none"},
			want: true,
		},
		{
			name: "a model with no reasoning options",
			req:  Request{Model: "claude-opus-4-7", Temperature: 0.7},
			want: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var body map[string]any
			srv := captureStream(t, &body)
			defer srv.Close()

			cl, err := NewAnthropic(config.Provider{Type: "anthropic", BaseURL: srv.URL, APIKey: "k"})
			if err != nil {
				t.Fatalf("NewAnthropic: %v", err)
			}
			c.req.Messages = []Message{{Role: RoleUser, Content: "hi"}}
			if _, err := cl.Stream(context.Background(), c.req, func(string) {}); err != nil {
				t.Fatalf("Stream: %v", err)
			}

			_, has := body["temperature"]
			if has != c.want {
				t.Errorf("temperature present = %v, want %v (body=%#v)", has, c.want, body)
			}
		})
	}
}
