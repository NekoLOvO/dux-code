package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/NekoLOvO/dux-code/core/internal/config"
)

// AnthropicClient talks to Anthropic's /messages.
//
// More than field names differ from OpenAI — the shape does:
//   - system is a top-level field, not a message in the array
//   - content is a block array (text / tool_use / tool_result), not a string
//   - tool results are not role=tool but a tool_result block inside a user message
//   - stream events are named (message_start / content_block_delta / message_delta)
//
// This layer only translates: it still emits llm.Result, so the tools layer never needs to know who
// it is talking to.
type AnthropicClient struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// anthropicVersion is the Messages API version header. Anthropic requires it, and the value maps to
// a set of behaviours — not "newer is better".
const anthropicVersion = "2023-06-01"

// NewAnthropic builds a client from a provider config.
func NewAnthropic(p config.Provider) (*AnthropicClient, error) {
	key, _ := p.ResolveKey()
	if key == "" && p.NeedsKey() {
		return nil, ErrNoAPIKey
	}
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = "https://api.anthropic.com/v1"
	}
	return &AnthropicClient{BaseURL: base, APIKey: key, HTTP: sharedClient}, nil
}

type anthropicBlock struct {
	Type string `json:"type"`
	// text
	Text string `json:"text,omitempty"`
	// thinking: note the field is thinking, not text. It is Anthropic's own block shape
	// {type:"thinking", thinking:"…", signature:"…"}, unlike a text block, and getting it wrong makes it
	// an empty thought with a 400.
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	// image
	Source *anthropicImageSource `json:"source,omitempty"`
}

// anthropicImageSource is the image source object.
// Note that data is plain base64: Claude does not accept a data:image/png;base64, prefix.
type anthropicImageSource struct {
	Type      string `json:"type"` // base64
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

// anthropicTool represents two kinds of tool:
//   - ours (empty type = custom): name + description + input_schema
//   - Anthropic's server tool (type = web_search_20250305): only name and max_uses, no input_schema
type anthropicTool struct {
	Type        string `json:"type,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema,omitempty"`
	// MaxUses only applies to server tools: how many searches one turn may run, so it cannot burn money
	// unattended.
	MaxUses int `json:"max_uses,omitempty"`
}

// anthropicThinking is the thinking field.
// Adaptive since 4.6: only type is given and the model budgets by difficulty. The older
// {"type":"enabled","budget_tokens":N} is a fixed budget, deprecated in 4.6 and unsupported from 4.7,
// so it is not offered here.
type anthropicThinking struct {
	Type string `json:"type"`
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	Thinking    *anthropicThinking `json:"thinking,omitempty"`
	// Effort is a top-level parameter (not inside thinking) controlling overall reasoning depth, reply
	// verbosity and how eagerly tools are called. Only 4.6 and later know the word.
	Effort string `json:"effort,omitempty"`
	Stream bool   `json:"stream"`
}

// --- translation: our messages → Anthropic's block array ---

func inputJSON(arguments string) json.RawMessage {
	s := strings.TrimSpace(arguments)
	// An empty string (the model sent empty arguments) has to become {}, since Anthropic rejects null.
	if s == "" {
		return json.RawMessage("{}")
	}
	if !json.Valid([]byte(s)) {
		return json.RawMessage("{}")
	}
	return json.RawMessage(s)
}

// appendBlocks merges blocks into the last message of the same role.
//
// Anthropic requires alternating user / assistant, while tool results are several consecutive user
// messages (one per tool). Without merging, the second tool result breaks the rule.
func appendBlocks(out []anthropicMessage, role string, blocks ...anthropicBlock) []anthropicMessage {
	if len(out) > 0 && out[len(out)-1].Role == role {
		out[len(out)-1].Content = append(out[len(out)-1].Content, blocks...)
		return out
	}
	return append(out, anthropicMessage{Role: role, Content: blocks})
}

func anthropicMessages(msgs []Message) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			blocks := make([]anthropicBlock, 0, 1+len(m.Images))
			// Images go before the text: Anthropic's recommendation, and visual understanding is better that
			// way.
			for _, img := range m.Images {
				blocks = append(blocks, anthropicBlock{
					Type: "image",
					Source: &anthropicImageSource{
						Type:      "base64",
						MediaType: img.MimeType,
						Data:      Base64(img),
					},
				})
			}
			if strings.TrimSpace(m.Content) != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: m.Content})
			}
			if len(blocks) == 0 {
				continue
			}
			out = appendBlocks(out, "user", blocks...)
		case RoleAssistant:
			blocks := make([]anthropicBlock, 0, len(m.Thinking)+len(m.ToolCalls)+1)
			// thinking must come first: inside one assistant message it has to precede text and tool_use, and
			// moving it is more than a 400 — the next turn's thinking breaks too.
			for _, tb := range m.Thinking {
				if tb.Text == "" && tb.Signature == "" {
					continue
				}
				blocks = append(blocks, anthropicBlock{
					Type:      "thinking",
					Thinking:  tb.Text,
					Signature: tb.Signature,
				})
			}
			if strings.TrimSpace(m.Content) != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, anthropicBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: inputJSON(tc.Function.Arguments),
				})
			}
			// Tool calls without text are normal; only when both are empty is there truly nothing to send.
			if len(blocks) == 0 {
				continue
			}
			out = appendBlocks(out, "assistant", blocks...)
		case RoleTool:
			text := m.Content
			if strings.TrimSpace(text) == "" {
				text = "(no output)"
			}
			out = appendBlocks(out, "user", anthropicBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   text,
			})
		}
	}
	return out
}

