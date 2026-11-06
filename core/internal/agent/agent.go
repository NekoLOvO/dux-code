// Package agent is the lifetime of one "send a message → stream a reply".
//
// This layer:
//   - creates a run, assigns a runID, manages cancellation
//   - throttles deltas onto the EventBus (the UI subscribes to one topic: "agent")
//   - persists the assistant message when the turn ends
//
// It knows neither what the UI looks like nor the ipc wire format — only the EventBus.
package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/NekoLOvO/dux-code/core/internal/applog"
	"github.com/NekoLOvO/dux-code/core/internal/config"
	"github.com/NekoLOvO/dux-code/core/internal/ipc"
	"github.com/NekoLOvO/dux-code/core/internal/llm"
	"github.com/NekoLOvO/dux-code/core/internal/mcp"
	"github.com/NekoLOvO/dux-code/core/internal/store"
	"github.com/NekoLOvO/dux-code/core/internal/tools"
)

// Topic is the single topic for agent events.
const Topic = "agent"

// flushInterval is how often deltas are merged.
// 50ms is about the point where motion still reads as motion, and it caps IPC traffic at 20 lines per
// second.
const flushInterval = 50 * time.Millisecond

// Runner manages every run in flight.
type Runner struct {
	bus   *ipc.EventBus
	store *store.Store
	cfg   *config.Config
	tools *tools.Registry
	// mcp is the external tool servers. nil means no MCP (tests, other callers).
	mcp *mcp.Manager

	mu   sync.Mutex
	runs map[string]*run

	// cfgMu guards swapping cfg (on config.reload).
	cfgMu sync.RWMutex

	// OnEvent is the in-process side channel for observers (the task scheduler learns when a turn
	// ended). Unlike the EventBus, which delivers to connections, this is for the daemon itself. It runs
	// on the publishing goroutine, so it must not block.
	OnEvent func(map[string]any)
}

type run struct {
	id        string
	sessionID string
	startedAt time.Time
	cancel    context.CancelFunc
	ctx       context.Context
	// done is closed when execute has returned, i.e. when this run will never write to the transcript
	// again. A new turn waits on it: writing a user message while a run is still finishing a tool call
	// puts that call's result after the message, and no provider accepts that transcript.
	done chan struct{}
	// env is this turn's tool environment (cwd boundary + approver + skill state). It is bound to the
	// connection that started the turn, not to global settings.
	env tools.Env

	// reasoning is this turn's depth (empty sends nothing, provider decides). It lives on the run rather
	// than being passed per Request: a turn with tool steps sends several requests, and every step must
	// use the same depth. The value went through config.NormalizeReasoning, so it is supported.
	reasoning string

	// nativeSearch: whether this turn uses the provider's built-in search. Fixed when the run starts,
	// since a run never changes provider.
	nativeSearch bool

	// The text of the step in flight, as it is being written.
	//
	// The transcript only ever holds finished steps ("only finished content is written"), so a window
	// that opens or comes back to this session mid-reply has nothing to draw for the step running right
	// now: it shows the user message and an empty bubble until run.finished. This is what fills that
	// gap — session.get reports it so the UI can start the bubble with the prefix already written.
	//
	// Reset at the start of every attempt, because the UI opens a new bubble per step and a retried
	// attempt writes its text from the beginning again.
	partMu    sync.Mutex
	partText  strings.Builder
	partThink strings.Builder

	// Skill state. An activated skill may narrow which tools later steps may use, while the registry is
	// shared by the whole daemon — changing it would pollute other running sessions, so the restriction
	// stays on the run.
	skillMu sync.Mutex
	skills  []string
	allowed map[string]bool // nil means unrestricted
}

// Activate implements tools.SkillRuntime.
func (rn *run) Activate(name string, allowed []string) {
	rn.skillMu.Lock()
	defer rn.skillMu.Unlock()

	for _, s := range rn.skills {
		if s == name {
			return // the same skill twice is not recorded twice
		}
	}
	rn.skills = append(rn.skills, name)

	if len(allowed) == 0 {
		return
	}
	if rn.allowed == nil {
		rn.allowed = make(map[string]bool, len(allowed))
	}
	// With several skills each contributing a list, take the union — otherwise the second skill would
	// remove what the first allowed.
	for _, t := range allowed {
		rn.allowed[t] = true
	}
}

// allowedSpecs returns the tool specs usable right now.
func (r *Runner) allowedSpecs(rn *run, cfg *config.Config) []llm.Tool {
	all := r.tools.Specs()

	// MCP tools are recomputed every time: a server may have just finished starting, or just changed its
	// list. The manager already filters to "tools of enabled servers", so toolAllowed is not applied
	// again.
	for _, t := range r.mcpTools() {
		all = append(all, t.Spec())
	}

	// A tool the user switched off must not even be listed. Rejecting at call time is not enough: only an
	// invisible tool stops the model from trying.
	filtered := make([]llm.Tool, 0, len(all))
	for _, s := range all {
		// Built-in search wins: keeping the local one would only make the model pick wrong and spend an
		// extra tool round trip.
		if s.Function.Name == "web_search" && rn.nativeSearch {
			continue
		}
		if toolAllowed(s.Function.Name, cfg) {
			filtered = append(filtered, s)
		}
	}
	all = filtered

	rn.skillMu.Lock()
	allowed := rn.allowed
	rn.skillMu.Unlock()
	if allowed == nil {
		return all
	}

	out := make([]llm.Tool, 0, len(all))
	for _, s := range all {
		// load_skill is always kept: narrowing tools is about focus, not about locking the model into one
		// skill.
		if s.Function.Name == "load_skill" || allowed[s.Function.Name] {
			out = append(out, s)
		}
	}
	return out
}

// New builds a runner.
func New(bus *ipc.EventBus, st *store.Store, cfg *config.Config) *Runner {
	reg := tools.NewRegistry()
	// No side effects: available at any time.
	reg.Add(tools.Echo{})
	reg.Add(tools.Clock{})
	// Read-only: no need to ask either.
	reg.Add(tools.ReadFile{})
	reg.Add(tools.Glob{})
	reg.Add(tools.Grep{})
	// Skills: prose only, no side effects.
	reg.Add(tools.LoadSkill{})
	// Asking the user back: no side effect, but someone must be there.
	reg.Add(tools.AskUser{})
	// Web: search needs a backend, page fetch does not (both obey the composer's switch).
	reg.Add(tools.WebSearch{})
	reg.Add(tools.WebFetch{})
	// Browser: registered unconditionally; toolAllowed and Run decide whether it is usable.
	//
	// Tying registration to startup state would force a daemon restart after the user picks a path in
	// settings; computing it per request makes the next message pick it up.
	reg.Add(&tools.Browser{})
	// Anything touching files goes through Env.Approve; no approver means refusal.
	reg.Add(tools.WriteFile{})
	reg.Add(tools.EditFile{})
	reg.Add(tools.Shell{})
	return &Runner{bus: bus, store: st, cfg: cfg, tools: reg, runs: make(map[string]*run)}
}

