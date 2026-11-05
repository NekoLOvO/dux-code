package api

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NekoLOvO/dux-code/core/internal/ipc"
	"github.com/NekoLOvO/dux-code/core/internal/tools"
)

// TestSubAskerRoundTrip goes through a real socket: the daemon sends question.request, the client
// answers, the answer reaches the tools layer.
//
// It also checks that no timeout is set: a zero timer would return ErrTimeout immediately and fail
// the test.
func TestSubAskerRoundTrip(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "q.sock")

	srv := ipc.NewServer(ipc.Config{SocketPath: sock, APIVersion: 1},
		func(ctx context.Context, w *ipc.Writer, req ipc.Request, sub *ipc.Subscriber) {
			answer, err := subAsker{sub: sub}.Ask(ctx, tools.Question{
				Question: "Which database should we use?",
				Options: []tools.QuestionOption{
					{Label: "Postgres", Description: "match what we have now"},
					{Label: "SQLite"},
				},
			})
			if err != nil {
				w.RespondError(req.ID, err.Error())
				return
			}
			w.Respond(req.ID, map[string]any{"answer": answer})
		})
	if err := srv.Listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx)
	}()
	defer func() {
		cancel()
		_ = srv.Close()
		<-done
	}()

	c, err := ipc.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	var gotMethod string
	var gotParams map[string]any
	c.OnRequest(func(method string, params map[string]any) (any, error) {
		gotMethod = method
		gotParams = params
		return map[string]any{"answer": "Postgres"}, nil
	})

	raw, err := c.Call("ask", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if gotMethod != "question.request" {
		t.Fatalf("wrong method: %q", gotMethod)
	}
	if q, _ := gotParams["question"].(string); !strings.Contains(q, "database") {
		t.Fatalf("the question was not passed through: %+v", gotParams)
	}
	opts, _ := gotParams["options"].([]any)
	if len(opts) != 2 {
		t.Fatalf("the options were not passed through: %+v", gotParams["options"])
	}
	first, _ := opts[0].(map[string]any)
	if first["label"] != "Postgres" || first["description"] != "match what we have now" {
		t.Fatalf("wrong option content: %+v", first)
	}
	if !strings.Contains(string(raw), "Postgres") {
		t.Fatalf("the answer never came back to the daemon: %s", raw)
	}
}

// TestSubAskerReportsDisconnect: closing the UI must error at once, not leave the model waiting for
// an answer that never comes.
func TestSubAskerReportsDisconnect(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "q2.sock")
	errc := make(chan error, 1)

	srv := ipc.NewServer(ipc.Config{SocketPath: sock, APIVersion: 1},
		func(ctx context.Context, w *ipc.Writer, req ipc.Request, sub *ipc.Subscriber) {
			_, err := subAsker{sub: sub}.Ask(ctx, tools.Question{Question: "Are you there?"})
			errc <- err
			w.Respond(req.ID, map[string]any{"ok": true})
		})
	if err := srv.Listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()
	defer srv.Close()

	c, err := ipc.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Close the connection on receiving the question, emulating a user closing the window.
	c.OnRequest(func(method string, params map[string]any) (any, error) {
		c.Close()
		return nil, nil
	})
	_, _ = c.Call("ask", nil)

	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("a disconnect should return an error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("it did not finish after the disconnect, which would stall the model")
	}
}
