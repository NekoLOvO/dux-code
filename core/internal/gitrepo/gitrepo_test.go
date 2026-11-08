package gitrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo makes an empty repository with one commit, so status has something to compare against.
// Every test that needs a work tree goes through here rather than assuming the checkout it runs in.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main", ".")
	// identity and an empty global config: a developer's own settings must not change the outcome.
	git(t, dir, "config", "user.email", "t@example.com")
	git(t, dir, "config", "user.name", "t")
	writeFile(t, filepath.Join(dir, "a.txt"), "one\n")
	git(t, dir, "add", "a.txt")
	git(t, dir, "commit", "-q", "-m", "first")
	return dir
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitOutput(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, out)
	}
	return out
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A clean repository on a branch with no upstream: the chip shows the branch and nothing else.
func TestReadCleanRepository(t *testing.T) {
	dir := initRepo(t)
	st := Read(context.Background(), dir)

	if !st.Repo {
		t.Fatalf("not recognised as a repository: %+v", st)
	}
	if st.Branch != "main" {
		t.Errorf("branch = %q, want main", st.Branch)
	}
	if st.Dirty != 0 || len(st.Files) != 0 {
		t.Errorf("dirty = %d, files = %v; want a clean tree", st.Dirty, st.Files)
	}
	if st.Detached {
		t.Error("a branch checkout must not report detached")
	}
	if len(st.Commit) != 7 {
		t.Errorf("commit = %q, want a 7-character sha", st.Commit)
	}
}

// Tracked changes and untracked files both count: an agent that has just written a file has changed the
// tree, and "clean" at that moment is worse than no chip at all.
func TestReadCountsTrackedAndUntracked(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "two\n")
	writeFile(t, filepath.Join(dir, "b.txt"), "new\n")

	st := Read(context.Background(), dir)
	if st.Dirty != 2 {
		t.Fatalf("dirty = %d, want 2 (one modified, one untracked): %+v", st.Dirty, st.Files)
	}
	joined := strings.Join(st.Files, " ")
	if !strings.Contains(joined, "a.txt") || !strings.Contains(joined, "b.txt") {
		t.Errorf("files = %v, want both paths", st.Files)
	}
	if st.More {
		t.Error("two files fit in the list; more must be false")
	}
}

// The list is capped and `More` says so, so a session that has rewritten a hundred files does not send a
// hundred paths over the socket for a tooltip.
func TestReadCapsTheFileList(t *testing.T) {
	dir := initRepo(t)
	for i := 0; i < maxFiles+5; i++ {
		writeFile(t, filepath.Join(dir, "f"+strings.Repeat("x", i)+".txt"), "x\n")
	}
	st := Read(context.Background(), dir)
	if st.Dirty != maxFiles+5 {
		t.Errorf("dirty = %d, want %d", st.Dirty, maxFiles+5)
	}
	if len(st.Files) != maxFiles {
		t.Errorf("len(files) = %d, want %d", len(st.Files), maxFiles)
	}
	if !st.More {
		t.Error("more must be true when the list was cut")
	}
}

// Ahead and behind come from the branch's upstream, and the UI draws them as arrows.
func TestReadAheadAndBehind(t *testing.T) {
	dir := initRepo(t)
	remote := t.TempDir()
	git(t, remote, "init", "-q", "--bare", "-b", "main", ".")
	git(t, dir, "remote", "add", "origin", remote)
	git(t, dir, "push", "-q", "-u", "origin", "main")

	writeFile(t, filepath.Join(dir, "a.txt"), "two\n")
	git(t, dir, "commit", "-q", "-am", "second")

	st := Read(context.Background(), dir)
	if st.Upstream != "origin/main" {
		t.Errorf("upstream = %q, want origin/main", st.Upstream)
	}
	if st.Ahead != 1 || st.Behind != 0 {
		t.Errorf("ahead/behind = %d/%d, want 1/0", st.Ahead, st.Behind)
	}
}

// A detached HEAD has no branch name; the chip shows the commit instead, and says it is detached.
func TestReadDetachedHead(t *testing.T) {
	dir := initRepo(t)
	sha := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
	git(t, dir, "checkout", "-q", "--detach", sha)

	st := Read(context.Background(), dir)
	if !st.Detached {
		t.Fatal("a detached checkout must report detached")
	}
	if st.Branch != st.Commit {
		t.Errorf("branch = %q, want the short commit %q", st.Branch, st.Commit)
	}
}

// A directory that is not a repository is not an error: the chip is simply absent.
func TestReadOutsideARepository(t *testing.T) {
	for _, dir := range []string{t.TempDir(), "/", ""} {
		st := Read(context.Background(), dir)
		if st.Repo {
			t.Errorf("%q was reported as a repository", dir)
		}
		if st.Error != "" {
			t.Errorf("%q produced error %q; not being a repository is not an error", dir, st.Error)
		}
	}
}