// SetMCP attaches the external tool servers.
//
// MCP tools stay out of the registry: that holds built-ins whose lifetime is the daemon's, while MCP
// tools change with settings and server state. Stuffing them in would mean solving removal.
func (r *Runner) SetMCP(m *mcp.Manager) {
	r.mcp = m
}

// SetTools replaces the tool registry (for later settings / MCP work).
func (r *Runner) SetTools(reg *tools.Registry) {
	r.tools = reg
}

// Config returns the current settings (used by the api layer).
func (r *Runner) Config() *config.Config {
	r.cfgMu.RLock()
	defer r.cfgMu.RUnlock()
	return r.cfg
}

// SetConfig swaps the settings.
func (r *Runner) SetConfig(cfg *config.Config) {
	r.cfgMu.Lock()
	defer r.cfgMu.Unlock()
	r.cfg = cfg
}

// Busy reports whether a session has a run going.
func (r *Runner) Busy(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rn := range r.runs {
		if rn.sessionID == sessionID {
			return true
		}
	}
	return false
}

// RunningText reports the step a session is writing right now: the text and the thinking that the
// transcript does not have yet, because neither is persisted until the step ends.
//
// It is the same run Busy reports, so a caller can ask both and get an answer about one turn. Empty
// strings mean "nothing written yet" and "not running" alike; the caller already knows which from Busy.
func (r *Runner) RunningText(sessionID string) (text, reasoning string) {
	r.mu.Lock()
	var rn *run
	for _, candidate := range r.runs {
		if candidate.sessionID == sessionID {
			rn = candidate
			break
		}
	}
	r.mu.Unlock()
	if rn == nil {
		return "", ""
	}
	rn.partMu.Lock()
	defer rn.partMu.Unlock()
	return rn.partText.String(), rn.partThink.String()
}

// beginAttempt clears the in-flight buffers. Called once per model attempt: a retry rewrites the text
// from the start, and the UI would otherwise be handed the same sentence twice.
func (rn *run) beginAttempt() {
	rn.partMu.Lock()
	rn.partText.Reset()
	rn.partThink.Reset()
	rn.partMu.Unlock()
}

func (rn *run) addPart(text string) {
	if text == "" {
		return
	}
	rn.partMu.Lock()
	rn.partText.WriteString(text)
	rn.partMu.Unlock()
}

func (rn *run) addThink(text string) {
	if text == "" {
		return
	}
	rn.partMu.Lock()
	rn.partThink.WriteString(text)
	rn.partMu.Unlock()
}

// prepare resolves the model and builds a client. Shared by Send and Regenerate.
//
// It does not judge whether the session is already running: a new turn stops the previous one and
// waits for it (see stopSession). Refusing instead was what broke "stop, then say something else" —
// the UI cancels and sends at once, and the send arrived while the old turn was still putting its
// tool result away.
func (r *Runner) prepare(sessionID, modelID string) (llm.Client, config.Resolved, config.Provider, error) {
	cfg := r.Config()
	if _, ok := r.store.Get(sessionID); !ok {
		return nil, config.Resolved{}, config.Provider{}, errors.New("session not found")
	}

	resolved, provider, ok := cfg.Resolve(modelID)
	if !ok {
		return nil, config.Resolved{}, config.Provider{}, errors.New("unknown model: " + modelID)
	}
	client, err := llm.New(provider)
	if err != nil {
		if errors.Is(err, llm.ErrNoAPIKey) {
			return nil, config.Resolved{}, config.Provider{}, errors.New("provider " + resolved.Provider + " has no API key; add one in Settings")
		}
		return nil, config.Resolved{}, config.Provider{}, err
	}
	return client, resolved, provider, nil
}

// nativeSearchFor reports whether this provider should use built-in search.
//
// Only three wire formats have it: Responses' web_search, Gemini's google_search, Anthropic's
// web_search server tool. Compatible endpoints (/chat/completions) do not, so deepseek / openrouter /
// Ollama all answer false.
func nativeSearchFor(p config.Provider) bool {
	if !config.SupportsNativeSearch(p.Type) {
		return false
	}
	if p.NativeSearch != nil {
		return *p.NativeSearch
	}
	return true
}

// newRun registers a run and publishes run.started.
//
// Note that ctx grows from context.Background() and does not inherit the caller's: a dropped UI must
// not kill a running run. env, by contrast, belongs to the caller and is passed in explicitly.
func (r *Runner) newRun(sessionID string, resolved config.Resolved, env tools.Env, reasoning string, nativeSearch bool) *run {
	runID := "run-" + store.NewID()
	ctx, cancel := context.WithCancel(context.Background())
	rn := &run{
		id:        runID,
		sessionID: sessionID,
		startedAt: time.Now(),
		cancel:    cancel,
		done:      make(chan struct{}),
		env:       env,
		// Clamp again: callers may not behave (chat.send is an external interface).
		reasoning:    config.NormalizeReasoning(reasoning, resolved.Reasoning),
		nativeSearch: nativeSearch,
	}
	rn.env.Skills = rn

	// One debug line: when someone later asks "I picked high, why no effect", this shows whether it was
	// skipped as unsupported or never sent at all.
	applog.Debugf("run %s reasoning effort %s", runID, config.ReasoningSummary(reasoning, resolved.Reasoning))

	r.mu.Lock()
	r.runs[runID] = rn
	r.mu.Unlock()

	r.publish(map[string]any{
		"kind":      "run.started",
		"runId":     runID,
		"sessionId": sessionID,
		"model":     resolved.ID,
		"reasoning": rn.reasoning,
	})

	rn.ctx = ctx
	return rn
}

// Regenerate reruns the last user message without adding one. The caller must truncate the later
// answers first (session.truncate), or the history ends with an assistant reply.
func (r *Runner) Regenerate(sessionID, modelID, reasoning string, env tools.Env) (string, error) {
	sess, ok := r.store.Get(sessionID)
	if !ok {
		return "", errors.New("session not found")
	}
	if len(sess.Messages) == 0 {
		return "", errors.New("the session has no messages")
	}
	last := sess.Messages[len(sess.Messages)-1]
	if last.Role != "user" {
		return "", errors.New("the last message is not from the user")
	}

	// Same rule as Send: this turn appends to the same transcript the last one may still be writing.
	if !r.stopSession(sessionID) {
		return "", errors.New("the previous turn is still stopping; try again in a moment")
	}

	client, resolved, provider, err := r.prepare(sessionID, modelID)
	if err != nil {
		return "", err
	}

	rn := r.newRun(sessionID, resolved, env, reasoning, nativeSearchFor(provider))
	go r.execute(rn.ctx, rn, client, resolved, last.Text)
	return rn.id, nil
}

