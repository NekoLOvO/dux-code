package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NekoLOvO/dux-code/core/internal/config"
)

// shortenIdle makes the watchdog fire in the test's lifetime instead of in two minutes.
func shortenIdle(t *testing.T, d time.Duration) {
	t.Helper()
	old := streamIdleTimeout
	streamIdleTimeout = d
	t.Cleanup(func() { streamIdleTimeout = old })
}

// sseServer answers one chat completion, running write after the stream is open.
func sseServer(write func(w http.ResponseWriter, r *http.Request, flush func())) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flush := func() {
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		write(w, r, flush)
	}))
}

func openaiTestClient(t *testing.T, url string) Client {
	t.Helper()
	c, err := NewOpenAI(config.Provider{Type: "openai", BaseURL: url, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	return c
}

// silence ends when the request does: the client hung up, or the test server was torn down. It is how
// a handler holds a connection open without saying anything.
func silence(r *http.Request) { <-r.Context().Done() }

// A provider that goes silent mid-reply must fail the step, not hang the turn.
//
// This is the difference between "the app looks frozen and the only way out is pressing stop" and "it
// says the reply stopped, and retries".
func TestSilentStreamFailsInsteadOfHanging(t *testing.T) {
	shortenIdle(t, 200*time.Millisecond)

	srv := sseServer(func(w http.ResponseWriter, r *http.Request, flush func()) {
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"half a sen"}}]}`+"\n\n")
		flush()
		// Then nothing. The client is left holding an open connection that will never speak again.
		silence(r)
	})
	defer srv.Close()

	c := openaiTestClient(t, srv.URL)
	done := make(chan error, 1)
	go func() {
		_, err := c.Stream(context.Background(), Request{Model: "m"}, func(string) {})
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, ErrStreamStalled) {
			t.Fatalf("want a stall, got %v", err)
		}
		if !Retryable(err) {
			t.Error("a stalled stream produced nothing that made it a refusal, so it should be retried")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stream never ended: a silent provider hangs the turn")
	}
}

// A reply that keeps talking must not be killed by the watchdog, however long it runs.
func TestSlowButLiveStreamSurvivesTheWatchdog(t *testing.T) {
	shortenIdle(t, 150*time.Millisecond)

	srv := sseServer(func(w http.ResponseWriter, _ *http.Request, flush func()) {
		// Six chunks, each after a pause longer than half the timeout: silence never lasts long enough.
		for i := 0; i < 6; i++ {
			time.Sleep(80 * time.Millisecond)
			fmt.Fprintf(w, `data: {"choices":[{"index":0,"delta":{"content":"%d"}}]}`+"\n\n", i)
			flush()
		}
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	defer srv.Close()

	c := openaiTestClient(t, srv.URL)
	res, err := c.Stream(context.Background(), Request{Model: "m"}, func(string) {})
	if err != nil {
		t.Fatalf("a live stream must not be cut: %v", err)
	}
	if res.Content != "012345" {
		t.Errorf("every chunk should arrive, got %q", res.Content)
	}
}

// SSE keep-alives are traffic. A provider that pings while it thinks is not silent, and killing it
// would cut off exactly the long reasoning turns the timeout was meant to protect.
func TestKeepAlivesCountAsTraffic(t *testing.T) {
	shortenIdle(t, 150*time.Millisecond)

	srv := sseServer(func(w http.ResponseWriter, _ *http.Request, flush func()) {
		for i := 0; i < 6; i++ {
			time.Sleep(80 * time.Millisecond)
			fmt.Fprint(w, ": keep-alive\n\n")
			flush()
		}
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	defer srv.Close()

	c := openaiTestClient(t, srv.URL)
	res, err := c.Stream(context.Background(), Request{Model: "m"}, func(string) {})
	if err != nil {
		t.Fatalf("keep-alives should hold the connection open: %v", err)
	}
	if res.Content != "done" {
		t.Errorf("got %q", res.Content)
	}
}

// Pressing stop is not a stall: the user needs to see "cancelled", not "the provider went quiet".
func TestCancelIsNotReportedAsAStall(t *testing.T) {
	shortenIdle(t, 2*time.Second)

	started := make(chan struct{})
	var once atomic.Bool
	srv := sseServer(func(w http.ResponseWriter, r *http.Request, flush func()) {
		if !once.Swap(true) {
			close(started)
		}
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"thinking"}}]}`+"\n\n")
		flush()
		silence(r)
	})
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c := openaiTestClient(t, srv.URL)
	done := make(chan error, 1)
	go func() {
		_, err := c.Stream(ctx, Request{Model: "m"}, func(string) {})
		done <- err
	}()
	<-started
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, ErrCancelled) {
			t.Fatalf("want ErrCancelled, got %v", err)
		}
		if errors.Is(err, ErrStreamStalled) {
			t.Error("a stop must never read as a stalled connection")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling did not end the stream")
	}
}
