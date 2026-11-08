// Package mcp is the client side of MCP (Model Context Protocol).
//
// The protocol itself comes from the official modelcontextprotocol/go-sdk; only the layers this
// project cares about are kept:
//
//   - process lifetime: servers like Chrome spawn children, and the SDK only SIGTERMs the parent,
//     so the process group is reaped here (see Close)
//   - failure messages: SDK errors carry no stderr, while "why did it not start" is almost always
//     written there, so cmd.Stderr feeds a tailBuffer that is attached on error
//   - tool name and schema normalisation: some servers send no inputSchema, and forwarding that
//     verbatim sends parameters:null and breaks the model
//
// The hand-written version (612 lines of JSON-RPC) was replaced in v1.8 for one reason: transport.
// Remote MCP needs streamable HTTP and SSE, neither of which is worth writing by hand, and the SDK
// already tracks the spec (MRTR and elicitation from 2026-07-28).
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/NekoLOvO/dux-code/core/internal/config"
)

// defaultSchema is for servers that send a name but no schema: without it the model receives
// parameters:null and the whole turn fails to send.
var defaultSchema = json.RawMessage(`{"type":"object","properties":{}}`)

// startKillWait caps how long Close waits for a clean exit before killing the process group.
const startKillWait = 1500 * time.Millisecond

// Tool is one tool a server lists. Schema keeps the original JSON Schema untouched.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// Client is one running MCP server connection.
type Client struct {
	id string

	session *mcpsdk.ClientSession
	cmd     *exec.Cmd
	tail    *tailBuffer
	// remote is set for a URL server: its transport records a refused token, which startError turns
	// into advice instead of passing "Unauthorized" along.
	remote *headerRoundTripper

	// dirty is set by notifications/tools/list_changed; the next Tools() refetches.
	dirty atomic.Bool

	closeOnce sync.Once
}

// Start connects to a server and completes the handshake.
//
// The caller's ctx is not the lifetime here: the server outlives any single conversation, so ctx
// only bounds the handshake. A url means streamable HTTP, otherwise the command is run.
func Start(ctx context.Context, id string, spec config.MCPServer, version string) (*Client, error) {
	c := &Client{id: id, tail: newTailBuffer(8 << 10)}

	sdk := mcpsdk.NewClient(
		&mcpsdk.Implementation{Name: "dux-code", Version: version},
		&mcpsdk.ClientOptions{
			ToolListChangedHandler: func(context.Context, *mcpsdk.ToolListChangedRequest) {
				c.dirty.Store(true)
			},
		},
	)

	transport, err := c.transport(spec)
	if err != nil {
		return nil, err
	}

	session, err := sdk.Connect(ctx, transport, nil)
	if err != nil {
		// A failed handshake means the server is unusable; stderr comes back with it, or the user only
		// sees "initialize failed" with no reason.
		msg := c.startError(err)
		_ = c.Close()
		return nil, msg
	}
	c.session = session
	return c, nil
}

// startError explains a failed handshake: the token's fault first when the server said so, then
// whatever the SDK and the server's stderr have to say.
func (c *Client) startError(err error) error {
	tail := errorfWithTail(err, c.tail)
	if c.remote == nil {
		return tail
	}
	refused := c.remote.refused.Load()
	if refused == nil {
		return tail
	}
	return fmt.Errorf("%s (the server said: %v)", refused.String(), tail)
}

// transport picks a path from the settings.
func (c *Client) transport(spec config.MCPServer) (mcpsdk.Transport, error) {
	if url := strings.TrimSpace(spec.URL); url != "" {
		// A stream cannot carry an overall timeout (it is a long-lived connection); the connection
		// deadline comes from the caller's ctx.
		ht := &headerRoundTripper{base: http.DefaultTransport, headers: spec.Headers}
		c.remote = ht
		return &mcpsdk.StreamableClientTransport{
			Endpoint:   url,
			HTTPClient: &http.Client{Transport: ht},
		}, nil
	}

	if strings.TrimSpace(spec.Command) == "" {
		return nil, errors.New("neither a launch command nor a url was given")
	}

	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Env = mergeEnv(spec.Env)
	// Its own process group: npx/uvx spawn children, and killing only the parent leaves orphans.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = c.tail
	c.cmd = cmd

	return &mcpsdk.CommandTransport{
		Command:           cmd,
		TerminateDuration: startKillWait,
	}, nil
}