// emptyReplyNote is appended to the one retry a silent reply gets.
//
// It names both halves of what may be wrong, because the two need different actions: work left
// undone, or work done but never described. Telling the model which one applies is not possible from
// here, so it is told to look.
const emptyReplyNote = "\n\nYour previous message was empty: no text and no tool calls." +
	" That reaches the user as nothing at all, which looks like the turn died." +
	" If there is still work to do, do it now. Otherwise write the reply you owed: what you did, what came of it, and what is left."

// failEmptyReply ends a turn that produced no text at all.
//
// An empty reply is not stored. It would be a blank bubble in the transcript that reads as an
// interrupted turn, and there is nothing in it for the next request to use. The failure is reported
// instead, so the UI can say what happened rather than showing a gap.
func (r *Runner) failEmptyReply(rn *run, usage llm.Usage, why string) {
	applog.Warnf("run %s ended without a reply: %s", rn.id, why)
	if err := r.store.Spend(rn.sessionID, usage.Input, usage.Output); err != nil {
		applog.Warnf("failed to record usage of session %s: %v", rn.sessionID, err)
	}
	r.publish(map[string]any{
		"kind":         "run.failed",
		"runId":        rn.id,
		"sessionId":    rn.sessionID,
		"error":        "the model finished without writing a reply (" + why + ")",
		"elapsedMs":    time.Since(rn.startedAt).Milliseconds(),
		"sessionUsage": r.sessionUsagePayload(rn.sessionID),
	})
}

// wrapUpNote is the system note appended to the closing round.
//
// It exists for that request only and is never written to history: the next time the user asks, the
// model must not think it has no tools forever. The wording forbids pretending to have just done
// something new — the worst answer when steps run out is a fabricated transcript of work.
const wrapUpNote = "\n\nThe tool-step budget for this turn is used up, so you cannot call any more tools." +
	" Answer from what you have: what got done, what is still missing, and what you suggest next." +
	" Do not pretend you just performed more actions."

// Send starts a turn and returns the runID; all progress from here is events. attachments are file
// paths the user attached (attachments alone, with no text, is fine). reasoning is this turn's
// depth, empty meaning send nothing.
func (r *Runner) Send(sessionID, text, modelID, reasoning string, attachments []string, env tools.Env) (string, string, error) {
	text = strings.TrimSpace(text)
	if text == "" && len(attachments) == 0 {
		return "", "", errors.New("message is empty")
	}

	// A new turn implies the previous one is over. The UI cancels first, but cancelling is a request:
	// the old run may still be inside a tool call and will append its result when it returns. Waiting
	// here is what keeps that result above this message instead of below it.
	if !r.stopSession(sessionID) {
		return "", "", errors.New("the previous turn is still stopping; try again in a moment")
	}

	client, resolved, provider, err := r.prepare(sessionID, modelID)
	if err != nil {
		return "", "", err
	}

	// The user message is persisted at once: a daemon crash must not lose what the user asked.
	userMsg := store.Message{
		ID:          store.NewID(),
		Role:        "user",
		Text:        text,
		CreatedAt:   time.Now().UnixMilli(),
		Attachments: attachments,
	}
	if err := r.store.Append(sessionID, userMsg); err != nil {
		return "", "", err
	}

	rn := r.newRun(sessionID, resolved, env, reasoning, nativeSearchFor(provider))
	go r.execute(rn.ctx, rn, client, resolved, text)
	// The id comes back so the UI can address this message by id before it reloads the
	// session; row indexes are not stable enough to edit or regenerate from.
	return rn.id, userMsg.ID, nil
}

// Cancel stops a turn; an unknown runID does nothing.
func (r *Runner) Cancel(runID string) bool {
	r.mu.Lock()
	rn, ok := r.runs[runID]
	r.mu.Unlock()
	if !ok {
		return false
	}
	rn.cancel()
	return true
}

// stopSession cancels this session's turns and waits until they have stopped writing.
//
// This is the guard for "one turn at a time per session", and it exists because the transcript is
// append-only while a turn runs: a run persists its assistant message with the tool calls first and
// appends each result as the tool returns. A second turn that writes its user message during that
// window puts the message between the calls and their results, which is a transcript neither OpenAI
// nor Anthropic will accept — permanently, since it is stored. Cancelling alone does not help: the
// cancelled run still appends the result it is holding.
//
// false means a run would not stop in time (a wedged tool). The caller must then refuse the new turn
// rather than write into a transcript that is still being modified.
func (r *Runner) stopSession(sessionID string) bool {
	r.mu.Lock()
	var targets []*run
	for _, rn := range r.runs {
		if rn.sessionID == sessionID {
			targets = append(targets, rn)
		}
	}
	r.mu.Unlock()
	if len(targets) == 0 {
		return true
	}
	for _, rn := range targets {
		rn.cancel()
	}
	deadline := time.After(runStopTimeout)
	for _, rn := range targets {
		select {
		case <-rn.done:
		case <-deadline:
			applog.Warnf("session %s still has a run writing after %s", sessionID, runStopTimeout)
			return false
		}
	}
	return true
}

// StopSession is stopSession for callers outside this package: anything that rewrites the transcript
// has to make sure no run is still appending to it, or the rewrite and the run interleave into a shape
// no provider accepts.
func (r *Runner) StopSession(sessionID string) bool { return r.stopSession(sessionID) }

// CancelSession stops every run of one session (used when switching sessions).
func (r *Runner) CancelSession(sessionID string) int {
	r.mu.Lock()
	var targets []*run
	for _, rn := range r.runs {
		if rn.sessionID == sessionID {
			targets = append(targets, rn)
		}
	}
	r.mu.Unlock()
	for _, rn := range targets {
		rn.cancel()
	}
	return len(targets)
}

