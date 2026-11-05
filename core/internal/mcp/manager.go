package mcp

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/NekoLOvO/dux-code/core/internal/applog"
	"github.com/NekoLOvO/dux-code/core/internal/config"
	"github.com/NekoLOvO/dux-code/core/internal/tools"
)

// startTimeout caps "startup through listing tools".
//
// 60 seconds because `npx -y @modelcontextprotocol/server-*` really downloads the package on the
// first run and prints nothing until it is done.
const startTimeout = 60 * time.Second

// Manager owns the processes of every MCP server.
//
// It blocks nobody: Sync only starts what should run and stops what should not, startup happens in
// the background, and the tool list stays empty until it finishes. So the model may miss a tool this
// turn and see it the next — deliberate, and better than stalling for tens of seconds.
type Manager struct {
	version string

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	entries map[string]*entry
}

type entry struct {
	spec   config.MCPServer
	client *Client
	tools  []Tool
	// starting means a background start is in flight; err is the most recent failure.
	starting bool
	err      error
	// gen increments on every restart; a late startup result uses it to tell it is no longer current.
	gen int
	// shown is the name last reported to the model, so the UI can show what it is really called.
	shown []string
}

// New builds a manager. version is only used to tell the server who we are during the handshake.
func New(version string) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		version: version,
		ctx:     ctx,
		cancel:  cancel,
		entries: make(map[string]*entry),
	}
}

// Sync makes the running set match the settings.
//
// Three rules: absent (or disabled) in the settings means stop, a changed fingerprint means
// restart, not started yet means start. The fingerprint covers "how to launch" only — renaming must
// not restart a process.
func (m *Manager) Sync(cfg *config.Config) {
	desired := make(map[string]config.MCPServer)
	if cfg != nil {
		for _, id := range cfg.EnabledMCPIDs() {
			desired[id] = cfg.MCPServers[id]
		}
	}

	var stop []*Client
	start := make(map[string]config.MCPServer)

	m.mu.Lock()
	for id, e := range m.entries {
		want, ok := desired[id]
		if !ok {
			stop = append(stop, e.client)
			delete(m.entries, id)
			continue
		}
		if !sameLaunchSpec(e.spec, want) {
			stop = append(stop, e.client)
			e.client = nil
			e.tools = nil
			e.err = nil
			e.shown = nil
			e.spec = want
			e.gen++
			start[id] = want
		}
	}
	for id, want := range desired {
		if _, ok := m.entries[id]; !ok {
			m.entries[id] = &entry{spec: want}
			start[id] = want
		}
	}
	m.mu.Unlock()

	for _, c := range stop {
		if c != nil {
			go func(c *Client) { _ = c.Close() }(c)
		}
	}
	for id, spec := range start {
		m.startAsync(id, spec)
	}
}

func sameLaunchSpec(a, b config.MCPServer) bool {
	return a.URL == b.URL &&
		a.Command == b.Command &&
		reflect.DeepEqual(a.Args, b.Args) &&
		reflect.DeepEqual(a.Env, b.Env) &&
		reflect.DeepEqual(a.IncludeTools, b.IncludeTools) &&
		reflect.DeepEqual(a.ExcludeTools, b.ExcludeTools) &&
		a.AutoApprove == b.AutoApprove
}

func (m *Manager) startAsync(id string, spec config.MCPServer) {
	m.mu.Lock()
	e := m.entries[id]
	if e == nil {
		m.mu.Unlock()
		return
	}
	e.starting = true
	gen := e.gen
	m.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(m.ctx, startTimeout)
		defer cancel()

		client, err := Start(ctx, id, spec, m.version)
		var list []Tool
		if err == nil {
			list, err = client.ListTools(ctx)
		}

		m.mu.Lock()
		cur := m.entries[id]
		if cur == nil || cur.gen != gen {
			// This instance was replaced (settings changed, or the server was removed); do not write the
			// result back.
			m.mu.Unlock()
			if client != nil {
				_ = client.Close()
			}
			return
		}
		cur.starting = false
		if err != nil {
			cur.err = err
			m.mu.Unlock()
			if client != nil {
				_ = client.Close()
			}
			applog.Warnf("MCP %s failed to start: %v", id, err)
			return
		}
		cur.client = client
		cur.tools = list
		cur.shown = rawToolNames(id, list)
		cur.err = nil
		m.mu.Unlock()

		applog.Infof("MCP %s ready (%d tools)", id, len(list))
	}()
}

