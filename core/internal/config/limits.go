package config

import "strings"

// Model limits: how much a model can read, and how much it can write in one reply.
//
// Both matter, for different reasons. A context window that is too small only costs an early
// compaction; an output ceiling that is too *large* is a 400 that fails the whole turn, because
// providers reject max_tokens above the model's maximum instead of clamping it.
//
// That asymmetry decides what goes in this table:
//
//   - the context window is listed where it is published and stable — being wrong there is visible
//     and recoverable (an early compaction, or an overflow with a clear message)
//   - an output ceiling is listed only where it has been measured against the live API, because a
//     guess that is too low silently truncates replies and a guess that is too high breaks the turn
//   - anything set in config.json wins, and a provider that reports the numbers itself (DeepSeek,
//     OpenRouter, Gemini) fills them in when its model list is fetched
//
// Measured on api.deepseek.com in 2026-11 (GET /v1/models):
//
//	deepseek-flash, deepseek-v4-pro → context_window 1048576, max_output_tokens 393216
//
// and confirmed by the error a larger value earns: "the valid range of max_tokens is [1, 393216]".
func ModelLimits(p Provider, model string) (context, maxOutput int) {
	m := normalizeModelID(model)
	if m == "" {
		return 0, 0
	}

	switch {
	case isAnthropicProvider(p):
		if _, _, ok := dottedVersion(strings.TrimPrefix(m, "claude-")); ok {
			return 200000, 0
		}
	case strings.HasPrefix(m, "gemini-"):
		return 1048576, 0
	case strings.HasPrefix(m, "gpt-6"), strings.HasPrefix(m, "gpt-5"):
		return 400000, 0
	case strings.HasPrefix(m, "grok-"):
		return 256000, 0
	case strings.HasPrefix(m, "deepseek-"):
		return 1048576, 393216
	}
	return 0, 0
}

// withLimits fills the limits this model's config leaves empty.
func (m Resolved) withLimits(p Provider) Resolved {
	if m.Context > 0 && m.MaxOutput > 0 {
		return m
	}
	context, maxOutput := ModelLimits(p, m.Model)
	if m.Context == 0 {
		m.Context = context
	}
	if m.MaxOutput == 0 {
		m.MaxOutput = maxOutput
	}
	return m
}
