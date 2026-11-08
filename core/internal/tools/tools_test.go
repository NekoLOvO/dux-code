package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NekoLOvO/dux-code/core/internal/llm"
)

// testEnv creates a temporary working directory and returns a context carrying Env.
func testEnv(t *testing.T, allow bool) (context.Context, Env, string) {
	t.Helper()
	root := t.TempDir()
	env := Env{Root: root, Approve: fakeApprover{allow: allow}}
	return WithEnv(context.Background(), env), env, root
}

type fakeApprover struct {
	allow bool
	last  *Approval
}

func (f fakeApprover) Ask(_ context.Context, a Approval) (bool, error) {
	if f.last != nil {
		*f.last = a
	}
	if !f.allow {
		return false, nil
	}
	return true, nil
}

// --- security boundary ---

func TestResolveRejectsEscape(t *testing.T) {
	_, env, root := testEnv(t, true)

	cases := []string{
		"../outside.txt",
		"../../etc/passwd",
		"a/../../b",
	}
	for _, c := range cases {
		if _, err := env.Resolve(c); err == nil {
			t.Errorf("%q should be rejected", c)
		}
	}

	// the root itself must work
	if got, err := env.Resolve("."); err != nil || got != root {
		t.Errorf("resolving . failed: %v / %s", err, got)
	}
	// an absolute path under the root must work
	if _, err := env.Resolve(filepath.Join(root, "sub/file.txt")); err != nil {
		t.Errorf("an absolute path inside the working directory should be accepted: %v", err)
	}
	// an absolute path outside must be rejected
	if _, err := env.Resolve("/etc/passwd"); err == nil {
		t.Error("/etc/passwd should be rejected")
	}
}

// TestResolveRejectsSymlinkEscape covers the easiest escape to overlook: the path is literally under the
// root but resolves outside it.
func TestResolveRejectsSymlinkEscape(t *testing.T) {
	_, env, root := testEnv(t, true)

	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create the symlink: %v", err)
	}

	if _, err := env.Resolve("link/secret.txt"); err == nil {
		t.Fatal("escaping the working directory through a symlink should be rejected")
	}

	// read_file must reject it too (Resolve alone is not enough).
	if _, err := (ReadFile{}).Run(env.context(), `{"path":"link/secret.txt"}`); err == nil {
		t.Fatal("read_file should refuse the symlink escape")
	}
}

func (e Env) context() context.Context { return WithEnv(context.Background(), e) }

// --- truncation ---

func TestTruncateKeepsHeadAndTail(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 5000; i++ {
		b.WriteString("0123456789 this is one line of test content\n")
	}
	s := b.String()
	if len(s) <= maxModelBytes {
		t.Fatalf("the test data is not long enough: %d", len(s))
	}

	out := TruncateForModel(s)
	if len(out) > maxModelBytes+2048 {
		t.Errorf("still too large after truncation: %d", len(out))
	}
	if !strings.HasPrefix(out, "0123456789") {
		t.Error("should keep the start")
	}
	if !strings.HasSuffix(strings.TrimSpace(s), strings.TrimSpace(out)[len(strings.TrimSpace(out))-11:]) {
		t.Error("should keep the end")
	}
	if !strings.Contains(out, "omitted") {
		t.Error("the model must be told something was omitted")
	}
	// UTF-8 must not be cut mid-character
	if strings.ContainsRune(out, '\uFFFD') {
		t.Error("truncation broke UTF-8")
	}
}

func TestTruncateLeavesShortTextAlone(t *testing.T) {
	s := "short content\n"
	if got := TruncateForModel(s); got != s {
		t.Errorf("short content must be left alone: %q", got)
	}
}

// --- file tools ---

func TestWriteReadRoundTrip(t *testing.T) {
	ctx, env, root := testEnv(t, true)

	out, err := (WriteFile{}).Run(ctx, `{"path":"a/b.txt","content":"first line\nsecond line\n"}`)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(out, "wrote") {
		t.Errorf("unexpected message: %s", out)
	}
	if _, err := os.Stat(filepath.Join(root, "a/b.txt")); err != nil {
		t.Fatalf("the file was not written: %v", err)
	}

	read, err := (ReadFile{}).Run(ctx, `{"path":"a/b.txt"}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if read != "first line\nsecond line\n" {
		t.Errorf("unexpected content read back: %q", read)
	}
	_ = env
}

func TestWriteDeniedLeavesNoFile(t *testing.T) {
	ctx, _, root := testEnv(t, false)

	_, err := (WriteFile{}).Run(ctx, `{"path":"no.txt","content":"x"}`)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("should be rejected, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "no.txt")); statErr == nil {
		t.Fatal("a denied write must leave no file")
	}
}

func TestWriteWithoutApproverIsDenied(t *testing.T) {
	root := t.TempDir()
	ctx := WithEnv(context.Background(), Env{Root: root}) // no Approve

	_, err := (WriteFile{}).Run(ctx, `{"path":"x.txt","content":"x"}`)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("with no approver it should be denied, got %v", err)
	}
}

func TestReadFileRange(t *testing.T) {
	ctx, _, root := testEnv(t, true)
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, "line"+string(rune('0'+i%10)))
	}
	if err := os.WriteFile(filepath.Join(root, "n.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := (ReadFile{}).Run(ctx, `{"path":"n.txt","start_line":3,"line_count":2}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, "line3") || !strings.Contains(out, "line4") || strings.Contains(out, "line5") {
		t.Errorf("wrong range read: %q", out)
	}
}

