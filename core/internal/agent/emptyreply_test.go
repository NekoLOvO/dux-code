package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/NekoLOvO/dux-code/core/internal/config"
	"github.com/NekoLOvO/dux-code/core/internal/llm"
	"github.com/NekoLOvO/dux-code/core/internal/tools"
)

// A turn that ends with no text at all.
//
// Models do this, most often right after a tool result: the work is done, so they stop — with no
// summary. The turn is technically a success, but what the user sees is a green tool card and nothing
// under it, which is indistinguishable from the app dying mid-reply.

// scripted answers with a fixed sequence of results; the last one repeats. Every request is kept, so
// a test can check what the retry was actually told.
type scripted struct {
	steps []llm.Result
	reqs  []llm.Request
}

func (s *scripted) Stream(_ context.Context, req llm.Request, _ llm.DeltaFunc) (llm.Result, error) {
	s.reqs = append(s.reqs, req)
	i := len(s.reqs) - 1
	if i >= len(s.steps) {
		i = len(s.steps) - 1
	}
	return s.steps[i], nil
}

func (s *scripted) lastSystem() string {
	if len(s.reqs) == 0 {
		return ""
	}
	return s.reqs[len(s.reqs)-1].System
}

func toolCallResult(id, name string) llm.Result {
	return llm.Result{
		ToolCalls:  []llm.ToolCall{{ID: id, Type: "function", Function: llm.FunctionCall{Name: name, Arguments: `{"text":"hi"}`}}},
		StopReason: "tool_calls",
	}
}

// The common case: the model stops silently, gets asked once, and answers.
func TestSilentReplyIsAskedToContinue(t *testing.T) {
	cfg := quietConfig(5)
	r, sid, events := newTestRunner(t, cfg)
	model := config.Resolved{ID: "fake:m", Provider: "fake", Model: "m"}

	fake := &scripted{steps: []llm.Result{
		toolCallResult("c1", "echo"),
		{Content: "", StopReason: "stop"},
		// Asked to continue, it turns out there was work left after all.
		toolCallResult("c2", "echo"),
		{Content: "I ran the rest; the file it wrote is in place.", StopReason: "stop"},
	}}
	rn := r.newRun(sid, model, tools.Env{Approval: tools.ApprovalAuto}, "", false)
	r.execute(context.Background(), rn, fake, model, "do the thing")

	if len(fake.reqs) != 4 {
		t.Fatalf("want tool call, silent reply, continuation, answer: got %d requests", len(fake.reqs))
	}
	// The retry carries the explanation; the request after it must not.
	if !strings.Contains(fake.reqs[2].System, "Your previous message was empty") {
		t.Errorf("the retry was not told why it is being asked again:\n%s", fake.reqs[2].System)
	}
	if strings.Contains(fake.reqs[3].System, "previous message was empty") {
		t.Error("the note leaked into a later request")
	}

	kinds := kindsOf(*events)
	if kinds[len(kinds)-1] != "run.finished" {
		t.Errorf("a reply that arrives should finish the turn, got %v", kinds)
	}
	// What the user ends up with: the tool card and a real answer.
	sess, _ := r.store.Get(sid)
	var texts []string
	for _, m := range sess.Messages {
		if m.Role == "assistant" {
			texts = append(texts, m.Text)
		}
	}
	found := false
	for _, s := range texts {
		if strings.Contains(s, "the file it wrote is in place") {
			found = true
		}
	}
	if !found {
		t.Errorf("the answer was not stored: %q", texts)
	}
}

// Asked twice and still silent: the turn is reported, not stored as a blank bubble.
func TestSilentReplyTwiceEndsAsAFailure(t *testing.T) {
	cfg := quietConfig(5)
	r, sid, events := newTestRunner(t, cfg)
	model := config.Resolved{ID: "fake:m", Provider: "fake", Model: "m"}

	fake := &scripted{steps: []llm.Result{
		toolCallResult("c1", "echo"),
		{Content: "", StopReason: "stop"},
		{Content: "", StopReason: "stop"},
		{Content: "", StopReason: "stop"},
	}}
	rn := r.newRun(sid, model, tools.Env{Approval: tools.ApprovalAuto}, "", false)
	r.execute(context.Background(), rn, fake, model, "do the thing")

	if len(fake.reqs) != 3 {
		t.Fatalf("one nudge, not a loop: got %d requests", len(fake.reqs))
	}
	kinds := kindsOf(*events)
	if kinds[len(kinds)-1] != "run.failed" {
		t.Fatalf("silence after being asked has to be reported: %v", kinds)
	}
	failed := onlyKind(*events, "run.failed")
	if len(failed) != 1 || !strings.Contains(failed[0]["error"].(string), "without writing a reply") {
		t.Errorf("the message should say the reply was missing: %v", failed)
	}

	// Nothing blank is left in the transcript: an empty assistant message renders as a blank bubble
	// and reads as an interrupted turn.
	sess, _ := r.store.Get(sid)
	for _, m := range sess.Messages {
		if m.Role == "assistant" && strings.TrimSpace(m.Text) == "" && len(m.ToolCalls) == 0 {
			t.Fatalf("a blank assistant message was stored: %+v", sess.Messages)
		}
	}
}

// An empty reply that also hit the output cap is a different problem: the budget went to thinking.
// Asking again would spend it the same way, so this is reported with the cause named.
func TestEmptyReplyAtTheCapNamesTheCause(t *testing.T) {
	cfg := quietConfig(5)
	cfg.MaxOutputTokens = 1024
	r, sid, events := newTestRunner(t, cfg)
	model := config.Resolved{ID: "fake:m", Provider: "fake", Model: "m", MaxOutput: 0}

	fake := &scripted{steps: []llm.Result{
		{Content: "", StopReason: "length"},
	}}
	rn := r.newRun(sid, model, tools.Env{Approval: tools.ApprovalAuto}, "", false)
	r.execute(context.Background(), rn, fake, model, "think hard")

	if len(fake.reqs) != 1 {
		t.Errorf("a capped reply is not a silent one; it must not be retried: %d requests", len(fake.reqs))
	}
	failed := onlyKind(*events, "run.failed")
	if len(failed) != 1 {
		t.Fatalf("want one failure, got %v", kindsOf(*events))
	}
	msg, _ := failed[0]["error"].(string)
	if !strings.Contains(msg, "output cap") || !strings.Contains(msg, "reasoning depth") {
		t.Errorf("the message should name the knob that caused it, got %q", msg)
	}
}

// A normal reply costs exactly one request: the nudge is for silence only.
func TestNormalReplyIsNotNudged(t *testing.T) {
	cfg := quietConfig(5)
	r, sid, _ := newTestRunner(t, cfg)
	model := config.Resolved{ID: "fake:m", Provider: "fake", Model: "m"}

	fake := &scripted{steps: []llm.Result{{Content: "here is the answer", StopReason: "stop"}}}
	rn := r.newRun(sid, model, tools.Env{Approval: tools.ApprovalAuto}, "", false)
	r.execute(context.Background(), rn, fake, model, "hi")

	if len(fake.reqs) != 1 {
		t.Errorf("a reply with text must not be retried, got %d requests", len(fake.reqs))
	}
	if strings.Contains(fake.lastSystem(), "previous message was empty") {
		t.Error("the note was sent on a turn that never went silent")
	}
}
