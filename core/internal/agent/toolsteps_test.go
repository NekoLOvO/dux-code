package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/NekoLOvO/dux-code/core/internal/config"
	"github.com/NekoLOvO/dux-code/core/internal/ipc"
	"github.com/NekoLOvO/dux-code/core/internal/llm"
	"github.com/NekoLOvO/dux-code/core/internal/store"
	"github.com/NekoLOvO/dux-code/core/internal/tools"
)

// The fake model reports this usage on every response, making the accumulation easy to verify.
const (
	fakeUsageIn  = 10
	fakeUsageOut = 5
)

// toolHungry is a fake model that keeps asking for tools as long as it sees any, and only talks once
// none are offered. Real models look roughly like this, which is why a step cap exists.
type toolHungry struct {
	requests  int
	toolCount []int    // how many tools each request carried
	systems   []string // the system prompt of each request

	// ignoreWrapUp makes it return tool_calls even in the closing round (no tools), to check that "model
	// ignores the instruction" never leaves unmatched tool calls behind.
	ignoreWrapUp bool
}

func (c *toolHungry) Stream(_ context.Context, req llm.Request, _ llm.DeltaFunc) (llm.Result, error) {
	c.requests++
	c.toolCount = append(c.toolCount, len(req.Tools))
	c.systems = append(c.systems, req.System)
	usage := llm.Usage{Input: fakeUsageIn, Output: fakeUsageOut}

	if len(req.Tools) == 0 && !c.ignoreWrapUp {
		return llm.Result{Content: "got this far, tests not run yet", Usage: usage, StopReason: "stop"}, nil
	}
	// It also emits text while ignoring the instruction: real models usually give both, and what is
	// checked here is "keep the text, drop the tool_calls".
	text := ""
	if c.ignoreWrapUp && len(req.Tools) == 0 {
		text = "let me look at the file once more"
	}
	return llm.Result{
		Content: text,
		Usage:   usage,
		ToolCalls: []llm.ToolCall{{
			ID:       "call-1",
			Type:     "function",
			Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`},
		}},
		StopReason: "tool_calls",
	}, nil
}

// newTestRunner builds a Runner that runs but cannot touch the world: the file / shell / network
// switches are off, so a tool request is only logged as "disabled by the user" — nothing executes and
// nothing asks for approval.
func newTestRunner(t *testing.T, cfg *config.Config) (*Runner, string, *[]map[string]any) {
	t.Helper()
	// store.Dir() honours XDG_DATA_HOME, so nothing is written to the real ~/.local/share.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	st, _, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	sess, err := st.Create("t")
	if err != nil {
		t.Fatal(err)
	}
	// A named session keeps the auto-title request out of every test. That request is deliberately
	// not something a turn waits for — `finishAnswer` starts it on its own goroutine — so it can
	// arrive after the test has finished with the session: a third HTTP request at a fake server
	// that was counting them (the ceiling test wants exactly two), or a write into a directory
	// `t.TempDir` has already removed, which surfaces as `rename …: no such file or directory` and a
	// cleanup that fails with "directory not empty". That is what CI hit. Naming is not what any
	// test in this package is about; the explicit `session.rename` path is covered in api.
	if err := st.MarkSmartNamed(sess.ID); err != nil {
		t.Fatal(err)
	}

	var events []map[string]any
	r := New(ipc.NewEventBus(), st, cfg)
	r.OnEvent = func(ev map[string]any) {
		events = append(events, ev)
	}
	return r, sess.ID, &events
}

func quietConfig(steps int) *config.Config {
	off := false
	return &config.Config{
		MaxToolSteps: steps,
		Tools: config.ToolSwitches{
			FileAccess: &off,
			Shell:      &off,
			WebSearch:  &off,
		},
	}
}

func runOnce(t *testing.T, cfg *config.Config, fake llm.Client) (*Runner, string, *[]map[string]any) {
	t.Helper()
	r, sid, events := newTestRunner(t, cfg)
	model := config.Resolved{ID: "fake:m", Provider: "fake", Model: "m"}
	rn := r.newRun(sid, model, tools.Env{}, "", false)
	r.execute(context.Background(), rn, fake, model, "hi")
	return r, sid, events
}

// kindsOf flattens the event stream into a kind sequence, which is easier to assert on.
func kindsOf(events []map[string]any) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		k, _ := ev["kind"].(string)
		out = append(out, k)
	}
	return out
}

func onlyKind(events []map[string]any, kind string) []map[string]any {
	var out []map[string]any
	for _, ev := range events {
		if ev["kind"] == kind {
			out = append(out, ev)
		}
	}
	return out
}

func usageField(t *testing.T, ev map[string]any, key string) llm.Usage {
	t.Helper()
	m, ok := ev[key].(map[string]any)
	if !ok {
		t.Fatalf("%s is missing or has the wrong shape: %#v", key, ev[key])
	}
	in, _ := m["input"].(int)
	out, _ := m["output"].(int)
	return llm.Usage{Input: in, Output: out}
}

// Running out of steps is not a failure but a closing round: take the tools away and let the model
// finish its sentence.
//
// Sending run.failed directly leaves the user with an error and no idea how far it got.
func TestStepsExhaustedStillEndsWithAnAnswer(t *testing.T) {
	fake := &toolHungry{}
	r, sid, events := runOnce(t, quietConfig(3), fake)

	// three tool steps plus one closing step
	if fake.requests != 4 {
		t.Fatalf("should call the model 4 times (3 steps + wrap-up), got %d", fake.requests)
	}
	if fake.toolCount[3] != 0 {
		t.Errorf("the wrap-up round must get no tools, got %d", fake.toolCount[3])
	}
	for i := 0; i < 3; i++ {
		if fake.toolCount[i] == 0 {
			t.Errorf("step %d should still have tools, got 0", i)
		}
	}
	if !strings.Contains(fake.systems[3], "cannot call any more tools") {
		t.Errorf("the wrap-up round must explain why there are no tools, got %q", fake.systems[3])
	}

	kinds := kindsOf(*events)
	if got := kinds[len(kinds)-1]; got != "run.finished" {
		t.Errorf("the last event should be run.finished (with an answer), got %q (all: %v)", got, kinds)
	}

	hist := r.store.History(sid)
	last := hist[len(hist)-1]
	if last.Role != "assistant" || !strings.Contains(last.Text, "got this far") {
		t.Errorf("the last message should be the wrap-up answer, got role=%s text=%q", last.Role, last.Text)
	}
}

// Usage must go out on every step rather than at the end: one tool-loop step can be tens of thousands
// of tokens, and not seeing the number mid-run is seeing nothing.
func TestUsageIsPublishedEveryStep(t *testing.T) {
	_, _, events := runOnce(t, quietConfig(3), &toolHungry{})

	usages := onlyKind(*events, "run.usage")
	if len(usages) != 4 {
		t.Fatalf("4 requests should produce 4 run.usage events, got %d", len(usages))
	}

	// lastUsage is "just that one request": identical every step. usage is this turn's total: n times as
	// large at step n.
	for i, ev := range usages {
		last := usageField(t, ev, "lastUsage")
		if last.Input != fakeUsageIn || last.Output != fakeUsageOut {
			t.Errorf("lastUsage for step %d should be the usage of this one request, got %+v", i, last)
		}
		total := usageField(t, ev, "usage")
		wantIn := (i + 1) * fakeUsageIn
		wantOut := (i + 1) * fakeUsageOut
		if total.Input != wantIn || total.Output != wantOut {
			t.Errorf("the running total after step %d should be %d/%d, got %+v", i, wantIn, wantOut, total)
		}
	}
}

// The closing round had no tools and the model still returned tool_calls: the text is the answer, but
// it must **not** be persisted — tool_calls without matching results make the provider reject the
// next request.
func TestWrapUpIgnoringInstructionsLeavesNoDanglingCall(t *testing.T) {
	fake := &toolHungry{ignoreWrapUp: true}
	r, sid, events := runOnce(t, quietConfig(2), fake)

	kinds := kindsOf(*events)
	if got := kinds[len(kinds)-1]; got == "run.failed" {
		t.Fatalf("text from the wrap-up round is the answer, not a failure (all: %v)", kinds)
	}

	hist := r.store.History(sid)
	answered := map[string]bool{}
	for _, m := range hist {
		if m.Role == "tool" {
			answered[m.ToolCallID] = true
		}
	}
	for _, m := range hist {
		for _, tc := range m.ToolCalls {
			if !answered[tc.ID] {
				t.Fatalf("the history holds an unanswered tool call %s (%s), which would 400 on the next request", tc.ID, tc.Function.Name)
			}
		}
	}
}

// 0 means "off" (the user turned the fuse off); only a negative value, which no UI produces, is
// treated as a typo and replaced by the default.
func TestToolStepLimitOffAndClamping(t *testing.T) {
	if got := (&config.Config{MaxToolSteps: 0}).ToolStepLimit(); got != 0 {
		t.Errorf("MaxToolSteps=0 should mean no cap, got %d", got)
	}
	if got := (&config.Config{MaxToolSteps: -1}).ToolStepLimit(); got != config.DefaultMaxToolSteps {
		t.Errorf("MaxToolSteps=-1 should fall back to the default %d, got %d", config.DefaultMaxToolSteps, got)
	}
	if got := (&config.Config{MaxToolSteps: 99999}).ToolStepLimit(); got != config.MaxToolStepsLimit {
		t.Errorf("above the cap it must clamp to %d, got %d", config.MaxToolStepsLimit, got)
	}
	if got := (&config.Config{MaxToolSteps: 7}).ToolStepLimit(); got != 7 {
		t.Errorf("a valid value must be left alone, got %d", got)
	}
}

// A live bubble has no id: the daemon mints it when the message is written. Without the id the UI
// cannot address that row, and every message action — branch, edit, regenerate, truncate — answers
// "that message is still being saved" for the rest of the window's life. A reload (or a restart)
// was the only way out, which is exactly the bug this pins.
//
// So both events that end a row have to name the message it became: run.step closes a bubble per
// tool step, run.finished closes the last one.
func TestEveryBubbleEndsWithItsMessageID(t *testing.T) {
	fake := &toolHungry{}
	r, sid, events := runOnce(t, quietConfig(3), fake)

	sess, ok := r.store.Get(sid)
	if !ok {
		t.Fatal("session vanished")
	}
	// Every assistant message the run persisted, in order: one per tool step, then the answer.
	persisted := make(map[string]bool)
	var order []string
	for _, m := range sess.Messages {
		if m.Role == "assistant" {
			persisted[m.ID] = true
			order = append(order, m.ID)
		}
	}
	if len(order) < 2 {
		t.Fatalf("expected a tool step and an answer, got %d assistant messages", len(order))
	}

	// run.step names the bubble it closes: the tool-call message of the step that just ended.
	steps := onlyKind(*events, "run.step")
	if len(steps) == 0 {
		t.Fatal("no run.step events")
	}
	for i, ev := range steps {
		id, _ := ev["previousMessageId"].(string)
		if id == "" {
			t.Errorf("run.step %d carries no previousMessageId", i)
			continue
		}
		if !persisted[id] {
			t.Errorf("run.step %d names %q, which is not a message this run wrote", i, id)
		}
	}

	// run.finished names the answer.
	finished := onlyKind(*events, "run.finished")
	if len(finished) != 1 {
		t.Fatalf("expected one run.finished, got %d", len(finished))
	}
	msg, _ := finished[0]["message"].(map[string]any)
	id, _ := msg["id"].(string)
	if id == "" {
		t.Fatal("run.finished carries no message id, so the last bubble stays unaddressable")
	}
	if !persisted[id] {
		t.Errorf("run.finished names %q, which is not a message this run wrote", id)
	}
	if id != order[len(order)-1] {
		t.Errorf("run.finished names %q, want the final message %q", id, order[len(order)-1])
	}

	// And the ids are distinct: the UI keys rows by them, so two rows sharing one would merge.
	seen := map[string]bool{}
	for _, ev := range steps {
		if v, _ := ev["previousMessageId"].(string); v != "" {
			if seen[v] {
				t.Errorf("two run.step events name the same message %q", v)
			}
			seen[v] = true
		}
	}
}
