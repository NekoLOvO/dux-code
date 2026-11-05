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

// GeminiClient talks to Gemini's native API (streamGenerateContent).
//
// More than field names differ from the OpenAI-compatible layer:
//   - the conversation is contents / parts, not messages / content strings
//   - system is a separate systemInstruction, not a message
//   - tools are functionDeclarations; calls and results are functionCall / functionResponse inside
//     parts, matched by name and with no id
//   - built-in search is google_search in tools, alongside function declarations
//   - reasoning depth is generationConfig.thinkingConfig.thinkingLevel (uppercase)
//
// Worth leaving the compatible layer for: google_search exists only in the native API, and the
// compatible endpoint (/v1beta/openai/chat/completions) explicitly does not support it.
type GeminiClient struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// NewGemini builds a client from a provider config.
func NewGemini(p config.Provider) (*GeminiClient, error) {
	key, _ := p.ResolveKey()
	if key == "" && !isLocal(p.BaseURL) {
		return nil, ErrNoAPIKey
	}
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta"
	}
	return &GeminiClient{
		BaseURL: base,
		APIKey:  key,
		HTTP:    sharedClient,
	}, nil
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *geminiInlineData       `json:"inline_data,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type geminiFunctionResponse struct {
	Name string `json:"name"`
	// Response must be an object: Gemini requires the {result: "..."} shape and rejects a bare string.
	Response map[string]any `json:"response"`
}

// geminiInlineData is Gemini's image format: mime_type plus plain base64 (no data: prefix).
type geminiInlineData struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiFunctionDecl struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

// One element of geminiTool can hold only one thing: function declarations or google_search. Mixing
// them in one object is rejected.
type geminiTool struct {
	FunctionDeclarations []geminiFunctionDecl `json:"functionDeclarations,omitempty"`
	GoogleSearch         *struct{}            `json:"google_search,omitempty"`
}

type geminiThinkingConfig struct {
	ThinkingLevel string `json:"thinkingLevel,omitempty"`
}

type geminiGenConfig struct {
	MaxOutputTokens int                   `json:"maxOutputTokens,omitempty"`
	Temperature     *float64              `json:"temperature,omitempty"`
	ThinkingConfig  *geminiThinkingConfig `json:"thinkingConfig,omitempty"`
}

type geminiRequest struct {
	Contents          []geminiContent  `json:"contents"`
	SystemInstruction *geminiContent   `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenConfig `json:"generationConfig,omitempty"`
	Tools             []geminiTool     `json:"tools,omitempty"`
}

