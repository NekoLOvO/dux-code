package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Reasoning levels. This is our vocabulary, not any vendor's.
//
// Every vendor's parameter looks different and is shaped differently — the OpenAI-compatible layer
// takes a reasoning_effort string, Anthropic wants a thinking object plus a top-level effort. The
// translation lives in each client (the llm package); above that only these strings exist.
const (
	ReasoningNone    = "none"
	ReasoningMinimal = "minimal"
	ReasoningLow     = "low"
	ReasoningMedium  = "medium"
	ReasoningHigh    = "high"
	ReasoningXHigh   = "xhigh"
	ReasoningMax     = "max"
)

// reasoningOrder runs shallow to deep. Display and validation follow it, so the list never jumps.
var reasoningOrder = []string{
	ReasoningNone,
	ReasoningMinimal,
	ReasoningLow,
	ReasoningMedium,
	ReasoningHigh,
	ReasoningXHigh,
	ReasoningMax,
}

// ReasoningLevels returns the levels a model supports. Empty means none, and the UI hides the
// menu entirely.
//
// This table is a guess and can only ever be one: the OpenAI-compatible /models endpoint does not
// say (only OpenRouter returns supported_parameters). The rules are
//
//   - list only families we are sure about, better too few
//   - leave anything uncertain empty (= hidden) rather than listing it and hoping
//
// A wrong guess is concrete: the user picks a level, sends, the API returns 400 and the
// conversation stops. Leaving it empty only means "this model has no such option".
//
// The model's own reasoning field overrides the table:
//
//	absent (nil)      use the built-in guess
//	[]                explicitly unsupported (turn the guess off)
//	["low", ...]      exactly these
//
// Overridable because the table is a guess, and a wrong guess has to be reversible by the user
// rather than by editing code.
func ReasoningLevels(p Provider, model string, override []string) []string {
	if override != nil {
		return normalizeLevels(override)
	}

	m := normalizeModelID(model)
	if m == "" {
		return nil
	}

	// OpenRouter normalises this itself: whatever sits underneath, it accepts these six and converts
	// them to that vendor's effort or budget.
	if strings.Contains(strings.ToLower(p.BaseURL), "openrouter.ai") {
		return []string{
			ReasoningNone, ReasoningMinimal, ReasoningLow,
			ReasoningMedium, ReasoningHigh, ReasoningXHigh,
		}
	}

	if isAnthropicProvider(p) {
		return claudeLevels(m)
	}

	// Otherwise it is all in the name. Order matters: gpt-6 must be matched before gpt-5, or the
	// prefixes swallow each other.
	switch {
	case strings.HasPrefix(m, "gemini-"):
		return geminiLevels(m)
	case strings.HasPrefix(m, "gpt-6"):
		return []string{
			ReasoningNone, ReasoningMinimal, ReasoningLow,
			ReasoningMedium, ReasoningHigh, ReasoningXHigh, ReasoningMax,
		}
	case strings.HasPrefix(m, "gpt-5"):
		// gpt-5 has minimal and also takes none (thinking off). Both come from OpenAI's generic values and
		// are untested; xhigh / max are not certain, so they are not listed.
		return []string{ReasoningNone, ReasoningMinimal, ReasoningLow, ReasoningMedium, ReasoningHigh}
	case isOSeries(m):
		return []string{ReasoningNone, ReasoningLow, ReasoningMedium, ReasoningHigh}
	case strings.HasPrefix(m, "grok-"):
		return grokLevels(m)
	case strings.HasPrefix(m, "deepseek-"):
		return deepseekLevels(m)
	}

	return nil
}

// geminiLevels: Gemini's native thinkingLevel has four steps and Gemini 4 dropped MINIMAL. These are
// our names; the client converts to the uppercase MINIMAL / LOW / MEDIUM / HIGH it sends (see
// geminiThinkingLevel in gemini.go).
func geminiLevels(m string) []string {
	switch {
	case strings.HasPrefix(m, "gemini-4"):
		return []string{ReasoningLow, ReasoningMedium, ReasoningHigh}
	case strings.HasPrefix(m, "gemini-3"):
		return []string{ReasoningMinimal, ReasoningLow, ReasoningMedium, ReasoningHigh}
	case strings.HasPrefix(m, "gemini-2"):
		return []string{ReasoningLow, ReasoningMedium, ReasoningHigh}
	}
	return nil
}

// claudeLevels: adaptive thinking plus effort arrived in 4.6. Earlier versions only had
// budget_tokens (a budget, not a level), so they count as unsupported here — showing low/medium/high
// and then sending a field an old model does not understand is worse than showing nothing.
//
// Turning thinking off (none) goes through thinking:{type:"disabled"}, unrelated to effort, and is
// translated by the client; effort has no "none" value.
func claudeLevels(m string) []string {
	major, minor, ok := dottedVersion(strings.TrimPrefix(m, "claude-"))
	if !ok {
		return nil
	}
	switch {
	case major >= 5:
		return []string{ReasoningNone, ReasoningLow, ReasoningMedium, ReasoningHigh, ReasoningXHigh, ReasoningMax}
	case major == 4 && minor >= 7:
		// xhigh arrived in 4.7.
		return []string{ReasoningNone, ReasoningLow, ReasoningMedium, ReasoningHigh, ReasoningXHigh, ReasoningMax}
	case major == 4 && minor == 6:
		return []string{ReasoningNone, ReasoningLow, ReasoningMedium, ReasoningHigh, ReasoningMax}
	}
	return nil
}

