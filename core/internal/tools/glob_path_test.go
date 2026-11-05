package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// touch creates a file (possibly with subdirectories) under root.
func touch(t *testing.T, root, rel string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testCtx(t *testing.T, root string) context.Context {
	t.Helper()
	return WithEnv(context.Background(), Env{Root: root})
}

// lowerWalkLimit makes "the walk budget ran out" reproducible in milliseconds without building 200,000
// real files.
func lowerWalkLimit(t *testing.T, n int) {
	t.Helper()
	old := walkLimit
	walkLimit = n
	t.Cleanup(func() { walkLimit = old })
}

// With path given, the pattern is relative to it — otherwise path only narrows the walk and the user
// has to repeat the path inside the pattern.
func TestGlobPathIsRelativeToBase(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "top.txt")
	touch(t, root, "sub/a.txt")
	ctx := testCtx(t, root)

	out, err := Glob{}.Run(ctx, `{"pattern":"*.txt","path":"sub"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "sub/a.txt") {
		t.Errorf("path=sub + pattern=*.txt should match sub/a.txt, got %q", out)
	}
	if strings.Contains(out, "top.txt") {
		t.Errorf("must not reach outside path, got %q", out)
	}

	// Without path, the pattern is relative to the working directory (existing behaviour must not
	// regress).
	out, err = Glob{}.Run(ctx, `{"pattern":"*.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "top.txt") || strings.Contains(out, "a.txt") {
		t.Errorf("with a pattern relative to the working directory only top.txt should match, got %q", out)
	}
}

// With zero hits after the walk was cut off, "nothing matched" is a lie: the model rephrases and
// retries, and the user sees "it cannot find things".
func TestGlobZeroMatchStillReportsTruncation(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		touch(t, root, n)
	}
	lowerWalkLimit(t, 3)
	ctx := testCtx(t, root)

	out, err := Glob{}.Run(ctx, `{"pattern":"*.nothing"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "may be incomplete") {
		t.Errorf("truncation must be mentioned, not just that nothing matched, got %q", out)
	}
}

// The walk has to start at the pattern's fixed prefix, or a large earlier directory eats the budget and
// later ones are never looked at. .aaa stands in for a neighbour like ~/.local: it sorts before proj and
// the budget runs out on it.
func TestGlobNarrowsWalkToPatternPrefix(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"1", "2", "3", "4", "5"} {
		touch(t, root, ".aaa/noise"+n+".dat")
	}
	touch(t, root, "proj/want.txt")
	lowerWalkLimit(t, 4)
	ctx := testCtx(t, root)

	out, err := Glob{}.Run(ctx, `{"pattern":"proj/*.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "proj/want.txt") {
		t.Errorf("a definite prefix in the pattern should be the only place walked, got %q", out)
	}
}

// grep's glob argument has the same base-relative problem.
func TestGrepGlobIsRelativeToBase(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "top.go")
	touch(t, root, "sub/main.go")
	if err := os.WriteFile(filepath.Join(root, "sub", "main.go"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := testCtx(t, root)

	out, err := Grep{}.Run(ctx, `{"pattern":"needle","path":"sub","glob":"*.go"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "sub/main.go") {
		t.Errorf("path=sub + glob=*.go should match sub/main.go, got %q", out)
	}
}

// grep must not swallow truncation on zero hits either.
func TestGrepZeroMatchStillReportsTruncation(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		touch(t, root, n)
	}
	lowerWalkLimit(t, 3)
	ctx := testCtx(t, root)

	out, err := Grep{}.Run(ctx, `{"pattern":"no such string anywhere"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "may be incomplete") {
		t.Errorf("truncation must be mentioned, got %q", out)
	}
}

// The old spelling (the pattern repeating the path) must not become a silent zero-hit. It was the only
// way to write it before this change, existing transcripts and examples are shaped like it, and
// "cannot find" is the hardest to debug — so both spellings are accepted.
func TestGlobAcceptsPathPrefixedPattern(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "top.txt")
	touch(t, root, "sub/a.txt")
	ctx := testCtx(t, root)

	for _, pat := range []string{"sub/*.txt", "./sub/*.txt"} {
		out, err := Glob{}.Run(ctx, `{"pattern":"`+pat+`","path":"sub"}`)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "sub/a.txt") {
			t.Errorf("path=sub + pattern=%s should match, got %q", pat, out)
		}
	}
}

// A trailing "/" means "what is under this", not "this path itself".
func TestGlobTrailingSlashMeansContents(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "sub/a.txt")
	touch(t, root, "top.txt")
	ctx := testCtx(t, root)

	out, err := Glob{}.Run(ctx, `{"pattern":"sub/"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "sub/a.txt") || strings.Contains(out, "top.txt") {
		t.Errorf("sub/ should list the files under sub, got %q", out)
	}
}

// Prefix stripping compares whole segments: sub is not a prefix of subway.
func TestStripPathPrefixMatchesWholeSegment(t *testing.T) {
	if got := stripPathPrefix("subway/*.txt", "sub"); got != "subway/*.txt" {
		t.Errorf("subway must not count as being under sub, got %q", got)
	}
	if got := stripPathPrefix("sub/*.txt", "sub"); got != "*.txt" {
		t.Errorf("should strip down to *.txt, got %q", got)
	}
	if got := stripPathPrefix("sub", "sub"); got != "sub" {
		t.Errorf("if it is not longer than path, leave it alone, got %q", got)
	}
}

// grep's glob argument accepts the old spelling too.
func TestGrepAcceptsPathPrefixedGlob(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "top.go")
	touch(t, root, "sub/main.go")
	ctx := testCtx(t, root)

	out, err := Grep{}.Run(ctx, `{"pattern":"x","path":"sub","glob":"sub/*.go"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "sub/main.go") {
		t.Errorf("path=sub + glob=sub/*.go should match, got %q", out)
	}
}

// "This level" spellings (./, ., //) mean everything below, not an empty pattern. An empty pattern
// would produce a message with a blank where the pattern belongs.
func TestGlobDotMeansEverythingUnderBase(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "sub/a.txt")
	touch(t, root, "sub/deep/b.txt")
	touch(t, root, "top.txt")
	ctx := testCtx(t, root)

	for _, pat := range []string{"./", ".", "//"} {
		out, err := Glob{}.Run(ctx, `{"path":"sub","pattern":"`+pat+`"}`)
		if err != nil {
			t.Fatalf("pattern=%q: %v", pat, err)
		}
		if !strings.Contains(out, "sub/a.txt") || !strings.Contains(out, "sub/deep/b.txt") {
			t.Errorf("pattern=%q should list everything under path, got %q", pat, out)
		}
		if strings.Contains(out, "top.txt") {
			t.Errorf("pattern=%q must not reach outside path, got %q", pat, out)
		}
	}

	// A genuinely empty pattern must still error, not be swallowed as "everything".
	if _, err := (Glob{}).Run(ctx, `{"pattern":"   "}`); err == nil {
		t.Error("a blank pattern should error")
	}
}
