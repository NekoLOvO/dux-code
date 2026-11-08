package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// browserApprover records every question, to check which actions ask and which do not.
type browserApprover struct {
	allow bool
	seen  []Approval
}

func (f *browserApprover) Ask(_ context.Context, a Approval) (bool, error) {
	f.seen = append(f.seen, a)
	return f.allow, nil
}

// fakeCLI writes a fake agent-browser that returns fixed JSON.
//
// The real agent-browser needs Chrome and a network, which tests must not depend on; what is verified
// here is how the command is assembled, how the response is read, and when the user is asked.
func fakeCLI(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-browser")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func runBrowser(t *testing.T, b *Browser, env Env, args string) string {
	t.Helper()
	ctx := WithEnv(context.Background(), env)
	out, err := b.Run(ctx, args)
	if err != nil {
		t.Fatalf("Run(%s): %v", args, err)
	}
	return out
}

func TestBrowserNavigateReportsTitle(t *testing.T) {
	bin := fakeCLI(t, `echo '{"success":true,"data":{"url":"https://x.test/","title":"X test"}}'`)
	b := &Browser{}

	out := runBrowser(t, b, Env{Approval: ApprovalAuto, BrowserPath: bin}, `{"action":"navigate","url":"https://x.test"}`)
	if !strings.Contains(out, "https://x.test/") || !strings.Contains(out, "X test") {
		t.Errorf("should report the URL and title: %q", out)
	}
}

func TestBrowserSnapshotMentionsRefs(t *testing.T) {
	bin := fakeCLI(t, `echo '{"success":true,"data":{"snapshot":"- link [ref=e1]","refs":{"e1":{"name":"a"}}}}'`)
	b := &Browser{}

	out := runBrowser(t, b, Env{Approval: ApprovalAuto, BrowserPath: bin}, `{"action":"snapshot"}`)
	if !strings.Contains(out, "[ref=e1]") {
		t.Errorf("the snapshot text should be kept: %q", out)
	}
	if !strings.Contains(out, "refs available") {
		t.Errorf("should mention how many refs there are: %q", out)
	}
}

func TestBrowserSnapshotInteractiveFlag(t *testing.T) {
	// The fake CLI prints the arguments it got, so -i / -d can be verified.
	bin := fakeCLI(t, `echo "{\"success\":true,\"data\":{\"snapshot\":\"args=$*\"}}"`)
	b := &Browser{}

	out := runBrowser(t, b, Env{Approval: ApprovalAuto, BrowserPath: bin}, `{"action":"snapshot","interactive":true,"depth":3}`)
	if !strings.Contains(out, "-i") || !strings.Contains(out, "-d 3") {
		t.Errorf("did not pass -i / -d through: %q", out)
	}
}

// Read-only actions must not ask: asking only breeds fatigue, and then everything gets approved.
func TestBrowserReadOnlyActionsSkipApproval(t *testing.T) {
	bin := fakeCLI(t, `echo '{"success":true,"data":{"content":"hello","snapshot":"x","scrolled":true}}'`)
	b := &Browser{}

	ap := &browserApprover{allow: true}
	env := Env{Approval: ApprovalAsk, Approve: ap, BrowserPath: bin}

	for _, action := range []string{`{"action":"snapshot"}`, `{"action":"read"}`, `{"action":"scroll"}`, `{"action":"back"}`} {
		runBrowser(t, b, env, action)
	}
	if len(ap.seen) != 0 {
		t.Errorf("read-only actions must not ask, but it asked %d times: %+v", len(ap.seen), ap.seen)
	}
}