// grokLevels: xhigh since 4.6, three levels on 4.5, a single intensity on 3 / 4.3 — that is not an
// optional level, so it is not listed.
func grokLevels(m string) []string {
	major, minor, ok := dottedVersion(strings.TrimPrefix(m, "grok-"))
	if !ok {
		return nil
	}
	switch {
	case major >= 5, major == 4 && minor >= 6:
		return []string{ReasoningLow, ReasoningMedium, ReasoningHigh, ReasoningXHigh}
	case major == 4 && minor == 5:
		return []string{ReasoningLow, ReasoningMedium, ReasoningHigh}
	}
	return nil
}

// deepseekLevels: the v4 family (including the deepseek-flash alias) takes reasoning_effort.
// deepseek-reasoner always thinks and cannot be adjusted, so it is not listed.
//
// Measured against api.deepseek.com in 2026-11:
//
//	absent                → 200, reasoning_content present (on by default)
//	none                  → 200, no reasoning_content (really off)
//	minimal               → 200 but no visible difference, so unlisted (accepted ≠ effective)
//	enable_thinking:false → 200 but ignored, still thinks (that route is unusable)
//
// So "thinking off" on deepseek is reasoning_effort:"none"; no separate endpoint needed.

func deepseekLevels(m string) []string {
	if strings.Contains(m, "reasoner") {
		return nil
	}
	if m == "deepseek-flash" || strings.HasPrefix(m, "deepseek-v4") {
		return []string{ReasoningNone, ReasoningLow, ReasoningHigh, ReasoningMax}
	}
	return nil
}

// normalizeModelID strips a prefix like "openai/gpt-5" down to the model name.
func normalizeModelID(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return m
}

// dottedVersion picks the first version number in the name, handling claude-opus-4-7 (dashes) and
// grok-4.7 (a dot). A major version alone (claude-opus-5) gives minor 0.
//
// Four-digit numbers are skipped: those are dates (claude-sonnet-4-20250514).
func dottedVersion(rest string) (int, int, bool) {
	parts := strings.FieldsFunc(rest, func(r rune) bool {
		return r == '-' || r == '.' || r == '_' || r == ':'
	})
	for i := 0; i < len(parts); i++ {
		a, err := strconv.Atoi(parts[i])
		if err != nil || a < 1 || a > 99 {
			continue
		}
		if i+1 < len(parts) {
			if b, err := strconv.Atoi(parts[i+1]); err == nil && b >= 0 && b <= 99 {
				return a, b, true
			}
		}
		return a, 0, true
	}
	return 0, 0, false
}

// isAnthropicProvider: Type is explicit in the config, BaseURL is for older configs without one.
func isAnthropicProvider(p Provider) bool {
	if strings.EqualFold(strings.TrimSpace(p.Type), "anthropic") {
		return true
	}
	return strings.Contains(strings.ToLower(p.BaseURL), "anthropic.com")
}

func isOSeries(m string) bool {
	for _, p := range []string{"o1", "o3", "o4"} {
		if m == p || strings.HasPrefix(m, p+"-") || strings.HasPrefix(m, p+":") {
			return true
		}
	}
	return false
}

// normalizeLevels keeps only values it understands, ordered by strength (input order is not
// trusted).
func normalizeLevels(in []string) []string {
	seen := make(map[string]bool, len(in))
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			continue
		}
		for _, ok := range reasoningOrder {
			if v == ok {
				seen[v] = true
				break
			}
		}
	}
	out := make([]string, 0, len(seen))
	for _, v := range reasoningOrder {
		if seen[v] {
			out = append(out, v)
		}
	}
	return out
}

// NormalizeReasoning turns the user's choice into the value to send. "auto", an empty string, or
// anything this model does not support all become empty (= send nothing).
//
// "Send nothing" is the only always-safe option, so every unreadable input falls back to it. The
// UI already validates; this is a second gate because chat.send is an external interface and should
// not assume well-behaved callers.
func NormalizeReasoning(level string, supported []string) string {
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" || level == "auto" {
		return ""
	}
	for _, s := range supported {
		if s == level {
			return level
		}
	}
	return ""
}

// ReasoningSummary is for logs: why this turn carried no parameter, at a glance.
func ReasoningSummary(level string, supported []string) string {
	if level == "" || level == "auto" {
		return fmt.Sprintf("auto (%d levels)", len(supported))
	}
	if len(supported) == 0 {
		return level + " (not supported by the model, ignored)"
	}
	return level
}
