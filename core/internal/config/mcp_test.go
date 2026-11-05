package config

import "testing"

// TestMCPServerEnabledByDefault: a hand-written entry should not have to remember "enabled": true.
func TestMCPServerEnabledByDefault(t *testing.T) {
	cfg := &Config{MCPServers: map[string]MCPServer{
		"a": {Command: "npx"},
		"b": {Command: "npx", Disabled: true},
		"c": {Disabled: true}, // no command: does not count
	}}
	ids := cfg.MCPIDs()
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("wrong MCPIDs: %v", ids)
	}
	enabled := cfg.EnabledMCPIDs()
	if len(enabled) != 1 || enabled[0] != "a" {
		t.Fatalf("should default to enabled, got %v", enabled)
	}
}

// A remote server has only a url and no command — and is still "configured". That line decides
// whether it appears in the list and whether Sync starts it.
func TestMCPServerConfiguredByURL(t *testing.T) {
	cfg := &Config{MCPServers: map[string]MCPServer{
		"local":  {Command: "npx"},
		"remote": {URL: "https://mcp.example.com/mcp"},
		"blank":  {Label: "not filled in yet"},
	}}
	ids := cfg.MCPIDs()
	if len(ids) != 2 || ids[0] != "local" || ids[1] != "remote" {
		t.Fatalf("wrong MCPIDs: %v", ids)
	}
}

// includeTools is an allowlist, excludeTools a denylist; with both, keep first, then subtract.
func TestMCPServerKeepsTools(t *testing.T) {
	cases := []struct {
		name string
		spec MCPServer
		want map[string]bool
	}{
		{
			name: "nothing listed keeps everything",
			spec: MCPServer{Command: "x"},
			want: map[string]bool{"a": true, "b": true},
		},
		{
			name: "allowlist keeps only what is listed",
			spec: MCPServer{Command: "x", IncludeTools: []string{"a"}},
			want: map[string]bool{"a": true, "b": false},
		},
		{
			name: "denylist drops what is listed",
			spec: MCPServer{Command: "x", ExcludeTools: []string{"b"}},
			want: map[string]bool{"a": true, "b": false},
		},
		{
			name: "both set: allowlist first, then denylist",
			spec: MCPServer{Command: "x", IncludeTools: []string{"a", "b"}, ExcludeTools: []string{"b"}},
			want: map[string]bool{"a": true, "b": false},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for name, want := range c.want {
				if got := c.spec.Keeps(name); got != want {
					t.Errorf("Keeps(%q) = %v, want %v", name, got, want)
				}
			}
		})
	}
}
