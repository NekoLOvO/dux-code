package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/NekoLOvO/dux-code/core/internal/llm"
)

// maxCaptureBytes caps the raw output kept in memory. Past it collection stops (the content means
// nothing and the model cannot take it anyway).
const maxCaptureBytes = 1 << 20

// Shell runs a shell command. It is the only tool that can reach outside the working directory — it is
// a real shell, and path limits mean nothing to it. The only gate is approval, so the dialog has to
// show the command verbatim.
type Shell struct{}

// Spec implements Tool.
func (Shell) Spec() llm.Tool {
	return llm.NewTool(
		"bash",
		"Run a shell command in the working directory (sh -c) and return stdout, stderr and the exit code."+
			" This is how to build, test and inspect; it is not how to edit files."+
			" Editing through the shell (sed -i, redirection, patch, git checkout, a formatter over the tree) skips the"+
			" review that write_file and edit_file give, so use those for anything that changes source.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "the command to run; handed to sh -c",
				},
				"timeout_seconds": map[string]any{
					"type":        "integer",
					"description": "timeout in seconds (default 60, max 600); on timeout the whole process group is killed",
				},
			},
			"required": []string{"command"},
		},
	)
}

// Run implements Tool.
func (Shell) Run(ctx context.Context, args string) (string, error) {
	var in struct {
		Command        string `json:"command"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	cmdText := strings.TrimSpace(in.Command)
	if cmdText == "" {
		return "", errors.New("command is empty")
	}

	env := EnvFrom(ctx)
	root, err := env.RootDir()
	if err != nil {
		return "", err
	}

	timeout := in.TimeoutSeconds
	if timeout <= 0 {
		timeout = 60
	}
	if timeout > 600 {
		timeout = 600
	}

	approval := Approval{
		Kind:    "bash",
		Title:   "run command",
		Detail:  fmt.Sprintf("$ %s\n\nrun in %s", cmdText, root),
		Paths:   []string{root},
		Command: cmdText,
	}
	if err := env.ask(ctx, approval); err != nil {
		return "", err
	}

	// Its own context: the caller's cancellation (the user pressing stop) and the timeout take the same
	// route.
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	// Reaching here means the command was approved. Under workspace mode, when the reason it was approved
	// is "it looks like it stays inside", it also gets a sandbox: judgement can be fooled (computed paths,
	// eval, dynamic loading) while the kernel cannot. Commands that were asked about are not wrapped — the
	// user read them and agreed, and wrapping them would stop their own command from running.
	sandboxed := env.Approval == ApprovalWorkspace && env.insideWorkspace(approval) && SandboxAvailable()

	argv := []string{"sh", "-c", cmdText}
	if sandboxed {
		argv = sandboxArgv(root, cmdText)
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = root
	cmd.Env = safeEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var buf bytes.Buffer
	cmd.Stdout = &limitedWriter{buf: &buf}
	cmd.Stderr = &limitedWriter{buf: &buf}

	started := time.Now()
	runErr := cmd.Start()
	if runErr != nil {
		return "", fmt.Errorf("could not start the command: %v", runErr)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timedOut := false
	select {
	case runErr = <-done:
	case <-runCtx.Done():
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			timedOut = true
		}
		// Kill the whole process group: killing sh alone leaves grandchildren running.
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		// Wait for the kill to land before reading the buffers, so a half-written line cannot show up.
		// The error here is the kill's own ("signal: killed"); the switch below reports the timeout or
		// the cancellation, and either way it wins over this value.
		runErr = <-done
	}

	out := buf.String()
	elapsed := time.Since(started).Milliseconds()

	// Exit code: exec.ExitError carries it; a command we killed has none.
	exitCode := 0
	var exitErr *exec.ExitError
	switch {
	case timedOut || errors.Is(runCtx.Err(), context.Canceled):
		exitCode = -1
	case errors.As(runErr, &exitErr):
		exitCode = exitErr.ExitCode()
	case runErr != nil:
		return "", fmt.Errorf("the command failed: %v", runErr)
	}

	var head string
	if timedOut {
		head = fmt.Sprintf("[timed out after %ds, terminated]\n", timeout)
	} else if exitCode != 0 {
		head = fmt.Sprintf("[exit code %d, %dms]\n", exitCode, elapsed)
	}

	body := strings.TrimRight(out, "\n")
	if body == "" {
		if head == "" {
			return "(no output, the command succeeded)", nil
		}
		return head + "(no output)", nil
	}
	return TruncateForModel(head + body), nil
}

// limitedWriter collects output but drops the rest past the cap. Without it `yes` or
// `cat /dev/urandom` would eat the daemon's memory.
type limitedWriter struct {
	buf    *bytes.Buffer
	closed bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.closed {
		return len(p), nil
	}
	room := maxCaptureBytes - w.buf.Len()
	if room <= 0 {
		w.closed = true
		return len(p), nil
	}
	if len(p) > room {
		w.buf.Write(p[:room])
		w.closed = true
		return len(p), nil
	}
	w.buf.Write(p)
	return len(p), nil
}

// secretName marks environment variables not to pass to the child. The command is written by the
// model, and the model is far away — putting the API key in its environment is a leak by default, so
// this blocks it explicitly.
var secretName = regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password|passwd|credential|_KEY$)`)

// safeEnv returns a filtered environment.
func safeEnv() []string {
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, kv := range src {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		// These are needed for a shell to work at all; a KEY in the name must not get them removed.
		switch name {
		case "PWD", "OLDPWD", "XDG_SESSION_TYPE":
			out = append(out, kv)
			continue
		}
		if secretName.MatchString(name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}
