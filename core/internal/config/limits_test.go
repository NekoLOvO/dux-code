package config

import "testing"

// TestModelLimits: the table answers only what it can defend — windows everywhere, output ceilings
// only where they were measured. A guessed ceiling is worse than none: too low truncates replies
// silently, too high fails the turn outright.
func TestModelLimits(t *testing.T) {
	ds := Provider{Type: "openai", BaseURL: "https://api.deepseek.com/v1"}
	anthropic := Provider{Type: "anthropic", BaseURL: "https://api.anthropic.com"}

	cases := []struct {
		name    string
		p       Provider
		model   string
		context int
		output  int
	}{
		{"deepseek was measured", ds, "deepseek-flash", 1048576, 393216},
		{"claude window only", anthropic, "claude-opus-5-5", 200000, 0},
		{"gemini window only", ds, "gemini-4-pro", 1048576, 0},
		{"gpt window only", ds, "gpt-6.1", 400000, 0},
		{"grok window only", ds, "grok-5", 256000, 0},
		{"unknown stays unknown", ds, "some-local-model", 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			context, output := ModelLimits(c.p, c.model)
			if context != c.context || output != c.output {
				t.Errorf("ModelLimits(%q) = %d/%d, want %d/%d", c.model, context, output, c.context, c.output)
			}
		})
	}
}

// TestConfigLimitsWin: a number in config.json is a correction of the table, not a suggestion. This is
// also the path a fetched model list takes — the provider's own numbers land in the entry.
func TestConfigLimitsWin(t *testing.T) {
	p := Provider{
		Type:    "openai",
		BaseURL: "https://api.deepseek.com/v1",
		Models: map[string]ModelEntry{
			"deepseek-flash":  {Enabled: true, Context: 64000, MaxOutput: 4096},
			"deepseek-v4-pro": {Enabled: true},
		},
	}
	cfg := &Config{Providers: map[string]Provider{"deepseek": p}}

	got, _, ok := cfg.Resolve("deepseek:deepseek-flash")
	if !ok {
		t.Fatal("the model should resolve")
	}
	if got.Context != 64000 || got.MaxOutput != 4096 {
		t.Errorf("config values were overwritten: %+v", got)
	}

	filled, _, ok := cfg.Resolve("deepseek:deepseek-v4-pro")
	if !ok {
		t.Fatal("the model should resolve")
	}
	if filled.Context != 1048576 || filled.MaxOutput != 393216 {
		t.Errorf("an empty entry should take the table's numbers: %+v", filled)
	}
}
