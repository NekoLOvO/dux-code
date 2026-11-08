// Package gitrepo reports and changes the Git state of a working directory.
//
// Everything here shells out to `git` rather than reading .git by hand. Fifty lines of object parsing
// buys nothing: the format is versioned, packed refs and worktrees move files around, and `git status
// --porcelain=v2` is a documented, stable interface that answers every question the UI has in one
// process. It is also the only way to get the answer git itself would give — a hand-rolled reader
// disagrees exactly when the index is mid-write.
//
// Nothing here is fatal: a directory that is not a repository, a machine without git, a status that
// takes too long — each of those answers "no repository" and the UI draws no chip. A coding agent
// works in all three kinds of directory, and refusing to start a turn over them would be absurd.
package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// statusTimeout bounds one `git status`. Generous for any real repository, short enough that a
// pathological one (a network filesystem, a tree of a million files) cannot hold the request open.
var statusTimeout = 3 * time.Second

// maxFiles caps the file list carried back to the UI. The count is not capped: the chip shows a number,
// and the tooltip lists the first few.
const maxFiles = 20

// Status is what the composer's chip draws.
type Status struct {
	// Repo is false when the directory is not inside a work tree, git is missing, or the command
	// failed. Every other field is then zero and the UI draws nothing.
	Repo     bool     `json:"repo"`
	Branch   string   `json:"branch,omitempty"`
	Detached bool     `json:"detached,omitempty"`
	Commit   string   `json:"commit,omitempty"`   // short sha, always
	Upstream string   `json:"upstream,omitempty"` // e.g. origin/main
	Ahead    int      `json:"ahead,omitempty"`
	Behind   int      `json:"behind,omitempty"`
	Dirty    int      `json:"dirty,omitempty"` // changed files, tracked and untracked
	Files    []string `json:"files,omitempty"` // the first maxFiles of them
	More     bool     `json:"more,omitempty"`  // dirty > len(Files)
	Error    string   `json:"error,omitempty"` // set when git ran but refused
}

// Read reports the state of the work tree containing dir.
//
// Untracked files count as dirty: an agent that has just written a new file has changed the tree, and a
// chip claiming "clean" at that moment is worse than no chip. Status is run with the default untracked
// mode for that reason; the timeout is what keeps a huge tree from being a problem.
func Read(ctx context.Context, dir string) Status {
	if strings.TrimSpace(dir) == "" {
		return Status{}
	}
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()

	// --no-optional-locks: a status that cannot take the index lock still works; without it this
	// competes with whatever git the user is running in a terminal.
	// core.quotepath=false: a non-ASCII path is otherwise octal-escaped ("\344\275\240.txt"), which is
	// unreadable in the tooltip it ends up in.
	out, err := run(ctx, dir, "--no-optional-locks", "-c", "core.quotepath=false",
		"status", "--porcelain=v2", "--branch")
	if err != nil {
		// "Not a repository" is the ordinary case and is silent. Everything else — dubious ownership, an
		// unreadable index, a corrupt object — is reported on the chip: those are fixable by the user and
		// invisible otherwise. The test is git's own wording, because the exit code is 128 for both.
		if notARepo(err, out) {
			return Status{}
		}
		return Status{Repo: true, Error: message(err, out)}
	}
	return parseStatus(string(out))
}