func anthropicTools(tools []Tool, nativeSearch bool) []anthropicTool {
	out := make([]anthropicTool, 0, len(tools)+1)
	for _, t := range tools {
		schema := t.Function.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, anthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: schema,
		})
	}
	if nativeSearch {
		// Anthropic searches server-side; we do not fetch pages ourselves. max_uses is the guard: at most five
		// searches per turn, or the model may keep going. The version string changes over time (currently
		// 2025-03-05); when it does, only this line changes.
		out = append(out, anthropicTool{
			Type:    "web_search_20250305",
			Name:    "web_search",
			MaxUses: 5,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// --- stream events ---

type anthropicEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	// message_start
	Message *struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
	// content_block_start
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
		Text string `json:"text"`
		// Thinking blocks use these two (again, the field is not text).
		Thinking  string `json:"thinking"`
		Signature string `json:"signature"`
	} `json:"content_block"`
	// content_block_delta
	Delta *struct {
		Type        string `json:"type"` // text_delta | input_json_delta | thinking_delta | signature_delta
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		Thinking    string `json:"thinking"`
		// Signature closes the thinking block (type=signature_delta). It is the verification code the model
		// signed the reasoning with, and the next turn must send it back verbatim.
		Signature  string `json:"signature"`
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	// message_delta
	Usage *struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// stopReason maps Anthropic's stop reasons onto our (OpenAI-style) vocabulary, so callers check one
// value to know a reply was cut off by max_tokens.
func stopReason(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	}
	return reason
}

func (c *AnthropicClient) Stream(ctx context.Context, req Request, onDelta DeltaFunc) (Result, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		// Anthropic requires max_tokens, so "off" cannot be sent as absent: 4096 is a floor small enough for
		// every model to accept, where a larger number would be a guess the API may reject. A cap the user
		// actually set arrives here as a real number, already lowered to the model's ceiling.
		maxTokens = 4096
	}

	body := anthropicRequest{
		Model:     req.Model,
		MaxTokens: maxTokens,
		System:    req.System,
		Messages:  anthropicMessages(req.Messages),
		Tools:     anthropicTools(req.Tools, req.NativeSearch),
		Stream:    true,
	}
	// The three thinking states have completely different shapes; do not merge them:
	//
	//	none            → thinking:{type:"disabled"}      no effort
	//	auto (unset)    → thinking:{type:"adaptive"}      no effort
	//	low/high/...    → thinking:{type:"adaptive"} + top-level effort
	//
	// "Off" is not a value of effort: effort only has low/medium/high/xhigh/max, and "none" is unknown
	// there. Off means switching thinking.type.
	//
	// Why "auto" sends adaptive instead of omitting: Anthropic's default is "do not think", so omitting
	// turns thinking off. adaptive is "decide by difficulty", which is what the menu's "auto" means. The
	// two are very different and must not be conflated.
	switch {
	case req.Reasoning == "none":
		body.Thinking = &anthropicThinking{Type: "disabled"}
	case req.Reasoning != "" || req.ReasoningAuto:
		// Reaching here means the capability table allowed it (config.ReasoningLevels), so an older model is
		// never sent adaptive — that only exists from 4.6.
		body.Thinking = &anthropicThinking{Type: "adaptive"}
		if req.Reasoning != "" {
			body.Effort = req.Reasoning
		}
	}

	// temperature has to be decided after thinking: with thinking on, Anthropic allows temperature 1
	// only, and 0.7 is an instant 400. It is always omitted here so the default applies — temperature
	// barely matters for "answer per spec" anyway.
	if req.Temperature > 0 && (body.Thinking == nil || body.Thinking.Type == "disabled") {
		t := req.Temperature
		body.Temperature = &t
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return Result{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/messages", bytes.NewReader(payload))
	if err != nil {
		return Result{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	if c.APIKey != "" {
		httpReq.Header.Set("x-api-key", c.APIKey)
	}

	resp, guard, err := openStream(ctx, c.HTTP, httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ErrCancelled
		}
		return Result{}, err
	}
	// Nothing after the last delta has anything to do with the connection: the response is already
	// fully read, and a close error there would be the socket's, not the answer's.
	defer func() { _ = guard.Close() }()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(guard, 2048))
		return Result{}, &HTTPError{Status: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}

	var res Result
	byIdx := map[int]*ToolCall{}
	var order []int
	// Thinking accumulates per index and becomes res.ThinkingBlocks in order. One reply can hold more
	// than one segment (the model may think again between tool calls).
	thinkByIdx := map[int]*ThinkingBlock{}
	var thinkOrder []int

	scanner := bufio.NewScanner(guard)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue // event: / ping / blank lines are all unneeded
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}

		var ev anthropicEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue // skip unknown events; one bad line must not end the turn
		}

		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				res.Usage.Input = ev.Message.Usage.InputTokens
				res.Usage.Output = ev.Message.Usage.OutputTokens
			}
		case "content_block_start":
			if ev.ContentBlock == nil {
				continue
			}
			switch ev.ContentBlock.Type {
			case "thinking":
				acc := &ThinkingBlock{
					Text:      ev.ContentBlock.Thinking,
					Signature: ev.ContentBlock.Signature,
				}
				thinkByIdx[ev.Index] = acc
				thinkOrder = append(thinkOrder, ev.Index)
				// A leading text block counts too, or the first segment is lost.
				if acc.Text != "" {
					res.Reasoning += acc.Text
					if req.OnReasoning != nil {
						req.OnReasoning(acc.Text)
					}
				}
			case "tool_use":
				acc := &ToolCall{ID: ev.ContentBlock.ID, Type: "function"}
				acc.Function.Name = ev.ContentBlock.Name
				byIdx[ev.Index] = acc
				order = append(order, ev.Index)
			}
		case "content_block_delta":
			if ev.Delta == nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					res.Content += ev.Delta.Text
					onDelta(ev.Delta.Text)
				}
			case "thinking_delta":
				// This used to be dropped: reasoning tokens are billed but not a word was visible.
				if ev.Delta.Thinking != "" {
					if acc, ok := thinkByIdx[ev.Index]; ok {
						acc.Text += ev.Delta.Thinking
					}
					res.Reasoning += ev.Delta.Thinking
					if req.OnReasoning != nil {
						req.OnReasoning(ev.Delta.Thinking)
					}
				}
			case "signature_delta":
				// The closing signature. Without it, sending the thinking back next turn is rejected.
				if acc, ok := thinkByIdx[ev.Index]; ok && ev.Delta.Signature != "" {
					acc.Signature += ev.Delta.Signature
				}
			case "input_json_delta":
				// Tool arguments arrive as a stream of JSON string fragments that only concatenate at the end.
				if acc, ok := byIdx[ev.Index]; ok {
					acc.Function.Arguments += ev.Delta.PartialJSON
				}
			}
		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				res.StopReason = stopReason(ev.Delta.StopReason)
			}
			if ev.Usage != nil {
				res.Usage.Output = ev.Usage.OutputTokens
			}
		case "error":
			msg := "unknown error"
			if ev.Error != nil {
				msg = ev.Error.Message
				if ev.Error.Type != "" {
					msg = ev.Error.Type + ": " + msg
				}
			}
			return res, fmt.Errorf("anthropic: %s", msg)
		}
	}

	for _, idx := range thinkOrder {
		if tb := thinkByIdx[idx]; tb != nil && (tb.Text != "" || tb.Signature != "") {
			res.ThinkingBlocks = append(res.ThinkingBlocks, *tb)
		}
	}

	for _, idx := range order {
		if tc := byIdx[idx]; tc.Function.Name != "" {
			if tc.Function.Arguments == "" {
				tc.Function.Arguments = "{}"
			}
			res.ToolCalls = append(res.ToolCalls, *tc)
		}
	}

	if err := scanner.Err(); err != nil {
		return res, streamErr(ctx, guard, err)
	}
	return res, nil
}
