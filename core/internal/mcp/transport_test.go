package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/NekoLOvO/dux-code/core/internal/config"
)

// Remote MCP goes over streamable HTTP: a url in the settings, no command.
//
// This is why the SDK was adopted — the hand-written version was stdio only, and connecting to
// something like https://mcp.example.com would have meant rewriting the transport.
func TestClientStreamableHTTP(t *testing.T) {
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "remote", Version: "1"}, nil)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "hello", Description: "say hello"},
		func(context.Context, *mcpsdk.CallToolRequest, any) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{
				Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "hi from http"}},
			}, nil, nil
		})

	ts := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return srv }, nil))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c, err := Start(ctx, "remote", config.MCPServer{URL: ts.URL}, "test")
	if err != nil {
		t.Fatalf("cannot reach the remote server: %v", err)
	}
	defer c.Close()

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("listing tools failed: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "hello" {
		t.Fatalf("unexpected tool list: %+v", tools)
	}

	out, err := c.CallTool(ctx, "hello", `{}`)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if out != "hi from http" {
		t.Errorf("unexpected content: %q", out)
	}
}

// includeTools / excludeTools are for servers with twenty-odd tools of which four get used. The
// filter applies where tools are produced for the model, so the UI's count follows too.
func TestManagerFiltersTools(t *testing.T) {
	m := New("test")
	defer m.Close()

	spec := helperSpec(t)
	spec.ExcludeTools = []string{"fail"}
	cfg := &config.Config{MCPServers: map[string]config.MCPServer{"fake": spec}}
	m.Sync(cfg)

	if !waitFor(func() bool { return len(m.Tools()) == 1 }, 20*time.Second) {
		t.Fatalf("only echo should remain: %+v", m.Status("fake"))
	}
	if got := m.Tools()[0].Spec().Function.Name; got != "mcp__fake__echo" {
		t.Errorf("the remaining tools are wrong: %q", got)
	}

	// Switch to an allowlist: keep only fail.
	spec.ExcludeTools = nil
	spec.IncludeTools = []string{"fail"}
	cfg.MCPServers["fake"] = spec
	m.Sync(cfg)

	if !waitFor(func() bool { return len(m.Tools()) == 1 }, 20*time.Second) {
		t.Fatalf("the allowlist had no effect: %+v", m.Status("fake"))
	}
	if got := m.Tools()[0].Spec().Function.Name; got != "mcp__fake__fail" {
		t.Errorf("the remaining tools are wrong: %q", got)
	}
}
