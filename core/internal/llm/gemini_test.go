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
//
// The call carries a signature because a real one always does; without it the exchange is the
// unsigned-tail case, which is dropped on purpose (see geminiDropUnsignedTail).
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
				ID:        "gemini-1-read_file",
				Type:      "function",
				Function:  FunctionCall{Name: "read_file", Arguments: `{"path":"a.txt"}`},
				Signature: "sig",
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

// "Off" is thinkingBudget:0, not a level — and the two must never travel together, which is a 400.
// The level table only offers "off" to the models that accept the budget switch, so this is the
// request a user gets there.
func TestGeminiSendsThinkingBudgetForOff(t *testing.T) {
	var body map[string]any
	srv := geminiServer(t, &body, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]},\"finishReason\":\"STOP\"}]}\n\n")
	defer srv.Close()

	c, err := NewGemini(config.Provider{Type: "gemini", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewGemini: %v", err)
	}
	if _, err := c.Stream(context.Background(), Request{
		Model:     "gemini-test",
		Reasoning: config.ReasoningNone,
		MaxTokens: 256,
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
	}, func(string) {}); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	gen, _ := body["generationConfig"].(map[string]any)
	if gen == nil {
		t.Fatalf("no generationConfig: %#v", body)
	}
	tc, _ := gen["thinkingConfig"].(map[string]any)
	if tc == nil {
		t.Fatalf("thinkingConfig missing, so nothing turned thinking off: %#v", gen)
	}
	// 0 has to survive the encoder: an int with omitempty would drop the one value that means off.
	if v, ok := tc["thinkingBudget"]; !ok || v != float64(0) {
		t.Errorf("thinkingBudget = %#v, want 0", tc["thinkingBudget"])
	}
	if _, has := tc["thinkingLevel"]; has {
		t.Errorf("thinkingLevel must not be sent with a budget")
	}
}

// "Auto" is sending nothing: the provider default is the definition of it, and for a model whose
// levels we do not know it is also the only safe request.
func TestGeminiAutoSendsNoThinkingConfig(t *testing.T) {
	var body map[string]any
	srv := geminiServer(t, &body, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]},\"finishReason\":\"STOP\"}]}\n\n")
	defer srv.Close()

	c, err := NewGemini(config.Provider{Type: "gemini", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewGemini: %v", err)
	}
	if _, err := c.Stream(context.Background(), Request{
		Model:         "gemini-test",
		ReasoningAuto: true,
		MaxTokens:     256,
		Messages:      []Message{{Role: RoleUser, Content: "hi"}},
	}, func(string) {}); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	gen, _ := body["generationConfig"].(map[string]any)
	if gen == nil {
		t.Fatalf("no generationConfig: %#v", body)
	}
	if tc, has := gen["thinkingConfig"]; has {
		t.Errorf("auto must not send thinkingConfig, got %#v", tc)
	}
}

// A built-in tool next to function declarations is a 400 unless the request says the server-side
// invocations are welcome:
//
//	Please enable tool_config.include_server_side_tool_invocations to use Built-in tools with
//	Function calling.
//
// That is our normal shape (google_search is ours, the declarations are the agent's tools), so the
// flag has to ride along — and it must not appear when only one of the two is sent.
func TestGeminiToolConfigWhenSearchAndFunctionsTogether(t *testing.T) {
	specs := []Tool{NewTool("read_file", "read a file", map[string]any{"type": "object"})}

	cases := []struct {
		name   string
		req    Request
		wanted bool
	}{
		{
			name:   "search and functions",
			req:    Request{Model: "gemini-test", Tools: specs, NativeSearch: true},
			wanted: true,
		},
		{
			name:   "functions only",
			req:    Request{Model: "gemini-test", Tools: specs},
			wanted: false,
		},
		{
			name:   "search only",
			req:    Request{Model: "gemini-test", NativeSearch: true},
			wanted: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var body map[string]any
			srv := geminiServer(t, &body, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]},\"finishReason\":\"STOP\"}]}\n\n")
			defer srv.Close()

			client, err := NewGemini(config.Provider{Type: "gemini", BaseURL: srv.URL, APIKey: "k"})
			if err != nil {
				t.Fatalf("NewGemini: %v", err)
			}
			c.req.MaxTokens = 64
			c.req.Messages = []Message{{Role: RoleUser, Content: "hi"}}
			if _, err := client.Stream(context.Background(), c.req, func(string) {}); err != nil {
				t.Fatalf("Stream: %v", err)
			}

			cfg, has := body["toolConfig"].(map[string]any)
			if !c.wanted {
				if has {
					t.Errorf("toolConfig must not be sent here, got %#v", cfg)
				}
				return
			}
			if !has {
				t.Fatalf("toolConfig missing, so the API would reject the request: %#v", body)
			}
			if cfg["includeServerSideToolInvocations"] != true {
				t.Errorf("includeServerSideToolInvocations = %#v, want true", cfg["includeServerSideToolInvocations"])
			}
		})
	}
}

