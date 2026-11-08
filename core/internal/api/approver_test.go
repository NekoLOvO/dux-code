package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NekoLOvO/dux-code/core/internal/agent"
	"github.com/NekoLOvO/dux-code/core/internal/config"
	"github.com/NekoLOvO/dux-code/core/internal/ipc"
	"github.com/NekoLOvO/dux-code/core/internal/store"
	"github.com/NekoLOvO/dux-code/core/internal/tools"
)

// startApproverServer runs a real write_file so the whole chain — tool → Env.ask →
// subApprover → reverse IPC → client — is exercised. Testing subApprover alone would miss the
// step where the tools layer turns the error into a refusal.
func startApproverServer(t *testing.T, root string) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "a.sock")
	srv := ipc.NewServer(ipc.Config{SocketPath: sock, APIVersion: 1},
		func(ctx context.Context, w *ipc.Writer, req ipc.Request, sub *ipc.Subscriber) {
			env := tools.Env{Root: root, Approve: subApprover{sub: sub}}
			ctx = tools.WithEnv(ctx, env)
			out, err := (tools.WriteFile{}).Run(ctx, `{"path":"x.txt","content":"hi"}`)
			res := map[string]any{"ok": true, "out": out}
			if err != nil {
				res["ok"] = false
				res["error"] = err.Error()
			}
			w.Respond(req.ID, res)
		})
	if err := srv.Listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ctx) }()
	t.Cleanup(func() { cancel(); _ = srv.Close(); <-done })
	return sock
}

// TestPermissionAllowWrites checks that "the UI pressed allow" really writes the file, and that
// the fields sent match what the QML expects (kind/title/detail/paths).
func TestPermissionAllowWrites(t *testing.T) {
	root := t.TempDir()
	sock := startApproverServer(t, root)

	c, err := ipc.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	got := map[string]any{}
	c.OnRequest(func(method string, params map[string]any) (any, error) {
		if method != "permission.request" {
			t.Errorf("wrong method: %s", method)
		}
		for k, v := range params {
			got[k] = v
		}
		return map[string]any{"allow": true}, nil
	})

	raw, err := c.Call("ask", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("should succeed, got %s", raw)
	}
	// The QML reads these keys; miss one and the dialog is blank.
	for _, k := range []string{"kind", "title", "detail", "paths"} {
		if _, ok := got[k]; !ok {
			t.Errorf("the reverse request is missing field %s", k)
		}
	}
	if got["kind"] != "write" {
		t.Errorf("wrong kind: %v", got["kind"])
	}
	if detail, _ := got["detail"].(string); !strings.Contains(detail, "x.txt") {
		t.Errorf("detail does not name the file, so the user cannot tell what will change: %q", detail)
	}
}

// TestPermissionDenyBlocksWrite checks that a refusal leaves no file and the reason reaches the
// model.
func TestPermissionDenyBlocksWrite(t *testing.T) {
	root := t.TempDir()
	sock := startApproverServer(t, root)

	c, err := ipc.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	c.OnRequest(func(string, map[string]any) (any, error) {
		return map[string]any{"allow": false, "reason": "this path is not right"}, nil
	})

	raw, err := c.Call("ask", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	s := string(raw)
	if !strings.Contains(s, `"ok":false`) {
		t.Fatalf("should fail, got %s", s)
	}
	if !strings.Contains(s, "denied") || !strings.Contains(s, "this path is not right") {
		t.Fatalf("the error must let the model understand why it was blocked: %s", s)
	}
	if hasFile(t, filepath.Join(root, "x.txt")) {
		t.Fatal("a denial must leave no file behind")
	}
}

// TestPermissionNoClientFailsFast checks that a CLI (no UI) fails at once instead of letting the
// tool wait out a five-minute timeout.
func TestPermissionNoClientFailsFast(t *testing.T) {
	root := t.TempDir()
	sock := startApproverServer(t, root)

	c, err := ipc.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	start := time.Now()
	raw, err := c.Call("ask", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("should fail immediately; took %v", elapsed)
	}
	if !strings.Contains(string(raw), "denied") {
		t.Fatalf("with no UI this counts as a denial: %s", raw)
	}
}

func hasFile(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Stat(p)
	return err == nil
}

// TestApprovalModeOf covers "config string → tools policy". A wrong guess may only be stricter:
// unreadable means ask, never approve.
func TestApprovalModeOf(t *testing.T) {
	cases := []struct {
		in   string
		want tools.ApprovalMode
	}{
		{"", tools.ApprovalAsk},
		{"ask", tools.ApprovalAsk},
		{"workspace", tools.ApprovalWorkspace},
		{"auto", tools.ApprovalAuto},
		{"true", tools.ApprovalAsk},
		{"yes", tools.ApprovalAsk},
	}
	for _, c := range cases {
		if got := approvalModeOf(c.in); got != c.want {
			t.Errorf("%q → %q, want %q", c.in, got, c.want)
		}
	}
}

// TestToolEnvCarriesApprovalMode checks the whole wiring: config → Runner → toolEnv → tool.
// approvalModeOf alone only proves the string maps; this proves it reaches the tools layer, and
// that a command inside the directory still runs under workspace mode with no UI attached (nobody
// to ask).
func TestToolEnvCarriesApprovalMode(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	st, _, err := store.Open()
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}

	root := t.TempDir()
	cfg := &config.Config{ApprovalMode: "workspace", WorkingDirectory: root}
	d := Deps{Agent: agent.New(ipc.NewEventBus(), st, cfg), Store: st}

	env := d.toolEnv("no such session", nil)
	if env.Approval != tools.ApprovalWorkspace {
		t.Fatalf("the approval policy never reached the tool layer: %q", env.Approval)
	}
	if env.Root != root {
		t.Fatalf("wrong working directory: %q", env.Root)
	}

	// The UI uses this field to say "no sandbox here"; omitting it would claim a sandbox forever, so
	// the key name is fixed.
	if view := configView(d); view["sandbox"] == nil {
		t.Fatal("the config response has no sandbox field")
	}
	// The settings page uses these two keys to decide whether to show tool switches and the search
	// backend row.
	view := configView(d)
	if view["tools"] == nil {
		t.Fatal("the config response has no tools field")
	}
	if view["search"] == nil || view["searchKinds"] == nil {
		t.Fatal("the config response has no search / searchKinds field")
	}

	ctx := tools.WithEnv(context.Background(), env)

	out, err := (tools.Shell{}).Run(ctx, `{"command":"echo inside-workspace"}`)
	if err != nil {
		t.Fatalf("a command inside the directory should just run (there is no UI to ask here): %v", err)
	}
	if !strings.Contains(out, "inside-workspace") {
		t.Fatalf("unexpected output: %q", out)
	}

	_, err = (tools.Shell{}).Run(ctx, `{"command":"cat /etc/hostname"}`)
	if !errors.Is(err, tools.ErrDenied) {
		t.Fatalf("a command outside should ask, and with nobody to ask it is denied; got %v", err)
	}
}
