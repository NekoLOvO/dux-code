package agent

import (
	"testing"

	"github.com/NekoLOvO/dux-code/core/internal/config"
)

// Built-in search exists in only three wire formats; compatible endpoints (/chat/completions) do not,
// so deepseek / openrouter / Ollama can never use it.
func TestNativeSearchFor(t *testing.T) {
	off := false
	on := true

	cases := []struct {
		name string
		p    config.Provider
		want bool
	}{
		{"gemini defaults to on", config.Provider{Type: "gemini"}, true},
		{"responses defaults to on", config.Provider{Type: "openai-responses"}, true},
		{"anthropic defaults to on", config.Provider{Type: "anthropic"}, true},
		{"compat endpoint has none", config.Provider{Type: "openai"}, false},
		{"empty type counts as a compat endpoint", config.Provider{}, false},
		{"explicitly off", config.Provider{Type: "gemini", NativeSearch: &off}, false},
		// Even forced on, an unsupported endpoint must not be sent it — that is only a 400.
		{"forcing it on an unsupported endpoint does nothing", config.Provider{Type: "openai", NativeSearch: &on}, false},
	}

	for _, c := range cases {
		if got := nativeSearchFor(c.p); got != c.want {
			t.Errorf("%s: nativeSearchFor = %v, want %v", c.name, got, c.want)
		}
	}
}