// headerRoundTripper adds the configured headers to every request.
//
// The streamable transport takes an http.Client and nothing else, so a remote server that wants a
// token (GitHub's hosted endpoint asks for "Authorization: Bearer …") has nowhere else to put it.
// http.DefaultTransport is the base on purpose: it keeps the proxy and TLS settings from the
// environment in play.
type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
	// refused records the last 401/403 the server answered, for startError. Everything else (a
	// timeout, a 500, a name that does not resolve) is the SDK's error to describe; a refusal is the
	// one case where it says only "Unauthorized" and the thing to change is in these headers.
	refused atomic.Pointer[refusal]
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone rather than mutate: RoundTrip must not touch the caller's request, and a redirect or a
	// retry hands the same one back.
	out := req.Clone(req.Context())
	for k, v := range h.headers {
		out.Header.Set(k, v)
	}
	res, err := h.base.RoundTrip(out)
	if err != nil {
		h.refused.Store(nil)
		return res, err
	}
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		h.refused.Store(&refusal{status: res.StatusCode, tokenSent: hasAuthorization(h.headers)})
	} else {
		h.refused.Store(nil)
	}
	return res, err
}

// refusal is a server saying no to the credentials rather than to the request.
type refusal struct {
	status int
	// tokenSent separates "you gave me a token and I will not take it" from "you gave me nothing",
	// which need opposite advice.
	tokenSent bool
}

// String is what the settings page shows instead of a bare "Unauthorized".
//
// It leads with the thing to change, because the row truncates it and the SDK's text says nothing.
func (r *refusal) String() string {
	switch {
	case r.status == http.StatusForbidden:
		return "the server accepted the token but not for this: HTTP 403 Forbidden. Check the token's scopes and the account's access to what it is being asked for."
	case !r.tokenSent:
		return "this server wants a token first: HTTP 401 Unauthorized, and none is configured. Add one in this server's settings."
	default:
		return "the server refused the token: HTTP 401 Unauthorized. Check it is correct, not expired, and has access to what you asked for."
	}
}

// hasAuthorization reports whether the configured headers carry a credential, case-insensitively as
// HTTP does.
func hasAuthorization(headers map[string]string) bool {
	for k, v := range headers {
		if strings.EqualFold(k, "Authorization") && strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// ID returns the name from the settings.
func (c *Client) ID() string { return c.id }

// Stderr returns the tail of the server's stderr (for diagnosis).
func (c *Client) Stderr() string { return c.tail.String() }

// Dirty reports whether the server notified a tool-list change.
func (c *Client) Dirty() bool { return c.dirty.Load() }

// ClearDirty is called after refetching the list.
func (c *Client) ClearDirty() { c.dirty.Store(false) }

// ListTools lists every tool, pagination included.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	if c.session == nil {
		return nil, errors.New("not connected yet")
	}

	var out []Tool
	cursor := ""
	// The page cap is only a guard: servers needing a 21st page barely exist, and an endless loop
	// would stall daemon startup.
	for page := 0; page < 20; page++ {
		res, err := c.session.ListTools(ctx, &mcpsdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, wrapWithTail("list tools", err, c.tail)
		}
		for _, t := range res.Tools {
			name := strings.TrimSpace(t.Name)
			if name == "" {
				continue
			}
			out = append(out, Tool{
				Name:        name,
				Description: t.Description,
				Schema:      schemaOf(t.InputSchema),
			})
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return out, nil
}

// schemaOf turns the SDK's decoded schema back into raw JSON. A tool without one still has to
// work — some servers only send a name.
func schemaOf(v any) json.RawMessage {
	if v == nil {
		return defaultSchema
	}
	raw, err := json.Marshal(v)
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		return defaultSchema
	}
	return raw
}

// CallTool calls a tool and flattens the result into plain text.
func (c *Client) CallTool(ctx context.Context, name, args string) (string, error) {
	if c.session == nil {
		return "", errors.New("not connected yet")
	}

	var arguments any = map[string]any{}
	if strings.TrimSpace(args) != "" {
		if err := json.Unmarshal([]byte(args), &arguments); err != nil {
			return "", fmt.Errorf("the arguments are not valid JSON (%v); received: %s", err, args)
		}
	}

	res, err := c.session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      name,
		Arguments: arguments,
	})
	if err != nil {
		return "", wrapWithTail("call tool", err, c.tail)
	}
	return renderResult(res), nil
}

