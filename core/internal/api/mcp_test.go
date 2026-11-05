package api

import (
	"testing"

	"github.com/NekoLOvO/dux-code/core/internal/config"
)

// The id becomes part of tool names (mcp__<id>__<tool>), so it must be ASCII. A remote server has
// no command to name it after, so it falls back to the url host.
func TestMCPIDHint(t *testing.T) {
	cases := []struct {
		command string
		url     string
		want    string
	}{
		{command: "npx", url: "", want: "npx"},
		{command: "/usr/local/bin/uvx", url: "", want: "/usr/local/bin/uvx"},
		{command: "", url: "https://mcp.example.com/mcp", want: "mcp.example.com"},
		{command: "", url: "http://localhost:8080/sse", want: "localhost:8080"},
		// With both, the command wins (saving clears command when url is set; this is only a guard).
		{command: "npx", url: "https://x.test", want: "npx"},
	}
	for _, c := range cases {
		if got := mcpIDHint(c.command, c.url); got != c.want {
			t.Errorf("mcpIDHint(%q, %q) = %q, want %q", c.command, c.url, got, c.want)
		}
	}
}

func TestNewMCPIDFallsBackToURLHost(t *testing.T) {
	cfg := &config.Config{MCPServers: map[string]config.MCPServer{}}
	id := newMCPID(cfg, "", mcpIDHint("", "https://mcp.example.com/mcp"))
	if id != "mcp-example-com" {
		t.Errorf("id = %q, want mcp-example-com", id)
	}

	// Adding another with the same name must increment instead of overwriting the first.
	cfg.MCPServers[id] = config.MCPServer{URL: "https://mcp.example.com/mcp"}
	if got := newMCPID(cfg, "", mcpIDHint("", "https://mcp.example.com/mcp")); got != "mcp-example-com-2" {
		t.Errorf("the second id = %q, want mcp-example-com-2", got)
	}
}

func TestCleanNames(t *testing.T) {
	got := cleanNames([]string{" echo ", "", "   ", "fail"})
	if len(got) != 2 || got[0] != "echo" || got[1] != "fail" {
		t.Errorf("cleanNames = %#v", got)
	}
	if got := cleanNames(nil); got != nil {
		t.Errorf("an empty list should be nil so the old settings are kept, got %#v", got)
	}
}

// The browser has no switch of its own: it shares the web toggle with search and page fetch, so
// configView must not report enabled — a UI still reading it would see a permanent true.
func TestBrowserViewHasNoOwnToggle(t *testing.T) {
	cfg := &config.Config{}
	v := browserView(cfg)

	if _, ok := v["enabled"]; ok {
		t.Error("browser must not have its own toggle")
	}
	if _, ok := v["path"]; !ok {
		t.Error("the path must survive: it is the only thing the settings page can change")
	}
	if _, ok := v["reason"]; !ok {
		t.Error("when it cannot be found it must say why")
	}
}