// The response for a search-enabled turn carries the search itself as parts of its own: toolCall
// with toolType GOOGLE_SEARCH_WEB, then the toolResponse holding the results. They are not function
// calls — Google ran them inside the same request, so there is nobody to answer — and they must not
// turn into a tool call the agent would try to execute. Only the text is the answer.
func TestGeminiIgnoresServerSideToolParts(t *testing.T) {
	var body map[string]any
	sse := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"toolCall\":{\"toolType\":\"GOOGLE_SEARCH_WEB\",\"args\":{\"queries\":[\"x\"]},\"id\":\"call_1\"}}]}}]}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"toolResponse\":{\"toolType\":\"GOOGLE_SEARCH_WEB\",\"response\":{\"results\":\"many\"}}}]}}]}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"the answer\"}]},\"finishReason\":\"STOP\"}]}\n\n"
	srv := geminiServer(t, &body, sse)
	defer srv.Close()

	c, err := NewGemini(config.Provider{Type: "gemini", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewGemini: %v", err)
	}
	res, err := c.Stream(context.Background(), Request{
		Model:        "gemini-test",
		NativeSearch: true,
		MaxTokens:    64,
		Messages:     []Message{{Role: RoleUser, Content: "hi"}},
	}, func(string) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if len(res.ToolCalls) != 0 {
		t.Errorf("a server-side search became a tool call the agent would try to run: %+v", res.ToolCalls)
	}
	if res.Content != "the answer" {
		t.Errorf("content = %q, want the text part only", res.Content)
	}
}

// Gemini signs the function-call part of its reply, and the next request has to send the signature
// back on the same part; it is per call, so it is captured onto the ToolCall.
func TestGeminiRoundTripsThoughtSignature(t *testing.T) {
	var first, second map[string]any
	callSSE := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"echo\",\"args\":{\"text\":\"hi\"}},\"thoughtSignature\":\"sig-abc\"}]}}]}\n\n"
	srv := geminiServer(t, &first, callSSE)
	defer srv.Close()

	c, err := NewGemini(config.Provider{Type: "gemini", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewGemini: %v", err)
	}
	res, err := c.Stream(context.Background(), Request{
		Model:     "gemini-test",
		Tools:     []Tool{NewTool("echo", "echo", map[string]any{"type": "object"})},
		MaxTokens: 64,
		Messages:  []Message{{Role: RoleUser, Content: "use echo"}},
	}, func(string) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %+v", res.ToolCalls)
	}
	if res.ToolCalls[0].Signature != "sig-abc" {
		t.Fatalf("the signature was dropped: %q", res.ToolCalls[0].Signature)
	}

	// Second request: the call goes back with its result, and the signature has to be on the call part.
	srv2 := geminiServer(t, &second, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"done\"}]},\"finishReason\":\"STOP\"}]}\n\n")
	defer srv2.Close()
	c2, err := NewGemini(config.Provider{Type: "gemini", BaseURL: srv2.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewGemini: %v", err)
	}
	if _, err := c2.Stream(context.Background(), Request{
		Model:     "gemini-test",
		Tools:     []Tool{NewTool("echo", "echo", map[string]any{"type": "object"})},
		MaxTokens: 64,
		Messages: []Message{
			{Role: RoleUser, Content: "use echo"},
			{Role: RoleAssistant, ToolCalls: res.ToolCalls},
			{Role: RoleTool, ToolCallID: res.ToolCalls[0].ID, Content: "echoed"},
		},
	}, func(string) {}); err != nil {
		t.Fatalf("Stream (second turn): %v", err)
	}

	parts := geminiPartsOf(t, second, "model")
	callPart, ok := parts[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected part: %#v", parts[0])
	}
	if callPart["thoughtSignature"] != "sig-abc" {
		t.Errorf("thoughtSignature = %#v, want sig-abc; without it this request would 400", callPart["thoughtSignature"])
	}
	if _, has := callPart["functionCall"]; !has {
		t.Errorf("the call part lost its functionCall: %#v", callPart)
	}
}

// The signature also has to survive being written to a session and read back: a tool loop resumed
// after a restart sends the stored call again.
func TestGeminiToolCallSignatureSurvivesJSON(t *testing.T) {
	in := ToolCall{
		ID: "gemini-1-echo", Type: "function",
		Function:  FunctionCall{Name: "echo", Arguments: `{"text":"hi"}`},
		Signature: "sig-abc",
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out ToolCall
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Signature != "sig-abc" {
		t.Errorf("signature lost through the session file: %q", out.Signature)
	}
}

// geminiPartsOf digs out the parts of the message with the given role.
func geminiPartsOf(t *testing.T, body map[string]any, role string) []any {
	t.Helper()
	contents, ok := body["contents"].([]any)
	if !ok {
		t.Fatalf("no contents: %#v", body)
	}
	for _, c := range contents {
		m, ok := c.(map[string]any)
		if !ok || m["role"] != role {
			continue
		}
		parts, _ := m["parts"].([]any)
		return parts
	}
	t.Fatalf("no %q turn in %#v", role, contents)
	return nil
}

// A tool exchange whose call carries no signature cannot be sent, so the tail is dropped and the
// conversation continues from the request that asked for the tool.
func TestGeminiDropsUnsignedToolTail(t *testing.T) {
	legacy := []Message{
		{Role: RoleUser, Content: "run the test tool"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Function: FunctionCall{Name: "echo", Arguments: "{}"}}}},
		{Role: RoleTool, ToolCallID: "c1", Content: "tool output"},
	}
	got := geminiDropUnsignedTail(legacy)
	if len(got) != 1 || got[0].Role != RoleUser {
		t.Fatalf("the unsigned exchange was not dropped: %+v", got)
	}

	// Signed calls are exactly what the API wants back, so nothing is touched.
	signed := []Message{
		{Role: RoleUser, Content: "run the test tool"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Signature: "sig", Function: FunctionCall{Name: "echo"}}}},
		{Role: RoleTool, ToolCallID: "c1", Content: "ok"},
	}
	if got := geminiDropUnsignedTail(signed); len(got) != 3 {
		t.Errorf("a signed exchange must be left alone, got %d messages", len(got))
	}

	// An assistant message that only answered has no calls to repair.
	plain := []Message{{Role: RoleUser, Content: "hi"}, {Role: RoleAssistant, Content: "hello"}}
	if got := geminiDropUnsignedTail(plain); len(got) != 2 {
		t.Errorf("a text answer must not be dropped, got %d messages", len(got))
	}

	// An older unsigned call is fine (the API only rejects the last model turn), so it stays.
	older := []Message{
		{Role: RoleUser, Content: "a"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Function: FunctionCall{Name: "echo"}}}},
		{Role: RoleTool, ToolCallID: "c1", Content: "ok"},
		{Role: RoleAssistant, Content: "done"},
		{Role: RoleUser, Content: "b"},
	}
	if got := geminiDropUnsignedTail(older); len(got) != 5 {
		t.Errorf("an unsigned call further back must be kept, got %d messages", len(got))
	}
}
