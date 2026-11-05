package ipc

import (
	"bufio"
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestServer starts a socket only tests use.
func newTestServer(t *testing.T, handler Handler) (*Server, string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "test.sock")
	s := NewServer(Config{SocketPath: sock, APIVersion: 1}, handler)
	if err := s.Listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		_ = s.Close()
		<-done
	})
	return s, sock
}

// TestReverseRequestAllow is the important one: the daemon asks the client something, the client
// answers, the daemon gets it. The whole permission flow hangs on this.
func TestReverseRequestAllow(t *testing.T) {
	_, sock := newTestServer(t, func(ctx context.Context, w *Writer, req Request, sub *Subscriber) {
		res, err := sub.Request(ctx, "permission.request", map[string]any{
			"kind": "bash",
		}, 3*time.Second)
		if err != nil {
			w.RespondError(req.ID, err.Error())
			return
		}
		w.Respond(req.ID, res)
	})

	c, err := Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	var seenMethod string
	c.OnRequest(func(method string, params map[string]any) (any, error) {
		seenMethod = method
		return map[string]any{"allow": true}, nil
	})

	raw, err := c.Call("ask", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if seenMethod != "permission.request" {
		t.Fatalf("client got the wrong method: %q", seenMethod)
	}
	if string(raw) != `{"allow":true}` {
		t.Fatalf("unexpected response: %s", raw)
	}
}

// TestReverseRequestNoHandler checks that a CLI without a handler answers "unsupported" at
// once instead of letting the daemon wait out the timeout — this decides whether running tools
// from the CLI stalls for five minutes.
func TestReverseRequestNoHandler(t *testing.T) {
	var gotErr error
	_, sock := newTestServer(t, func(ctx context.Context, w *Writer, req Request, sub *Subscriber) {
		_, gotErr = sub.Request(ctx, "permission.request", nil, 10*time.Second)
		w.Respond(req.ID, map[string]any{"ok": true})
	})

	c, err := Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	start := time.Now()
	if _, err := c.Call("ask", nil); err != nil {
		t.Fatalf("call: %v", err)
	}
	// Wait for the handler to finish writing gotErr.
	deadline := time.Now().Add(2 * time.Second)
	for gotErr == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !errors.Is(gotErr, ErrNoClient) {
		t.Fatalf("want ErrNoClient, got %v", gotErr)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("should not have waited for a timeout; took %v", elapsed)
	}
}

// TestReverseRequestTimeout checks that a timeout returns ErrTimeout instead of hanging.
func TestReverseRequestTimeout(t *testing.T) {
	var gotErr error
	_, sock := newTestServer(t, func(ctx context.Context, w *Writer, req Request, sub *Subscriber) {
		_, gotErr = sub.Request(ctx, "permission.request", nil, 150*time.Millisecond)
		w.Respond(req.ID, map[string]any{"ok": true})
	})

	c, err := Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	// A handler that answers very slowly, on purpose.
	c.OnRequest(func(string, map[string]any) (any, error) {
		time.Sleep(10 * time.Second)
		return map[string]any{"allow": true}, nil
	})

	if _, err := c.Call("ask", nil); err != nil {
		t.Fatalf("call: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !errors.Is(gotErr, ErrTimeout) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !errors.Is(gotErr, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", gotErr)
	}
}

// TestReverseRequestDisconnect checks that closing the window (dropping the connection) releases
// a waiting request at once instead of waiting five minutes for the timeout.
func TestReverseRequestDisconnect(t *testing.T) {
	done := make(chan error, 1)
	_, sock := newTestServer(t, func(ctx context.Context, w *Writer, req Request, sub *Subscriber) {
		_, err := sub.Request(ctx, "permission.request", nil, 30*time.Second)
		done <- err
	})

	// A bare connection standing in for "a UI that closes its window on every question" — no need
	// for a full client.
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	reader := bufio.NewReader(conn)
	if _, err := reader.ReadBytes('\n'); err != nil { // handshake
		t.Fatalf("handshake: %v", err)
	}
	if _, err := conn.Write([]byte("{\"id\":1,\"method\":\"ask\"}\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Reading the reverse request proves the daemon is waiting on us; now close.
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read reverse request: %v", err)
	}
	if !strings.Contains(string(line), `"rid"`) {
		t.Fatalf("expected a reverse request, got %s", line)
	}
	conn.Close()

	select {
	case err := <-done:
		if !errors.Is(err, ErrDisconnected) {
			t.Fatalf("want ErrDisconnected, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not give up immediately after the disconnect")
	}
}