func (r *Runner) execute(ctx context.Context, rn *run, client llm.Client, model config.Resolved, prompt string) {
	defer func() {
		rn.cancel()
		r.mu.Lock()
		delete(r.runs, rn.id)
		r.mu.Unlock()
		// Last, so a waiter that wakes up sees the run already gone from r.runs.
		close(rn.done)
	}()

	// Tools take their Env from the context (cwd boundary and approver), so this is the only place that
	// connects "who started this turn" to the tools layer.
	ctx = tools.WithEnv(ctx, rn.env)

	cfg := r.Config()
	// The skill list is read from disk once per turn (it is resent on every model call).
	skillBlock := tools.SkillsPrompt()
	var lastUsage llm.Usage

	// An overlong history has its earlier part replaced by a summary (only the outgoing copy).
	r.maybeCompact(ctx, rn, client, model, cfg)
	var usage llm.Usage
	// What the session had spent before this turn. Read after maybeCompact so the summary request
	// (which Spend records) is already in it. Published as an absolute total, never a delta: the UI
	// must not add per-step events up, or run.finished repeating the same numbers would double them.
	spentBefore := r.store.TotalUsage(rn.sessionID)

	// Working directory, project instructions and this turn's images are prepared once and reused for
	// every step.
	//
	// Images are resent on every step (or the model loses sight of them mid tool loop), but reading and
	// sniffing happen once — a file changing during the turn is not worth N extra reads.
	workingDir := r.workingDirOf(rn.sessionID)
	instructions := projectInstructions(workingDir)
	images := r.pendingImages(rn.sessionID)

	// The step cap is a fuse, not a budget: hitting it changes how the turn closes, it does not return an
	// error.
	limit := cfg.ToolStepLimit()
	// The cap this turn is really sent with. A model that reports its own ceiling lowers the user's
	// number instead of rejecting the request, so both the wire and the system prompt use this one.
	outputCap := cfg.OutputCap(model)
	// wrappingUp means "steps are used up; the last request carries no tools so the model finishes its
	// sentence".
	wrappingUp := false
	// note is the one-off instruction for the next attempt (empty except after an empty reply).
	note := ""
	// nudged records that this turn already asked once why it went silent. Asked twice it would be
	// nagging, and a model that answers nothing twice will not answer the third time either.
	nudged := false

	for step := 0; ; step++ {
		if step > 0 {
			// The UI opens a new answer block on this event (tool results must not join the same text).
			r.publish(map[string]any{
				"kind":  "run.step",
				"runId": rn.id,
				"step":  step,
			})
		}

		// Each step's reasoning belongs to that step's message: a tool step opens a new bubble, and mixing
		// them would not line up.
		var stepReasoning strings.Builder

		// The closing round gets no tools: given tools it would keep working and never finish.
		var specs []llm.Tool
		if !wrappingUp {
			specs = r.allowedSpecs(rn, cfg)
		}

		// Built per attempt rather than once: the cap can change between them when the provider tells us
		// the model's real ceiling, and the system prompt has to state the number actually sent.
		//
		// note is this attempt's only extra instruction (see emptyReplyNote). It is a parameter rather
		// than shared state so it cannot leak into a later step.
		send := func(cap int, note string) (llm.Result, error) {
			system := systemPromptFor(cfg, cap, workingDir, skillBlock, instructions)
			if wrappingUp {
				system += wrapUpNote
			}
			system += note
			return r.streamStep(ctx, rn, client, llm.Request{
				Model:       model.Model,
				System:      system,
				Messages:    withImages(r.historyMessages(rn.sessionID), images),
				Tools:       specs,
				MaxTokens:   cap,
				Temperature: cfg.Temperature,
				Reasoning:   rn.reasoning,
				// "Auto" must be distinguished from "model has no reasoning": the former is
				// thinking:{type:"adaptive"} on Anthropic, the latter cannot send even that (old models 400).
				ReasoningAuto: rn.reasoning == "" && len(model.Reasoning) > 0,
				NativeSearch:  rn.nativeSearch,
				OnReasoning: func(text string) {
					rn.addThink(text)
					stepReasoning.WriteString(text)
					r.publish(map[string]any{
						"kind":      "reasoning.delta",
						"runId":     rn.id,
						"sessionId": rn.sessionID,
						"text":      text,
					})
				},
			})
		}

		res, err := send(outputCap, note)
		// Consumed by this step: whatever happened, the next one starts clean.
		note = ""
		// A 400 that names the model's ceiling is a fact to keep, not a dead end: record it and send the
		// same step again with the number the provider will accept. Retrying blind would fail the same
		// way, and failing the turn costs the user everything the steps so far produced.
		if ceil, ok := llm.OutputCeilingOf(err); ok && outputCap > ceil {
			r.recordOutputCeiling(model, ceil)
			applog.Infof("model %s writes at most %d tokens; the cap was %d, sending that instead", model.ID, ceil, outputCap)
			outputCap = ceil
			res, err = send(outputCap, note)
		}
		usage.Input += res.Usage.Input
		usage.Output += res.Usage.Output
		// The last step's usage is kept separately: the total is the bill, this one is "how big the context is
		// now".
		lastUsage = res.Usage

		// Usage goes out on every step. Not seeing the burn mid-run is worst inside a tool loop: one round
		// trip can be tens of thousands of tokens, and hearing about it at the end is hearing nothing. usage
		// is this turn's total; lastUsage is the raw report of the last request (its input is the current
		// context size); sessionUsage is the whole session, this turn included.
		r.publish(map[string]any{
			"kind":      "run.usage",
			"runId":     rn.id,
			"sessionId": rn.sessionID,
			"usage": map[string]any{
				"input":  usage.Input,
				"output": usage.Output,
			},
			"lastUsage": map[string]any{
				"input":  lastUsage.Input,
				"output": lastUsage.Output,
			},
			"sessionUsage": map[string]any{
				"input":  spentBefore.Input + usage.Input,
				"output": spentBefore.Output + usage.Output,
			},
		})

		if errors.Is(err, llm.ErrCancelled) {
			r.finishCancelled(rn, model, res.Content, usage, lastUsage, stepReasoning.String())
			return
		}
		if err != nil {
			// Images attached but a 4xx came back: usually this model cannot see images. Say so, or the user only
			// sees an HTTP 400 and a blob of JSON.
			if imageRejected(err, images) {
				err = fmt.Errorf("%w (this model may not support images; remove the image attachments or switch to a vision-capable model)", err)
			}
			applog.Warnf("run %s failed: %v", rn.id, err)
			// The steps before the failure were billed whether or not they produced anything.
			if spendErr := r.store.Spend(rn.sessionID, usage.Input, usage.Output); spendErr != nil {
				applog.Warnf("failed to record usage of session %s: %v", rn.sessionID, spendErr)
			}
			r.publish(map[string]any{
				"kind":         "run.failed",
				"runId":        rn.id,
				"sessionId":    rn.sessionID,
				"error":        err.Error(),
				"elapsedMs":    time.Since(rn.startedAt).Milliseconds(),
				"sessionUsage": r.sessionUsagePayload(rn.sessionID),
			})
			return
		}

		// No tool calls → the model is done. Whether it said anything is a separate question, and the
		// difference matters: a reply that ends with no text is a turn the user cannot read.
		if len(res.ToolCalls) == 0 {
			empty := strings.TrimSpace(res.Content) == ""
			// StopReason length means max_tokens cut it off, not that it finished. This must reach the UI,
			// or the user sees a sentence that stops mid-way and takes it for a conclusion.
			truncated := res.StopReason == "length"

			switch {
			case !empty:
				r.finishAnswer(rn, model, res.Content, usage, lastUsage, stepReasoning.String(), truncated)

			case truncated:
				// Nothing written and the budget is gone, so the tokens went somewhere unseen — almost
				// always thinking. Name the knob instead of storing a blank reply.
				r.failEmptyReply(rn, usage, "the output cap was used up before any text was written; raise the cap or lower the reasoning depth")

			case !nudged:
				// Silence after a tool result is the shape that confuses people most: the card says the
				// tool ran, the turn ends, and the window shows nothing — indistinguishable from a crash,
				// when all that happened is that the model decided it was finished and said so with no
				// words. Asking once, with the tools still in hand, usually gets the summary it owed (or
				// the work it still owed).
				nudged = true
				note = emptyReplyNote
				applog.Infof("run %s: the model replied with nothing; asking it to continue once", rn.id)
				continue

			default:
				// Asked and still nothing: it is not going to answer, and pretending the turn succeeded
				// leaves an empty bubble that reads as an interruption.
				r.failEmptyReply(rn, usage, "it answered with nothing even when asked to continue")
			}
			return
		}

		// The closing round had no tools and still returned tool_calls: the model ignored the instruction.
		// Any text counts as the answer; with none, say plainly where it stopped rather than leaving an empty
		// reply.
		//
		// Note this must **not** be persisted: tool_calls without matching tool responses make the provider
		// reject the next request (calls and results must pair).
		if wrappingUp {
			if strings.TrimSpace(res.Content) != "" {
				r.finishAnswer(rn, model, res.Content, usage, lastUsage, stepReasoning.String(), false)
				return
			}
			applog.Warnf("run %s still asked for tool calls while wrapping up", rn.id)
			r.publish(map[string]any{
				"kind":      "run.failed",
				"runId":     rn.id,
				"sessionId": rn.sessionID,
				"error":     fmt.Sprintf("step limit %d reached and the model still asked for tool calls; stopped", limit),
				"elapsedMs": time.Since(rn.startedAt).Milliseconds(),
			})
			return
		}

		// Tool calls: persist the assistant message (tool_calls included) first. Content may be empty, and
		// that means "it is going to do something" rather than "no answer".
		if err := r.store.Append(rn.sessionID, store.Message{
			ID:        store.NewID(),
			Role:      "assistant",
			Text:      res.Content,
			ToolCalls: res.ToolCalls,
			Model:     model.ID,
			CreatedAt: time.Now().UnixMilli(),
			Reasoning: stepReasoning.String(),
			// Reasoning inside a tool loop is kept: Anthropic requires it back verbatim (signature included)
			// next turn, or the whole step is a 400.
			Thinking: res.ThinkingBlocks,
		}); err != nil {
			applog.Warnf("failed to write session %s: %v", rn.sessionID, err)
		}

		for _, tc := range res.ToolCalls {
			started := time.Now()
			r.publish(map[string]any{
				"kind":      "tool.started",
				"runId":     rn.id,
				"callId":    tc.ID,
				"name":      tc.Function.Name,
				"arguments": tc.Function.Arguments,
			})

			var out string
			if toolAllowed(tc.Function.Name, cfg) {
				out = r.runTool(ctx, tc.Function.Name, tc.Function.Arguments)
			} else {
				// Settings may change mid-turn; do not let a now-invisible tool run.
				out = "the user disabled this tool: " + tc.Function.Name
			}

			r.publish(map[string]any{
				"kind":      "tool.finished",
				"runId":     rn.id,
				"callId":    tc.ID,
				"name":      tc.Function.Name,
				"output":    out,
				"elapsedMs": time.Since(started).Milliseconds(),
			})

			if err := r.store.Append(rn.sessionID, store.Message{
				ID:         store.NewID(),
				Role:       "tool",
				Text:       out,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				ToolArgs:   tc.Function.Arguments,
				CreatedAt:  time.Now().UnixMilli(),
			}); err != nil {
				applog.Warnf("failed to write the tool result: %v", err)
			}
		}

		// This step uses the last of the budget: the next becomes the closing round rather than an immediate
		// end. Ending here leaves "failed", while the user wants "how far it got, what is missing".
		// limit 0 means the user turned the fuse off, so there is nothing to count down to.
		if limit > 0 && step+1 >= limit {
			wrappingUp = true
		}
	}
}

