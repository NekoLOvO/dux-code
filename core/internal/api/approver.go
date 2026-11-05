package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/NekoLOvO/dux-code/core/internal/config"
	"github.com/NekoLOvO/dux-code/core/internal/ipc"
	"github.com/NekoLOvO/dux-code/core/internal/search"
	"github.com/NekoLOvO/dux-code/core/internal/tasks"
	"github.com/NekoLOvO/dux-code/core/internal/tools"
)

// permissionTimeout caps how long a permission question waits for an answer.
//
// Generous because the user may be looking at something else; this is not about performance but
// about not condemning a tool because they came back three seconds late. A real disconnect goes
// through Subscriber's closedCh, so nothing waits blindly.
const permissionTimeout = 5 * time.Minute

// subApprover turns the tools layer's "allow this?" into one reverse IPC request.
//
// It only knows there is a connection to ask on, not what the UI looks like — replacing the QML,
// opening a second window, or moving to a phone is not its business.
type subApprover struct {
	sub *ipc.Subscriber
}

// Ask implements tools.Approver.
func (a subApprover) Ask(ctx context.Context, req tools.Approval) (bool, error) {
	if a.sub == nil {
		return false, ipc.ErrNoClient
	}

	res, err := a.sub.Request(ctx, "permission.request", map[string]any{
		"kind":   req.Kind,
		"title":  req.Title,
		"detail": req.Detail,
		"paths":  req.Paths,
	}, permissionTimeout)
	if err != nil {
		return false, err
	}

	m, ok := res.(map[string]any)
	if !ok {
		return false, fmt.Errorf("malformed permission response")
	}
	if allow, _ := m["allow"].(bool); allow {
		return true, nil
	}
	if reason, _ := m["reason"].(string); reason != "" {
		return false, fmt.Errorf("the user denied it: %s", reason)
	}
	return false, nil
}

// TaskEnvFactory returns the tool environment factory used by scheduled runs.
//
// A scheduled run usually has no window attached, so "nobody to ask" is the normal case: a task
// with autoApprove goes through ApprovalAuto (never asks), the rest keep the usual rules (sub ==
// nil → Ask returns ErrNoClient → the tool is refused). A task without autoApprove is therefore
// read-only, which is safer than approving by default.
//
// Parameters are getters rather than Deps: Deps must exist before an env can be built, while the
// Manager has to be created before Deps (it is attached to the agent), so values are read late.
func TaskEnvFactory(get func() Deps) tasks.EnvFactory {
	return func(sessionID string, autoApprove bool) tools.Env {
		env := get().toolEnv(sessionID, nil)
		if autoApprove {
			env.Approval = tools.ApprovalAuto
		}
		return env
	}
}

// toolEnv builds the tool environment for one run.
//
// The root prefers the session's own workingDirectory: sessions may live in different projects,
// and a global setting would point the tools at the wrong place after a session switch.
//
// Approval mode stays global (config.ApprovalMode): it expresses "how far I trust this", which
// follows the person rather than the project, and per-session rules would be hard to predict.
func (d Deps) toolEnv(sessionID string, sub *ipc.Subscriber) tools.Env {
	root := ""
	if sess, ok := d.Store.Get(sessionID); ok {
		root = sess.WorkingDirectory
	}
	if root == "" {
		root = d.Agent.Config().WorkingDirectory
	}

	mode := approvalModeOf(d.Agent.Config().ApprovalMode)

	return tools.Env{
		Root:        root,
		Approve:     subApprover{sub: sub},
		Ask:         subAsker{sub: sub},
		Search:      searchAdapter{cfg: d.Agent.Config()},
		Approval:    mode,
		BrowserPath: d.Agent.Config().Browser.Path,
	}
}

// searchAdapter connects the configured search backend to the tools layer.
//
// The backend is chosen per call, not pinned at startup: change it or turn it off in the settings
// and the next message uses that.
type searchAdapter struct {
	cfg *config.Config
}

func (a searchAdapter) Search(ctx context.Context, query string, limit int) ([]tools.WebResult, error) {
	if a.cfg == nil {
		return nil, errors.New("no search backend is available")
	}
	_, p, ok := a.cfg.ActiveSearch()
	if !ok {
		return nil, errors.New("no search backend is available")
	}

	client := search.Client{Kind: p.Type, BaseURL: p.BaseURL, APIKey: p.APIKey}
	raw, err := client.Search(ctx, query, limit)
	if err != nil {
		return nil, err
	}

	out := make([]tools.WebResult, 0, len(raw))
	for _, r := range raw {
		out = append(out, tools.WebResult{Title: r.Title, URL: r.URL, Snippet: r.Snippet})
	}
	return out, nil
}

// subAsker turns "the model asks the user" into one reverse IPC request too.
//
// No timeout, unlike approvals: the user may be busy elsewhere, and declaring it dead after five
// minutes only lets the model continue on a fake answer. Only two ways out — they answer, or they
// press stop (ctx cancels).
type subAsker struct {
	sub *ipc.Subscriber
}

func (a subAsker) Ask(ctx context.Context, q tools.Question) (string, error) {
	if a.sub == nil {
		return "", ipc.ErrNoClient
	}

	options := make([]map[string]any, 0, len(q.Options))
	for _, o := range q.Options {
		options = append(options, map[string]any{
			"label":       o.Label,
			"description": o.Description,
		})
	}

	res, err := a.sub.Request(ctx, "question.request", map[string]any{
		"question": q.Question,
		"options":  options,
	}, 0)
	if err != nil {
		return "", err
	}

	m, ok := res.(map[string]any)
	if !ok {
		return "", fmt.Errorf("malformed answer response")
	}
	answer, _ := m["answer"].(string)
	return answer, nil
}

// approvalModeOf maps the config string onto the tools layer's policy.
//
// An empty string (older config) and unreadable values both fall back to "ask every time": this
// value decides whether tools may touch files by themselves, so a wrong guess may only be
// stricter.
func approvalModeOf(s string) tools.ApprovalMode {
	switch tools.ApprovalMode(s) {
	case tools.ApprovalAuto, tools.ApprovalWorkspace:
		return tools.ApprovalMode(s)
	default:
		return tools.ApprovalAsk
	}
}