func TestEditFileUniqueness(t *testing.T) {
	ctx, _, root := testEnv(t, true)
	p := filepath.Join(root, "e.txt")
	if err := os.WriteFile(p, []byte("aaa\nbbb\naaa\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Two occurrences without replace_all → must fail.
	_, err := (EditFile{}).Run(ctx, `{"path":"e.txt","old_string":"aaa","new_string":"ccc"}`)
	if err == nil || !strings.Contains(err.Error(), "appears 2 times") {
		t.Fatalf("should fail because the match is not unique, got %v", err)
	}

	// With replace_all → all are replaced.
	if _, err := (EditFile{}).Run(ctx, `{"path":"e.txt","old_string":"aaa","new_string":"ccc","replace_all":true}`); err != nil {
		t.Fatalf("replace_all failed: %v", err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "ccc\nbbb\nccc\n" {
		t.Errorf("unexpected content: %q", got)
	}
}

// --- search ---

func TestGlobAndGrep(t *testing.T) {
	ctx, _, root := testEnv(t, true)
	// Setup that cannot fail loudly would make the assertions below meaningless.
	for path, content := range map[string]string{
		"src/a.go":      "package a\nfunc Hello() {}\n",
		"src/deep/b.go": "package b\n// Hello\n",
		"src/c.txt":     "Hello\n",
	} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", path, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	out, err := (Glob{}).Run(ctx, `{"pattern":"**/*.go"}`)
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if !strings.Contains(out, "src/a.go") || !strings.Contains(out, "src/deep/b.go") {
		t.Errorf("glob found nothing: %s", out)
	}
	if strings.Contains(out, "c.txt") {
		t.Errorf("glob must not include c.txt: %s", out)
	}

	out, err = (Grep{}).Run(ctx, `{"pattern":"Hello","glob":"**/*.go"}`)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !strings.Contains(out, "src/a.go:2:") {
		t.Errorf("grep should report line numbers: %s", out)
	}
	if strings.Contains(out, "c.txt") {
		t.Errorf("the grep glob had no effect: %s", out)
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pat, name string
		want      bool
	}{
		{"**/*.go", "a.go", true},
		{"**/*.go", "src/a.go", true},
		{"**/*.go", "src/deep/a.go", true},
		{"*.go", "src/a.go", false},
		{"src/*.go", "src/a.go", true},
		{"src/*.go", "src/deep/a.go", false},
		{"**", "anything/at/all", true},
	}
	for _, c := range cases {
		if got := matchGlob(c.pat, c.name); got != c.want {
			t.Errorf("matchGlob(%q,%q)=%v, want %v", c.pat, c.name, got, c.want)
		}
	}
}

// ── shell ───────────────────────────────────────────────────────────

func TestShellRunsInRoot(t *testing.T) {
	ctx, _, root := testEnv(t, true)

	out, err := (Shell{}).Run(ctx, `{"command":"pwd && echo hi"}`)
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if !strings.Contains(out, root) {
		t.Errorf("cwd is not the working directory: %s", out)
	}
	if !strings.Contains(out, "hi") {
		t.Errorf("no output: %s", out)
	}
}

func TestShellExitCodeAndTimeout(t *testing.T) {
	ctx, _, _ := testEnv(t, true)

	out, err := (Shell{}).Run(ctx, `{"command":"exit 3"}`)
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if !strings.Contains(out, "exit code 3") {
		t.Errorf("should report the exit code: %s", out)
	}

	out, err = (Shell{}).Run(ctx, `{"command":"sleep 5","timeout_seconds":1}`)
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if !strings.Contains(out, "timed out") {
		t.Errorf("should report the timeout: %s", out)
	}
}

func TestShellDenied(t *testing.T) {
	ctx, _, _ := testEnv(t, false)

	if _, err := (Shell{}).Run(ctx, `{"command":"echo hi"}`); !errors.Is(err, ErrDenied) {
		t.Fatalf("should be rejected, got %v", err)
	}
}

func TestSafeEnvDropsSecrets(t *testing.T) {
	t.Setenv("MY_API_KEY", "sk-should-not-leak")
	t.Setenv("GITHUB_TOKEN", "ghp_xxx")
	t.Setenv("NORMAL_VAR", "ok")

	env := safeEnv()
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "sk-should-not-leak") || strings.Contains(joined, "ghp_xxx") {
		t.Error("a secret env var leaked into the child process")
	}
	if !strings.Contains(joined, "NORMAL_VAR=ok") {
		t.Error("ordinary env vars must be kept")
	}
}

// The bash description is what the model reads about the tool on every request, so the rule lives
// there.
func TestShellDescriptionSteersAwayFromEditing(t *testing.T) {
	desc := Shell{}.Spec().Function.Description
	for _, want := range []string{"not how to edit files", "write_file", "edit_file"} {
		if !strings.Contains(desc, want) {
			t.Errorf("the bash description does not mention %q: %q", want, desc)
		}
	}
}

// Both file tools name the shell as the thing they replace.
func TestFileToolsDescriptionSayWhyThem(t *testing.T) {
	for _, spec := range []llm.Tool{WriteFile{}.Spec(), EditFile{}.Spec()} {
		d := spec.Function.Description
		if !strings.Contains(d, "bash") && !strings.Contains(d, "shell") {
			t.Errorf("%s does not contrast itself with the shell: %q", spec.Function.Name, d)
		}
	}
}
