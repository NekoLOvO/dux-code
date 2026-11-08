package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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
		func(*http.Request) *mcpsdk.Server { return srv }, nil,
	))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c, err := Start(ctx, "remote", config.MCPServer{URL: ts.URL}, "test")
	if err != nil {
		t.Fatalf("cannot reach the remote server: %v", err)
	}
	defer func() { _ = c.Close() }()

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

// A remote server that wants a token gets it as a header (config.MCPServer.Headers).
//
// This is the GitHub case: the hosted endpoint answers 401 without "Authorization: Bearer …", and
// the SDK's streamable transport takes an http.Client and no headers of its own, so without this the
// only way to reach such a server would be a proxy in front of it.
func TestClientSendsConfiguredHeaders(t *testing.T) {
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "remote", Version: "1"}, nil)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "hello", Description: "say hello"},
		func(context.Context, *mcpsdk.CallToolRequest, any) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{
				Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "hi"}},
			}, nil, nil
		})
	inner := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv }, nil)

	// Counted rather than asserted inside the handler: that runs on its own goroutine, and a t.Fatal
	// there would not stop this one.
	var refused atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer s3cret" {
			refused.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Without the header the handshake must fail: otherwise this test would pass on a server that
	// never asked for one.
	if _, err := Start(ctx, "no-token", config.MCPServer{URL: ts.URL}, "test"); err == nil {
		t.Fatal("a server that answers 401 was connected without a token")
	}

	c, err := Start(ctx, "with-token", config.MCPServer{
		URL:     ts.URL,
		Headers: map[string]string{"Authorization": "Bearer s3cret"},
	}, "test")
	if err != nil {
		t.Fatalf("cannot reach the server with a token: %v", err)
	}
	defer func() { _ = c.Close() }()

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("listing tools failed: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "hello" {
		t.Fatalf("unexpected tool list: %+v", tools)
	}
}

// A refused token is the failure a hosted server gives, and the SDK's text for it is a bare
// "Unauthorized" — no hint that the token is the thing to change, and no difference between "the one
// you gave me is wrong" and "you gave me none". Both are the user's next keystroke.
func TestRemoteStartExplainsARefusedToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request: missing required Authorization header", http.StatusUnauthorized)
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// With a token: the advice is to check it.
	_, err := Start(ctx, "with-token", config.MCPServer{
		URL:     ts.URL,
		Headers: map[string]string{"Authorization": "Bearer wrong"},
	}, "test")
	if err == nil {
		t.Fatal("a server answering 401 was connected")
	}
	for _, want := range []string{"refused the token", "401", "Unauthorized"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not mention %q: %v", want, err)
		}
	}

	// Without one: the advice is to add one, not to check one that is not there.
	_, err = Start(ctx, "no-token", config.MCPServer{URL: ts.URL}, "test")
	if err == nil {
		t.Fatal("a server answering 401 was connected without a token")
	}
	if !strings.Contains(err.Error(), "wants a token") {
		t.Errorf("a missing token must be reported as missing, got: %v", err)
	}

	// 403 is the other half of the story: the token is real and the permission is not.
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer forbidden.Close()
	_, err = Start(ctx, "forbidden", config.MCPServer{
		URL:     forbidden.URL,
		Headers: map[string]string{"Authorization": "Bearer real"},
	}, "test")
	if err == nil {
		t.Fatal("a server answering 403 was connected")
	}
	if !strings.Contains(err.Error(), "scopes") {
		t.Errorf("403 must point at the scopes, got: %v", err)
	}
}