// recordOutputCeiling writes a ceiling the provider stated into config.json.
//
// It is a fact about the model, so it belongs with the other facts about the model — otherwise every
// session rediscovers it with another failed request, and the user sees "HTTP 400" each time. The config
// is re-read first rather than reusing the runner's copy: a settings change made minutes ago must not be
// undone by this write.
//
// A failure to write is not worth failing the turn over. The request has already been resent with the
// right number, so the only cost is learning it again next time.
func (r *Runner) recordOutputCeiling(model config.Resolved, limit int) {
	cfg, err := config.Load()
	if err != nil {
		applog.Warnf("could not record the output ceiling of %s: %v", model.ID, err)
		return
	}
	provider, ok := cfg.Providers[model.Provider]
	if !ok {
		return
	}
	entry, ok := provider.Models[model.Model]
	if !ok {
		// Only correct what is already configured: inventing a disabled entry here would put a model the
		// user never enabled into their settings.
		applog.Warnf("not recording the output ceiling of %s: it is not in config.json", model.ID)
		return
	}
	if entry.MaxOutput == limit {
		return
	}
	entry.MaxOutput = limit
	provider.Models[model.Model] = entry
	cfg.Providers[model.Provider] = provider
	if err := cfg.Save(); err != nil {
		applog.Warnf("could not record the output ceiling of %s: %v", model.ID, err)
		return
	}
	// The next turn starts from the corrected number; this turn already lowered its own cap.
	r.SetConfig(cfg)
	// The settings page shows this number. Without the event it would go on claiming the old one until the
	// app restarts, which is the same lie as sending it.
	r.publish(map[string]any{"kind": "config.changed", "reason": "outputCeiling"})
}

// workingDirOf returns the session's working directory (tools and system prompt must agree).
func (r *Runner) workingDirOf(sessionID string) string {
	if sess, ok := r.store.Get(sessionID); ok {
		return sess.WorkingDirectory
	}
	return ""
}

// runStopTimeout bounds how long a new turn waits for the previous one to stop writing.
//
// Generous enough for a killed tool to return its result (a signal plus a pipe close), short enough
// that a wedged tool does not leave the user staring at nothing. Past it the new turn is refused:
// writing anyway is what corrupts the transcript. A var so a test can shorten it.
var runStopTimeout = 5 * time.Second

// maxRetries is how many extra attempts one step gets.
//
// Two rides out one 429 or one disconnect; more only leaves the user staring at a frozen screen, when
// showing the error so they can decide (switch model, wait, stop) is better.
const maxRetries = 2

