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

// A token is write-only in both directions: the form sends the key names it drew, an empty value
// means "keep what is stored", and a key it no longer sends is gone.
func TestMergeSecrets(t *testing.T) {
	base := map[string]string{
		"Authorization": "Bearer old",
		"X-Gone":        "bye",
	}

	// X-Gone is absent on purpose: a key the form stops sending is a key the user deleted.
	got := mergeSecrets(base, map[string]any{
		"Authorization": "",        // empty keeps the stored value
		" X-New ":       " value ", // trimmed both ways
		"X-Empty":       "",        // a new key with no value is not a key
	})

	if got["Authorization"] != "Bearer old" {
		t.Errorf("an empty value must keep the stored one, got %q", got["Authorization"])
	}
	if got["X-New"] != "value" {
		t.Errorf("keys and values must be trimmed, got %q", got["X-New"])
	}
	if _, ok := got["X-Empty"]; ok {
		t.Error("a new key with no value must be dropped")
	}
	if _, ok := got["X-Gone"]; ok {
		t.Error("a key the form did not send must be deleted")
	}

	// A form that sends nothing at all is "no change", not "delete everything".
	if keep := mergeSecrets(base, nil); len(keep) != 2 {
		t.Errorf("a missing map must keep the stored one, got %#v", keep)
	}
}

// The view names the headers and never carries their values: a token that reaches the UI ends up in
// logs and screenshots, and the form has no use for it.
func TestMCPViewReportsHeaderKeysOnly(t *testing.T) {
	cfg := &config.Config{MCPServers: map[string]config.MCPServer{
		"gh": {
			URL:     "https://api.githubcopilot.com/mcp/",
			Headers: map[string]string{"Authorization": "Bearer s3cret"},
		},
	}}

	view := mcpView(Deps{Config: cfg})
	servers, _ := view["servers"].([]map[string]any)
	if len(servers) != 1 {
		t.Fatalf("expected one server, got %#v", view["servers"])
	}
	keys, _ := servers[0]["headerKeys"].([]string)
	if len(keys) != 1 || keys[0] != "Authorization" {
		t.Fatalf("headerKeys = %#v, want [Authorization]", servers[0]["headerKeys"])
	}
	for k, v := range servers[0] {
		if s, ok := v.(string); ok && s == "Bearer s3cret" {
			t.Fatalf("%q leaks the token", k)
		}
	}
}