// Anything sending data out or navigating asks.
func TestBrowserMutatingActionsAsk(t *testing.T) {
	bin := fakeCLI(t, `echo '{"success":true,"data":{"url":"https://x.test/","title":"X"}}'`)
	b := &Browser{}

	ap := &browserApprover{allow: true}
	env := Env{Approval: ApprovalAsk, Approve: ap, BrowserPath: bin}

	runBrowser(t, b, env, `{"action":"navigate","url":"https://x.test"}`)
	runBrowser(t, b, env, `{"action":"click","ref":"e1"}`)
	runBrowser(t, b, env, `{"action":"fill","ref":"e2","text":"secret"}`)
	runBrowser(t, b, env, `{"action":"press","key":"Enter"}`)

	if len(ap.seen) != 4 {
		t.Fatalf("all four actions should ask, got %d", len(ap.seen))
	}
	for _, a := range ap.seen {
		if a.Kind != "browser" {
			t.Errorf("Kind = %q, want browser", a.Kind)
		}
	}
	// What gets typed has to be visible — otherwise approval is a blind signature.
	if !strings.Contains(ap.seen[2].Detail, "secret") {
		t.Errorf("the fill text should show up in the approval: %q", ap.seen[2].Detail)
	}
}

func TestBrowserDeniedActionDoesNothing(t *testing.T) {
	// If a refused action still ran, this fake CLI would record it in a file.
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	bin := fakeCLI(t, `touch `+marker+`; echo '{"success":true,"data":{"clicked":true}}'`)
	b := &Browser{}

	ap := &browserApprover{allow: false}
	env := Env{Approval: ApprovalAsk, Approve: ap, BrowserPath: bin}

	out := runBrowser(t, b, env, `{"action":"click","ref":"e1"}`)
	if !strings.Contains(out, "denied") {
		t.Errorf("should say it was denied: %q", out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a denied action still ran")
	}
}

func TestBrowserSurfacesCLIError(t *testing.T) {
	bin := fakeCLI(t, `echo '{"success":false,"data":null,"error":"Unknown ref: e9"}'`)
	b := &Browser{}

	out := runBrowser(t, b, Env{Approval: ApprovalAuto, BrowserPath: bin}, `{"action":"click","ref":"e9"}`)
	if !strings.Contains(out, "Unknown ref") {
		t.Errorf("the CLI error text should be passed through: %q", out)
	}
	if !strings.Contains(out, "take a new snapshot") {
		t.Errorf("should warn that the ref is stale: %q", out)
	}
}

func TestBrowserMissingRefIsRejectedEarly(t *testing.T) {
	bin := fakeCLI(t, `echo '{"success":true,"data":{}}'`)
	b := &Browser{}

	out := runBrowser(t, b, Env{Approval: ApprovalAuto, BrowserPath: bin}, `{"action":"click"}`)
	if !strings.Contains(out, "needs a ref") {
		t.Errorf("a missing ref should be rejected locally: %q", out)
	}
}

func TestBrowserUnknownAction(t *testing.T) {
	b := &Browser{}
	out := runBrowser(t, b, Env{Approval: ApprovalAuto, BrowserPath: "/bin/true"}, `{"action":"eval","text":"alert(1)"}`)
	if !strings.Contains(out, "unsupported action") {
		t.Errorf("eval is not whitelisted and should be rejected: %q", out)
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("abc", 10); got != "abc" {
		t.Errorf("short strings must be left alone: %q", got)
	}
	got := truncateRunes(strings.Repeat("ab", 20), 5)
	if !strings.HasPrefix(got, "ababa") {
		t.Errorf("should keep the start: %q", got)
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("should say it was truncated: %q", got)
	}
	// CJK must not be split mid-character
	if !strings.Contains(got, "ab") {
		t.Errorf("truncation must not break UTF-8: %q", got)
	}
}

// A live test: this machine needs agent-browser (with Chrome) actually installed. It does not run by
// default, so CI and other machines are not blocked on an external tool.
//
//	DUXCODE_LIVE_BROWSER=1 go test ./internal/tools/ -run Live -v
func TestBrowserLive(t *testing.T) {
	if os.Getenv("DUXCODE_LIVE_BROWSER") == "" {
		t.Skip("set DUXCODE_LIVE_BROWSER=1 to actually start a browser")
	}
	// Only the settings, DUXCODE_AGENT_BROWSER and PATH are consulted; without any there is nothing to
	// test.
	bin, reason := ResolveAgentBrowser("")
	if bin == "" {
		t.Skip(reason)
	}
	t.Logf("using %s", bin)

	b := &Browser{}
	env := Env{Approval: ApprovalAuto, BrowserPath: bin}
	ctx := WithEnv(context.Background(), env)

	out, err := b.Run(ctx, `{"action":"navigate","url":"https://example.com"}`)
	if err != nil {
		t.Fatalf("navigate: %v / %s", err, out)
	}
	if !strings.Contains(out, "Example Domain") {
		t.Errorf("wrong title: %q", out)
	}

	snap, err := b.Run(ctx, `{"action":"snapshot","interactive":true}`)
	if err != nil {
		t.Fatalf("snapshot: %v / %s", err, snap)
	}
	if !strings.Contains(snap, "[ref=") {
		t.Errorf("no refs, so nothing can be operated afterwards: %q", snap)
	}

	// An expired ref must produce a readable error, not a bare CDP exception.
	if out, _ := b.Run(ctx, `{"action":"click","ref":"e9999"}`); !strings.Contains(out, "take a new snapshot") {
		t.Errorf("the invalid-ref message is not clear enough: %q", out)
	}

	if out, err := b.Run(ctx, `{"action":"close"}`); err != nil {
		t.Errorf("close: %v / %s", err, out)
	}
}

// Only PATH and the environment variable are consulted; other apps' install directories are not
// searched.
func TestFindAgentBrowserFromPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "agent-browser")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("DUXCODE_AGENT_BROWSER", "")

	if got, _ := ResolveAgentBrowser(""); got != bin {
		t.Errorf("should be found on PATH, got %q", got)
	}
}