// retryBaseDelay is where exponential backoff starts (0.8s, 1.6s).
// It is a var only so tests can drop it to 1ms; otherwise two tests would cost an extra second.
var retryBaseDelay = 800 * time.Millisecond

// streamStep calls the model, backing off and retrying on retryable errors.
//
// Retry only while nothing has been emitted: retrying after half a sentence is on screen shows the
// same words twice, which is worse than failing.
func (r *Runner) streamStep(ctx context.Context, rn *run, client llm.Client, req llm.Request) (llm.Result, error) {
	for attempt := 0; ; attempt++ {
		rn.beginAttempt()
		res, spoke, err := r.streamOnce(ctx, rn, client, req)
		if err == nil {
			return res, nil
		}
		if spoke || attempt >= maxRetries || !llm.Retryable(err) {
			return res, err
		}

		delay := retryBaseDelay << attempt
		applog.Warnf("run %s step %d failed (%v); retrying in %s", rn.id, attempt+1, err, delay)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return res, ctx.Err()
		}
	}
}

// streamOnce is one attempt, pushing text to the UI throttled. spoke records whether anything was
// emitted — the caller uses it to decide whether a retry is allowed.
func (r *Runner) streamOnce(ctx context.Context, rn *run, client llm.Client, req llm.Request) (res llm.Result, spoke bool, err error) {
	var (
		bufMu sync.Mutex
		buf   strings.Builder
		got   bool
	)

	flush := func() {
		bufMu.Lock()
		if buf.Len() == 0 {
			bufMu.Unlock()
			return
		}
		chunk := buf.String()
		buf.Reset()
		bufMu.Unlock()

		r.publish(map[string]any{
			"kind":  "delta",
			"runId": rn.id,
			"text":  chunk,
		})
	}

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(flushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				flush()
			}
		}
	}()

	res, err = client.Stream(ctx, req, func(text string) {
		rn.addPart(text)
		bufMu.Lock()
		if text != "" {
			got = true
		}
		buf.WriteString(text)
		bufMu.Unlock()
	})

	close(done)
	flush() // flush the last partial interval

	bufMu.Lock()
	spoke = got
	bufMu.Unlock()
	return res, spoke, err
}

// finishAnswer persists the final answer and closes out. usagePtr keeps "no usage" nil instead of
// writing a pile of zeros into the JSON.
func usagePtr(u llm.Usage) *store.Usage {
	if u.Input == 0 && u.Output == 0 {
		return nil
	}
	return &store.Usage{Input: u.Input, Output: u.Output}
}

// sessionUsagePayload is the session's running total in the shape the UI reads. It is absolute on
// purpose: events repeat (every step, then run.finished), so a delta would be counted twice by any
// listener that adds rather than assigns.
func (r *Runner) sessionUsagePayload(sessionID string) map[string]any {
	total := r.store.TotalUsage(sessionID)
	return map[string]any{"input": total.Input, "output": total.Output}
}

func (r *Runner) finishAnswer(rn *run, model config.Resolved, answer string, usage, lastUsage llm.Usage, reasoning string, truncated bool) {
	msg := store.Message{
		ID:        store.NewID(),
		Role:      "assistant",
		Text:      answer,
		Model:     model.ID,
		CreatedAt: time.Now().UnixMilli(),
		ElapsedMs: time.Since(rn.startedAt).Milliseconds(),
		Truncated: truncated,
	}
	if usage.Input > 0 || usage.Output > 0 {
		msg.Usage = &store.Usage{Input: usage.Input, Output: usage.Output}
	}
	msg.LastUsage = usagePtr(lastUsage)
	msg.Reasoning = reasoning
	if err := r.store.Append(rn.sessionID, msg); err != nil {
		applog.Warnf("failed to write session %s: %v", rn.sessionID, err)
	}

	r.publish(map[string]any{
		"kind":      "run.finished",
		"runId":     rn.id,
		"sessionId": rn.sessionID,
		"status":    "done",
		"truncated": truncated,
		"elapsedMs": msg.ElapsedMs,
		"usage": map[string]any{
			"input":  usage.Input,
			"output": usage.Output,
		},
		"lastUsage": map[string]any{
			"input":  lastUsage.Input,
			"output": lastUsage.Output,
		},
		"sessionUsage": r.sessionUsagePayload(rn.sessionID),
		"message": map[string]any{
			"id":        msg.ID,
			"text":      msg.Text,
			"model":     msg.Model,
			"elapsed":   msg.ElapsedMs,
			"truncated": truncated,
			"reasoning": reasoning,
		},
	})

	go r.maybeAutoTitle(rn.sessionID, model.ID)
}

// finishCancelled: the user stopped it. Whatever was produced is still persisted (marked cancelled).
func (r *Runner) finishCancelled(rn *run, model config.Resolved, answer string, usage, lastUsage llm.Usage, reasoning string) {
	elapsed := time.Since(rn.startedAt)

	if answer == "" {
		// Nothing to persist, but the cancelled request still cost tokens.
		if err := r.store.Spend(rn.sessionID, usage.Input, usage.Output); err != nil {
			applog.Warnf("failed to record usage of session %s: %v", rn.sessionID, err)
		}
		r.publish(map[string]any{
			"kind":         "run.finished",
			"runId":        rn.id,
			"sessionId":    rn.sessionID,
			"status":       "cancelled",
			"elapsedMs":    elapsed.Milliseconds(),
			"sessionUsage": r.sessionUsagePayload(rn.sessionID),
		})
		return
	}

	msg := store.Message{
		ID:        store.NewID(),
		Role:      "assistant",
		Text:      answer,
		Model:     model.ID,
		CreatedAt: time.Now().UnixMilli(),
		ElapsedMs: elapsed.Milliseconds(),
		Reasoning: reasoning,
	}
	if usage.Input > 0 || usage.Output > 0 {
		msg.Usage = &store.Usage{Input: usage.Input, Output: usage.Output}
	}
	if err := r.store.Append(rn.sessionID, msg); err != nil {
		applog.Warnf("failed to write session %s: %v", rn.sessionID, err)
	}

	r.publish(map[string]any{
		"kind":      "run.finished",
		"runId":     rn.id,
		"sessionId": rn.sessionID,
		"status":    "cancelled",
		"elapsedMs": elapsed.Milliseconds(),
		"usage": map[string]any{
			"input":  usage.Input,
			"output": usage.Output,
		},
		"lastUsage": map[string]any{
			"input":  lastUsage.Input,
			"output": lastUsage.Output,
		},
		"sessionUsage": r.sessionUsagePayload(rn.sessionID),
		"message": map[string]any{
			"id":      msg.ID,
			"text":    msg.Text,
			"model":   msg.Model,
			"elapsed": msg.ElapsedMs,
		},
	})
}

