package config

import (
	"strings"
	"testing"
)

// Every entry in the built-in table is a guess, so the guesses are pinned here: changing one changes
// what is sent to the API, which should not happen by accident.
func TestReasoningLevelsBuiltin(t *testing.T) {
	openai := Provider{Type: "openai", BaseURL: "https://api.openai.com/v1"}
	deepseek := Provider{Type: "openai", BaseURL: "https://api.deepseek.com/v1"}
	gemini := Provider{Type: "openai", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai"}
	anthropic := Provider{Type: "anthropic", BaseURL: "https://api.anthropic.com/v1"}
	openrouter := Provider{Type: "openai", BaseURL: "https://openrouter.ai/api/v1"}

	cases := []struct {
		name  string
		p     Provider
		model string
		want  []string
	}{
		// deepseek: three levels on the v4 family; reasoner always thinks and cannot be adjusted.
		{"deepseek-flash", deepseek, "deepseek-flash", []string{"none", "low", "high", "max"}},
		{"deepseek-v4-pro", deepseek, "deepseek-v4-pro", []string{"none", "low", "high", "max"}},
		{"deepseek-reasoner unsupported", deepseek, "deepseek-reasoner", nil},
		{"deepseek-chat unsupported", deepseek, "deepseek-chat", nil},

		// gpt-5 has minimal, but xhigh / max are not certain.
		{"gpt-5", openai, "gpt-5-nano", []string{"none", "minimal", "low", "medium", "high"}},
		{"gpt-6 every level", openai, "gpt-6-luna", []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}},
		{"o series has no minimal", openai, "o3-mini", []string{"none", "low", "medium", "high"}},

		// Gemini: 4 dropped MINIMAL, 3 has it.
		{"gemini-4", gemini, "gemini-4-argon", []string{"low", "medium", "high"}},
		{"gemini-3", gemini, "gemini-3-flash", []string{"minimal", "low", "medium", "high"}},
		{"gemini-2", gemini, "gemini-2.5-pro", []string{"low", "medium", "high"}},

		// Claude: adaptive + effort from 4.6; earlier versions only had budget_tokens.
		{"claude-4-6 has no xhigh", anthropic, "claude-opus-4-6", []string{"none", "low", "medium", "high", "max"}},
		{"claude-4-7 has xhigh", anthropic, "claude-opus-4-7", []string{"none", "low", "medium", "high", "xhigh", "max"}},
		{"claude-5", anthropic, "claude-opus-5-5", []string{"none", "low", "medium", "high", "xhigh", "max"}},
		{"claude-4-5 unsupported", anthropic, "claude-sonnet-4-5", nil},
		{"claude-3-7 unsupported", anthropic, "claude-3-7-sonnet-20250219", nil},
		// A dated id must not be read as a version number.
		{"claude date suffix", anthropic, "claude-sonnet-4-20250514", nil},

		// Grok: xhigh from 4.6.
		{"grok-4.7", openai, "grok-4.7", []string{"low", "medium", "high", "xhigh"}},
		{"grok-4.5", openai, "grok-4.5", []string{"low", "medium", "high"}},
		{"grok-3-mini unsupported", openai, "grok-3-mini", nil},

		// OpenRouter normalises itself, whoever sits underneath.
		{"openrouter", openrouter, "whatever/model", []string{"none", "minimal", "low", "medium", "high", "xhigh"}},

		// An unknown family gets nothing (better too few).
		{"unknown model", openai, "some-local-llm", nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ReasoningLevels(c.p, c.model, nil)
			if !equalLevels(got, c.want) {
				t.Errorf("ReasoningLevels(%q) = %v, want %v", c.model, got, c.want)
			}
		})
	}
}

// The three override meanings: nil uses the built-in table, an empty array turns it off, a list
// means exactly those.
func TestReasoningOverride(t *testing.T) {
	p := Provider{Type: "openai", BaseURL: "https://api.deepseek.com/v1"}

	if got := ReasoningLevels(p, "deepseek-flash", nil); len(got) == 0 {
		t.Fatal("nil should fall back to the built-in table")
	}
	if got := ReasoningLevels(p, "deepseek-flash", []string{}); len(got) != 0 {
		t.Errorf("an empty list should mean unsupported, got %v", got)
	}
	// Ordered by strength, unreadable values dropped.
	got := ReasoningLevels(p, "deepseek-flash", []string{"high", "bogus", "low", "HIGH"})
	if !equalLevels(got, []string{"low", "high"}) {
		t.Errorf("override = %v, want [low high]", got)
	}
	// A model without a built-in guess can still be enabled through an override.
	if got := ReasoningLevels(p, "some-local-llm", []string{"medium"}); !equalLevels(got, []string{"medium"}) {
		t.Errorf("an override should enable an unknown model, got %v", got)
	}
}

func TestNormalizeReasoning(t *testing.T) {
	supported := []string{"low", "medium", "high"}

	cases := []struct {
		level string
		want  string
	}{
		{"", ""},
		{"auto", ""},
		{"AUTO", ""},
		{" high ", "high"},
		{"minimal", ""}, // this model has no such level
		{"xhigh", ""},
		{"bogus", ""},
	}
	for _, c := range cases {
		if got := NormalizeReasoning(c.level, supported); got != c.want {
			t.Errorf("NormalizeReasoning(%q) = %q, want %q", c.level, got, c.want)
		}
	}

	// Without a support list nothing may be sent.
	if got := NormalizeReasoning("high", nil); got != "" {
		t.Errorf("unsupported models must not send a level, got %q", got)
	}
}

func equalLevels(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Print a readable diff on failure (otherwise all you get is []string addresses).
func TestReasoningLevelsAreLowercase(t *testing.T) {
	// The sent value is a string the API accepts; uppercase is a 400, so the table must stay lowercase.
	models := []string{
		"gpt-6", "gpt-5", "o3", "gemini-4", "gemini-3", "gemini-2",
		"grok-4.7", "deepseek-flash", "claude-opus-4-6",
	}
	for _, m := range models {
		p := Provider{Type: "openai", BaseURL: "https://api.example.com/v1"}
		if strings.HasPrefix(m, "claude-") {
			p = Provider{Type: "anthropic"}
		}
		for _, lv := range ReasoningLevels(p, m, nil) {
			if lv != strings.ToLower(lv) {
				t.Errorf("level %q of %s is not lowercase", lv, m)
			}
		}
	}
}
