package config

import (
	"sort"
	"strings"
)

// MCPServer is one MCP server.
//
// Only how to reach it is stored: command and args (stdio), or a URL (streamable HTTP). The tool
// list is not persisted — that is the server's business, and a cache would point at tools that no
// longer exist after a package update.
type MCPServer struct {
	// Label is the name in the list and in tool descriptions; id when empty.
	Label string `json:"label,omitempty"`
	// URL means streamable HTTP (remote MCP). Set means Command is not run.
	URL string `json:"url,omitempty"`
	// Command is the executable (npx, uvx, /usr/bin/foo). stdio only.
	// omitempty: a remote server has no command, and "command": "" is just noise.
	Command string `json:"command,omitempty"`
	// Args are its arguments.
	Args []string `json:"args,omitempty"`
	// Env is extra environment variables (GITHUB_TOKEN, say), layered over the daemon's.
	Env map[string]string `json:"env,omitempty"`
	// Disabled true means it does not start.
	//
	// Default-enabled rather than Enabled, so a hand-written entry works without remembering to add
	// true. The UI only ever writes disabled.
	Disabled bool `json:"disabled,omitempty"`
	// AutoApprove lets this server's tools run without asking each time.
	//
	// Off by default: an MCP server is an arbitrary program, and the daemon cannot tell whether it will
	// touch something it should not. Turn it on only when you are sure.
	AutoApprove bool `json:"autoApprove,omitempty"`
	// IncludeTools keeps only these tools (empty keeps all).
	//
	// It exists because a server like playwright brings twenty-odd tools, and stuffing all of them into
	// the context to use four is waste. Configurable here because different people connect different
	// servers.
	IncludeTools []string `json:"includeTools,omitempty"`
	// ExcludeTools drops these. With IncludeTools both apply (keep the list, then subtract).
	ExcludeTools []string `json:"excludeTools,omitempty"`
}

// Enabled reports whether this server should start.
func (s MCPServer) Enabled() bool { return !s.Disabled }

// Configured reports whether it knows how to connect.
//
// An entry with only a label and no command/url is half-built (a UI mid-save) and does not belong
// in the list.
func (s MCPServer) Configured() bool {
	return strings.TrimSpace(s.URL) != "" || strings.TrimSpace(s.Command) != ""
}

// Keeps reports whether a tool name may be shown to the model.
func (s MCPServer) Keeps(name string) bool {
	if len(s.IncludeTools) > 0 && !containsName(s.IncludeTools, name) {
		return false
	}
	return !containsName(s.ExcludeTools, name)
}

func containsName(list []string, name string) bool {
	for _, v := range list {
		if v == name {
			return true
		}
	}
	return false
}

// MCPIDs returns every MCP server id, sorted.
//
// Sorting is not cosmetic: start order, the UI list and tests need to be stable, and map iteration
// is not.
func (c *Config) MCPIDs() []string {
	if c == nil {
		return nil
	}
	ids := make([]string, 0, len(c.MCPServers))
	for id, s := range c.MCPServers {
		if s.Configured() {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// EnabledMCPIDs returns only the enabled ones.
func (c *Config) EnabledMCPIDs() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.MCPServers))
	for _, id := range c.MCPIDs() {
		if c.MCPServers[id].Enabled() {
			out = append(out, id)
		}
	}
	return out
}
