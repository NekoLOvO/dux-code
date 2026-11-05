package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Env is one tool call's environment.
//
// It centralises the two things no tool should implement itself:
//
//   - Root: the session's working directory. Every relative path resolves against it and must not
//     escape. This is a security boundary rather than a convenience — duplicated into each tool it
//     would be hard to change, so it is fixed here.
//   - Approve: who asks before something needs a nod (writes, commands). nil means nobody to ask,
//     which always counts as a refusal.
type Env struct {
	Root    string
	Approve Approver
	// Approval decides whether to ask first. The zero value equals ApprovalAsk.
	Approval ApprovalMode
	// Ask is the channel for the model asking the user. nil means no window can answer.
	Ask Asker
	// Search is the web-search channel. nil means no backend configured.
	Search Searcher
	// Skills is this turn's skill state; nil means no skill mechanism (tests, say).
	Skills SkillRuntime
	// BrowserPath is the configured agent-browser path (empty = look in PATH).
	//
	// It lives on Env rather than on a tool instance: after the user changes the path in settings, the
	// next message must use it, without a daemon restart.
	BrowserPath string
}

// ApprovalMode is the approval policy. A string on purpose: the config file, IPC and this share one
// set of values, so three mapping tables are not needed.
type ApprovalMode string

const (
	// ApprovalAsk asks every time.
	ApprovalAsk ApprovalMode = "ask"
	// ApprovalAuto approves everything.
	ApprovalAuto ApprovalMode = "auto"
	// ApprovalWorkspace approves what clearly stays under Root and asks otherwise.
	ApprovalWorkspace ApprovalMode = "workspace"
)

// Approval is one pending action. These fields are enough for the UI to explain the question.
type Approval struct {
	// Kind is "bash" or "write", so the UI can pick a presentation.
	Kind string
	// Title is a one-line summary (the dialog title).
	Title string
	// Detail is the whole thing: the command for bash, the diff for write.
	Detail string
	// Paths are the absolute paths involved (for hints like "inside the working directory").
	Paths []string
	// Command is only set for bash: the full command line.
	// The workspace policy needs it to judge whether the command reaches outside.
	Command string
}

// Approver asks the user to allow something.
// (false, nil) means the user refused; (_, err) means nobody could be asked (timeout, disconnect).
type Approver interface {
	Ask(ctx context.Context, a Approval) (bool, error)
}

// SkillRuntime is how a skill affects this turn.
//
// After loading, a skill does two things: put its prose into the context (that is the tool's return
// value) and, when needed, narrow the tools available afterwards (frontmatter allowed-tools). The
// latter cannot touch the registry from a tool — the registry is shared by the daemon and changing it
// would affect other sessions — so it is handed up to the run.
type SkillRuntime interface {
	// Activate records which skill was loaded and what it allows; an empty allowed means unrestricted.
	Activate(name string, allowed []string)
}

// ErrDenied is the single refusal error. Callers use it to decide how to word it to the model.
var ErrDenied = errors.New("the user denied this action")

// ErrOutsideRoot is a path escaping the working directory.
var ErrOutsideRoot = errors.New("path is outside the working directory")

type envKey struct{}

// WithEnv puts the environment in the context. Tools always read it from there, so the Tool interface
// needs no extra parameter for "who is allowed".
func WithEnv(ctx context.Context, env Env) context.Context {
	return context.WithValue(ctx, envKey{}, env)
}

// EnvFrom reads the environment. Without one it returns a shell whose Root is the current directory
// and which has no approver — that is, nothing can be written or executed, a safe default.
func EnvFrom(ctx context.Context) Env {
	if v, ok := ctx.Value(envKey{}).(Env); ok {
		return v
	}
	wd, _ := os.Getwd()
	return Env{Root: wd}
}

// RootDir returns the working directory's absolute (and symlink-resolved) path.
func (e Env) RootDir() (string, error) {
	root := e.Root
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		root = wd
	}
	if strings.HasPrefix(root, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			root = filepath.Join(home, strings.TrimPrefix(root, "~"))
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return realPathBestEffort(abs), nil
}

// Resolve turns a user/model path into an absolute one and checks it does not escape the working
// directory.
//
// symlink is the only hard part: after `ln -s /etc x`, `x/passwd` is literally under Root but is not.
// So the check uses the real path after resolving symlinks as far as possible, and Root itself is
// resolved too (home directories are sometimes symlinks).
func (e Env) Resolve(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("path is empty")
	}

	root, err := e.RootDir()
	if err != nil {
		return "", err
	}

	if strings.HasPrefix(p, "~") {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", herr
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	p = filepath.Clean(p)

	real := realPathBestEffort(p)
	if !within(root, real) {
		return "", fmt.Errorf("%w: %s (working directory is %s)", ErrOutsideRoot, real, root)
	}
	return real, nil
}

// within reports whether target is under root (root itself included).
func within(root, target string) bool {
	if root == target {
		return true
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	// Only a real "one level up" counts: the separator must be ".." or "../…". HasPrefix(rel, "..")
	// would misread a legitimate name like the `...` that `go test ./...` expands to.
	if rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// realPathBestEffort gives a real path even for a path that does not exist yet: evaluate symlinks
// from the deepest existing ancestor and re-append the rest.
func realPathBestEffort(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	dir := filepath.Dir(p)
	if dir == p {
		return p
	}
	return filepath.Join(realPathBestEffort(dir), filepath.Base(p))
}

// The cap before it reaches the model. What is persisted is complete; only the copy fed to the model
// is cut.
const (
	maxModelBytes = 32 * 1024
	maxModelLines = 2000
)

// TruncateForModel cuts oversized tool output into "head + note + tail".
//
// Keeping both ends is deliberate: command output usually matters at the tail (errors), file content
// at the head (declarations, imports). Keeping one side only makes the model misjudge. How much was
// dropped must be stated, or the model thinks the file is that short.
func TruncateForModel(s string) string {
	totalLines := strings.Count(s, "\n")
	if len(s) <= maxModelBytes && totalLines < maxModelLines {
		return s
	}

	headBudget := maxModelBytes * 6 / 10
	tailBudget := maxModelBytes - headBudget

	headEnd := runeBoundary(s, headBudget)
	headPart := s[:headEnd]
	// Back off to a line boundary so a line is not cut in half.
	if i := strings.LastIndexByte(headPart, '\n'); i > 0 {
		headPart = headPart[:i+1]
	}

	tailStart := len(s) - tailBudget
	if tailStart < len(headPart) {
		tailStart = len(headPart)
	}
	tailStart = runeBoundary(s, tailStart)
	tailPart := s[tailStart:]
	// Advance to a line boundary.
	if i := strings.IndexByte(tailPart, '\n'); i >= 0 && i+1 < len(tailPart) {
		tailPart = tailPart[i+1:]
	}

	skippedBytes := len(s) - len(headPart) - len(tailPart)
	skippedLines := totalLines - strings.Count(headPart, "\n") - strings.Count(tailPart, "\n")

	note := fmt.Sprintf(
		"\n\n… (%d lines / %d bytes omitted; the original output had %d lines / %d bytes and was truncated)\n"+
			"To see the omitted part, narrow the command output or read the source with read_file.\n\n",
		skippedLines, skippedBytes, totalLines+1, len(s))

	return headPart + note + tailPart
}

// runeBoundary moves an index to a UTF-8 character boundary (never splits CJK).
func runeBoundary(s string, i int) int {
	if i <= 0 {
		return 0
	}
	if i >= len(s) {
		return len(s)
	}
	for i > 0 && i < len(s) && (s[i]&0xC0) == 0x80 {
		i--
	}
	return i
}