// historyMessages turns persisted messages into the shape the LLM layer knows (tool calls and
// results included).
func (r *Runner) historyMessages(sessionID string) []llm.Message {
	history := r.store.History(sessionID)
	out := make([]llm.Message, 0, len(history)+1)

	// With a compaction record, start at the cut point and send the summary instead of the earlier part.
	// If that id is gone (the session was truncated), treat it as uncompacted and send everything — better
	// to hit the limit than to send a conversation missing its beginning.
	if c := r.store.Compaction(sessionID); c != nil {
		for i, m := range history {
			if m.ID != c.UpToID {
				continue
			}
			out = append(out, summaryMessage(c))
			history = history[i:]
			break
		}
	}
	// Calls and results are paired while building, not sent as found.
	//
	// Both APIs reject a request in which an assistant message's tool_calls are not followed
	// immediately by one tool message per call, and the rejection is not retried: a stored transcript
	// with a gap fails every turn from then on. A gap can be on disk already (a daemon killed between
	// the assistant message and the results), or a result can sit past a later user message. Pairing
	// here repairs both without touching the transcript: what is stored is history, and history is not
	// rewritten to make a request legal.
	pending := make([]string, 0, 4)
	flush := func() {
		for _, id := range pending {
			out = append(out, llm.Message{Role: llm.RoleTool, ToolCallID: id, Content: toolInterrupted})
		}
		pending = pending[:0]
	}
	take := func(id string) bool {
		for i, want := range pending {
			if want != id {
				continue
			}
			pending = append(pending[:i], pending[i+1:]...)
			return true
		}
		return false
	}

	for _, m := range history {
		switch m.Role {
		case "user":
			flush()
			out = append(out, llm.Message{Role: llm.RoleUser, Content: messageContent(m)})
		case "assistant":
			// The previous assistant's calls were never answered before this one: they get a note now, so
			// its tool_calls still sit directly before results.
			flush()
			// Thinking is carried only when this message calls tools: that is the tool-loop case where Anthropic
			// demands it back verbatim. Older reasoning from previous turns can be omitted; including it only
			// wastes context.
			var thinking []llm.ThinkingBlock
			if len(m.ToolCalls) > 0 {
				thinking = m.Thinking
			}
			out = append(out, llm.Message{
				Role:      llm.RoleAssistant,
				Content:   m.Text,
				ToolCalls: m.ToolCalls,
				Thinking:  thinking,
			})
			for _, tc := range m.ToolCalls {
				pending = append(pending, tc.ID)
			}
		case "tool":
			// A result for a call that is not waiting for one answers nothing: it belongs to a turn that
			// has been cut away, or arrived after the user had already said something else. Sending it
			// would be rejected outright.
			if !take(m.ToolCallID) {
				applog.Debugf("session %s: dropping a tool result (%s) that does not follow its call", sessionID, m.ToolCallID)
				continue
			}
			content := tools.TruncateForModel(m.Text)
			if strings.TrimSpace(content) == "" {
				// "It ran and said nothing" is information; an empty block is not, and some providers
				// reject one.
				content = toolNoOutput
			}
			out = append(out, llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: m.ToolCallID,
				// What is persisted is the full output (the UI must show all of it); what goes to the model is the
				// truncated version — a 10,000-line grep result means nothing and only eats the context.
				Content: content,
			})
		}
	}
	flush()
	return out
}

// toolInterrupted stands in for a result that never reached the transcript: the daemon was killed
// while the tool ran, or the turn was cut off in between. The model has to see that the call was
// made and did not answer, or it will assume the work happened.
const toolInterrupted = "(this tool call did not finish; the turn was interrupted before it returned)"

// toolNoOutput is the sent text for a tool that genuinely succeeded with nothing to say.
const toolNoOutput = "(no output)"

// maybeAutoTitle names the session with the model after the first exchange, once: success sets
// SmartNamed, failure (no key, say) leaves it for the next turn, and neither affects the conversation.
func (r *Runner) maybeAutoTitle(sessionID, modelID string) {
	sess, ok := r.store.Get(sessionID)
	if !ok || sess.SmartNamed || len(sess.Messages) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	title, err := r.SuggestTitle(ctx, sessionID, modelID)
	if err != nil {
		applog.Warnf("failed to auto-name session %s: %v", sessionID, err)
		return
	}

	// The user may have renamed it meanwhile; re-read before deciding to overwrite.
	if fresh, ok := r.store.Get(sessionID); ok && fresh.SmartNamed {
		return
	}

	if err := r.store.Rename(sessionID, title); err != nil {
		applog.Warnf("failed to save the auto-generated title for session %s: %v", sessionID, err)
		return
	}
	_ = r.store.MarkSmartNamed(sessionID)

	r.publish(map[string]any{
		"kind":      "session.renamed",
		"sessionId": sessionID,
		"title":     title,
		// The title request was billed after the turn closed; the chip picks the total up from here.
		"sessionUsage": r.sessionUsagePayload(sessionID),
	})
}

// messageContent builds the text to send for a user message. Attachments are currently only
// announced — reading them needs the tools layer — but at least the model knows something is there
// and does not see an empty message.
func messageContent(m store.Message) string {
	if len(m.Attachments) == 0 {
		return m.Text
	}
	var b strings.Builder
	if strings.TrimSpace(m.Text) != "" {
		b.WriteString(m.Text)
		b.WriteString("\n\n")
	}
	// Images go through vision directly rather than being listed in the text: listed, the model tries
	// read_file, gets "unsupported file type", and rephrases and retries.
	var files []string
	for _, p := range m.Attachments {
		if !looksLikeImage(p) {
			files = append(files, p)
		}
	}
	if len(files) == 0 {
		// Images only: do not leave those two padding newlines at the end.
		return strings.TrimSpace(b.String())
	}
	b.WriteString("(the user attached the following files)")
	for _, p := range files {
		b.WriteString("\n- " + p)
	}
	return b.String()
}

func (r *Runner) publish(data map[string]any) {
	r.bus.Publish(Topic, data)
	if r.OnEvent != nil {
		r.OnEvent(data)
	}
}

// instructionsFile is the project instruction file in the working directory.
//
// Only this one name: accepting several (CLAUDE.md, .cursorrules…) would let any directory the user
// opens stuff things into the system prompt without them knowing.
const instructionsFile = "AGENTS.md"

// instructionsLimit caps how much is read. Project instructions are background for the model,
// not a novel — an oversized file only pushes out the work at hand.
const instructionsLimit = 32 << 10