// renderResult flattens content blocks into text.
//
// Images have no vision channel yet; base64 would eat the context, so this only notes that an
// image was there.
func renderResult(res *mcpsdk.CallToolResult) string {
	if res == nil {
		return "(the tool returned no content)"
	}

	parts := make([]string, 0, len(res.Content))
	for _, part := range res.Content {
		switch v := part.(type) {
		case *mcpsdk.TextContent:
			if v.Text != "" {
				parts = append(parts, v.Text)
			}
		case *mcpsdk.ImageContent:
			parts = append(parts,
				fmt.Sprintf("[image: %s, %d bytes, contents not visible here]", orDefault(v.MIMEType, "unknown"), len(v.Data)))
		case *mcpsdk.AudioContent:
			parts = append(parts,
				fmt.Sprintf("[audio: %s, %d bytes, not audible here]", orDefault(v.MIMEType, "unknown"), len(v.Data)))
		case *mcpsdk.EmbeddedResource:
			label := "resource"
			if v.Resource != nil {
				label = firstNonEmpty(v.Resource.URI, label)
			}
			parts = append(parts, fmt.Sprintf("[resource: %s]", label))
		case *mcpsdk.ResourceLink:
			parts = append(parts, fmt.Sprintf("[resource: %s]", firstNonEmpty(v.Title, v.Name, v.URI)))
		default:
			parts = append(parts, fmt.Sprintf("[%T content skipped]", part))
		}
	}

	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if text == "" && res.StructuredContent != nil {
		if raw, err := json.Marshal(res.StructuredContent); err == nil && string(raw) != "null" {
			text = strings.TrimSpace(string(raw))
		}
	}
	if text == "" {
		text = "(the tool returned no content)"
	}
	if res.IsError {
		return "The tool reported an error:\n" + text
	}
	return text
}

// Close drops the connection: let it exit on its own, then kill the process group if it will
// not.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		if c.session != nil {
			_ = c.session.Close()
		}
		// The SDK only reaps the parent. The node children npx/uvx started stay in the process group, so
		// finish the job here (a negative pid means the whole group).
		c.kill()
	})
	return nil
}

func (c *Client) kill() {
	if c.cmd == nil || c.cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = c.cmd.Process.Kill()
	}
}

// mergeEnv layers the server's own variables over the daemon's environment.
//
// Sorted for reproducibility: identical settings must not produce a different env order.
func mergeEnv(extra map[string]string) []string {
	env := os.Environ()
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+extra[k])
	}
	return env
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// wrapWithTail appends the stderr tail to an error.
//
// SDK errors only say "failed", while the reason is almost always in the server's stderr; without
// this the user sees one incomprehensible jsonrpc2 line.
func wrapWithTail(op string, err error, tail *tailBuffer) error {
	if err == nil {
		return nil
	}
	if detail := shortTail(tail); detail != "" {
		return fmt.Errorf("%s failed: %w (stderr: %s)", op, err, detail)
	}
	return fmt.Errorf("%s failed: %w", op, err)
}

func errorfWithTail(err error, tail *tailBuffer) error {
	if detail := shortTail(tail); detail != "" {
		return fmt.Errorf("%w (stderr: %s)", err, detail)
	}
	return err
}

// shortTail folds the stderr tail onto one line: the UI's error row has room for that much.
//
// Cut on runes so CJK is never split mid-character.
func shortTail(tail *tailBuffer) string {
	if tail == nil {
		return ""
	}
	s := tail.String()
	if s == "" {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	const limit = 300
	if runes := []rune(s); len(runes) > limit {
		s = "…" + string(runes[len(runes)-limit:])
	}
	return s
}

// tailBuffer keeps the last N bytes of output for error messages.
//
// It is also cmd.Stderr's writer: a server may emit half a line and stall, so it collects bytes
// rather than splitting lines.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func newTailBuffer(max int) *tailBuffer { return &tailBuffer{max: max} }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}
