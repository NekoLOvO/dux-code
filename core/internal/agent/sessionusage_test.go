package agent

import (
	"context"
	"testing"

	"github.com/NekoLOvO/dux-code/core/internal/config"
	"github.com/NekoLOvO/dux-code/core/internal/llm"
	"github.com/NekoLOvO/dux-code/core/internal/tools"
)

// runTurn executes one more turn on an existing runner and session, so a test can watch the session
// total grow across turns the way the composer chip does.
func runTurn(t *testing.T, r *Runner, sid string, fake llm.Client, prompt string) []map[string]any {
	t.Helper()
	var events []map[string]any
	r.OnEvent = func(ev map[string]any) {
		events = append(events, ev)
	}
	model := config.Resolved{ID: "fake:m", Provider: "fake", Model: "m"}
	rn := r.newRun(sid, model, tools.Env{}, "", false)
	r.execute(context.Background(), rn, fake, model, prompt)
	return events
}

// toolThenFails reports usage for a tool call (that request was billed), then fails the next one. The
// tokens of the successful step must not disappear with the failed turn.
type toolThenFails struct{ calls int }

func (c *toolThenFails) Stream(_ context.Context, _ llm.Request, _ llm.DeltaFunc) (llm.Result, error) {
	c.calls++
	if c.calls > 1 {
		return llm.Result{}, &llm.HTTPError{Status: 400, Body: "no"}
	}
	return llm.Result{
		Usage: llm.Usage{Input: fakeUsageIn, Output: fakeUsageOut},
		ToolCalls: []llm.ToolCall{{
			ID:       "call-1",
			Type:     "function",
			Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`},
		}},
		StopReason: "tool_calls",
	}, nil
}

// The composer chip reads one number — everything this session has spent — and the daemon has to
// report it as an absolute value. It repeats on every step and again on run.finished, so a delta
// would be counted twice by any listener that adds instead of assigns.
func TestSessionUsageIsAbsoluteAndKeepsGrowing(t *testing.T) {
	r, sid, _ := newTestRunner(t, quietConfig(2))

	// Turn one: two tool steps plus the closing round = three requests.
	first := runTurn(t, r, sid, &toolHungry{}, "one")
	finished := onlyKind(first, "run.finished")
	if len(finished) != 1 {
		t.Fatalf("want one run.finished, got %d", len(finished))
	}
	stepUsage := onlyKind(first, "run.usage")
	lastStepTotal := usageField(t, stepUsage[len(stepUsage)-1], "sessionUsage")
	if lastStepTotal.Input != 3*fakeUsageIn || lastStepTotal.Output != 3*fakeUsageOut {
		t.Errorf("after turn one the session total should be 3 requests worth (%d/%d), got %+v",
			3*fakeUsageIn, 3*fakeUsageOut, lastStepTotal)
	}
	if got := usageField(t, finished[0], "sessionUsage"); got != lastStepTotal {
		t.Errorf("run.finished reports %+v but the last step already reported %+v; the same money twice", got, lastStepTotal)
	}

	// Turn two adds to it rather than replacing it: the chip must not fall back to "this turn only".
	second := runTurn(t, r, sid, &toolHungry{}, "two")
	finished = onlyKind(second, "run.finished")
	if got := usageField(t, finished[0], "sessionUsage"); got.Input != 6*fakeUsageIn || got.Output != 6*fakeUsageOut {
		t.Errorf("after turn two the session total should be 6 requests worth (%d/%d), got %+v",
			6*fakeUsageIn, 6*fakeUsageOut, got)
	}

	// Whatever the events said, the stored total is the authority the next session.get hands the UI.
	if stored := r.store.TotalUsage(sid); stored.Input != 6*fakeUsageIn || stored.Output != 6*fakeUsageOut {
		t.Errorf("stored total = %+v, want 6 requests worth", stored)
	}
}

// A turn that fails still burned tokens on the steps before it. Dropping them would make the chip
// quietly under-report, which is the one thing it must not do.
func TestFailedTurnStillCounts(t *testing.T) {
	r, sid, _ := newTestRunner(t, quietConfig(3))

	events := runTurn(t, r, sid, &toolThenFails{}, "hi")
	failed := onlyKind(events, "run.failed")
	if len(failed) != 1 {
		t.Fatalf("want one run.failed, got %v", kindsOf(events))
	}
	if got := usageField(t, failed[0], "sessionUsage"); got.Input != fakeUsageIn || got.Output != fakeUsageOut {
		t.Errorf("the failed turn's session total = %+v, want the billed step (%d/%d)",
			got, fakeUsageIn, fakeUsageOut)
	}
	if got := r.store.TotalUsage(sid); got.Input != fakeUsageIn || got.Output != fakeUsageOut {
		t.Errorf("stored total = %+v, want the billed step", got)
	}
}
