// Package api wires ipc methods to store / agent.
//
// This is the only layer that knows method strings; no other package should contain a literal like
// "session.list".
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/NekoLOvO/dux-code/core/internal/agent"
	"github.com/NekoLOvO/dux-code/core/internal/applog"
	"github.com/NekoLOvO/dux-code/core/internal/autostart"
	"github.com/NekoLOvO/dux-code/core/internal/config"
	"github.com/NekoLOvO/dux-code/core/internal/files"
	"github.com/NekoLOvO/dux-code/core/internal/highlight"
	"github.com/NekoLOvO/dux-code/core/internal/ipc"
	"github.com/NekoLOvO/dux-code/core/internal/llm"
	"github.com/NekoLOvO/dux-code/core/internal/mcp"
	"github.com/NekoLOvO/dux-code/core/internal/search"
	"github.com/NekoLOvO/dux-code/core/internal/skills"
	"github.com/NekoLOvO/dux-code/core/internal/store"
	"github.com/NekoLOvO/dux-code/core/internal/tasks"
	"github.com/NekoLOvO/dux-code/core/internal/tools"
)

// APIVersion is the protocol version; the UI uses it for compatibility checks.
const APIVersion = 1

// Deps is what api needs.
type Deps struct {
	Version string
	Store   *store.Store
	Agent   *agent.Runner
	Config  *config.Config
	// Bus carries the agent's events. It must be the same one the Runner uses, or events land on a bus
	// nobody subscribed to.
	Bus *ipc.EventBus
	// Files backs the file browser (folder picking).
	Files *files.Service
	// MCP is the external tool servers. May be nil (tests, or not wired up yet).
	MCP *mcp.Manager
	// Tasks stores scheduled tasks; TaskMgr runs them. Both may be nil (tests).
	Tasks   *tasks.Store
	TaskMgr *tasks.Manager
}

// Capabilities is what the handshake advertises.
func Capabilities() []string {
	return []string{
		"subscribe",
		"session",
		"chat",
		"config",
		"files",
		"skills",
		"mcp",
		"tasks",
	}
}

// NewServer builds an ipc server with the routes attached.
func NewServer(deps Deps, socketPath string) *ipc.Server {
	cfg := ipc.Config{
		AppName:       "dcode",
		SocketPath:    socketPath,
		APIVersion:    APIVersion,
		Version:       deps.Version,
		Capabilities:  Capabilities(),
		DefaultTopics: []string{agent.Topic},
		Bus:           deps.Bus,
		OnSubscribe: func(topics []string, sub *ipc.Subscriber) {
			for _, t := range topics {
				if t == agent.Topic {
					// A fresh subscriber needs to know which sessions are running.
					applog.Debugf("new connection subscribed to %s", t)
				}
			}
		},
	}

	return ipc.NewServer(cfg, func(ctx context.Context, w *ipc.Writer, req ipc.Request, sub *ipc.Subscriber) {
		route(ctx, w, req, sub, deps)
	})
}