type geminiChunk struct {
	Candidates []struct {
		Content      geminiContent `json:"content"`
		FinishReason string        `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
	Error *struct {
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// geminiThinkingLevel translates our vocabulary into the native uppercase levels.
//
// Gemini has no "off" level (turning it off needs thinkingBudget:0, a different mechanism), so none
// returns an empty string (= send nothing) and the provider default applies. The level table never
// contains none anyway, so a clamped caller cannot reach here.
func geminiThinkingLevel(level string) string {
	switch level {
	case config.ReasoningMinimal:
		return "MINIMAL"
	case config.ReasoningLow:
		return "LOW"
	case config.ReasoningMedium:
		return "MEDIUM"
	case config.ReasoningHigh:
		return "HIGH"
	}
	return ""
}

// geminiContents translates our messages into contents.
//
// RoleTool's functionResponse needs a name, while llm.Message only has ToolCallID, so the
// assistant's toolCalls are scanned first to build an id→name map.
func geminiContents(msgs []Message) []geminiContent {
	names := map[string]string{}
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			if tc.ID != "" && tc.Function.Name != "" {
				names[tc.ID] = tc.Function.Name
			}
		}
	}

	out := make([]geminiContent, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			parts := make([]geminiPart, 0, 1+len(m.Images))
			if m.Content != "" {
				parts = append(parts, geminiPart{Text: m.Content})
			}
			for _, img := range m.Images {
				parts = append(parts, geminiPart{
					InlineData: &geminiInlineData{MimeType: img.MimeType, Data: Base64(img)},
				})
			}
			if len(parts) == 0 {
				continue
			}
			out = append(out, geminiContent{Role: "user", Parts: parts})
		case RoleAssistant:
			parts := make([]geminiPart, 0, 1+len(m.ToolCalls))
			if m.Content != "" {
				parts = append(parts, geminiPart{Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				args := map[string]any{}
				if strings.TrimSpace(tc.Function.Arguments) != "" {
					// Unparseable arguments become an empty object: letting the model see "this call had no parameters"
					// beats a 400 for the whole turn.
					_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
				}
				parts = append(parts, geminiPart{
					FunctionCall: &geminiFunctionCall{Name: tc.Function.Name, Args: args},
				})
			}
			if len(parts) == 0 {
				continue
			}
			out = append(out, geminiContent{Role: "model", Parts: parts})
		case RoleTool:
			name := names[m.ToolCallID]
			if name == "" {
				name = "tool"
			}
			text := m.Content
			if strings.TrimSpace(text) == "" {
				text = "(no output)"
			}
			out = append(out, geminiContent{
				Role: "user",
				Parts: []geminiPart{{
					FunctionResponse: &geminiFunctionResponse{
						Name:     name,
						Response: map[string]any{"result": text},
					},
				}},
			})
		}
	}
	return out
}

func (c *GeminiClient) Stream(ctx context.Context, req Request, onDelta DeltaFunc) (Result, error) {
	body := geminiRequest{
		Contents: geminiContents(req.Messages),
	}
	if req.System != "" {
		body.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: req.System}}}
	}

	gen := &geminiGenConfig{MaxOutputTokens: req.MaxTokens}
	if req.Temperature > 0 {
		t := req.Temperature
		gen.Temperature = &t
	}
	if lvl := geminiThinkingLevel(req.Reasoning); lvl != "" {
		gen.ThinkingConfig = &geminiThinkingConfig{ThinkingLevel: lvl}
	}
	if gen.MaxOutputTokens > 0 || gen.Temperature != nil || gen.ThinkingConfig != nil {
		body.GenerationConfig = gen
	}

	if len(req.Tools) > 0 || req.NativeSearch {
		tools := make([]geminiTool, 0, 2)
		if len(req.Tools) > 0 {
			decls := make([]geminiFunctionDecl, 0, len(req.Tools))
			for _, t := range req.Tools {
				decls = append(decls, geminiFunctionDecl{
					Name:        t.Function.Name,
					Description: t.Function.Description,
					Parameters:  t.Function.Parameters,
				})
			}
			tools = append(tools, geminiTool{FunctionDeclarations: decls})
		}
		if req.NativeSearch {
			tools = append(tools, geminiTool{GoogleSearch: &struct{}{}})
		}
		body.Tools = tools
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return Result{}, err
	}

	model := strings.TrimPrefix(req.Model, "models/")
	url := c.BaseURL + "/models/" + model + ":streamGenerateContent?alt=sse"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return Result{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.APIKey != "" {
		// A header rather than ?key=: query strings land in proxies and access logs.
		httpReq.Header.Set("x-goog-api-key", c.APIKey)
	}

	resp, guard, err := openStream(ctx, c.HTTP, httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ErrCancelled
		}
		return Result{}, err
	}
	defer guard.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(guard, 2048))
		return Result{}, &HTTPError{Status: resp.StatusCode, Body: strings.TrimSpace(string(snippet))}
	}

	var res Result
	scanner := bufio.NewScanner(guard)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return res, ErrCancelled
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}

		var chunk geminiChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // one bad line must not ruin the turn
		}
		if chunk.Error != nil {
			return res, fmt.Errorf("%s", chunk.Error.Message)
		}
		if chunk.UsageMetadata != nil {
			res.Usage.Input = chunk.UsageMetadata.PromptTokenCount
			res.Usage.Output = chunk.UsageMetadata.CandidatesTokenCount
		}

		for _, cand := range chunk.Candidates {
			for _, part := range cand.Content.Parts {
				// thought=true is a reasoning summary, not the answer.
				if part.Text != "" {
					if part.Thought {
						res.Reasoning += part.Text
						if req.OnReasoning != nil {
							req.OnReasoning(part.Text)
						}
					} else {
						res.Content += part.Text
						onDelta(part.Text)
					}
				}
				if part.FunctionCall != nil {
					args, _ := json.Marshal(part.FunctionCall.Args)
					// Gemini has no call id, so one is generated: unique within the turn is enough, since the next
					// turn's functionResponse matches by name.
					res.ToolCalls = append(res.ToolCalls, ToolCall{
						ID:       fmt.Sprintf("gemini-%d-%s", len(res.ToolCalls)+1, part.FunctionCall.Name),
						Type:     "function",
						Function: FunctionCall{Name: part.FunctionCall.Name, Arguments: string(args)},
					})
				}
			}
			if cand.FinishReason != "" {
				res.StopReason = geminiStopReason(cand.FinishReason)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return res, streamErr(ctx, guard, err)
	}
	return res, nil
}

// geminiStopReason maps Gemini's finishReason onto our three values.
func geminiStopReason(fr string) string {
	switch fr {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT":
		return "content_filter"
	}
	return strings.ToLower(fr)
}