func TestFindAgentBrowserFromEnv(t *testing.T) {
	// PATH points at an empty directory, proving the binary was not found through it.
	t.Setenv("PATH", t.TempDir())

	dir := t.TempDir()
	bin := filepath.Join(dir, "agent-browser")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DUXCODE_AGENT_BROWSER", bin)

	if got, _ := ResolveAgentBrowser(""); got != bin {
		t.Errorf("the env var should win, got %q", got)
	}
}

// When the variable points at a non-executable file, fall back to PATH instead of using it.
func TestFindAgentBrowserIgnoresBadEnv(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "agent-browser")
	if err := os.WriteFile(good, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	bad := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(bad, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DUXCODE_AGENT_BROWSER", bad)

	if got, _ := ResolveAgentBrowser(""); got != good {
		t.Errorf("a broken env var should be skipped, got %q", got)
	}
}

func TestFindAgentBrowserMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("DUXCODE_AGENT_BROWSER", "")
	if got, reason := ResolveAgentBrowser(""); got != "" || reason == "" {
		t.Errorf("with nothing available it should return empty plus a reason, got %q / %q", got, reason)
	}
}

// The configured path wins: the user picked it in settings,
// and a DUXCODE_AGENT_BROWSER in the startup environment must not override it.
func TestResolveAgentBrowserPrefersConfigured(t *testing.T) {
	dir := t.TempDir()
	configured := filepath.Join(dir, "mine")
	other := filepath.Join(dir, "other")
	for _, p := range []string{configured, other} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DUXCODE_AGENT_BROWSER", other)
	t.Setenv("PATH", dir)

	if got, _ := ResolveAgentBrowser(configured); got != configured {
		t.Errorf("the config value should win, got %q", got)
	}
}

// When the configured path is not executable, report the reason instead of quietly using the one on
// PATH.
//
// "I thought it ran A, but it ran B" is a hundred times harder to debug than "it does not work".
func TestResolveAgentBrowserBadConfiguredDoesNotFallBack(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "agent-browser")
	if err := os.WriteFile(good, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("DUXCODE_AGENT_BROWSER", "")

	bad := filepath.Join(t.TempDir(), "nope")
	if err := os.WriteFile(bad, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, reason := ResolveAgentBrowser(bad)
	if got != "" {
		t.Errorf("must not fall back to %q on PATH, got %q", good, got)
	}
	if !strings.Contains(reason, "not executable") {
		t.Errorf("a reason is needed, got %q", reason)
	}
}
