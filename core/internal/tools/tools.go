// Package tools is the registry of local tools.
//
// Design rules:
//   - a tool knows only its context, its arguments (a raw JSON string) and an output string; how to
//     display it and whether to ask for permission is the agent's / UI's business
//   - unknown tools and broken arguments return an error **string** rather than a Go error, because it
//     goes back to the model verbatim so the model can correct itself
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"sort"

	"github.com/NekoLOvO/dux-code/core/internal/applog"
	"github.com/NekoLOvO/dux-code/core/internal/llm"
)

// Tool is something the model can call.
type Tool interface {
	// Spec is the spec the model sees (name, description, JSON Schema).
	Spec() llm.Tool
	// Run executes it. args is the raw JSON string the model emitted.
	Run(ctx context.Context, args string) (string, error)
}

// Registry collects tools. Later they come from settings and MCP; this is the placeholder.
type Registry struct {
	list map[string]Tool
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{list: make(map[string]Tool)}
}

// Add registers a tool; a duplicate name is overwritten (later sources win).
func (r *Registry) Add(t Tool) {
	if r == nil || t == nil {
		return
	}
	r.list[t.Spec().Function.Name] = t
}

// Get returns one tool.
func (r *Registry) Get(name string) (Tool, bool) {
	if r == nil {
		return nil, false
	}
	t, ok := r.list[name]
	return t, ok
}

// Specs returns every tool spec, sorted by name (a stable order for the model).
func (r *Registry) Specs() []llm.Tool {
	if r == nil || len(r.list) == 0 {
		return nil
	}
	names := make([]string, 0, len(r.list))
	for n := range r.list {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]llm.Tool, 0, len(names))
	for _, n := range names {
		out = append(out, r.list[n].Spec())
	}
	return out
}

// Run executes a tool. Any failure becomes a string for the model; only "no such tool" returns an
// error (that is our bug, not the model's).
func (r *Registry) Run(ctx context.Context, name, args string) (out string, err error) {
	t, ok := r.Get(name)
	if !ok {
		return fmt.Sprintf("no such tool: %s", name), nil
	}
	if args == "" {
		args = "{}"
	}

	// Tools run on the run's goroutine, so a panic would take the daemon down. One badly written tool must
	// not make the user's "settings suddenly vanish".
	defer func() {
		if rec := recover(); rec != nil {
			applog.Errorf("tool %s panicked: %v\n%s", name, rec, debug.Stack())
			out = fmt.Sprintf("tool %s failed internally: %v", name, rec)
			err = nil
		}
	}()

	res, runErr := t.Run(ctx, args)
	if runErr != nil {
		return fmt.Sprintf("tool %s failed: %v", name, runErr), nil
	}
	return res, nil
}

// decodeArgs decodes JSON arguments into dest, turning errors into text the model understands.
func decodeArgs(args string, dest any) error {
	if err := json.Unmarshal([]byte(args), dest); err != nil {
		return fmt.Errorf("the arguments are not valid JSON (%v); received: %s", err, args)
	}
	return nil
}
