// Package llm is a thin abstraction over providers: send a message list (tools optional) and get text
// and tool calls back piece by piece.
//
// Four wire formats:
//   - OpenAI-compatible (/chat/completions) covers deepseek, openrouter, llama.cpp, ollama
//   - OpenAI Responses (/responses) for official OpenAI: nested reasoning, built-in web_search
//   - native Gemini (:streamGenerateContent) for Google: thinkingLevel, google_search
//   - Anthropic (/messages): thinking + effort, web_search server tool
//
// Everything above knows only the types here, never who is on the other end.
package llm

import (
	"context"
	"encoding/base64"
	"errors"
)

// Role is a message role. "tool" appears after a tool call.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is one tool the model asked to call.
// Arguments is the raw JSON string the model emitted, unparsed — the tools layer validates it.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // function
	Function FunctionCall `json:"function"`
}

// FunctionCall is a ToolCall's payload.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ThinkingBlock is one piece of reasoning plus its signature.
//
// Only Anthropic needs it, and only when "thinking on + calling a tool": it requires the previous
// reply's thinking blocks back verbatim (signature included) or the next turn is a 400. Other
// providers leave both fields empty.
type ThinkingBlock struct {
	Text      string `json:"text"`
	Signature string `json:"signature,omitempty"`
}

// Image is a picture sent along with a message.
//
// Every provider shapes it differently (data URI / source object / inline_data), all variants of
// "media type + base64", so only the raw bytes are kept and each client wraps them.
type Image struct {
	MimeType string
	Data     []byte
}

// Base64 returns plain base64. Anthropic and Gemini want this — a data: prefix is rejected, and only
// the OpenAI-compatible layer accepts one.
func Base64(img Image) string {
	return base64.StdEncoding.EncodeToString(img.Data)
}

// DataURI returns "data:image/png;base64,...", which the OpenAI-compatible image_url takes.
func DataURI(img Image) string {
	return "data:" + img.MimeType + ";base64," + Base64(img)
}

// Message is one exchange.
// An assistant asking for tools carries ToolCalls; results come back as RoleTool + ToolCallID.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	// Thinking is the reasoning inside this tool loop (see ThinkingBlock).
	// Nothing is carried across turns: Anthropic allows omitting older reasoning, and including it only
	// wastes context.
	Thinking []ThinkingBlock `json:"thinking,omitempty"`
	// Images are sent with this message. Only user messages carry them.
	Images []Image `json:"-"`
}

// Tool is a tool spec for the model (JSON Schema).
type Tool struct {
	Type     string  `json:"type"` // function
	Function ToolDef `json:"function"`
}

// ToolDef is a Tool's payload.
type ToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// NewTool builds a function-type tool spec.
func NewTool(name, desc string, params any) Tool {
	return Tool{
		Type: "function",
		Function: ToolDef{
			Name:        name,
			Description: desc,
			Parameters:  params,
		},
	}
}

// Request is one call.
type Request struct {
	Model    string
	System   string
	Messages []Message
	Tools    []Tool
	// MaxTokens is the output cap; 0 means "leave it to the provider". Callers pass
	// config.OutputCap, which already lowered it to the model's own ceiling when that is known.
	MaxTokens   int
	Temperature float64
	// Reasoning is the depth wanted this turn: none / minimal / low / medium / high / xhigh / max. Empty
	// sends nothing and lets the provider decide.
	//
	// These are our names, not any vendor's parameter — each client translates (the OpenAI-compatible
	// layer uses reasoning_effort, Anthropic thinking + effort). Callers have run
	// config.NormalizeReasoning, so the value is supported by this model; still worth a look, because a
	// wrong value costs a 400 for the whole conversation.
	Reasoning string
	// ReasoningAuto means "the user picked no depth, let the model decide".
	//
	// It differs from Reasoning == "" in exactly one place: Anthropic. Sending no thinking means
	// "thinking off" there, while "auto" should be thinking:{type:"adaptive"} letting the model allocate
	// by difficulty. For every other provider sending nothing is the default behaviour, so only the
	// Anthropic client reads this field. It is false when the model has no reasoning at all — then even
	// adaptive cannot be sent.
	ReasoningAuto bool
	// NativeSearch means this turn carries the provider's own search tool (OpenAI's web_search, Gemini's
	// google_search, Anthropic's web_search server tool). The provider then searches server-side, saving
	// a round trip against fetching pages ourselves, and returns digested results.
	NativeSearch bool
	// OnReasoning receives the reasoning text; nil is fine. It lives on Request rather than changing
	// Stream's signature, so the Client interface stays put and providers that cannot do it do nothing.
	OnReasoning DeltaFunc
}

// Usage is this turn's usage.
type Usage struct {
	Input  int `json:"input"`
	Output int `json:"output"`
}

// Result is the turn's outcome. Text already went out through onDelta during Stream; this carries
// only what is known at the end.
type Result struct {
	Content   string
	ToolCalls []ToolCall
	Usage     Usage
	// Reasoning is this turn's reasoning text (the words behind the invisible tokens).
	Reasoning string
	// ThinkingBlocks is the returnable form of the same reasoning: text plus signature. It already went
	// out through OnReasoning while streaming; this copy is for the next turn.
	ThinkingBlocks []ThinkingBlock
	StopReason     string // stop | tool_calls | length | content_filter | ""
}

// DeltaFunc receives a new piece of text. Order is guaranteed and nothing repeats.
type DeltaFunc func(text string)

// ErrCancelled means the user stopped it, not a failure.
var ErrCancelled = errors.New("cancelled")

// ErrNoAPIKey means no usable key was found; callers should point the user at settings.
var ErrNoAPIKey = errors.New("no api key")

// Client is a provider.
type Client interface {
	// Stream blocks until the turn ends. onDelta is called several times.
	Stream(ctx context.Context, req Request, onDelta DeltaFunc) (Result, error)
}
