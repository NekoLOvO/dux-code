package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSandboxArgvBoundaries(t *testing.T) {
	if !SandboxAvailable() {
		t.Skip("no usable bwrap on this machine")
	}
	root := t.TempDir()
	argv := sandboxArgv(root, "echo hi")
	joined := strings.Join(argv, " ")

	for _, want := range []string{
		"--ro-bind / /",  // outside is read-only
		"--bind " + root, // working directory is writable
		"--bind /tmp /tmp",
		"--chdir " + root,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv is missing %q: %v", want, argv)
		}
	}
	if argv[len(argv)-3] != "sh" || argv[len(argv)-2] != "-c" {
		t.Errorf("the tail should be sh -c <command>: %v", argv[len(argv)-3:])
	}
}

// TestSandboxBlocksWritesOutside is the whole point of the feature:
// Outside the working directory and /tmp, not even the home directory is writable.
// The home directory is the target because /tmp and the working directory are already writable, and it
// is the one place where "we have permission but the sandbox refuses" — otherwise the test would pass
// spuriously on insufficient permissions.
func TestSandboxBlocksWritesOutside(t *testing.T) {
	if !SandboxAvailable() {
		t.Skip("no usable bwrap on this machine")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	outside, err := os.MkdirTemp(home, ".dcode-sandbox-test-")
	if err != nil {
		t.Skipf("the home directory is not writable: %v", err)
	}
	defer func() { _ = os.RemoveAll(outside) }()

	root := t.TempDir()
	target := filepath.Join(outside, "x.txt")
	script := "echo hi > " + target

	// Control: unwrapped, the write succeeds (proving the failure is not insufficient permission).
	if out, err := exec.Command("sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("the control case should succeed: %v %s", err, out)
	}
	_ = os.Remove(target)

	argv := sandboxArgv(root, script)
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	if err == nil {
		t.Fatalf("writing outside the sandbox (home) unexpectedly worked: %s", out)
	}
	if _, statErr := os.Stat(target); statErr == nil {
		t.Fatal("a file outside the sandbox was created")
	}
}

// TestSandboxAllowsWorkspaceWrites checks the sandbox does not block what it should allow.
func TestSandboxAllowsWorkspaceWrites(t *testing.T) {
	if !SandboxAvailable() {
		t.Skip("no usable bwrap on this machine")
	}
	root := t.TempDir()
	script := "echo hi > in-root.txt && echo ok > /tmp/dcode-sandbox-tmp.txt"

	argv := sandboxArgv(root, script)
	if out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("the working directory and /tmp should be writable: %v %s", err, out)
	}
	defer func() { _ = os.Remove("/tmp/dcode-sandbox-tmp.txt") }()

	if _, err := os.Stat(filepath.Join(root, "in-root.txt")); err != nil {
		t.Fatalf("the file in the working directory was not created: %v", err)
	}
}

// TestShellSandboxesWorkspaceApproval checks Shell really wraps commands, rather than sandboxArgv merely
// looking correct on its own.
func TestShellSandboxesWorkspaceApproval(t *testing.T) {
	if !SandboxAvailable() {
		t.Skip("no usable bwrap on this machine")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	outside, err := os.MkdirTemp(home, ".dcode-sandbox-test-")
	if err != nil {
		t.Skipf("the home directory is not writable: %v", err)
	}
	defer func() { _ = os.RemoveAll(outside) }()

	root := t.TempDir()
	ap := &countingApprover{allow: true}
	ctx := WithEnv(context.Background(), Env{Root: root, Approve: ap, Approval: ApprovalWorkspace})

	// The command itself shows no path outside the directory: where to write lives inside the script. That
	// is exactly what literal checking cannot catch, and what the sandbox is for.
	target := filepath.Join(outside, "x.txt")
	if err := os.WriteFile(filepath.Join(root, "write.sh"),
		[]byte("echo hi > \""+target+"\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := (Shell{}).Run(ctx, `{"command":"sh write.sh"}`)
	if err != nil {
		t.Fatalf("Shell.Run: %v", err)
	}
	if ap.asked != 0 {
		t.Fatalf("a command inside must not ask, but it asked %d times", ap.asked)
	}
	if _, statErr := os.Stat(target); statErr == nil {
		t.Fatalf("workspace mode wrote outside the directory: %s", out)
	}
	if !strings.Contains(out, "exit code") {
		t.Errorf("the failure should be visible to the model, got %q", out)
	}
}

// TestShellDoesNotSandboxApprovedCommands records a deliberate decision: an approved command is not
// sandboxed, or `sudo apt install` would fail forever.
func TestShellDoesNotSandboxApprovedCommands(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	outside, err := os.MkdirTemp(home, ".dcode-sandbox-test-")
	if err != nil {
		t.Skipf("the home directory is not writable: %v", err)
	}
	defer func() { _ = os.RemoveAll(outside) }()

	root := t.TempDir()
	ap := &countingApprover{allow: true}
	ctx := WithEnv(context.Background(), Env{Root: root, Approve: ap, Approval: ApprovalAsk})

	target := filepath.Join(outside, "x.txt")
	out, err := (Shell{}).Run(ctx, `{"command":"echo hi > `+target+`"}`)
	if err != nil {
		t.Fatalf("Shell.Run: %v", err)
	}
	if ap.asked != 1 {
		t.Fatalf("ask mode should ask once, asked %d times", ap.asked)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("after approval it should run as-is and not be blocked by the sandbox: %s", out)
	}
}
