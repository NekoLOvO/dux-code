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

// OpenAIClient talks to OpenAI's /chat/completions.
// deepseek, openrouter, llama.cpp server and ollama all speak this shape.
type OpenAIClient struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client

	// useMaxCompletionTokens decides whether the output cap is called max_tokens or
	// max_completion_tokens. OpenAI's newer models take only the latter and a 400 on the old field,
	// while every other compatible endpoint (deepseek / gemini / openrouter …) still wants max_tokens.
	useMaxCompletionTokens bool
}

// NewOpenAI builds a client from a provider config.
func NewOpenAI(p config.Provider) (*OpenAIClient, error) {
	key, _ := p.ResolveKey()
	if key == "" && !isLocal(p.BaseURL) {
		return nil, ErrNoAPIKey
	}
	return &OpenAIClient{
		BaseURL: strings.TrimRight(p.BaseURL, "/"),
		APIKey:  key,
		HTTP:    sharedClient,
		// Decided by endpoint rather than provider id: Provider stores no id, and switching baseUrl to reach
		// a proxy is exactly when this field should change.
		useMaxCompletionTokens: isOfficialOpenAI(p.BaseURL),
	}, nil
}

// isOfficialOpenAI reports whether this is OpenAI's own API (including Azure-style xx.openai.com).
func isOfficialOpenAI(base string) bool {
	return strings.Contains(strings.ToLower(base), "api.openai.com")
}

func isLocal(base string) bool {
	return strings.Contains(base, "127.0.0.1") ||
		strings.Contains(base, "localhost") ||
		strings.Contains(base, "::1")
}

type chatMessage struct {
	Role string `json:"role"`
	// Content is a string, or a []map when images are attached (OpenAI's content array).
	Content    any        `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// chatContent stays a plain string when there are no images (widest compatibility) and switches to a
// content array only when there are.
func chatContent(text string, images []Image) any {
	if len(images) == 0 {
		return text
	}
	parts := make([]map[string]any, 0, len(images)+1)
	if text != "" {
		parts = append(parts, map[string]any{"type": "text", "text": text})
	}
	for _, img := range images {
		parts = append(parts, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": DataURI(img)},
		})
	}
	return parts
}

type chatRequest struct {
	Model               string         `json:"model"`
	Messages            []chatMessage  `json:"messages"`
	Tools               []Tool         `json:"tools,omitempty"`
	Stream              bool           `json:"stream"`
	StreamOptions       map[string]any `json:"stream_options,omitempty"`
	MaxTokens           int            `json:"max_tokens,omitempty"`
	MaxCompletionTokens int            `json:"max_completion_tokens,omitempty"`
	Temperature         *float64       `json:"temperature,omitempty"`
	// ReasoningEffort appears only when the user picked a level. Not sending it lets the provider use
	// its default, the only always-safe option.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type chatChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			// Names differ: DeepSeek uses reasoning_content, OpenRouter and some reasoning models use reasoning.
			// Both are accepted.
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func (c *OpenAIClient) Stream(ctx context.Context, req Request, onDelta DeltaFunc) (Result, error) {
	msgs := make([]chatMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, chatMessage{
			Role:       string(m.Role),
			Content:    chatContent(m.Content, m.Images),
			ToolCalls:  m.ToolCalls,
			ToolCallID: m.ToolCallID,
		})
	}

	body := chatRequest{
		Model:         req.Model,
		Messages:      msgs,
		Tools:         req.Tools,
		Stream:        true,
		StreamOptions: map[string]any{"include_usage": true},
	}
	if c.useMaxCompletionTokens {
		body.MaxCompletionTokens = req.MaxTokens
	} else {
		body.MaxTokens = req.MaxTokens
	}
	if req.Temperature > 0 {
		t := req.Temperature
		body.Temperature = &t
	}
	body.ReasoningEffort = req.Reasoning

	payload, err := json.Marshal(body)
	if err != nil {
		return Result{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(payload))
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

	var (
		res   Result
		byIdx = map[int]*ToolCall{}
		order []int
	)
	scanner := bufio.NewScanner(guard)
	// Per-SSE-line cap: some providers put a whole reasoning block on one line.
	scanner.Buffer(make([]byte, 64*1024), 4<<20)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return res, ErrCancelled
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue // heartbeat or blank line
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var chunk chatChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// One bad line must not ruin the turn.
			continue
		}
		if chunk.Error != nil {
			return res, fmt.Errorf("%s", chunk.Error.Message)
		}
		if chunk.Usage != nil {
			res.Usage.Input = chunk.Usage.PromptTokens
			res.Usage.Output = chunk.Usage.CompletionTokens
		}
		for _, choice := range chunk.Choices {
			// Reasoning comes first, the answer after; separate channels so the UI can show them apart.
			if r := choice.Delta.ReasoningContent + choice.Delta.Reasoning; r != "" {
				res.Reasoning += r
				if req.OnReasoning != nil {
					req.OnReasoning(r)
				}
			}
			if choice.Delta.Content != "" {
				res.Content += choice.Delta.Content
				onDelta(choice.Delta.Content)
			}
			// tool_calls arrive as fragments: several indexes per turn, id/name only in the first fragment, and
			// arguments as a growing string.
			for _, tc := range choice.Delta.ToolCalls {
				acc, ok := byIdx[tc.Index]
				if !ok {
					acc = &ToolCall{Type: "function"}
					byIdx[tc.Index] = acc
					order = append(order, tc.Index)
				}
				if tc.ID != "" {
					acc.ID = tc.ID
				}
				if tc.Type != "" {
					acc.Type = tc.Type
				}
				if tc.Function.Name != "" {
					acc.Function.Name = tc.Function.Name
				}
				acc.Function.Arguments += tc.Function.Arguments
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				res.StopReason = *choice.FinishReason
			}
		}
	}

	for _, idx := range order {
		if tc := byIdx[idx]; tc.Function.Name != "" {
			res.ToolCalls = append(res.ToolCalls, *tc)
		}
	}

	if err := scanner.Err(); err != nil {
		return res, streamErr(ctx, guard, err)
	}
	return res, nil
}

// New picks a client from the provider config.
func New(p config.Provider) (Client, error) {
	switch p.Type {
	case "", "openai":
		return NewOpenAI(p)
	case "anthropic":
		// /messages has a completely different shape, see anthropic.go.
		return NewAnthropic(p)
	case "openai-responses":
		// Official endpoint's newer interface: nested reasoning, standalone tool items, built-in web_search.
		return NewOpenAIResponses(p)
	case "gemini":
		// Native generateContent: the only route that has google_search.
		return NewGemini(p)
	default:
		return nil, fmt.Errorf("unknown provider type: %q", p.Type)
	}
}
