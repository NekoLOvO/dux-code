package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// countingApprover records whether it was asked, to prove auto-approval really does not ask.
type countingApprover struct {
	asked int
	allow bool
	last  Approval
}

func (c *countingApprover) Ask(_ context.Context, a Approval) (bool, error) {
	c.asked++
	c.last = a
	return c.allow, nil
}

// TestCommandStaysInside is the entire basis of the workspace policy. Every false here means "one
// more confirmation", every true means "straight through", so either kind of mistake shows: too many is
// annoying, too few is dangerous.
func TestCommandStaysInside(t *testing.T) {
	root := t.TempDir()

	cases := []struct {
		cmd    string
		inside bool
	}{
		// inside: ordinary development commands
		{"go test ./...", true},
		{"ls -la", true},
		{"rm -rf build", true},
		{"cat src/main.go", true},
		{"go build ./cmd/dcode", true},
		{"make build > /dev/null", true},
		{"go run ./cmd/x 2>/dev/null", true},
		{"FOO=1 go test", true},

		// outside: the path is written literally
		{"cat /etc/passwd", false},
		{"rm -rf /", false},
		{"cat ../secret", false},
		{"cd ../other && ls", false},
		{"curl -o /tmp/x https://example.com", false},
		{"cat ~/notes.txt", false},
		{"echo $HOME/x", false},
		{"cp x /usr/local/bin/x", false},

		// Target invisible: not a wrong judgement but an impossible one, so always ask.
		// (these are exactly how people write files in bash)
		{"rm -rf $DIR", false},
		{"echo hi > $OUT", false},
		{"cat $(pwd)/notes.txt", false},
		{"cp a `date +%s`.bak", false},
		{"sudo -u root cat x", false}, // escalation removes the boundary; paths are irrelevant
		{"awk '{print $1}' out.txt", false},
		{"cp ~other/file x", false},
		{"sudo rm -rf x", false},

		// $HOME is known, so resolve it and compare.
		{"echo $HOME/x > y", false},

		// A bare `..` or `-`: where it goes is invisible, but it does leave the directory.
		{"cd .. && echo hi > x", false},
		{"cd - && ls", false},
	}

	for _, c := range cases {
		if got := commandStaysInside(root, c.cmd); got != c.inside {
			t.Errorf("%q: want %v, got %v", c.cmd, c.inside, got)
		}
	}
}

// TestCommandStaysInsideSymlink covers the old symlink trick:
// A link to /etc inside the directory: `x/passwd` looks like it is under the directory.
func TestCommandStaysInsideSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "passwd"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "x")); err != nil {
		t.Skipf("cannot create the symlink: %v", err)
	}
	if commandStaysInside(root, "cat x/passwd") {
		t.Fatal("a symlink pointing outside must not count as inside")
	}
}

func TestApprovalAutoSkipsAsking(t *testing.T) {
	root := t.TempDir()
	ap := &countingApprover{allow: false} // must not be asked even though the approver would refuse
	ctx := WithEnv(context.Background(), Env{Root: root, Approve: ap, Approval: ApprovalAuto})

	if _, err := (WriteFile{}).Run(ctx, `{"path":"a.txt","content":"hi"}`); err != nil {
		t.Fatalf("auto mode should just write successfully: %v", err)
	}
	if ap.asked != 0 {
		t.Fatalf("auto mode must not ask, but it asked %d times", ap.asked)
	}
}

func TestApprovalWorkspaceSkipsInsideCommands(t *testing.T) {
	root := t.TempDir()
	ap := &countingApprover{allow: false}
	ctx := WithEnv(context.Background(), Env{Root: root, Approve: ap, Approval: ApprovalWorkspace})

	if _, err := (Shell{}).Run(ctx, `{"command":"echo hi"}`); err != nil {
		t.Fatalf("a command inside the directory should just run: %v", err)
	}
	if ap.asked != 0 {
		t.Fatalf("a command inside must not ask, but it asked %d times", ap.asked)
	}
	if ap.last.Kind != "" {
		t.Fatal("it recorded an Approval without asking, so the decision order is wrong")
	}
}

func TestApprovalWorkspaceStillAsksOutside(t *testing.T) {
	root := t.TempDir()
	ap := &countingApprover{allow: false}
	ctx := WithEnv(context.Background(), Env{Root: root, Approve: ap, Approval: ApprovalWorkspace})

	_, err := (Shell{}).Run(ctx, `{"command":"cat /etc/passwd"}`)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("a command outside should ask (the user here denies), got %v", err)
	}
	if ap.asked != 1 {
		t.Fatalf("a command outside should ask once, asked %d times", ap.asked)
	}
	if ap.last.Command != "cat /etc/passwd" {
		t.Errorf("the user must see exactly what will run, got %q", ap.last.Command)
	}
}

func TestApprovalWorkspaceWritesInside(t *testing.T) {
	root := t.TempDir()
	ap := &countingApprover{allow: false}
	ctx := WithEnv(context.Background(), Env{Root: root, Approve: ap, Approval: ApprovalWorkspace})

	if _, err := (WriteFile{}).Run(ctx, `{"path":"sub/a.txt","content":"hi"}`); err != nil {
		t.Fatalf("a write inside the directory should just happen: %v", err)
	}
	if ap.asked != 0 {
		t.Fatalf("a write inside must not ask, but it asked %d times", ap.asked)
	}
}

// TestHasUnknowablePath covers one function: true means "always ask", so a mistake may only be asking
// too often, never missing one.
func TestHasUnknowablePath(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"go test ./...", false},
		{"echo hi > out.txt", false},
		{"cat ~/notes.txt", false},
		{"echo $HOME/x", false},
		{"rm -rf $DIR", true},
		{"echo ${DIR}/x", true},
		{"awk '{print $1}'", true},
		{"cat $(pwd)/x", true},
		{"cp a `date`.bak", true},
		{"cp ~other/x y", true},
	}
	for _, c := range cases {
		if got := hasUnknowablePath(c.cmd); got != c.want {
			t.Errorf("%q: want %v, got %v", c.cmd, c.want, got)
		}
	}
}
