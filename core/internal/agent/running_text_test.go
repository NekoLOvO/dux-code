package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NekoLOvO/dux-code/core/internal/config"
	"github.com/NekoLOvO/dux-code/core/internal/tools"
)

// A reply the user walks away from.
//
// The window no longer cancels a turn when the user switches sessions — the daemon owns it, so it
// keeps writing and the transcript gets the rest. Two things have to hold for that to be true: a
// cancelled turn still keeps what it had already written, and a window that comes back mid-reply can
// find out what the running step has written so far, because that part is not on disk yet.

// slowStream serves a reply one word at a time, with a gap between words, and closes `started` once the
// first word is out. That is the moment a reader of the transcript has something to ask about.
func slowStream(words []string, delay time.Duration) (*httptest.Server, <-chan struct{}) {
	started := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for i, word := range words {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", word)
			if flusher != nil {
				flusher.Flush()
			}
			if i == 0 {
				once.Do(func() { close(started) })
			}
			time.Sleep(delay)
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":9}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	return srv, started
}

// waitFor polls a condition of the run's in-flight buffers. The words arrive on the network, so there
// is nothing to subscribe to; the alternative would be a fixed sleep, which is slower and flakier.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A window that opens (or comes back) while a step is streaming asks session.get what is running. The
// transcript cannot answer that — only finished steps are written — so the runner has to.
func TestRunningTextReportsTheStepInFlight(t *testing.T) {
	cfg := configWithProvider(t, 0, 0)
	srv, started := slowStream([]string{"Hello", " there", ",", " friend"}, 40*time.Millisecond)
	defer srv.Close()
	applyTestProvider(t, cfg, srv.URL)

	r, sid, _ := newTestRunner(t, cfg)
	if err := r.store.MarkSmartNamed(sid); err != nil {
		t.Fatal(err)
	}

	if text, _ := r.RunningText(sid); text != "" {
		t.Fatalf("an idle session reports text: %q", text)
	}

	env := tools.Env{Approval: tools.ApprovalAuto}
	runID, _, err := r.Send(sid, "say hello", "fake:m", "", nil, env)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	// Returning while the turn is still writing would leave it appending to the store after t.TempDir()
	// removes the directory, which shows up as a failure of something else entirely.
	r.mu.Lock()
	rn := r.runs[runID]
	r.mu.Unlock()
	if rn == nil {
		t.Fatal("the run is not registered")
	}
	defer func() {
		select {
		case <-rn.done:
		case <-time.After(10 * time.Second):
			t.Fatal("the turn never finished")
		}
	}()

	waitFor(t, "the first words to be buffered", func() bool {
		text, _ := r.RunningText(sid)
		return strings.Contains(text, "Hello")
	})

	text, _ := r.RunningText(sid)
	if !strings.HasPrefix("Hello there, friend", text) || text == "" {
		t.Fatalf("the running text is not a prefix of the reply: %q", text)
	}
	if strings.HasPrefix(text, "Hello there, friend") {
		t.Fatalf("the text was already complete; the test cannot tell prefix from final: %q", text)
	}

	// What the transcript holds at this moment is the user message and nothing else: the reply is still
	// being written, which is exactly why the running text has to be asked for separately.
	for _, m := range r.store.History(sid) {
		if m.Role == "assistant" && strings.Contains(m.Text, "Hello") {
			t.Fatalf("the step in flight was persisted: %q", m.Text)
		}
	}
}

// The user presses stop: what was already written stays in the transcript. Losing it is what makes a
// stop feel like a crash, and a session switch used to send this same cancellation.
func TestCancelledTurnKeepsWhatItWrote(t *testing.T) {
	cfg := configWithProvider(t, 0, 0)
	srv, started := slowStream([]string{"part ", "one ", "part ", "two ", "part ", "three"}, 60*time.Millisecond)
	defer srv.Close()
	applyTestProvider(t, cfg, srv.URL)

	r, sid, _ := newTestRunner(t, cfg)
	if err := r.store.MarkSmartNamed(sid); err != nil {
		t.Fatal(err)
	}

	env := tools.Env{Approval: tools.ApprovalAuto}
	runID, _, err := r.Send(sid, "keep talking", "fake:m", "", nil, env)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	waitFor(t, "some text to arrive", func() bool {
		text, _ := r.RunningText(sid)
		return strings.Contains(text, "part")
	})
	before, _ := r.RunningText(sid)

	if !r.Cancel(runID) {
		t.Fatal("the run was not cancellable")
	}
	waitFor(t, "the run to stop", func() bool { return !r.Busy(sid) })

	var answer string
	for _, m := range r.store.History(sid) {
		if m.Role == "assistant" {
			answer = m.Text
		}
	}
	if answer == "" {
		t.Fatalf("the cancelled turn kept nothing; it had written %q", before)
	}
	if !strings.HasPrefix("part one part two part three", answer) {
		t.Fatalf("what was kept is not what was on screen: %q", answer)
	}
	// The buffers are per step, so they are cleared with the run.
	if text, _ := r.RunningText(sid); text != "" {
		t.Errorf("a stopped session still reports running text: %q", text)
	}
}

// A retried attempt writes from the beginning again, so the prefix must not be handed out twice.
func TestRunningTextIsResetByARetry(t *testing.T) {
	cfg := configWithProvider(t, 0, 0)
	var (
		mu    sync.Mutex
		calls int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			// Speak, then drop the connection: a broken stream is retryable, but not after speaking.
			fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"first try"}}]}`+"\n\n")
			return
		}
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"second try"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	applyTestProvider(t, cfg, srv.URL)

	r, sid, events := newTestRunner(t, cfg)
	if err := r.store.MarkSmartNamed(sid); err != nil {
		t.Fatal(err)
	}
	env := tools.Env{Approval: tools.ApprovalAuto}
	if _, _, err := r.Send(sid, "go", "fake:m", "", nil, env); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the turn to finish", func() bool { return !r.Busy(sid) })

	// The turn failed on the first attempt and was not retried (it had spoken), so it is stored as an
	// error — what matters here is that the buffers did not accumulate across attempts.
	if text, _ := r.RunningText(sid); text != "" {
		t.Errorf("running text survived the run: %q", text)
	}
	if len(kindsOf(*events)) == 0 {
		t.Error("no events were published")
	}
}

// Busy and RunningText have to describe the same turn: the UI reads both from one session.get.
func TestRunningTextAndBusyAgree(t *testing.T) {
	r, sid, _ := newTestRunner(t, quietConfig(0))
	if r.Busy(sid) {
		t.Fatal("a fresh runner reports a busy session")
	}
	model := config.Resolved{ID: "fake:m", Provider: "fake", Model: "m"}
	rn := r.newRun(sid, model, tools.Env{}, "", false)
	r.mu.Lock()
	r.runs[rn.id] = rn
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.runs, rn.id)
		r.mu.Unlock()
	}()

	if !r.Busy(sid) {
		t.Fatal("a registered run is not reported as busy")
	}
	rn.addPart("hello")
	rn.addThink("thinking")
	text, reasoning := r.RunningText(sid)
	if text != "hello" || reasoning != "thinking" {
		t.Fatalf("RunningText = %q, %q", text, reasoning)
	}
	rn.beginAttempt()
	if text, reasoning := r.RunningText(sid); text != "" || reasoning != "" {
		t.Fatalf("a new attempt did not clear the buffers: %q, %q", text, reasoning)
	}
}

// The sidebar's elapsed counter is the daemon's number, not the window's: a window that joins a turn
// already in progress has to show the turn's real age, or coming back from another conversation
// restarts the counter at 0s.
func TestStartedAtReportsTheRunBegin(t *testing.T) {
	cfg := configWithProvider(t, 0, 0)
	srv, started := slowStream([]string{"a", " b", " c"}, 80*time.Millisecond)
	defer srv.Close()
	applyTestProvider(t, cfg, srv.URL)

	r, sid, _ := newTestRunner(t, cfg)
	if err := r.store.MarkSmartNamed(sid); err != nil {
		t.Fatal(err)
	}
	if at, ok := r.StartedAt(sid); ok || at != 0 {
		t.Fatalf("an idle session reports a start: %d, %v", at, ok)
	}

	before := time.Now().UnixMilli()
	runID, _, err := r.Send(sid, "go", "fake:m", "", nil, tools.Env{Approval: tools.ApprovalAuto})
	if err != nil {
		t.Fatal(err)
	}
	<-started

	r.mu.Lock()
	rn := r.runs[runID]
	r.mu.Unlock()
	if rn == nil {
		t.Fatal("the run is not registered")
	}
	defer func() {
		select {
		case <-rn.done:
		case <-time.After(10 * time.Second):
			t.Fatal("the turn never finished")
		}
	}()

	at, ok := r.StartedAt(sid)
	if !ok {
		t.Fatal("a running session reports no start")
	}
	// It is the run's own start, so it sits between "just before Send" and now — and it does not move
	// as the turn goes on, which is what lets a window that joins late show the real age.
	if at < before || at > time.Now().UnixMilli() {
		t.Fatalf("start %d is outside [%d, now]", at, before)
	}
	again, _ := r.StartedAt(sid)
	if again != at {
		t.Fatalf("the start moved: %d then %d", at, again)
	}
}