// projectInstructions reads AGENTS.md from the working directory, empty when absent (or not a
// file).
//
// Read once per turn rather than cached: an edit applies to the next message, consistent with the rest
// of the settings.
func projectInstructions(dir string) string {
	if dir == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(dir, instructionsFile))
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(strings.ToValidUTF8(string(raw), ""))
	if text == "" {
		return ""
	}
	if len(text) > instructionsLimit {
		// Cut on a character boundary, or CJK is split mid-character.
		cut := instructionsLimit
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "\n\n… (AGENTS.md exceeds the limit, truncated)"
	}
	return text
}

// systemPromptFor appends this session's working directory to the configured system prompt. The
// working directory is "where the model should work"; tools will share the same value once shell and
// file tools are wired up.
// outputCap is the number the request really carries (config.OutputCap of the active model), not the
// raw setting: a model with its own ceiling gets lowered, and promising the model a budget it will not
// have is worse than saying nothing.
func systemPromptFor(cfg *config.Config, outputCap int, workingDir, skillBlock, instructions string) string {
	base := cfg.SystemPrompt

	// The output cap is hard: past it the reply is simply cut. The model cannot count tokens, so the only
	// usable instruction is "wrap up when it will not fit".
	budget := ""
	if outputCap > 0 {
		budget = fmt.Sprintf("\n\nReplies are capped at %d tokens per turn (longer output is cut off)."+
			" When there is a lot to say, lead with the conclusion and the essential parts and expand the rest only if the user asks to continue;"+
			" never trade accuracy to fit.", outputCap)
	}

	// The project's own rules go last: instructions nearer the end are followed more reliably.
	project := ""
	if instructions != "" {
		project = "\n\nBelow are this project's own instructions (from " + instructionsFile + " in the working directory)." +
			" Where they conflict with the general guidance above, these win:\n\n" + instructions
	}

	if workingDir == "" {
		return strings.TrimSpace(base + budget + skillBlock + project)
	}
	block := "\n\nWorking directory: " + workingDir +
		"\nAll file paths are relative to this directory; run commands with it as the cwd too."
	return strings.TrimSpace(base + budget + block + skillBlock + project)
}

// SuggestTitle asks the given model for a short title. Only the first four messages are read, so a
// long conversation does not burn tokens.
func (r *Runner) SuggestTitle(ctx context.Context, sessionID, modelID string) (string, error) {
	cfg := r.Config()
	hist := r.store.History(sessionID)
	if len(hist) == 0 {
		return "", errors.New("the session is still empty")
	}

	var b strings.Builder
	for i, m := range hist {
		if i >= 4 {
			break
		}
		text := m.Text
		if runes := []rune(text); len(runes) > 600 {
			text = string(runes[:600])
		}
		b.WriteString(m.Role + ": " + text + "\n")
	}

	resolved, provider, ok := cfg.Resolve(modelID)
	if !ok {
		return "", errors.New("unknown model: " + modelID)
	}
	client, err := llm.New(provider)
	if err != nil {
		return "", err
	}

	var out strings.Builder
	req := llm.Request{
		Model:       resolved.Model,
		System:      "You are a naming assistant. Output only a title of 3 to 8 words, in the same language as the conversation, with no quotes, no punctuation and no explanation.",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "Here is the start of the conversation; name it:\n\n" + b.String()}},
		MaxTokens:   200, // reasoning models spend tokens before any text; 32 often yields none
		Temperature: 0.3,
	}
	titleRes, err := client.Stream(ctx, req, func(delta string) { out.WriteString(delta) })
	// Naming the session costs tokens too, and it is a request the user never sees in the transcript.
	if spendErr := r.store.Spend(sessionID, titleRes.Usage.Input, titleRes.Usage.Output); spendErr != nil {
		applog.Warnf("failed to record usage of session %s: %v", sessionID, spendErr)
	}
	if err != nil {
		return "", err
	}
	title := sanitizeTitle(out.String())
	if title == "" {
		return "", errors.New("the model produced no usable title")
	}
	return title, nil
}

// sanitizeTitle strips the quotes, periods and newlines the model loves, and caps the length.
//
// The cutset holds full-width marks because a Chinese title arrives wrapped in them, and stripping
// only ASCII quotes would leave 「」 inside the title. They are data, not prose.
func sanitizeTitle(raw string) string {
	t := strings.TrimSpace(raw)
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = t[:i]
	}
	t = strings.Trim(t, " \t\"'“”「」『』《》`。.,，:：")
	runes := []rune(t)
	if len(runes) > 24 {
		t = string(runes[:24])
	}
	return strings.TrimSpace(t)
}

// fileTools is the whole file-touching group. The settings switch is per group: users think in terms
// of "may this agent read my files", not per-tool checkboxes.
var fileTools = map[string]bool{
	"read_file":  true,
	"write_file": true,
	"edit_file":  true,
	"glob":       true,
	"grep":       true,
}

// mcpTools returns the MCP tools currently available (nil-safe).
func (r *Runner) mcpTools() []tools.Tool {
	if r.mcp == nil {
		return nil
	}
	return r.mcp.Tools()
}

// runTool runs one tool: MCP first, then built-ins.
//
// MCP goes through a temporary registry instead of calling t.Run directly, to reuse Registry.Run's
// panic protection and error stringification — with external processes involved, that protection
// matters more, not less.
func (r *Runner) runTool(ctx context.Context, name, args string) string {
	if r.mcp != nil {
		reg := tools.NewRegistry()
		for _, t := range r.mcpTools() {
			reg.Add(t)
		}
		if _, ok := reg.Get(name); ok {
			out, _ := reg.Run(ctx, name, args)
			return out
		}
	}
	out, _ := r.tools.Run(ctx, name, args)
	return out
}

// toolAllowed reports whether this tool should be offered right now.
//
// Without a search backend web_search disappears: leaving it makes the model call once, get "no
// backend", and try again.
func toolAllowed(name string, cfg *config.Config) bool {
	if cfg == nil {
		return true
	}
	if fileTools[name] {
		return cfg.Tools.FileEnabled()
	}
	switch name {
	case "bash":
		return cfg.Tools.ShellEnabled()
	case "web_search":
		// Both must hold: the user turned the switch on and a backend really exists.
		return cfg.Tools.WebSearchEnabled() && cfg.SearchEnabled()
	case "web_fetch":
		// Page fetch needs no backend but shares the switch: turning "web" off means "do not go online", not
		// "save search quota".
		return cfg.Tools.WebSearchEnabled()

	case "browser":
		// The browser is part of "web", sharing the composer's button with search and page fetch. Turning web
		// off disables all three — the user means "do not go online", not "do not search".
		//
		// Also false when the executable is missing: a visible tool gets tried, returns something the model
		// cannot act on, and gets retried with different wording. Computed per request, so picking a path in
		// settings takes effect immediately.
		if !cfg.Tools.WebSearchEnabled() {
			return false
		}
		bin, _ := tools.ResolveAgentBrowser(cfg.Browser.Path)
		return bin != ""
	}
	return true
}
