package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/NekoLOvO/dux-code/core/internal/llm"
)

// MCPTool is a tool that runs in another process.
//
// Only two things differ from a built-in: the arguments and results are the server's own JSON, and
// there is no way to judge whether it will touch something it should not — so it asks every time.
type MCPTool struct {
	// Server is the name from the settings (for humans).
	Server string
	// Name is the name the model sees (mcp__<server>__<tool>).
	Name string
	// Tool is the original name on the server.
	Tool string
	// Desc is a one-line description, prefixed with the server.
	Desc string
	// Schema is the server's inputSchema, forwarded to the model as-is.
	Schema json.RawMessage
	// AutoApprove skips the per-call question (the switch from settings).
	AutoApprove bool
	// Call actually sends it. Errors become strings the model can read.
	Call func(ctx context.Context, args string) (string, error)
}

// Spec implements Tool.
func (t MCPTool) Spec() llm.Tool {
	params := any(json.RawMessage(t.Schema))
	if len(t.Schema) == 0 {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	desc := t.Desc
	if strings.TrimSpace(desc) == "" {
		desc = "MCP tool: " + t.Tool
	}
	return llm.NewTool(t.Name, fmt.Sprintf("[%s] %s", t.Server, desc), params)
}

// Run implements Tool.
//
// An MCP server is an arbitrary program the user connected, and the daemon cannot tell what it does,
// so every call asks by default. Anyone wanting fewer clicks can enable autoApprove for one server in
// settings — that is their judgement about that server, not ours.
func (t MCPTool) Run(ctx context.Context, args string) (string, error) {
	env := EnvFrom(ctx)
	if !t.AutoApprove {
		if err := env.ask(ctx, Approval{
			Kind:   "mcp",
			Title:  t.Server + " · " + t.Tool,
			Detail: prettyArgs(args),
		}); err != nil {
			return "", err
		}
	}

	out, err := t.Call(ctx, args)
	if err != nil {
		return "", err
	}
	return TruncateForModel(out), nil
}

// prettyArgs formats the arguments readably. The user decides based on this, and one line of packed
// JSON hides the point.
func prettyArgs(args string) string {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" || trimmed == "{}" {
		return "(no arguments)"
	}
	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return trimmed
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return trimmed
	}
	return string(out)
}

// maxToolName is the function-name length limit most model APIs have.
const maxToolName = 64

var reInvalidToolName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// MCPToolName builds the name the model sees.
//
// The mcp__ prefix does two jobs: the user sees at a glance that it is external, and turning MCP off
// in settings can filter them in bulk. Two underscores separate the parts because server and tool
// names often contain single ones (read_file).
func MCPToolName(server, tool string) string {
	s := sanitizeToolName(server)
	t := sanitizeToolName(tool)
	head := "mcp__" + s + "__"
	room := maxToolName - len(head)
	if room < 1 {
		// The server name alone is too long: keep just it, so the model at least knows which machine.
		return truncateName(head, maxToolName)
	}
	return head + truncateName(t, room)
}

func sanitizeToolName(s string) string {
	s = strings.Trim(reInvalidToolName.ReplaceAllString(s, "_"), "_")
	if s == "" {
		s = "server"
	}
	return s
}

func truncateName(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimRight(s[:n], "_")
}
