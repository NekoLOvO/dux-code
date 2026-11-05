package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NekoLOvO/dux-code/core/internal/config"
	"github.com/NekoLOvO/dux-code/core/internal/ipc"
	"github.com/NekoLOvO/dux-code/core/internal/llm"
	"github.com/NekoLOvO/dux-code/core/internal/store"
	"github.com/NekoLOvO/dux-code/core/internal/tools"
)

// One turn at a time per session, and the reason for it.
//
// A turn persists its assistant message (with the tool calls) first and appends each result as the
// tool returns. Anything that writes to the transcript during that window — a new user message, a
// truncate, a rewrite — puts itself between the calls and their results, and that transcript is
// rejected by both OpenAI and Anthropic forever, because the transcript is what gets sent.

// blockingTool returns only when the turn is cancelled. It stands in for a shell command or an
// approval dialog: work that is already running when the user changes their mind.
type blockingTool struct {
	entered chan struct{}
	once    sync.Once
}

func newBlockingTool() *blockingTool { return &blockingTool{entered: make(chan struct{})} }

func (b *blockingTool) Spec() llm.Tool {
	return llm.NewTool("block", "blocks until the turn is cancelled", map[string]any{"type": "object"})
}

func (b *blockingTool) Run(ctx context.Context, _ string) (string, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-ctx.Done():
		return "stopped before it finished", nil
	case <-time.After(2 * time.Second):
		// Nothing cancelled this turn. It will still put its result away — late, which is the point:
		// the test waits for that and then checks where it landed.
		return "finished after the user had moved on", nil
	}
}