func route(_ context.Context, w *ipc.Writer, req ipc.Request, sub *ipc.Subscriber, deps Deps) {
	// deps is passed by value, so `deps.Config = cfg` inside a handler only changes that request's
	// copy and the next config.get still sees the daemon-startup snapshot. agent holds the live
	// config (everything swaps it through SetConfig), so realign at the start of each request or
	// "saved but not visible" appears.
	if deps.Agent != nil {
		if c := deps.Agent.Config(); c != nil {
			deps.Config = c
		}
	}

	switch req.Method {
	case "info":
		w.Respond(req.ID, infoOf(deps))

	case "session.search":
		// Cross-session message search. An empty query returns an empty list rather than everything —
		// "no keywords" is not "matches all".
		query := strings.TrimSpace(req.GetString("query"))
		if query == "" {
			w.Respond(req.ID, map[string]any{"hits": []any{}})
			return
		}
		hits := deps.Store.Search(query, req.GetInt("limit"))
		w.Respond(req.ID, map[string]any{"hits": hits})

	case "task.list":
		w.Respond(req.ID, map[string]any{"tasks": taskViews(deps)})

	case "task.save":
		if deps.Tasks == nil {
			w.RespondError(req.ID, "no task manager")
			return
		}
		t := tasks.Task{
			ID:               req.GetString("id"),
			Name:             strings.TrimSpace(req.GetString("name")),
			Prompt:           strings.TrimSpace(req.GetString("prompt")),
			Model:            strings.TrimSpace(req.GetString("model")),
			WorkingDirectory: strings.TrimSpace(req.GetString("workingDirectory")),
			AutoApprove:      req.GetBool("autoApprove"),
			Enabled:          req.GetBool("enabled"),
		}
		t.Schedule = tasks.Schedule{
			Kind:    req.GetString("kind"),
			At:      strings.TrimSpace(req.GetString("at")),
			Minutes: req.GetInt("minutes"),
			Weekday: req.GetInt("weekday"),
		}
		saved, err := deps.Tasks.Put(t)
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		w.Respond(req.ID, map[string]any{"task": taskView(deps, saved)})

	case "task.delete":
		if deps.Tasks == nil {
			w.RespondError(req.ID, "no task manager")
			return
		}
		if err := deps.Tasks.Delete(req.GetString("id")); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		w.Respond(req.ID, map[string]any{"ok": true})

	case "task.run":
		if deps.TaskMgr == nil {
			w.RespondError(req.ID, "no task scheduler")
			return
		}
		sessionID, err := deps.TaskMgr.RunNow(req.GetString("id"))
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		// Return the new session so the UI can jump straight to what it did.
		w.Respond(req.ID, map[string]any{"sessionId": sessionID})

	case "session.list":
		w.Respond(req.ID, map[string]any{"sessions": listSessions(deps)})

	case "session.get":
		id := req.GetString("id")
		sess, ok := deps.Store.Get(id)
		if !ok {
			w.RespondError(req.ID, "session not found")
			return
		}
		// The step in flight is not in the transcript yet, so a window that opens mid-reply would show
		// an empty bubble until it ends. runningText runningReasoning carry what that step has written
		// so far; both are empty when nothing is running.
		runningText, runningReasoning := deps.Agent.RunningText(id)
		w.Respond(req.ID, map[string]any{
			"session":          sess,
			"busy":             deps.Agent.Busy(id),
			"runningText":      runningText,
			"runningReasoning": runningReasoning,
		})

	case "session.new":
		title := req.GetString("title")
		sess, err := deps.Store.Create(title)
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		if dir := req.GetString("workingDirectory"); dir != "" {
			if err := deps.Store.SetWorkingDirectory(sess.ID, dir); err == nil {
				if fresh, ok := deps.Store.Get(sess.ID); ok {
					sess = fresh
				}
			}
		}
		w.Respond(req.ID, map[string]any{"session": sess})

	case "session.setWorkingDirectory":
		id := req.GetString("sessionId")
		dir := req.GetString("path")
		if err := deps.Store.SetWorkingDirectory(id, dir); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		sess, ok := deps.Store.Get(id)
		if !ok {
			w.RespondError(req.ID, "session not found: "+id)
			return
		}
		w.Respond(req.ID, map[string]any{"session": sess})

	case "session.rename":
		id := req.GetString("id")
		title := strings.TrimSpace(req.GetString("title"))
		if title == "" {
			w.RespondError(req.ID, "the title cannot be empty")
			return
		}
		if err := deps.Store.Rename(id, title); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		// A session renamed by hand is never overwritten by auto-naming.
		_ = deps.Store.MarkSmartNamed(id)
		w.Respond(req.ID, map[string]any{"ok": true})

	case "session.pin":
		id := req.GetString("id")
		if err := deps.Store.SetPinned(id, req.GetBool("pinned")); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		w.Respond(req.ID, map[string]any{"ok": true})

	case "session.delete":
		id := req.GetString("id")
		deps.Agent.CancelSession(id)
		if err := deps.Store.Delete(id); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		w.Respond(req.ID, map[string]any{"ok": true})

	case "chat.send":
		sessionID := req.GetString("sessionId")
		text := req.GetString("text")
		model := req.GetString("model")
		// Reasoning depth (none/minimal/low/medium/high/xhigh/max). Empty sends no parameter, letting the
		// provider use its own default.
		reasoning := req.GetString("reasoning")
		runID, messageID, err := deps.Agent.Send(sessionID, text, model, reasoning, req.GetStrings("attachments"), deps.toolEnv(sessionID, sub))
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		// messageId lets the UI edit or regenerate this turn before it reloads the
		// session, without guessing at a row index.
		w.Respond(req.ID, map[string]any{"runId": runID, "messageId": messageID})

	case "chat.regenerate":
		// No new message: rerun the last user message. The caller has to truncate the later answers first.
		sessionID := req.GetString("sessionId")
		model := req.GetString("model")
		reasoning := req.GetString("reasoning")
		runID, err := deps.Agent.Regenerate(sessionID, model, reasoning, deps.toolEnv(sessionID, sub))
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		w.Respond(req.ID, map[string]any{"runId": runID})

	// These three address one message the user clicked. The id, not a row index: the UI
	// merges tool results and drops empty assistant messages, so its row numbers do not
	// line up with session.messages and an index picks the wrong message.
	case "session.truncate":
		id := req.GetString("id")
		messageID := req.GetString("messageId")
		if messageID == "" {
			w.RespondError(req.ID, "messageId is required")
			return
		}
		if !deps.Agent.StopSession(id) {
			w.RespondError(req.ID, "the turn that is running has not stopped yet; try again in a moment")
			return
		}
		if err := deps.Store.TruncateAfter(id, messageID); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		w.Respond(req.ID, map[string]any{"ok": true})

	case "session.rewrite":
		id := req.GetString("id")
		messageID := req.GetString("messageId")
		text := strings.TrimSpace(req.GetString("text"))
		if messageID == "" {
			w.RespondError(req.ID, "messageId is required")
			return
		}
		if text == "" {
			w.RespondError(req.ID, "the message cannot be empty")
			return
		}
		if !deps.Agent.StopSession(id) {
			w.RespondError(req.ID, "the turn that is running has not stopped yet; try again in a moment")
			return
		}
		if err := deps.Store.RewriteUser(id, messageID, text); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		w.Respond(req.ID, map[string]any{"ok": true})

	case "session.branch":
		id := req.GetString("id")
		messageID := req.GetString("messageId")
		if messageID == "" {
			w.RespondError(req.ID, "messageId is required")
			return
		}
		sess, err := deps.Store.Branch(id, messageID, req.GetString("title"))
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		w.Respond(req.ID, map[string]any{"session": sess})

	case "chat.cancel":
		runID := req.GetString("runId")
		ok := deps.Agent.Cancel(runID)
		w.Respond(req.ID, map[string]any{"ok": ok})

	case "chat.cancelSession":
		n := deps.Agent.CancelSession(req.GetString("sessionId"))
		w.Respond(req.ID, map[string]any{"cancelled": n})

	case "session.smartRename":
		id := req.GetString("sessionId")
		model := req.GetString("model")
		if model == "" {
			model = deps.Config.DefaultModel
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		title, err := deps.Agent.SuggestTitle(ctx, id, model)
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		if err := deps.Store.Rename(id, title); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		_ = deps.Store.MarkSmartNamed(id)
		sess, _ := deps.Store.Get(id)
		w.Respond(req.ID, map[string]any{"session": sess, "title": title})

	case "session.export":
		id := req.GetString("sessionId")
		sess, ok := deps.Store.Get(id)
		if !ok {
			w.RespondError(req.ID, "session not found: "+id)
			return
		}
		// The session JSON as-is (messages, workingDirectory, timestamps): other agents read it, and there
		// are no markdown conversion rules to maintain.
		dataHome := os.Getenv("XDG_DATA_HOME")
		if dataHome == "" {
			home, _ := os.UserHomeDir()
			dataHome = filepath.Join(home, ".local", "share")
		}
		dir := filepath.Join(dataHome, "dcode", "exports")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		slug := exportSlug(sess.Title)
		name := slug + "-" + time.Now().Format("20060102-1504") + ".json"
		full := filepath.Join(dir, name)
		body, err := json.MarshalIndent(sess, "", "  ")
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		if err := os.WriteFile(full, append(body, '\n'), 0o644); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		w.Respond(req.ID, map[string]any{"path": full})

	case "config.get":
		w.Respond(req.ID, configView(deps))

	// Launch at login means writing a .desktop under ~/.config/autostart; the switch is whether the
	// file exists. It points at the user's own executable rather than a hard-coded /usr/bin/dcode: a
	// development run is core/bin/dcode and an install is /usr/bin/dcode, and both must work.
	case "config.setAutostart":
		exe, err := os.Executable()
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		if err := autostart.Set(req.GetBool("enabled"), exe); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		w.Respond(req.ID, configView(deps))

	case "config.reload":
		cfg, err := config.Load()
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		deps.Agent.SetConfig(cfg)
		deps.Config = cfg
		if deps.MCP != nil {
			deps.MCP.Sync(cfg)
		}
		w.Respond(req.ID, configView(deps))

	// Only the key is changed; the rest is left for the user to edit in the file. This method is named
	// "set key", so an empty string meaning clear is reasonable — provider.update is not, see the rules
	// there.
	case "config.setKey":
		name := req.GetString("provider")
		if name == "" {
			w.RespondError(req.ID, "provider is required")
			return
		}
		cfg, err := config.Load()
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		p, ok := cfg.Providers[name]
		if !ok {
			w.RespondError(req.ID, "unknown provider: "+name)
			return
		}
		p.APIKey = strings.TrimSpace(req.GetString("apiKey"))
		cfg.Providers[name] = p
		if err := cfg.Save(); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		deps.Agent.SetConfig(cfg)
		w.Respond(req.ID, configView(deps))

	case "config.setDefaultModel":
		id := req.GetString("id")
		cfg, err := config.Load()
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		if _, _, ok := cfg.Resolve(id); !ok {
			w.RespondError(req.ID, "unknown model: "+id)
			return
		}
		cfg.DefaultModel = id
		if err := cfg.Save(); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		deps.Agent.SetConfig(cfg)
		w.Respond(req.ID, configView(deps))

	// Agent-related numbers. Currently the per-reply output cap; the context budget will live here
	// too, so each new setting does not need a method of its own.
	case "config.setAgent":
		cfg, err := config.Load()
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		// nil tells "field absent"; 0 cannot, because 0 is a legal input (reset to default) while an
		// omitted field must not overwrite.
		if req.Get("maxOutputTokens") != nil {
			n := req.GetInt("maxOutputTokens")
			// 0 is legal: it means "leave the cap to the provider".
			if n < 0 || n > config.MaxOutputCap {
				w.RespondError(req.ID, fmt.Sprintf("the output cap must be between 0 (off) and %d", config.MaxOutputCap))
				return
			}
			cfg.MaxOutputTokens = n
		}
		if req.Get("compactAt") != nil {
			n := req.GetInt("compactAt")
			// 0 disables it; anything else must be meaningful (too small a threshold compacts every turn).
			if n != 0 && n < 4000 {
				w.RespondError(req.ID, "the compaction threshold must be at least 4000 (0 disables it)")
				return
			}
			cfg.CompactAt = n
		}
		if req.Get("maxToolSteps") != nil {
			n := req.GetInt("maxToolSteps")
			// 0 turns the fuse off, which is the point of offering it: the cap is a guard against
			// circling, not a quota, and some people would rather watch the burn than be stopped.
			if n < 0 || n > config.MaxToolStepsLimit {
				w.RespondError(req.ID, fmt.Sprintf("the step limit must be 0 (off) or between 1 and %d", config.MaxToolStepsLimit))
				return
			}
			cfg.MaxToolSteps = n
		}
		if req.Get("fileAccess") != nil {
			v := req.GetBool("fileAccess")
			cfg.Tools.FileAccess = &v
		}
		if req.Get("shell") != nil {
			v := req.GetBool("shell")
			cfg.Tools.Shell = &v
		}
		if req.Get("webSearch") != nil {
			v := req.GetBool("webSearch")
			cfg.Tools.WebSearch = &v
		}
		if req.Get("browserPath") != nil {
			// An empty string is legal: it means "look in PATH again".
			cfg.Browser.Path = strings.TrimSpace(req.GetString("browserPath"))
		}
		if req.Get("approvalMode") != nil {
			mode := req.GetString("approvalMode")
			switch mode {
			case "ask", "workspace", "auto":
			default:
				w.RespondError(req.ID, "approvalMode must be ask / workspace / auto")
				return
			}
			cfg.ApprovalMode = mode
		}
		cfg.FixDefaults()
		if err := cfg.Save(); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		deps.Agent.SetConfig(cfg)
		w.Respond(req.ID, configView(deps))

	// --- provider management ---

	case "search.save":
		cfg, err := config.Load()
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		kindID := strings.TrimSpace(req.GetString("type"))
		kind, known := search.KindByID(kindID)
		if !known {
			w.RespondError(req.ID, "unknown search backend: "+kindID)
			return
		}
		baseURL := strings.TrimRight(strings.TrimSpace(req.GetString("baseUrl")), "/")
		if kind.NeedURL && baseURL == "" {
			w.RespondError(req.ID, "this backend needs an instance URL")
			return
		}

		id := strings.TrimSpace(req.GetString("id"))
		if cfg.Search == nil {
			cfg.Search = map[string]config.SearchProvider{}
		}
		existing, editing := cfg.Search[id]

		apiKey := strings.TrimSpace(req.GetString("apiKey"))
		if editing && apiKey == "" {
			// Empty while editing keeps the old key. Clearing the whole entry would make the user repaste a
			// key for every rename.
			apiKey = existing.APIKey
		}
		if kind.NeedKey && apiKey == "" {
			w.RespondError(req.ID, "fill in the API key first")
			return
		}

		enabled := true
		if editing {
			enabled = existing.Enabled
		}
		if req.Get("enabled") != nil {
			enabled = req.GetBool("enabled")
		}

		if !editing {
			if id == "" {
				id = kindID + "-" + store.NewID()
			}
		}

		// The name defaults to the backend name. Editing without a label keeps the existing one, so a URL
		// change does not reset a user-chosen name to "Tavily".
		label := strings.TrimSpace(req.GetString("label"))
		switch {
		case label != "":
		case editing && existing.Label != "":
			label = existing.Label
		default:
			label = kind.Label
		}

		cfg.Search[id] = config.SearchProvider{
			Type:       kindID,
			Label:      label,
			BaseURL:    baseURL,
			APIKey:     apiKey,
			Enabled:    enabled,
			MaxResults: req.GetInt("maxResults"),
		}
		if cfg.SearchDefault == "" || !cfg.Search[cfg.SearchDefault].Enabled {
			cfg.SearchDefault = id
		}
		if err := cfg.Save(); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		deps.Agent.SetConfig(cfg)
		w.Respond(req.ID, configView(deps))

	case "search.remove":
		cfg, err := config.Load()
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		id := strings.TrimSpace(req.GetString("id"))
		if _, ok := cfg.Search[id]; !ok {
			w.RespondError(req.ID, "search backend not found")
			return
		}
		delete(cfg.Search, id)
		if cfg.SearchDefault == id {
			cfg.SearchDefault = ""
		}
		// Removing the default picks another, or the config keeps a dead id.
		if cfg.SearchDefault == "" {
			if next, _, ok := cfg.ActiveSearch(); ok {
				cfg.SearchDefault = next
			}
		}
		if err := cfg.Save(); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		deps.Agent.SetConfig(cfg)
		w.Respond(req.ID, configView(deps))

	// Switches which one is active. Unnecessary with a single backend, but with several configured the
	// only alternative would be delete and re-add.
	case "search.use":
		cfg, err := config.Load()
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		id := strings.TrimSpace(req.GetString("id"))
		p, ok := cfg.Search[id]
		if !ok {
			w.RespondError(req.ID, "search backend not found")
			return
		}
		if !p.Enabled {
			p.Enabled = true
			cfg.Search[id] = p
		}
		cfg.SearchDefault = id
		if err := cfg.Save(); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		deps.Agent.SetConfig(cfg)
		w.Respond(req.ID, configView(deps))

	// Connectivity test: a real query, not a ping. A 200 that cannot be parsed (SearXNG without JSON
	// output) only shows up when actually queried.
	case "search.test":
		kindID := strings.TrimSpace(req.GetString("type"))
		kind, known := search.KindByID(kindID)
		if !known {
			w.RespondError(req.ID, "unknown search backend: "+kindID)
			return
		}
		baseURL := strings.TrimRight(strings.TrimSpace(req.GetString("baseUrl")), "/")
		apiKey := strings.TrimSpace(req.GetString("apiKey"))
		// No new key in the form: borrow the stored one.
		if apiKey == "" {
			if stored, ok := deps.Agent.Config().Search[req.GetString("id")]; ok {
				apiKey = stored.APIKey
			}
		}
		if kind.NeedURL && baseURL == "" {
			w.RespondError(req.ID, "this backend needs an instance URL")
			return
		}
		if kind.NeedKey && apiKey == "" {
			w.RespondError(req.ID, "fill in the API key first")
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		started := time.Now()
		results, err := search.Client{Kind: kindID, BaseURL: baseURL, APIKey: apiKey}.
			Search(ctx, "quickshell", 3)
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		top := make([]map[string]any, 0, len(results))
		for _, r := range results {
			top = append(top, map[string]any{"title": r.Title, "url": r.URL})
		}
		w.Respond(req.ID, map[string]any{
			"count":     len(results),
			"results":   top,
			"elapsedMs": time.Since(started).Milliseconds(),
		})

	case "provider.templates":
		w.Respond(req.ID, map[string]any{"templates": config.Templates()})

	// Probe an endpoint that is not saved yet: return the model list without persisting. The add flow
	// needs the list before the user presses save.
	case "provider.probe":
		tmplID := req.GetString("type")
		tmpl, known := config.TemplateByID(tmplID)
		if !known {
			tmpl = config.Template{Type: "openai"}
		}
		baseURL := strings.TrimRight(strings.TrimSpace(req.GetString("baseUrl")), "/")
		if baseURL == "" {
			baseURL = tmpl.BaseURL
		}
		if baseURL == "" {
			w.RespondError(req.ID, "the endpoint URL is required")
			return
		}
		probe := config.Provider{
			Type:    firstNonEmpty(req.GetString("providerType"), tmpl.Type, "openai"),
			BaseURL: baseURL,
			APIKey:  strings.TrimSpace(req.GetString("apiKey")),
		}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		infos, err := llm.FetchModels(ctx, probe)
		if err != nil {
			if errors.Is(err, llm.ErrNoAPIKey) {
				w.RespondError(req.ID, "fill in the API key first")
				return
			}
			w.RespondError(req.ID, err.Error())
			return
		}
		w.Respond(req.ID, map[string]any{"models": infos})

	case "provider.add":
		cfg, err := config.Load()
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		tmplID := req.GetString("type")
		if tmplID == "" {
			w.RespondError(req.ID, "type is required")
			return
		}
		tmpl, known := config.TemplateByID(tmplID)
		if !known {
			w.RespondError(req.ID, "unknown provider type: "+tmplID)
			return
		}

		baseURL := strings.TrimRight(strings.TrimSpace(req.GetString("baseUrl")), "/")
		if baseURL == "" {
			baseURL = tmpl.BaseURL
		}
		if baseURL == "" {
			w.RespondError(req.ID, "this provider needs an endpoint URL")
			return
		}

		apiKey := strings.TrimSpace(req.GetString("apiKey"))
		if apiKey == "" && !config.IsLocalURL(baseURL) {
			w.RespondError(req.ID, "fill in the API key before adding")
			return
		}

		// The id is the type name; an existing one gets a numeric suffix, so two OpenAI-compatible
		// endpoints can coexist.
		id := tmplID
		for n := 2; ; n++ {
			if _, exists := cfg.Providers[id]; !exists {
				break
			}
			id = fmt.Sprintf("%s-%d", tmplID, n)
		}

		// Models ticked in the add dialog are written in one go; the rest can still be enabled from the
		// provider's detail page.
		//
		// Both shapes are accepted: a string (older UI) and {id, label} (which now carries the upstream
		// display name). label is the large line in the UI; without it, it equals id.
		models := map[string]config.ModelEntry{}
		if list, ok := req.Get("models").([]any); ok {
			for _, item := range list {
				switch v := item.(type) {
				case string:
					if id := strings.TrimSpace(v); id != "" {
						models[id] = config.ModelEntry{Label: id, Enabled: true}
					}
				case map[string]any:
					id, _ := v["id"].(string)
					id = strings.TrimSpace(id)
					if id == "" {
						continue
					}
					label, _ := v["label"].(string)
					if strings.TrimSpace(label) == "" {
						label = id
					}
					models[id] = config.ModelEntry{Label: label, Enabled: true}
				}
			}
		}

		label := strings.TrimSpace(req.GetString("label"))
		if label == "" {
			label = tmpl.Label
		}

		cfg.Providers[id] = config.Provider{
			Type:         tmpl.Type,
			NativeSearch: nativeSearchParam(req),
			Label:        label,
			Icon:         tmpl.Icon,
			BaseURL:      baseURL,
			APIKey:       apiKey,
			APIKeyEnv:    defaultEnvFor(tmplID),
			Models:       models,
			FetchedAt:    time.Now().UnixMilli(),
		}
		cfg.FixDefaults()
		if err := cfg.Save(); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		deps.Agent.SetConfig(cfg)
		w.Respond(req.ID, map[string]any{"id": id, "config": configView(deps)})

	case "provider.update":
		id := req.GetString("id")
		cfg, err := config.Load()
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		p, ok := cfg.Providers[id]
		if !ok {
			w.RespondError(req.ID, "provider not found: "+id)
			return
		}
		if v, present := req.Params["label"]; present {
			if s, _ := v.(string); true {
				p.Label = strings.TrimSpace(s)
			}
		}
		if v, present := req.Params["baseUrl"]; present {
			if s, _ := v.(string); true {
				u := strings.TrimRight(strings.TrimSpace(s), "/")
				if u == "" {
					w.RespondError(req.ID, "the endpoint cannot be empty")
					return
				}
				p.BaseURL = u
			}
		}
		// Only touched when the field is present. An empty string does **not** clear it: in edit mode the
		// UI has no plaintext key and easily submits an empty field, and treating that as "clear" would
		// destroy a stored key while the user was only pressing "fetch models". Use clearApiKey: true to
		// clear it.
		if _, present := req.Params["apiKey"]; present {
			if key := strings.TrimSpace(req.GetString("apiKey")); key != "" {
				p.APIKey = key
			}
		}
		if req.GetBool("clearApiKey") {
			p.APIKey = ""
		}
		if v, present := req.Params["nativeSearch"]; present {
			if b, ok := v.(bool); ok {
				p.NativeSearch = &b
			}
		}
		cfg.Providers[id] = p
		if err := cfg.Save(); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		deps.Agent.SetConfig(cfg)
		w.Respond(req.ID, configView(deps))

	case "provider.remove":
		id := req.GetString("id")
		cfg, err := config.Load()
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		if _, ok := cfg.Providers[id]; !ok {
			w.RespondError(req.ID, "provider not found: "+id)
			return
		}
		delete(cfg.Providers, id)
		// If the default model points at a deleted provider, pick another.
		if pid, _ := config.SplitModelID(cfg.DefaultModel); pid == id {
			cfg.DefaultModel = ""
		}
		cfg.FixDefaults()
		if err := cfg.Save(); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		deps.Agent.SetConfig(cfg)
		w.Respond(req.ID, configView(deps))

	case "provider.fetchModels":
		id := req.GetString("id")
		cfg, err := config.Load()
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		p, ok := cfg.Providers[id]
		if !ok {
			w.RespondError(req.ID, "provider not found: "+id)
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		infos, err := llm.FetchModels(ctx, p)
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}

		// Merge: existing switches and prices are kept, new models start disabled. Models missing from
		// this response are kept too — endpoints under-report now and then, and that must not drop the
		// user's selection.
		if p.Models == nil {
			p.Models = map[string]config.ModelEntry{}
		}
		for _, info := range infos {
			if cur, exists := p.Models[info.ID]; exists {
				// The id used to be stored as the name. When upstream now provides a display name (OpenRouter
				// style) it is filled in — only where the name was still the id, never over a name the user
				// chose.
				if info.Name != "" && cur.Label == info.ID {
					cur.Label = info.Title()
				}
				// Limits fill blanks and never overwrite a number that is already there: the hand-set value
				// is the one that keeps a request legal when the endpoint under-reports.
				if cur.Context == 0 && info.Context > 0 {
					cur.Context = info.Context
				}
				if cur.MaxOutput == 0 && info.MaxOutput > 0 {
					cur.MaxOutput = info.MaxOutput
				}
				p.Models[info.ID] = cur
				continue
			}
			p.Models[info.ID] = config.ModelEntry{
				Label:     info.Title(),
				Enabled:   false,
				Context:   info.Context,
				MaxOutput: info.MaxOutput,
			}
		}
		p.FetchedAt = time.Now().UnixMilli()
		cfg.Providers[id] = p
		if err := cfg.Save(); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		deps.Agent.SetConfig(cfg)
		w.Respond(req.ID, map[string]any{"fetched": len(infos), "config": configView(deps)})

	case "provider.setModel":
		id := req.GetString("id")
		model := req.GetString("model")
		cfg, err := config.Load()
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		p, ok := cfg.Providers[id]
		if !ok {
			w.RespondError(req.ID, "provider not found: "+id)
			return
		}
		entry, ok := p.Models[model]
		if !ok {
			w.RespondError(req.ID, "model not found: "+model)
			return
		}
		entry.Enabled = req.GetBool("enabled")
		p.Models[model] = entry
		cfg.Providers[id] = p
		cfg.FixDefaults()
		if err := cfg.Save(); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		deps.Agent.SetConfig(cfg)
		w.Respond(req.ID, configView(deps))

	// --- MCP (external tool servers) ---
	//
	// Save / delete / toggle always Sync: servers are processes, so a settings change has to actually
	// start or stop them instead of waiting for a restart.
	case "mcp.save":
		cfg, err := config.Load()
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		command := strings.TrimSpace(req.GetString("command"))
		rawURL := strings.TrimSpace(req.GetString("url"))
		if command == "" && rawURL == "" {
			w.RespondError(req.ID, "give a launch command (local server) or a server URL (remote HTTP)")
			return
		}
		// Only one of the two: a url means no command, or which one it connects to is ambiguous.
		if rawURL != "" {
			command = ""
		}
		if cfg.MCPServers == nil {
			cfg.MCPServers = map[string]config.MCPServer{}
		}

		id := strings.TrimSpace(req.GetString("id"))
		existing, editing := cfg.MCPServers[id]
		label := strings.TrimSpace(req.GetString("label"))

		if !editing {
			id = newMCPID(cfg, label, mcpIDHint(command, rawURL))
			if label == "" {
				label = id
			}
		} else if label == "" {
			label = existing.Label
		}

		// A blank argument is usually a stray Enter in the form, not an intentional empty string.
		args := make([]string, 0, len(req.GetStrings("args")))
		for _, a := range req.GetStrings("args") {
			if strings.TrimSpace(a) != "" {
				args = append(args, a)
			}
		}

		env := existing.Env
		if req.Get("env") != nil {
			env = mergeMCPEnv(existing.Env, req.Get("env"))
		}

		disabled := existing.Disabled
		if req.Get("disabled") != nil {
			disabled = req.GetBool("disabled")
		}
		autoApprove := existing.AutoApprove
		if req.Get("autoApprove") != nil {
			autoApprove = req.GetBool("autoApprove")
		}

		// Tool filters follow the same rule as env: absent keeps, present replaces the whole set (the UI
		// sends the complete list it drew).
		includeTools := existing.IncludeTools
		if req.Get("includeTools") != nil {
			includeTools = cleanNames(req.GetStrings("includeTools"))
		}
		excludeTools := existing.ExcludeTools
		if req.Get("excludeTools") != nil {
			excludeTools = cleanNames(req.GetStrings("excludeTools"))
		}

		cfg.MCPServers[id] = config.MCPServer{
			Label:        label,
			URL:          rawURL,
			Command:      command,
			Args:         args,
			Env:          env,
			Disabled:     disabled,
			AutoApprove:  autoApprove,
			IncludeTools: includeTools,
			ExcludeTools: excludeTools,
		}
		if err := cfg.Save(); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		deps.Agent.SetConfig(cfg)
		deps.Config = cfg
		if deps.MCP != nil {
			deps.MCP.Sync(cfg)
		}
		w.Respond(req.ID, configView(deps))

	case "mcp.remove":
		id := strings.TrimSpace(req.GetString("id"))
		cfg, err := config.Load()
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		if _, ok := cfg.MCPServers[id]; !ok {
			w.RespondError(req.ID, "unknown MCP server: "+id)
			return
		}
		delete(cfg.MCPServers, id)
		if err := cfg.Save(); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		deps.Agent.SetConfig(cfg)
		deps.Config = cfg
		if deps.MCP != nil {
			deps.MCP.Sync(cfg)
		}
		w.Respond(req.ID, configView(deps))

	case "mcp.toggle":
		id := strings.TrimSpace(req.GetString("id"))
		cfg, err := config.Load()
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		srv, ok := cfg.MCPServers[id]
		if !ok {
			w.RespondError(req.ID, "unknown MCP server: "+id)
			return
		}
		srv.Disabled = !req.GetBool("enabled")
		cfg.MCPServers[id] = srv
		if err := cfg.Save(); err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		deps.Agent.SetConfig(cfg)
		deps.Config = cfg
		if deps.MCP != nil {
			deps.MCP.Sync(cfg)
		}
		w.Respond(req.ID, configView(deps))

	// Restart means try again. A failed server is never retried automatically (that is an endless
	// restart loop), so the user gets an explicit button.
	case "mcp.restart":
		id := strings.TrimSpace(req.GetString("id"))
		if deps.MCP == nil {
			w.RespondError(req.ID, "MCP is not running")
			return
		}
		if _, ok := deps.Config.MCPServers[id]; !ok {
			w.RespondError(req.ID, "unknown MCP server: "+id)
			return
		}
		deps.MCP.Restart(id, deps.Config)
		w.Respond(req.ID, configView(deps))

	// Syntax highlighting: chroma runs in the daemon and emits HTML with inline styles, which QML's
	// TextEdit takes as rich text. It lives here because QML has no parser and Go has one.
	case "code.highlight":
		lang := req.GetString("lang")
		code := req.GetString("code")
		html, err := highlight.HTML(lang, code, req.GetBool("light"))
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		// debug level: only needed when someone asks "why is my code not coloured".
		applog.Debugf("highlight lang=%q %d bytes -> %d bytes", lang, len(code), len(html))
		w.Respond(req.ID, map[string]any{"html": html})

	case "skills.list":
		scan, err := skills.ScanSkills()
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		w.Respond(req.ID, map[string]any{
			"dir":     scan.Dir,
			"skills":  scan.Skills,
			"skipped": scan.Skipped,
		})

	case "files.list", "files.watch", "files.unwatch", "files.userDirs", "files.count":
		if deps.Files == nil {
			w.RespondError(req.ID, "file service unavailable")
			return
		}
		deps.Files.Handle(w, req)

	default:
		w.RespondError(req.ID, "unknown method: "+req.Method)
	}
}

func defaultEnvFor(templateID string) string {
	switch templateID {
	case "deepseek":
		return "DEEPSEEK_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "google":
		return "GEMINI_API_KEY"
	case "openrouter":
		return "OPENROUTER_API_KEY"
	}
	return ""
}

func infoOf(deps Deps) map[string]any {
	cfg := deps.Agent.Config()
	return map[string]any{
		"appName":          "dcode",
		"version":          deps.Version,
		"apiVersion":       APIVersion,
		"capabilities":     Capabilities(),
		"models":           enabledModelViews(cfg),
		"defaultModel":     cfg.DefaultModel,
		"workingDirectory": cfg.WorkingDirectory,
	}
}

// enabledModelViews is what the home model picker offers: the models that are switched on.
// taskView flattens a task into the shape the UI draws.
//
// nextRunAt is computed here rather than in the UI: it means "the next one after the last run",
// catch-up semantics included, and two implementations would drift apart.
func taskView(deps Deps, t tasks.Task) map[string]any {
	// Disabled or manual-only tasks have no "next". Computing one anyway returns a past time (the
	// baseline is the last run), and the UI would show "next: yesterday", which looks broken.
	var nextMs int64
	if t.Enabled && t.Schedule.Kind != tasks.ScheduleManual {
		if next := t.NextRun(time.Now()); !next.IsZero() {
			nextMs = next.UnixMilli()
		}
	}
	return map[string]any{
		"id":               t.ID,
		"name":             t.Name,
		"prompt":           t.Prompt,
		"kind":             t.Schedule.Kind,
		"at":               t.Schedule.At,
		"minutes":          t.Schedule.Minutes,
		"weekday":          t.Schedule.Weekday,
		"model":            t.Model,
		"workingDirectory": t.WorkingDirectory,
		"autoApprove":      t.AutoApprove,
		"enabled":          t.Enabled,
		"lastRunAt":        t.LastRunAt,
		"lastSessionId":    t.LastSession,
		"lastStatus":       t.LastStatus,
		"lastError":        t.LastError,
		"nextRunAt":        nextMs,
		"running":          deps.TaskMgr != nil && deps.TaskMgr.IsRunning(t.ID),
	}
}

func taskViews(deps Deps) []map[string]any {
	if deps.Tasks == nil {
		return []map[string]any{}
	}
	list := deps.Tasks.List()
	out := make([]map[string]any, 0, len(list))
	for _, t := range list {
		out = append(out, taskView(deps, t))
	}
	return out
}

func enabledModelViews(cfg *config.Config) []map[string]any {
	models := cfg.EnabledModels()
	out := make([]map[string]any, 0, len(models))
	for _, m := range models {
		// null and [] are different things to QML, so always hand back an empty array: the UI checks
		// length > 0 and should not have to handle null.
		levels := m.Reasoning
		if levels == nil {
			levels = []string{}
		}
		out = append(out, map[string]any{
			"id":            m.ID,
			"label":         m.Label,
			"provider":      m.Provider,
			"model":         m.Model,
			"contextWindow": m.Context,
			// maxOutput is this model's own ceiling (0 = unknown), so the UI can say that a larger cap will
			// be lowered rather than silently truncate the reply.
			"maxOutput": m.MaxOutput,
			// reasoning lists the levels this model offers; the UI decides whether to show the menu and which
			// entries go in it.
			"reasoning": levels,
			"ready":     true,
		})
	}
	return out
}

func configView(deps Deps) map[string]any {
	cfg := deps.Agent.Config()
	providers := make([]map[string]any, 0, len(cfg.Providers))
	for _, id := range cfg.ProviderIDs() {
		p := cfg.Providers[id]
		key, source := p.ResolveKey()

		models := make([]map[string]any, 0, len(p.Models))
		for _, m := range cfg.AllModels() {
			if m.Provider != id {
				continue
			}
			models = append(models, map[string]any{
				"id":            m.Model,
				"globalId":      m.ID,
				"label":         m.Label,
				"enabled":       m.Enabled,
				"contextWindow": m.Context,
				"maxOutput":     m.MaxOutput,
			})
		}

		providers = append(providers, map[string]any{
			"id":           id,
			"label":        p.DisplayName(id),
			"icon":         p.Icon,
			"type":         p.Type,
			"baseUrl":      p.BaseURL,
			"hasKey":       key != "" || !p.NeedsKey(),
			"keySource":    source, // config / env:XXX / ""
			"ready":        p.Ready(),
			"fetchedAt":    p.FetchedAt,
			"enabledCount": countEnabled(models),
			"models":       models,
			// Every type currently has a client (openai / openai-responses / gemini / anthropic).
			"supported": true,
			// Built-in search: whether it can be used (type) plus whether it is on (settings).
			"canNativeSearch": config.SupportsNativeSearch(p.Type),
			"nativeSearch":    p.NativeSearch == nil || *p.NativeSearch,
		})
	}

	return map[string]any{
		"providers":        providers,
		"autostart":        autostart.Enabled(),
		"models":           enabledModelViews(cfg),
		"defaultModel":     cfg.DefaultModel,
		"workingDirectory": cfg.WorkingDirectory,
		"configPath":       config.Path(),
		"maxOutputTokens":  cfg.MaxOutputTokens,
		"maxToolSteps":     cfg.ToolStepLimit(),
		"compactAt":        cfg.CompactAt,
		// The threshold that will actually be used for the current model, which is lower when the setting
		// is above what the model can read. Sent so the UI can say "this is lowered" without repeating the
		// rule — and so it says nothing when nothing changes.
		"effectiveCompactAt": effectiveCompactAt(cfg),
		"approvalMode":       cfg.ApprovalMode,
		"tools": map[string]any{
			"fileAccess": cfg.Tools.FileEnabled(),
			"shell":      cfg.Tools.ShellEnabled(),
			"webSearch":  cfg.Tools.WebSearchEnabled(),
		},
		// The browser has no switch of its own (it shares the web toggle), but the UI still needs to know
		// where it is: resolved is what would actually run, and an empty value comes with a reason.
		"browser":     browserView(cfg),
		"search":      searchView(cfg),
		"searchKinds": search.Kinds(),
		"mcp":         mcpView(deps),
		// Whether this machine has bwrap. Without a sandbox, "inside the working directory" is only a
		// literal path check, and the UI has to say so rather than let the option look stricter than it
		// is.
		"sandbox": tools.SandboxAvailable(),
	}
}

// browserView reports which agent-browser executable would be used.
//
// An empty resolved comes with a reason. The switch lives with the web toggle, not here.
func browserView(cfg *config.Config) map[string]any {
	bin, reason := tools.ResolveAgentBrowser(cfg.Browser.Path)
	return map[string]any{
		"path":     cfg.Browser.Path,
		"resolved": bin,
		"reason":   reason,
	}
}

func countEnabled(models []map[string]any) int {
	n := 0
	for _, m := range models {
		if m["enabled"] == true {
			n++
		}
	}
	return n
}

// nativeSearchParam reads nativeSearch from the "add provider" payload. Absent means nil (use the
// default, on), so it does not have to be sent for every provider.
func nativeSearchParam(req ipc.Request) *bool {
	v, present := req.Params["nativeSearch"]
	if !present {
		return nil
	}
	if b, ok := v.(bool); ok {
		return &b
	}
	return nil
}

func listSessions(deps Deps) []map[string]any {
	all := deps.Store.List()
	out := make([]map[string]any, 0, len(all))
	for _, s := range all {
		out = append(out, map[string]any{
			"id":           s.ID,
			"title":        s.Title,
			"createdAt":    s.CreatedAt,
			"updatedAt":    s.UpdatedAt,
			"pinned":       s.Pinned,
			"messageCount": len(s.Messages),
			"preview":      previewOf(s),
			"busy":         deps.Agent.Busy(s.ID),
		})
	}
	return out
}

// previewOf takes the start of the last message for the sidebar's second line.
func previewOf(s store.Session) string {
	for i := len(s.Messages) - 1; i >= 0; i-- {
		text := strings.Join(strings.Fields(s.Messages[i].Text), " ")
		if text == "" {
			continue
		}
		runes := []rune(text)
		if len(runes) > 64 {
			return string(runes[:64]) + "…"
		}
		return text
	}
	return ""
}

func isLocalURL(u string) bool {
	return strings.Contains(u, "127.0.0.1") ||
		strings.Contains(u, "localhost") ||
		strings.Contains(u, "::1")
}

// TimeNow exists so the functions above can be handed a fake clock in tests.
var TimeNow = time.Now

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// exportSlug turns a title into a filename-safe string.
func exportSlug(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "session"
	}
	var b strings.Builder
	for _, r := range title {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		case r > 127:
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "session"
	}
	if len([]rune(out)) > 40 {
		out = string([]rune(out)[:40])
	}
	return out
}

// searchView is a search backend as the UI sees it.
//
// The key is never returned (only hasKey): the UI does not need it, and once it appears in an event
// stream or a log it is leaked.
// mcpView returns the MCP server list.
//
// env carries keys only, never values: the UI has no reason to hold the user's tokens, and returned
// values end up in logs and screenshots. Empty while editing keeps the existing one.
func mcpView(deps Deps) map[string]any {
	cfg := deps.Config
	servers := make([]map[string]any, 0)
	if cfg != nil {
		for _, id := range cfg.MCPIDs() {
			s := cfg.MCPServers[id]
			st := mcp.Status{Tools: []string{}}
			if deps.MCP != nil {
				st = deps.MCP.Status(id)
			}
			label := s.Label
			if label == "" {
				label = id
			}
			envKeys := make([]string, 0, len(s.Env))
			for k := range s.Env {
				envKeys = append(envKeys, k)
			}
			sort.Strings(envKeys)
			args := s.Args
			if args == nil {
				args = []string{}
			}
			servers = append(servers, map[string]any{
				"id":           id,
				"label":        label,
				"url":          s.URL,
				"command":      s.Command,
				"args":         args,
				"envKeys":      envKeys,
				"includeTools": orEmpty(s.IncludeTools),
				"excludeTools": orEmpty(s.ExcludeTools),
				"disabled":     s.Disabled,
				"autoApprove":  s.AutoApprove,
				"running":      st.Running,
				"starting":     st.Starting,
				"toolCount":    st.ToolCount,
				"tools":        st.Tools,
				"error":        st.Error,
			})
		}
	}
	enabled := 0
	if cfg != nil {
		enabled = len(cfg.EnabledMCPIDs())
	}
	return map[string]any{"servers": servers, "enabled": enabled}
}

// newMCPID derives a stable id from the name.
//
// The id appears in tool names (mcp__<id>__<tool>), so it must be ASCII: a CJK name yields nothing,
// and the fallback to the command's basename is at least readable.
func newMCPID(cfg *config.Config, label, hint string) string {
	base := slug(label)
	if base == "" {
		base = slug(strings.TrimSuffix(filepath.Base(hint), ".exe"))
	}
	if base == "" {
		base = "server"
	}
	id := base
	for i := 2; ; i++ {
		if _, taken := cfg.MCPServers[id]; !taken {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, i)
	}
}

// mcpIDHint gives newMCPID something to guess from.
//
// A stdio server uses the command basename; a remote one has no command, so it uses the url host
// (mcp.example.com reads better and shorter than https-mcp-example-com-sse).
func mcpIDHint(command, rawURL string) string {
	if command != "" {
		return command
	}
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return rawURL
}

// cleanNames drops blank entries (a stray Enter in the form leaves empty strings).
func cleanNames(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// orEmpty makes a nil slice [] in JSON instead of null, so the UI needs no check at every use.
func orEmpty(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r == ' ' || r == '-' || r == '_' || r == '.' || r == '/':
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 24 {
		out = strings.TrimRight(out[:24], "-")
	}
	return out
}

// mergeMCPEnv layers the form's KEY→VALUE over the old map.
//
// Same rule as apiKey: an empty value keeps the existing one, and a new key with an empty value is
// ignored. A key missing from the input counts as deleted — the UI sends the complete list it
// drew.
func mergeMCPEnv(base map[string]string, raw any) map[string]string {
	in, ok := raw.(map[string]any)
	if !ok {
		return base
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		val, _ := v.(string)
		val = strings.TrimSpace(val)
		if val == "" {
			if old, has := base[key]; has {
				out[key] = old
			}
			continue
		}
		out[key] = val
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func searchView(cfg *config.Config) map[string]any {
	ids := make([]string, 0, len(cfg.Search))
	for id := range cfg.Search {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		p := cfg.Search[id]
		out = append(out, map[string]any{
			"id":         id,
			"type":       p.Type,
			"label":      p.Label,
			"baseUrl":    p.BaseURL,
			"hasKey":     p.APIKey != "",
			"enabled":    p.Enabled,
			"maxResults": p.MaxResults,
			"active":     id == cfg.SearchDefault,
		})
	}
	return map[string]any{"providers": out, "default": cfg.SearchDefault}
}

// effectiveCompactAt is the configured threshold as the agent will apply it to the current model.
// 0 means compaction is off or there is nothing to lower.
func effectiveCompactAt(cfg *config.Config) int {
	model, _, ok := cfg.Resolve("")
	if !ok {
		return cfg.CompactAt
	}
	return cfg.CompactThreshold(model)
}
