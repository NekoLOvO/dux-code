package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NekoLOvO/dux-code/core/internal/config"
)

// TestHelperProcess is not a real test: it only does work when launched as an MCP server.
//
// That avoids shipping a separate fake-server binary in the repo and avoids installing npx.
// os.Args[0] is the test binary itself.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") != "1" {
		return
	}

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64<<10), 32<<20)
	out := json.NewEncoder(os.Stdout)

	reply := func(id json.RawMessage, result any) {
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}

	for in.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(in.Bytes(), &msg); err != nil {
			continue
		}

		switch msg.Method {
		case "initialize":
			reply(msg.ID, map[string]any{
				// The SDK negotiates the version during the handshake; declare an older one it knows.
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]string{"name": "fake", "version": "0"},
			})
		case "notifications/initialized":
			// A notification: no reply.
		case "tools/list":
			reply(msg.ID, map[string]any{"tools": []any{
				map[string]any{
					"name":        "echo",
					"description": "echo the input back",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"text": map[string]any{"type": "string"}},
					},
				},
				map[string]any{
					"name":        "fail",
					"description": "always fails",
				},
			}})
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if p.Name == "fail" {
				reply(msg.ID, map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "it broke"}},
					"isError": true,
				})
				continue
			}
			text, _ := p.Arguments["text"].(string)
			reply(msg.ID, map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "echo: " + text}},
			})
		default:
			if len(msg.ID) > 0 {
				_ = out.Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      msg.ID,
					"error":   map[string]any{"code": -32601, "message": "nope"},
				})
			}
		}
	}
	os.Exit(0)
}

func helperSpec(t *testing.T) config.MCPServer {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("cannot find the test executable: %v", err)
	}
	return config.MCPServer{
		Command: exe,
		Args:    []string{"-test.run=TestHelperProcess", "--"},
		Env:     map[string]string{"GO_WANT_MCP_HELPER": "1"},
	}
}

func TestClientHandshakeAndCallTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c, err := Start(ctx, "fake", helperSpec(t), "test")
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer c.Close()

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("listing tools failed: %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "echo" {
		t.Fatalf("unexpected tool list: %+v", tools)
	}
	// A tool without inputSchema still needs a valid empty schema, or the model receives null.
	if !strings.Contains(string(tools[1].Schema), "object") {
		t.Errorf("a tool without a schema did not get a default: %s", tools[1].Schema)
	}

	out, err := c.CallTool(ctx, "echo", `{"text":"hi"}`)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if out != "echo: hi" {
		t.Errorf("unexpected content: %q", out)
	}
}

func TestCallToolSurfacesServerError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c, err := Start(ctx, "fake", helperSpec(t), "test")
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer c.Close()

	out, err := c.CallTool(ctx, "fail", `{}`)
	if err != nil {
		t.Fatalf("an error reported by the tool must not become a Go error: %v", err)
	}
	if !strings.HasPrefix(out, "The tool reported an error:") || !strings.Contains(out, "it broke") {
		t.Errorf("the error was not passed back verbatim: %q", out)
	}
}

func TestBadArgsAreRejectedBeforeSending(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c, err := Start(ctx, "fake", helperSpec(t), "test")
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer c.Close()

	if _, err := c.CallTool(ctx, "echo", `{"text":`); err == nil {
		t.Fatal("malformed JSON should be rejected before sending")
	}
}

// TestManagerPicksUpAndDropsServers: changed settings must start and stop servers without a
// daemon restart.
func TestManagerPicksUpAndDropsServers(t *testing.T) {
	m := New("test")
	defer m.Close()

	cfg := &config.Config{MCPServers: map[string]config.MCPServer{"fake": helperSpec(t)}}
	m.Sync(cfg)

	if !waitFor(func() bool { return len(m.Tools()) == 2 }, 20*time.Second) {
		t.Fatalf("the server did not start: %+v", m.Status("fake"))
	}

	list := m.Tools()
	names := []string{list[0].Spec().Function.Name, list[1].Spec().Function.Name}
	for _, n := range names {
		if !strings.HasPrefix(n, "mcp__fake__") {
			t.Errorf("tool names should carry the mcp__<server>__ prefix, got %q", n)
		}
	}

	// Once disabled the tool must vanish immediately (reaping the process is background work).
	cfg.MCPServers["fake"] = config.MCPServer{Command: "x", Disabled: true}
	m.Sync(cfg)
	if got := len(m.Tools()); got != 0 {
		t.Errorf("%d tools remain after disabling it", got)
	}
}

func TestClientReportsDeadServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, err := Start(ctx, "dead", config.MCPServer{Command: "/bin/sh", Args: []string{"-c", "exit 3"}}, "test")
	if err == nil {
		_ = c.Close()
		t.Fatal("the process dies on start, so the handshake should fail")
	}
}

func waitFor(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}
