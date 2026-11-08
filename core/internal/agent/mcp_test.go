package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekoLOvO/dux-code/core/internal/applog"
	"github.com/NekoLOvO/dux-code/core/internal/config"
	"github.com/NekoLOvO/dux-code/core/internal/ipc"
	"github.com/NekoLOvO/dux-code/core/internal/mcp"
	"github.com/NekoLOvO/dux-code/core/internal/tools"
)

// TestMCPHelperProcess is the one launched as an MCP server.
//
// It is written here rather than shared with the mcp package's test: that TestHelperProcess only
// exists in the mcp test binary, and other packages cannot reach it.
func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_AGENT_HELPER") != "1" {
		return
	}

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64<<10), 8<<20)
	out := json.NewEncoder(os.Stdout)

	for in.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(in.Bytes(), &msg); err != nil {
			continue
		}
		switch msg.Method {
		case "initialize":
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]string{"name": "fake", "version": "0"},
			}})
		case "tools/list":
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"tools": []any{
				map[string]any{
					"name":        "read_note",
					"description": "read a note",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"id": map[string]any{"type": "string"}},
					},
				},
			}}})
		case "tools/call":
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "note: buy milk"}},
			}})
		default:
			if len(msg.ID) > 0 {
				_ = out.Encode(map[string]any{
					"jsonrpc": "2.0", "id": msg.ID,
					"error": map[string]any{"code": -32601, "message": "nope"},
				})
			}
		}
	}
	os.Exit(0)
}

type recordingApprover struct {
	calls []tools.Approval
	allow bool
}

func (a *recordingApprover) Ask(_ context.Context, req tools.Approval) (bool, error) {
	a.calls = append(a.calls, req)
	return a.allow, nil
}

func startAgentMCP(t *testing.T, cfg *config.Config) *mcp.Manager {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = map[string]config.MCPServer{}
	}
	cfg.MCPServers["fake"] = config.MCPServer{
		Command: exe,
		Args:    []string{"-test.run=TestMCPHelperProcess", "--"},
		Env:     map[string]string{"GO_WANT_MCP_AGENT_HELPER": "1"},
	}

	mgr := mcp.New("test")
	t.Cleanup(mgr.Close)
	mgr.Sync(cfg)

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if len(mgr.Tools()) > 0 {
			return mgr
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the MCP server did not start: %+v", mgr.Status("fake"))
	return nil
}

// TestMCPToolsReachTheModel: MCP tools must appear in the list given to the model, and their names
// must convey that they are external.
func TestMCPToolsReachTheModel(t *testing.T) {
	applog.SetLevel(applog.LevelError)
	cfg := &config.Config{}
	mgr := startAgentMCP(t, cfg)

	r := New(ipc.NewEventBus(), nil, cfg)
	r.SetMCP(mgr)

	var found bool
	for _, s := range r.allowedSpecs(&run{}, cfg) {
		if s.Function.Name != "mcp__fake__read_note" {
			continue
		}
		found = true
		if !strings.HasPrefix(s.Function.Description, "[fake]") {
			t.Errorf("the description must name the server: %q", s.Function.Description)
		}
		if s.Function.Parameters == nil {
			t.Error("the parameter schema was not carried over")
		}
	}
	if !found {
		t.Fatal("mcp__fake__read_note is missing from the list")
	}
}

// TestMCPToolAsksBeforeRunning: an MCP server is an arbitrary program, so asking is the default.
func TestMCPToolAsksBeforeRunning(t *testing.T) {
	applog.SetLevel(applog.LevelError)
	cfg := &config.Config{}
	mgr := startAgentMCP(t, cfg)

	r := New(ipc.NewEventBus(), nil, cfg)
	r.SetMCP(mgr)

	approver := &recordingApprover{allow: true}
	ctx := tools.WithEnv(context.Background(), tools.Env{
		Root:     t.TempDir(),
		Approval: tools.ApprovalAsk,
		Approve:  approver,
	})

	out := r.runTool(ctx, "mcp__fake__read_note", `{"id":"n1"}`)
	if !strings.Contains(out, "buy milk") {
		t.Fatalf("got no response from the server: %q", out)
	}
	if len(approver.calls) != 1 {
		t.Fatalf("should ask once, asked %d times", len(approver.calls))
	}
	if got := approver.calls[0]; got.Kind != "mcp" || !strings.Contains(got.Title, "read_note") {
		t.Errorf("wrong approval payload: %+v", got)
	}
	if !strings.Contains(approver.calls[0].Detail, `"id"`) {
		t.Errorf("the approval must show the arguments so the user knows what will happen: %q", approver.calls[0].Detail)
	}
}

// TestMCPToolDeniedWithoutApprover: no approval channel means refusal, not approval.
func TestMCPToolDeniedWithoutApprover(t *testing.T) {
	applog.SetLevel(applog.LevelError)
	cfg := &config.Config{}
	mgr := startAgentMCP(t, cfg)

	r := New(ipc.NewEventBus(), nil, cfg)
	r.SetMCP(mgr)

	ctx := tools.WithEnv(context.Background(), tools.Env{Root: t.TempDir(), Approval: tools.ApprovalAsk})
	out := r.runTool(ctx, "mcp__fake__read_note", `{}`)
	if !strings.Contains(out, "denied") {
		t.Fatalf("with no approver it should be denied, got: %q", out)
	}
}
