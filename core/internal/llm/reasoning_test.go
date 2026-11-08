package llm

import (
	"context"
	"testing"

	"github.com/NekoLOvO/dux-code/core/internal/config"
)

// The OpenAI-compatible layer takes a top-level reasoning_effort. Not sending it lets the provider
// decide, so with no level chosen the key must be absent entirely — not an empty string (some
// endpoints treat that as invalid).
func TestOpenAISendsReasoningEffort(t *testing.T) {
	var body map[string]any
	srv := captureStream(t, &body)
	defer srv.Close()

	c, err := NewOpenAI(config.Provider{Type: "openai", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	if _, err := c.Stream(context.Background(), Request{
		Model:     "deepseek-flash",
		Reasoning: "high",
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
	}, func(string) {}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if body["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %#v, want high", body["reasoning_effort"])
	}
}

func TestOpenAIOmitsReasoningWhenAuto(t *testing.T) {
	var body map[string]any
	srv := captureStream(t, &body)
	defer srv.Close()

	c, err := NewOpenAI(config.Provider{Type: "openai", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	if _, err := c.Stream(context.Background(), Request{
		Model:    "deepseek-flash",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(string) {}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, ok := body["reasoning_effort"]; ok {
		t.Error("auto must not send reasoning_effort: let the provider use its default")
	}
}

// Anthropic's shape is entirely different: a thinking object turns on adaptive mode and depth is a
// top-level effort. Both must appear — effort alone does not turn thinking on.
func TestAnthropicSendsAdaptiveThinkingAndEffort(t *testing.T) {
	var body map[string]any
	srv := captureStream(t, &body)
	defer srv.Close()

	c, err := NewAnthropic(config.Provider{Type: "anthropic", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}
	if _, err := c.Stream(context.Background(), Request{
		Model:     "claude-opus-4-7",
		Reasoning: "xhigh",
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
	}, func(string) {}); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking should be an object, got %#v", body["thinking"])
	}
	if thinking["type"] != "adaptive" {
		t.Errorf("thinking.type = %#v, want adaptive", thinking["type"])
	}
	// The old fixed-budget field must not appear (deprecated in 4.6, unsupported from 4.7).
	if _, ok := thinking["budget_tokens"]; ok {
		t.Error("budget_tokens must not be sent: adaptive thinking since 4.6")
	}
	if body["effort"] != "xhigh" {
		t.Errorf("effort = %#v, want xhigh", body["effort"])
	}
}

// "Auto" is not "omit the parameter" for Anthropic: its default is not thinking, so omitting turns
// thinking off. "Decide by difficulty" needs an explicit adaptive (but no effort, since no depth was
// chosen).
func TestAnthropicSendsAdaptiveForAuto(t *testing.T) {
	var body map[string]any
	srv := captureStream(t, &body)
	defer srv.Close()

	c, err := NewAnthropic(config.Provider{Type: "anthropic", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}
	if _, err := c.Stream(context.Background(), Request{
		Model:         "claude-opus-4-7",
		ReasoningAuto: true,
		Messages:      []Message{{Role: RoleUser, Content: "hi"}},
	}, func(string) {}); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("auto should send thinking, got %#v", body["thinking"])
	}
	if thinking["type"] != "adaptive" {
		t.Errorf("thinking.type = %#v, want adaptive", thinking["type"])
	}
	if _, ok := body["effort"]; ok {
		t.Error("auto must not send effort: let the model pick its depth")
	}
}

// "Model has no reasoning" and "the user chose auto" both look like an empty Reasoning, but the
// latter must send adaptive while the former cannot send it at all (old models lack the value).
func TestAnthropicOmitsThinkingWhenUnsupported(t *testing.T) {
	var body map[string]any
	srv := captureStream(t, &body)
	defer srv.Close()

	c, err := NewAnthropic(config.Provider{Type: "anthropic", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}
	if _, err := c.Stream(context.Background(), Request{
		Model:    "claude-opus-4-7",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(string) {}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, ok := body["thinking"]; ok {
		t.Error("thinking must not be sent when the model does not support reasoning")
	}
	if _, ok := body["effort"]; ok {
		t.Error("effort must not be sent")
	}
}

// "Thinking off" for Anthropic is not effort none (no such value; sending it is a 400) but
// thinking.type switched to disabled.
func TestAnthropicDisablesThinkingForNone(t *testing.T) {
	var body map[string]any
	srv := captureStream(t, &body)
	defer srv.Close()

	c, err := NewAnthropic(config.Provider{Type: "anthropic", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}
	if _, err := c.Stream(context.Background(), Request{
		Model:     "claude-opus-4-7",
		Reasoning: "none",
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
	}, func(string) {}); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking should be an object, got %#v", body["thinking"])
	}
	if thinking["type"] != "disabled" {
		t.Errorf("thinking.type = %#v, want disabled", thinking["type"])
	}
	if _, ok := body["effort"]; ok {
		t.Error("effort must not be sent with thinking off: effort has no none value")
	}
}

// For the OpenAI-compatible layer, none is simply a reasoning_effort value (measurably effective on
// deepseek).
func TestOpenAISendsNoneAsEffort(t *testing.T) {
	var body map[string]any
	srv := captureStream(t, &body)
	defer srv.Close()

	c, err := NewOpenAI(config.Provider{Type: "openai", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	if _, err := c.Stream(context.Background(), Request{
		Model:     "deepseek-flash",
		Reasoning: "none",
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
	}, func(string) {}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if body["reasoning_effort"] != "none" {
		t.Errorf("reasoning_effort = %#v, want none", body["reasoning_effort"])
	}
}