func parseStatus(text string) Status {
	st := Status{Repo: true, Files: []string{}}
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "# branch.oid "):
			sha := strings.TrimPrefix(line, "# branch.oid ")
			if sha != "(initial)" {
				st.Commit = shortSHA(sha)
			}
		case strings.HasPrefix(line, "# branch.head "):
			head := strings.TrimPrefix(line, "# branch.head ")
			// git spells a detached HEAD "(detached)"; the commit is what names it then.
			if head == "(detached)" {
				st.Detached = true
			} else {
				st.Branch = head
			}
		case strings.HasPrefix(line, "# branch.upstream "):
			st.Upstream = strings.TrimPrefix(line, "# branch.upstream ")
		case strings.HasPrefix(line, "# branch.ab "):
			// "+3 -1"
			for _, field := range strings.Fields(strings.TrimPrefix(line, "# branch.ab ")) {
				n, err := strconv.Atoi(field[1:])
				if err != nil || len(field) < 2 {
					continue
				}
				switch field[0] {
				case '+':
					st.Ahead = n
				case '-':
					st.Behind = n
				}
			}
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "),
			strings.HasPrefix(line, "u "), strings.HasPrefix(line, "? "):
			// An ordinary change, a rename, an unmerged path, or an untracked file. The path is the
			// last field of every one of those formats, and a path with spaces is not quoted.
			st.Dirty++
			if path := lastField(line); len(st.Files) < maxFiles {
				st.Files = append(st.Files, path)
			}
		}
	}
	// A detached HEAD has no branch name, so the commit is what names it — this has to happen after the
	// loop, because branch.head comes before branch.oid in no guaranteed order historically.
	if st.Detached && st.Branch == "" {
		st.Branch = st.Commit
	}
	st.More = st.Dirty > len(st.Files)
	return st
}

// lastField is the path at the end of a porcelain v2 entry. Only used for display, so a rename's
// "orig" half being dropped is acceptable — the new name is what changed.
func lastField(line string) string {
	if i := strings.LastIndexByte(line, ' '); i >= 0 {
		return line[i+1:]
	}
	return line
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// Branches lists local branches, most recently committed first: the one the user wants is almost
// always one of those.
//
// Remote-tracking branches are left out on purpose. Switching to one has to create a local tracking
// branch first, which is a second decision (what to name it) that a menu item cannot make.
func Branches(ctx context.Context, dir string) ([]string, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("no working directory")
	}
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()

	out, err := run(ctx, dir, "for-each-ref", "--sort=-committerdate",
		"--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil, errors.New(message(err, out))
	}
	names := make([]string, 0, 8)
	for _, line := range strings.Split(string(out), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// Checkout switches the work tree's branch, and returns git's own message on failure.
//
// `switch` rather than `checkout`: it refuses to lose uncommitted work instead of silently carrying it
// to the other branch, which is what a person clicking a menu means by "switch". The caller shows the
// message verbatim — "Your local changes to the following files would be overwritten" is more useful
// than any sentence written here.
func Checkout(ctx context.Context, dir, branch string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("no working directory")
	}
	if strings.TrimSpace(branch) == "" {
		return errors.New("no branch named")
	}
	// A branch name never starts with "-"; refusing one stops it being read as a flag.
	if strings.HasPrefix(branch, "-") {
		return errors.New("not a branch name: " + branch)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	out, err := run(ctx, dir, "switch", branch)
	if err != nil {
		return errors.New(message(err, out))
	}
	return nil
}

// run executes git in dir and returns its output. stderr is folded into the returned bytes so a failure
// carries the reason; git's complaints there are written for humans and are worth passing on.
func run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	// Three things are being decided here, and all three matter for parsing rather than taste.
	//
	// LC_ALL=C: git translates its own messages, so a substring test for "not a git repository" fails on
	// any non-English locale — measured on a zh_TW machine, where the reply was a Chinese sentence and
	// the chip silently never appeared. Everything the daemon says is English anyway.
	//
	// GIT_TERMINAL_PROMPT=0: a credential or editor prompt inside the daemon would hang the request with
	// no window to answer it.
	//
	// GIT_OPTIONAL_LOCKS=0: status must not queue behind a git the user is running in a terminal.
	cmd.Env = append(os.Environ(), "LC_ALL=C", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.Bytes(), err
}

// notARepo reports the ordinary "this is not a Git directory" case, which is not an error to show.
//
// The exit code cannot decide this: git also exits 128 for "detected dubious ownership" and for a
// corrupt repository, and those two are exactly what the user needs to be told about.
func notARepo(err error, out []byte) bool {
	// git not installed, or the directory does not exist.
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return true
	}
	text := strings.ToLower(string(out))
	return strings.Contains(text, "not a git repository") ||
		strings.Contains(text, "not a git work tree")
}

// message is git's own output, trimmed, or the error's text when git said nothing.
func message(err error, out []byte) string {
	if text := strings.TrimSpace(string(out)); text != "" {
		return text
	}
	return err.Error()
}