// A submodule is a repository of its own, even though its .git is a file pointing elsewhere.
func TestReadInsideASubmodule(t *testing.T) {
	parent := initRepo(t)
	sub := initRepo(t)
	git(t, parent, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "sub")
	git(t, parent, "commit", "-q", "-m", "sub")

	st := Read(context.Background(), filepath.Join(parent, "sub"))
	if !st.Repo {
		t.Fatalf("a submodule must be a repository of its own: %+v", st)
	}
	if st.Branch == "" {
		t.Error("no branch or commit reported inside the submodule")
	}
}

func TestBranchesListsLocalOnes(t *testing.T) {
	dir := initRepo(t)
	git(t, dir, "branch", "feature")
	git(t, dir, "branch", "other")

	names, err := Branches(context.Background(), dir)
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	if len(names) != 3 {
		t.Fatalf("branches = %v, want three", names)
	}
	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	if !found["main"] || !found["feature"] || !found["other"] {
		t.Errorf("branches = %v, want main, feature and other", names)
	}
}

// The order is "most recently committed first", which is what puts the branch in use at the top of the
// menu. Branches created from the same commit tie, so this has to move one of them forward to test.
func TestBranchesAreOrderedByRecency(t *testing.T) {
	dir := initRepo(t)
	git(t, dir, "branch", "older")
	// Make main newer than older.
	writeFile(t, filepath.Join(dir, "b.txt"), "x\n")
	git(t, dir, "add", "b.txt")
	git(t, dir, "commit", "-q", "-m", "newer")

	names, err := Branches(context.Background(), dir)
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	if len(names) < 2 || names[0] != "main" {
		t.Errorf("branches = %v, want main first", names)
	}
}

func TestCheckoutSwitchesBranch(t *testing.T) {
	dir := initRepo(t)
	git(t, dir, "branch", "feature")

	if err := Checkout(context.Background(), dir, "feature"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if st := Read(context.Background(), dir); st.Branch != "feature" {
		t.Errorf("branch = %q after switching, want feature", st.Branch)
	}
}

// A switch that would lose uncommitted work is refused, and git's own sentence is what comes back —
// rewriting it here would lose the file list it carries.
func TestCheckoutRefusesToLoseWork(t *testing.T) {
	dir := initRepo(t)
	git(t, dir, "checkout", "-q", "-b", "feature")
	writeFile(t, filepath.Join(dir, "a.txt"), "on feature\n")
	git(t, dir, "commit", "-q", "-am", "feature only")
	git(t, dir, "checkout", "-q", "main")
	// An uncommitted change to a file that differs between the two branches: this is the case git
	// refuses, and the one a user hits when the agent edited something and they try to switch.
	writeFile(t, filepath.Join(dir, "a.txt"), "edited, not committed\n")

	err := Checkout(context.Background(), dir, "feature")
	if err == nil {
		t.Fatal("switching with a conflicting change must fail")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "overwritten") &&
		!strings.Contains(strings.ToLower(err.Error()), "local changes") {
		t.Errorf("error %q does not carry git's explanation", err)
	}
	if st := Read(context.Background(), dir); st.Branch != "main" {
		t.Errorf("branch = %q; a refused switch must not move HEAD", st.Branch)
	}
}

// Names that would be read as flags never reach git.
func TestCheckoutRejectsFlagLikeNames(t *testing.T) {
	dir := initRepo(t)
	for _, name := range []string{"", "-f", "--force"} {
		if err := Checkout(context.Background(), dir, name); err == nil {
			t.Errorf("Checkout(%q) was allowed", name)
		}
	}
}

func TestCheckoutReportsAnUnknownBranch(t *testing.T) {
	dir := initRepo(t)
	err := Checkout(context.Background(), dir, "does-not-exist")
	if err == nil {
		t.Fatal("switching to a branch that does not exist must fail")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error %q does not name the branch", err)
	}
}

// A non-ASCII path comes back as it is on disk. git escapes it into octal by default, which is
// unreadable in the tooltip this list ends up in.
func TestReadKeepsNonASCIIPaths(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, "café.txt"), "x\n")

	st := Read(context.Background(), dir)
	if len(st.Files) != 1 {
		t.Fatalf("files = %v, want one", st.Files)
	}
	if st.Files[0] != "café.txt" {
		t.Errorf("path = %q, want it unescaped", st.Files[0])
	}
}

// git translates its own error messages, so the "not a repository" test has to survive a non-English
// locale — measured on a zh_TW machine, where git's refusal is a Chinese sentence and the chip silently
// never appeared.
func TestReadOutsideARepositoryIgnoresTheLocale(t *testing.T) {
	t.Setenv("LANG", "zh_TW.UTF-8")
	t.Setenv("LC_ALL", "zh_TW.UTF-8")
	if st := Read(context.Background(), t.TempDir()); st.Repo || st.Error != "" {
		t.Errorf("repo = %v, error = %q; a localised message must not be mistaken for a failure", st.Repo, st.Error)
	}
}
