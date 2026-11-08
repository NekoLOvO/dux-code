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

// OpenAIResponsesClient talks to OpenAI's /v1/responses.
//
// Differences from /chat/completions (more than the endpoint):
//   - messages go in input, and system becomes instructions
//   - tool calls are **standalone input items** (function_call / function_call_output), not
//     tool_calls attached to an assistant message
//   - reasoning depth is a nested reasoning:{effort}, not a top-level reasoning_effort
//   - stream events are named (response.output_text.delta and friends), not delta objects
//   - built-in search is a {"type":"web_search"} tool, which Chat Completions lacks
type OpenAIResponsesClient struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// NewOpenAIResponses builds a client from a provider config.
func NewOpenAIResponses(p config.Provider) (*OpenAIResponsesClient, error) {
	key, _ := p.ResolveKey()
	if key == "" && !isLocal(p.BaseURL) {
		return nil, ErrNoAPIKey
	}
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	return &OpenAIResponsesClient{
		BaseURL: base,
		APIKey:  key,
		HTTP:    sharedClient,
	}, nil
}

// One struct covers all three input item shapes: a normal message (role+content), function_call and
// function_call_output. Everything is omitempty, so only the relevant fields serialize.
type responsesInput struct {
	Type      string `json:"type,omitempty"`
	Role      string `json:"role,omitempty"`
	Content   any    `json:"content,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Output    string `json:"output,omitempty"`
}

type responsesTool struct {
	Type        string `json:"type"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type responsesReasoning struct {
	Effort string `json:"effort,omitempty"`
}

type responsesRequest struct {
	Model           string              `json:"model"`
	Instructions    string              `json:"instructions,omitempty"`
	Input           []responsesInput    `json:"input"`
	Tools           []responsesTool     `json:"tools,omitempty"`
	MaxOutputTokens int                 `json:"max_output_tokens,omitempty"`
	Temperature     *float64            `json:"temperature,omitempty"`
	Reasoning       *responsesReasoning `json:"reasoning,omitempty"`
	Stream          bool                `json:"stream"`
	// History is ours; OpenAI does not need to keep a copy.
	Store bool `json:"store"`
}

// responsesResult is the whole-turn result carried by response.completed / incomplete.
type responsesResult struct {
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type responsesEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
	// The item carried by output_item.added / output_item.done.
	Item *struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"item"`
	// function_call_arguments.delta is matched by item_id.
	ItemID string `json:"item_id"`
	// response.completed / incomplete
	Response *responsesResult `json:"response"`
	Error    *struct {
		Message string `json:"message"`
	} `json:"error"`
	Message string `json:"message"`
}

// responsesInputs translates our messages into input items.
func responsesInputs(msgs []Message) []responsesInput {
	out := make([]responsesInput, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			out = append(out, responsesInput{Role: "user", Content: responsesContent(m.Content, m.Images)})
		case RoleAssistant:
			if m.Content != "" {
				out = append(out, responsesInput{Role: "assistant", Content: m.Content})
			}
			// In Responses a tool call is a standalone item, not attached to the assistant message.
			for _, tc := range m.ToolCalls {
				out = append(out, responsesInput{
					Type:      "function_call",
					CallID:    tc.ID,
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				})
			}
		case RoleTool:
			text := m.Content
			if strings.TrimSpace(text) == "" {
				text = "(no output)"
			}
			out = append(out, responsesInput{
				Type:   "function_call_output",
				CallID: m.ToolCallID,
				Output: text,
			})
		}
	}
	return out
}

// responsesContent: a plain string without images, input_text / input_image with them.
func responsesContent(text string, images []Image) any {
	if len(images) == 0 {
		return text
	}
	parts := make([]map[string]any, 0, len(images)+1)
	if text != "" {
		parts = append(parts, map[string]any{"type": "input_text", "text": text})
	}
	for _, img := range images {
		// In Responses, image_url is a string, not an object.
		parts = append(parts, map[string]any{"type": "input_image", "image_url": DataURI(img)})
	}
	return parts
}

func (c *OpenAIResponsesClient) Stream(ctx context.Context, req Request, onDelta DeltaFunc) (Result, error) {
	body := responsesRequest{
		Model:        req.Model,
		Instructions: req.System,
		Input:        responsesInputs(req.Messages),
		Stream:       true,
		Store:        false,
	}
	if req.MaxTokens > 0 {
		body.MaxOutputTokens = req.MaxTokens
	}
	if req.Temperature > 0 {
		t := req.Temperature
		body.Temperature = &t
	}
	// A nested object, not a top-level reasoning_effort. Values are the same set as Chat Completions.
	if req.Reasoning != "" {
		body.Reasoning = &responsesReasoning{Effort: req.Reasoning}
	}

	tools := make([]responsesTool, 0, len(req.Tools)+1)
	for _, t := range req.Tools {
		tools = append(tools, responsesTool{
			Type:        "function",
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
		})
	}
	if req.NativeSearch {
		tools = append(tools, responsesTool{Type: "web_search"})
	}
	if len(tools) > 0 {
		body.Tools = tools
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return Result{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/responses", bytes.NewReader(payload))
	if err != nil {
		return Result{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, guard, err := openStream(ctx, c.HTTP, httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ErrCancelled
		}
		return Result{}, err
	}
	// Stops the watchdog and releases the connection; the error belongs to the socket, not the answer.
	defer func() { _ = guard.Close() }()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(guard, 2048))
		return Result{}, &HTTPError{Status: resp.StatusCode, Body: strings.TrimSpace(string(snippet))}
	}

	var res Result
	// function_call arrives in pieces: the item appears first (with id/name) and arguments fill in.
	// Responses' call_id is what becomes our ToolCallID.
	calls := map[string]*ToolCall{}
	var order []string

	scanner := bufio.NewScanner(guard)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return res, ErrCancelled
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}

		var ev responsesEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		if ev.Error != nil {
			return res, fmt.Errorf("%s", ev.Error.Message)
		}
		if ev.Type == "error" && ev.Message != "" {
			return res, fmt.Errorf("%s", ev.Message)
		}

		switch ev.Type {
		case "response.output_text.delta":
			if ev.Delta != "" {
				res.Content += ev.Delta
				onDelta(ev.Delta)
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if ev.Delta != "" {
				res.Reasoning += ev.Delta
				if req.OnReasoning != nil {
					req.OnReasoning(ev.Delta)
				}
			}
		case "response.output_item.added":
			if ev.Item != nil && ev.Item.Type == "function_call" && ev.Item.ID != "" {
				calls[ev.Item.ID] = &ToolCall{
					ID:   ev.Item.CallID,
					Type: "function",
					Function: FunctionCall{
						Name:      ev.Item.Name,
						Arguments: ev.Item.Arguments,
					},
				}
				order = append(order, ev.Item.ID)
			}
		case "response.function_call_arguments.delta":
			if acc := calls[ev.ItemID]; acc != nil {
				acc.Function.Arguments += ev.Delta
			}
		case "response.output_item.done":
			// done carries the complete arguments; do not overwrite accumulated deltas with an empty one.
			if ev.Item != nil && ev.Item.Type == "function_call" {
				if acc := calls[ev.Item.ID]; acc != nil {
					if ev.Item.Arguments != "" {
						acc.Function.Arguments = ev.Item.Arguments
					}
					if ev.Item.CallID != "" {
						acc.ID = ev.Item.CallID
					}
				}
			}
		case "response.completed", "response.incomplete":
			if ev.Response != nil {
				if ev.Response.Usage != nil {
					res.Usage.Input = ev.Response.Usage.InputTokens
					res.Usage.Output = ev.Response.Usage.OutputTokens
				}
				res.StopReason = responsesStopReason(ev.Response)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return res, streamErr(ctx, guard, err)
	}

	for _, id := range order {
		if tc := calls[id]; tc.Function.Name != "" {
			res.ToolCalls = append(res.ToolCalls, *tc)
		}
	}
	// With a tool call present, the stop reason follows "you have work to do", not the provider's
	// completed.
	if len(res.ToolCalls) > 0 {
		res.StopReason = "tool_calls"
	}
	return res, nil
}

func responsesStopReason(r *responsesResult) string {
	if r.IncompleteDetails != nil {
		switch r.IncompleteDetails.Reason {
		case "max_output_tokens":
			return "length"
		case "content_filter":
			return "content_filter"
		}
	}
	if r.Status == "incomplete" {
		return "length"
	}
	if r.Status == "failed" {
		return "error"
	}
	return "stop"
}