// Restart reopens one server, for retrying when the settings did not change.
func (m *Manager) Restart(id string, cfg *config.Config) {
	var old *Client
	m.mu.Lock()
	if e, ok := m.entries[id]; ok {
		old = e.client
		e.client = nil
		e.tools = nil
		e.err = nil
		e.shown = nil
		e.gen++
	}
	m.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	m.Sync(cfg)
}

// Tools returns every MCP tool available right now.
//
// Rebuilt on every call (a server may have just started or just changed its list); the cost is a
// few dozen strings, not worth caching.
func (m *Manager) Tools() []tools.Tool {
	type snap struct {
		id     string
		label  string
		spec   config.MCPServer
		client *Client
		tools  []Tool
		auto   bool
	}

	m.mu.Lock()
	ids := make([]string, 0, len(m.entries))
	for id := range m.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	snaps := make([]snap, 0, len(ids))
	for _, id := range ids {
		e := m.entries[id]
		if e.client == nil {
			continue
		}
		snaps = append(snaps, snap{
			id:     id,
			label:  labelOf(id, e.spec),
			spec:   e.spec,
			client: e.client,
			tools:  e.tools,
			auto:   e.spec.AutoApprove,
		})
	}
	m.mu.Unlock()

	used := make(map[string]bool)
	out := make([]tools.Tool, 0)
	shown := make(map[string][]string)

	for _, s := range snaps {
		client := s.client
		for _, t := range s.tools {
			// includeTools / excludeTools: for servers with twenty-odd tools of which four matter, otherwise
			// the descriptions alone eat the context.
			if !s.spec.Keeps(t.Name) {
				continue
			}
			name := tools.MCPToolName(s.id, t.Name)
			for i := 2; used[name]; i++ {
				// Sanitising can collide (my.server and my_server both having read_file). Numbering beats silently
				// dropping one.
				name = tools.MCPToolName(s.id, fmt.Sprintf("%s_%d", t.Name, i))
			}
			used[name] = true
			shown[s.id] = append(shown[s.id], name)

			tool := t
			out = append(out, tools.MCPTool{
				Server:      s.label,
				Name:        name,
				Tool:        tool.Name,
				Desc:        tool.Description,
				Schema:      tool.Schema,
				AutoApprove: s.auto,
				Call: func(ctx context.Context, args string) (string, error) {
					return client.CallTool(ctx, tool.Name, args)
				},
			})
		}

		// The server says its tool list changed: refetch in the background, do not block this turn.
		if client.Dirty() {
			client.ClearDirty()
			go m.refresh(s.id, client)
		}
	}

	m.mu.Lock()
	for id, names := range shown {
		if e, ok := m.entries[id]; ok {
			e.shown = names
		}
	}
	m.mu.Unlock()

	return out
}

func (m *Manager) refresh(id string, client *Client) {
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	defer cancel()
	list, err := client.ListTools(ctx)
	if err != nil {
		applog.Warnf("MCP %s failed to refresh its tool list: %v", id, err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[id]; ok && e.client == client {
		e.tools = list
		e.shown = rawToolNames(id, list)
	}
}

// Status is one server's state, for the UI.
type Status struct {
	Running   bool     `json:"running"`
	Starting  bool     `json:"starting"`
	ToolCount int      `json:"toolCount"`
	Tools     []string `json:"tools"`
	Error     string   `json:"error,omitempty"`
}

// Status returns one server's state; not running (or disabled) means empty.
func (m *Manager) Status(id string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return Status{Tools: []string{}}
	}
	st := Status{
		Running:   e.client != nil,
		Starting:  e.starting,
		ToolCount: len(e.tools),
		Tools:     append([]string{}, e.shown...),
	}
	if e.err != nil {
		st.Error = e.err.Error()
	}
	return st
}

// Close stops every server. Called when the daemon exits.
func (m *Manager) Close() {
	m.cancel()

	m.mu.Lock()
	clients := make([]*Client, 0, len(m.entries))
	for _, e := range m.entries {
		if e.client != nil {
			clients = append(clients, e.client)
		}
	}
	m.entries = make(map[string]*entry)
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func(c *Client) {
			defer wg.Done()
			_ = c.Close()
		}(c)
	}
	wg.Wait()
}

// rawToolNames are the names before dedup, for display. The set actually given to the model is
// recomputed in Tools(), which is where collisions are handled.
func rawToolNames(id string, list []Tool) []string {
	out := make([]string, 0, len(list))
	for _, t := range list {
		out = append(out, tools.MCPToolName(id, t.Name))
	}
	return out
}

func labelOf(id string, spec config.MCPServer) string {
	if s := spec.Label; s != "" {
		return s
	}
	return id
}