// toolCallThenAnswer serves the first request as a tool call (named toolName) and every later one as
// plain text.
func toolCallThenAnswer(toolName string) *httptest.Server {
	var (
		mu    sync.Mutex
		calls int
	)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		if first {
			for _, chunk := range []string{
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":%q,"arguments":"{}"}}]}}]}`, toolName),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`,
			} {
				fmt.Fprintf(w, "data: %s\n\n", chunk)
			}
		} else {
			fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

// The sequence the UI produces when someone gives up on a turn and types something else: cancel, then
// send. The new message must not be written until the cancelled turn has put its tool result away.
func TestSecondTurnWaitsForTheFirstToStopWriting(t *testing.T) {
	cfg := configWithProvider(t, 0, 0)
	block := newBlockingTool()
	srv := toolCallThenAnswer("block")
	defer srv.Close()
	applyTestProvider(t, cfg, srv.URL)

	r, sid, _ := newTestRunner(t, cfg)
	// A named session keeps the title request (a separate HTTP call) out of this test.
	if err := r.store.MarkSmartNamed(sid); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry()
	reg.Add(block)
	r.SetTools(reg)

	env := tools.Env{Approval: tools.ApprovalAuto}
	model := config.Resolved{ID: "fake:m", Provider: "fake", Model: "m"}

	firstID, _, err := r.Send(sid, "run the tool", model.ID, "", nil, env)
	if err != nil {
		t.Fatalf("first Send: %v", err)
	}
	select {
	case <-block.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the tool never started")
	}
	r.mu.Lock()
	firstRun := r.runs[firstID]
	r.mu.Unlock()
	if firstRun == nil {
		t.Fatal("the first run is not registered")
	}

	// "Stop, then say something else" — what the UI sends when the user gives up on a turn.
	second := make(chan error, 1)
	go func() {
		_, _, err := r.Send(sid, "never mind, do this instead", model.ID, "", nil, env)
		second <- err
	}()
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("the second turn was refused (%v); 'stop, then say something else' has to work", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the second Send never returned")
	}
	// However the two interleaved, wait for the first turn to be completely done before judging: its
	// result may still arrive (that is the bug), and checking earlier would miss it.
	select {
	case <-firstRun.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the first turn never finished")
	}

	// The order is the whole point: a result has to sit directly under its call, above the user's next
	// message. A result that drifts below a user message is what makes the session unsendable.
	h := r.store.History(sid)
	for i, m := range h {
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		ids := map[string]bool{}
		for _, tc := range m.ToolCalls {
			ids[tc.ID] = true
		}
		j := i + 1
		for j < len(h) && h[j].Role == "tool" {
			if !ids[h[j].ToolCallID] {
				t.Fatalf("a result for another call sits under %s: %s", m.ID, shapeOf(h))
			}
			j++
		}
		for ; j < len(h); j++ {
			if h[j].Role == "tool" && ids[h[j].ToolCallID] {
				t.Fatalf("result %s drifted below %s: %s", h[j].ToolCallID, h[j-1].Role, shapeOf(h))
			}
		}
	}
	// And the request built from it answers every call directly after its assistant message.
	assertCallsAnswered(t, r.historyMessages(sid))
}

// A transcript already carrying a gap must still be sendable. This is what makes an old broken
// session work again without editing the file: the repair is in the request, not in the history.
func TestRequestsRepairACallWithoutItsResult(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s, _, err := store.Open()
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	sess, _ := s.Create("t")
	r := New(ipc.NewEventBus(), s, quietConfig(0))

	// The shape a killed daemon leaves behind: calls, then straight to the user's next message.
	calls := []llm.ToolCall{
		{ID: "call_a", Type: "function", Function: llm.FunctionCall{Name: "bash", Arguments: `{}`}},
		{ID: "call_b", Type: "function", Function: llm.FunctionCall{Name: "bash", Arguments: `{}`}},
	}
	mustAppendAgent(t, s, sess.ID,
		store.Message{ID: "u1", Role: "user", Text: "do two things"},
		store.Message{ID: "a1", Role: "assistant", ToolCalls: calls},
		// Only one of the two results made it.
		store.Message{ID: "t1", Role: "tool", ToolCallID: "call_a", Name: "bash", Text: "first"},
		store.Message{ID: "u2", Role: "user", Text: "and now this"},
	)

	got := r.historyMessages(sess.ID)
	assertCallsAnswered(t, got)
	if !containsText(got, toolInterrupted) {
		t.Errorf("the missing result should be named, got %s", shapeOfMessages(got))
	}
}

// The poisoned shape: the result was appended after the user's next message. Sending it answers
// nothing, so it is dropped from the request — the transcript itself is left alone.
func TestRequestsDropAResultThatLandedAfterTheNextMessage(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s, _, err := store.Open()
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	sess, _ := s.Create("t")
	r := New(ipc.NewEventBus(), s, quietConfig(0))

	mustAppendAgent(t, s, sess.ID,
		store.Message{ID: "u1", Role: "user", Text: "go"},
		store.Message{ID: "a1", Role: "assistant", ToolCalls: []llm.ToolCall{
			{ID: "call_a", Type: "function", Function: llm.FunctionCall{Name: "bash", Arguments: `{}`}},
		}},
		store.Message{ID: "u2", Role: "user", Text: "instead do this"},
		store.Message{ID: "t1", Role: "tool", ToolCallID: "call_a", Name: "bash", Text: "late result"},
	)

	got := r.historyMessages(sess.ID)
	assertCallsAnswered(t, got)
	for _, m := range got {
		if m.Role == llm.RoleTool && m.Content == "late result" {
			t.Errorf("a result that belongs to an earlier turn must not be sent: %s", shapeOfMessages(got))
		}
	}
	// The file is history and is not rewritten to make a request legal.
	if len(r.store.History(sess.ID)) != 4 {
		t.Errorf("the stored transcript must be untouched, got %s", shapeOf(r.store.History(sess.ID)))
	}
}

// A tool that ran and said nothing is still an answer. An empty content block is not: some providers
// reject it, and the model learns nothing from it.
func TestEmptyToolResultIsStillAnswered(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s, _, err := store.Open()
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	sess, _ := s.Create("t")
	r := New(ipc.NewEventBus(), s, quietConfig(0))

	mustAppendAgent(t, s, sess.ID,
		store.Message{ID: "u1", Role: "user", Text: "go"},
		store.Message{ID: "a1", Role: "assistant", ToolCalls: []llm.ToolCall{
			{ID: "call_a", Type: "function", Function: llm.FunctionCall{Name: "bash", Arguments: `{}`}},
		}},
		store.Message{ID: "t1", Role: "tool", ToolCallID: "call_a", Name: "bash", Text: ""},
	)

	got := r.historyMessages(sess.ID)
	assertCallsAnswered(t, got)
	if !containsText(got, toolNoOutput) {
		t.Errorf("an empty result should say so, got %s", shapeOfMessages(got))
	}
}

// assertCallsAnswered checks the rule both providers enforce: after an assistant message with tool
// calls, one tool message per call, with nothing in between.
func assertCallsAnswered(t *testing.T, msgs []llm.Message) {
	t.Helper()
	for i, m := range msgs {
		if m.Role != llm.RoleAssistant || len(m.ToolCalls) == 0 {
			continue
		}
		answered := map[string]bool{}
		for j := i + 1; j < len(msgs) && msgs[j].Role == llm.RoleTool; j++ {
			answered[msgs[j].ToolCallID] = true
		}
		for _, tc := range m.ToolCalls {
			if !answered[tc.ID] {
				t.Fatalf("%s is not answered directly after its call: %s", tc.ID, shapeOfMessages(msgs))
			}
		}
	}
	// A tool message with no call in front of it is equally rejected.
	for i, m := range msgs {
		if m.Role != llm.RoleTool {
			continue
		}
		var call *llm.Message
		for j := i - 1; j >= 0; j-- {
			if msgs[j].Role != llm.RoleTool {
				call = &msgs[j]
				break
			}
		}
		if call == nil || call.Role != llm.RoleAssistant {
			t.Fatalf("a tool result with no call before it: %s", shapeOfMessages(msgs))
		}
	}
}

func containsText(msgs []llm.Message, want string) bool {
	for _, m := range msgs {
		if strings.Contains(m.Content, want) {
			return true
		}
	}
	return false
}

func mustAppendAgent(t *testing.T, s *store.Store, id string, msgs ...store.Message) {
	t.Helper()
	if err := s.Append(id, msgs...); err != nil {
		t.Fatalf("Append: %v", err)
	}
}

// shapeOf renders a stored transcript as one token per message, so a failure reads at a glance.
func shapeOf(h []store.Message) string {
	out := make([]string, 0, len(h))
	for _, m := range h {
		switch {
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			out = append(out, "A+")
		case m.Role == "user":
			out = append(out, "u")
		case m.Role == "tool":
			out = append(out, "t")
		default:
			out = append(out, "a")
		}
	}
	return strings.Join(out, " ")
}

func shapeOfMessages(ms []llm.Message) string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		switch {
		case m.Role == llm.RoleAssistant && len(m.ToolCalls) > 0:
			out = append(out, "A+")
		case m.Role == llm.RoleTool:
			out = append(out, "t("+m.ToolCallID+")")
		default:
			out = append(out, string(m.Role)[:1])
		}
	}
	return strings.Join(out, " ")
}

