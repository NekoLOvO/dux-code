package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/NekoLOvO/dux-code/core/internal/llm"
)

// flaky answers 429 for the first fails attempts, then replies normally.
type flaky struct {
	fails int
	calls int
	// speak: emit a sentence before failing. That case must not retry, or it appears twice.
	speak bool
}

func (c *flaky) Stream(_ context.Context, _ llm.Request, onDelta llm.DeltaFunc) (llm.Result, error) {
	c.calls++
	if c.calls <= c.fails {
		if c.speak && onDelta != nil {
			onDelta("half a sentence")
		}
		return llm.Result{}, &llm.HTTPError{Status: 429, Body: "slow down"}
	}
	return llm.Result{Content: "ok", Usage: llm.Usage{Input: 1, Output: 1}, StopReason: "stop"}, nil
}

func fastRetry(t *testing.T) {
	t.Helper()
	old := retryBaseDelay
	retryBaseDelay = time.Millisecond
	t.Cleanup(func() { retryBaseDelay = old })
}

// 429 means "wait a moment and it will pass", and the user should not see the turn fail.
func TestRetriesTransientFailure(t *testing.T) {
	fastRetry(t)
	fake := &flaky{fails: 1}
	_, _, events := runOnce(t, quietConfig(3), fake)

	if fake.calls != 2 {
		t.Fatalf("should retry once (2 requests in total), got %d", fake.calls)
	}
	kinds := kindsOf(*events)
	if kinds[len(kinds)-1] != "run.finished" {
		t.Errorf("a successful retry should finish normally, got %v", kinds)
	}
}

// After emitting, retrying is not allowed: the user would see the same sentence twice.
func TestNoRetryAfterTextReachedUI(t *testing.T) {
	fastRetry(t)
	fake := &flaky{fails: 1, speak: true}
	_, _, events := runOnce(t, quietConfig(3), fake)

	if fake.calls != 1 {
		t.Fatalf("once text was emitted there must be no retry, got %d requests", fake.calls)
	}
	kinds := kindsOf(*events)
	if kinds[len(kinds)-1] == "run.finished" {
		t.Errorf("this case should end in failure, got %v", kinds)
	}
}

// 4xx (bad key, bad parameters) will not become right on the hundredth retry; it only makes the user
// wait.
func TestDoesNotRetryClientErrors(t *testing.T) {
	fastRetry(t)
	fake := &badRequest{}
	_, _, _ = runOnce(t, quietConfig(3), fake)
	if fake.calls != 1 {
		t.Fatalf("400 must not be retried, got %d requests", fake.calls)
	}
}

type badRequest struct{ calls int }

func (c *badRequest) Stream(_ context.Context, _ llm.Request, _ llm.DeltaFunc) (llm.Result, error) {
	c.calls++
	return llm.Result{}, &llm.HTTPError{Status: 400, Body: "bad request"}
}

// silent is a stream that dies without saying anything: the provider stopped sending and the
// watchdog dropped the connection.
type silent struct {
	calls int
	spoke bool
}

func (c *silent) Stream(_ context.Context, _ llm.Request, onDelta llm.DeltaFunc) (llm.Result, error) {
	c.calls++
	if c.spoke && onDelta != nil {
		onDelta("half an ans")
	}
	return llm.Result{}, fmt.Errorf("%w (nothing for 2m0s)", llm.ErrStreamStalled)
}

// A provider that goes quiet and says nothing is worth another attempt: no refusal, no answer, and
// the user would otherwise lose the whole turn to a hiccup on the other side.
func TestRetriesAStalledStream(t *testing.T) {
	fastRetry(t)
	fake := &silent{}
	_, _, _ = runOnce(t, quietConfig(3), fake)

	if fake.calls != maxRetries+1 {
		t.Fatalf("a silent stream should be retried, got %d attempts", fake.calls)
	}
}

// Once text reached the screen, silence ends the turn: replaying it would show the same sentence
// twice, which is worse than a sentence that stops.
func TestNoRetryAfterASilenceThatSpoke(t *testing.T) {
	fastRetry(t)
	fake := &silent{spoke: true}
	_, _, events := runOnce(t, quietConfig(3), fake)

	if fake.calls != 1 {
		t.Fatalf("after emitting, a stall must not retry, got %d attempts", fake.calls)
	}
	kinds := kindsOf(*events)
	if kinds[len(kinds)-1] != "run.failed" {
		t.Errorf("the turn should fail with the stall named, got %v", kinds)
	}
	failed := onlyKind(*events, "run.failed")
	if len(failed) != 1 || !strings.Contains(failed[0]["error"].(string), "stopped arriving") {
		t.Errorf("the message should say the reply stopped, got %v", failed)
	}
}