// applyTestProvider points the fake provider at the test server and saves.
func applyTestProvider(t *testing.T, cfg *config.Config, url string) {
	t.Helper()
	p := cfg.Providers["fake"]
	p.BaseURL = url
	cfg.Providers["fake"] = p
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
}

// stuckTool ignores cancellation: a wedged child process, a stuck network call inside a tool.
type stuckTool struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *stuckTool) Spec() llm.Tool {
	return llm.NewTool("stuck", "ignores cancellation", map[string]any{"type": "object"})
}

func (s *stuckTool) Run(context.Context, string) (string, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return "finally", nil
}

// If the previous turn will not stop, the new one is refused. Writing anyway is precisely what makes
// the transcript unsendable, so a moment of "try again" is the lesser evil.
func TestTurnIsRefusedWhenTheOldOneWillNotStop(t *testing.T) {
	old := runStopTimeout
	runStopTimeout = 150 * time.Millisecond
	defer func() { runStopTimeout = old }()

	cfg := configWithProvider(t, 0, 0)
	srv := toolCallThenAnswer("stuck")
	defer srv.Close()
	applyTestProvider(t, cfg, srv.URL)

	r, sid, events := newTestRunner(t, cfg)
	if err := r.store.MarkSmartNamed(sid); err != nil {
		t.Fatal(err)
	}
	stuck := &stuckTool{entered: make(chan struct{}), release: make(chan struct{})}
	reg := tools.NewRegistry()
	reg.Add(stuck)
	r.SetTools(reg)

	env := tools.Env{Approval: tools.ApprovalAuto}
	model := config.Resolved{ID: "fake:m", Provider: "fake", Model: "m"}
	// Kept so the end of the test can wait for the turn instead of leaving it writing into a temp dir
	// that is about to be removed.
	var firstRun *run
	if firstID, _, err := r.Send(sid, "first", model.ID, "", nil, env); err != nil {
		t.Fatalf("first Send: %v", err)
	} else {
		r.mu.Lock()
		firstRun = r.runs[firstID]
		r.mu.Unlock()
		if firstRun == nil {
			t.Fatal("the first run is not registered")
		}
	}
	select {
	case <-stuck.entered:
	case <-time.After(5 * time.Second):
		t.Fatalf("the tool never started; events: %v", kindsOf(*events))
	}

	if _, _, err := r.Send(sid, "second", model.ID, "", nil, env); err == nil {
		t.Fatal("a turn that cannot stop must not be replaced silently")
	}
	for _, m := range r.store.History(sid) {
		if m.Role == "user" && m.Text == "second" {
			t.Errorf("the refused message was written into the transcript anyway: %s", shapeOf(r.store.History(sid)))
		}
	}
	close(stuck.release)

	// Returning here would leave the turn writing to the store while t.TempDir() removes it: the
	// cleanup then fails on a sessions directory that is not empty yet, which reads as a failure of
	// the thing under test. Wait for the run to say it will never write again.
	select {
	case <-firstRun.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the first turn never finished after its tool was released")
	}
}
