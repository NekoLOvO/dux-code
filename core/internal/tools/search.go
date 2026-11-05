package tools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/NekoLOvO/dux-code/core/internal/llm"
)

// skipDirs are directories skipped while walking. Their contents rarely help find code and account for
// most of the time.
var skipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, ".venv": true, "venv": true, "__pycache__": true,
	"dist": true, "build": true, "target": true, ".cache": true,
	".next": true, ".idea": true, ".vscode": true,
}

// walkLimit caps how many entries one walk inspects, so a huge directory cannot stall it.
//
// A var rather than a const only so tests can shrink it: building 200,000 real files is too slow for
// anyone to run, and "what a truncated result looks like" is exactly where this is easiest to get
// wrong.
var walkLimit = 200000

// truncNote explains "the walk was cut off by the budget".
//
// It must be included with zero hits: answering a clean "nothing matched" is a lie, and the hardest
// kind of bug to find — the model rephrases and retries, and the user sees "it cannot find things".
func truncNote() string {
	return fmt.Sprintf("\n… (stopped after %d entries, so results may be incomplete; narrow the scope with path or a more specific prefix)", walkLimit)
}

// literalDir takes the leading directory segments of pattern that never change.
//
// With it, glob("proj/*.txt") walks only proj instead of the whole working directory: when that is a
// home directory, .local alone holds 240,000 entries and eats the budget before any later directory is
// looked at.
//
// An empty string means the pattern varies from the start (**/*.go, *.go), so the walk begins at
// base.
func literalDir(pattern string) string {
	var fixed []string
	for _, seg := range strings.Split(pattern, "/") {
		if seg == "" || strings.ContainsAny(seg, `*?[\`) {
			break
		}
		fixed = append(fixed, seg)
	}
	return strings.Join(fixed, "/")
}

// narrowBase returns where the walk actually starts: base plus the pattern's fixed prefix.
//
// It only affects where the walk starts, not matching — the pattern stays relative to base. A missing
// prefix, a non-directory, or one pointing outside base all return base: slow beats missing.
func narrowBase(base, pattern string) string {
	prefix := literalDir(pattern)
	if prefix == "" {
		return base
	}
	candidate := filepath.Join(base, filepath.FromSlash(prefix))
	rel, err := filepath.Rel(base, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return base
	}
	if st, err := os.Stat(candidate); err != nil || !st.IsDir() {
		return base
	}
	return candidate
}

// normalizePattern removes two natural spellings that silently match nothing: a leading "./" (the
// relative-path habit) and a trailing "/" (meaning "things under this directory").
//
// A trailing "/" becomes "/**": the intent is "files underneath", not "this path itself", and an empty
// segment simply never matches.
func normalizePattern(p string) string {
	p = strings.TrimSpace(p)
	for strings.HasPrefix(p, "./") {
		p = strings.TrimPrefix(p, "./")
	}
	// Stripped down to "this level" (./, //, .): that means everything below. An empty string will not do
	// — the emptiness check runs before normalisation, so it would produce a message with a blank where
	// the pattern should be.
	trailing := strings.HasSuffix(p, "/")
	if trailing {
		p = strings.TrimRight(p, "/")
	}
	if p == "" || p == "." {
		return "**"
	}
	if trailing {
		return p + "/**"
	}
	return p
}

// stripPathPrefix eats the prefix where "the pattern repeats the path".
//
// Matching used to be working-directory relative, so path=sub with pattern=sub/*.txt was the only way
// to write it. Relative to base that silently matches nothing — exactly the false negative this change
// removes — so both spellings are accepted. Stripping it also lets the walk start in the right place
// instead of scanning from base.
func stripPathPrefix(pattern, rel string) string {
	if rel == "" || rel == "." {
		return pattern
	}
	pl := strings.Split(pattern, "/")
	rl := strings.Split(filepath.ToSlash(rel), "/")
	if len(pl) <= len(rl) {
		return pattern
	}
	for i := range rl {
		if pl[i] != rl[i] {
			return pattern
		}
	}
	return strings.Join(pl[len(rl):], "/")
}

// ── glob ────────────────────────────────────────────────────────────

// Glob finds files by glob pattern. Read-only.
type Glob struct{}

// Spec implements Tool.
func (Glob) Spec() llm.Tool {
	return llm.NewTool(
		"glob",
		"Find files under the working directory with a glob pattern (e.g. **/*.go, src/**/*.qml). Matches paths only, never file contents.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "glob pattern; ** matches any number of directories",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "subdirectory to start from (default: the working directory). The pattern is relative to it: path=src with pattern=*.go finds Go files under src without repeating src/",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "maximum number of matches (default 200)",
				},
			},
			"required": []string{"pattern"},
		},
	)
}

// Run implements Tool.
func (Glob) Run(ctx context.Context, args string) (string, error) {
	var in struct {
		Pattern    string `json:"pattern"`
		Path       string `json:"path"`
		MaxResults int    `json:"max_results"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	pattern := strings.TrimSpace(in.Pattern)
	if pattern == "" {
		return "", errors.New("pattern is empty")
	}
	pattern = normalizePattern(pattern)
	if in.MaxResults <= 0 {
		in.MaxResults = 200
	}

	env := EnvFrom(ctx)
	root, err := env.RootDir()
	if err != nil {
		return "", err
	}
	base := root
	if in.Path != "" {
		if base, err = env.Resolve(in.Path); err != nil {
			return "", err
		}
	}
	// Matching is relative to base; the walk may be narrower, starting at the pattern's fixed prefix.
	// Strip the repeated path prefix from the pattern first, so the two agree.
	if base != root {
		if rel, rerr := filepath.Rel(root, base); rerr == nil {
			pattern = stripPathPrefix(pattern, rel)
		}
	}
	walkRoot := narrowBase(base, pattern)

	var (
		matches  []string
		visited  int
		hitLimit bool
	)

	walkErr := filepath.WalkDir(walkRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // one unreadable directory must not fail the whole walk
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		visited++
		if visited > walkLimit {
			hitLimit = true
			return fs.SkipAll
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		// Output uses working-directory relative paths (the model can use them directly); matching uses
		// base-relative paths (relative to path when given).
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		mrel, merr := filepath.Rel(base, p)
		if merr != nil {
			return nil
		}
		if matchGlob(pattern, filepath.ToSlash(mrel)) {
			matches = append(matches, filepath.ToSlash(rel))
			if len(matches) >= in.MaxResults {
				return fs.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		if errors.Is(walkErr, context.Canceled) {
			return "", walkErr
		}
		return "", walkErr
	}

	if len(matches) == 0 {
		msg := fmt.Sprintf("no files match %s.", pattern)
		if hitLimit {
			msg += truncNote()
		}
		return msg, nil
	}
	sort.Strings(matches)
	out := strings.Join(matches, "\n")
	if hitLimit {
		out += truncNote()
	}
	return TruncateForModel(out), nil
}

// ── grep ────────────────────────────────────────────────────────────

// Grep searches inside file contents. Read-only.
type Grep struct{}

// Spec implements Tool.
func (Grep) Spec() llm.Tool {
	return llm.NewTool(
		"grep",
		"Search file contents under the working directory with a regular expression and return file:line:content. Good for finding where a symbol is used.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "regular expression in Go syntax",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "subdirectory or file to start from (default: the working directory). glob is relative to it",
				},
				"glob": map[string]any{
					"type":        "string",
					"description": "only search files matching this glob (e.g. *.go, **/*.qml), relative to path",
				},
				"case_insensitive": map[string]any{
					"type":        "boolean",
					"description": "case-insensitive",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "maximum number of matches (default 80)",
				},
			},
			"required": []string{"pattern"},
		},
	)
}

// Run implements Tool.
func (Grep) Run(ctx context.Context, args string) (string, error) {
	var in struct {
		Pattern         string `json:"pattern"`
		Path            string `json:"path"`
		Glob            string `json:"glob"`
		CaseInsensitive bool   `json:"case_insensitive"`
		MaxResults      int    `json:"max_results"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	expr := in.Pattern
	if expr == "" {
		return "", errors.New("pattern is empty")
	}
	if in.CaseInsensitive {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return "", fmt.Errorf("invalid regular expression: %v", err)
	}
	if in.MaxResults <= 0 {
		in.MaxResults = 80
	}

	env := EnvFrom(ctx)
	root, err := env.RootDir()
	if err != nil {
		return "", err
	}
	base := root
	if in.Path != "" {
		if base, err = env.Resolve(in.Path); err != nil {
			return "", err
		}
	}
	// Same as glob: when the glob argument's prefix is a certain path, start the walk there.
	walkRoot := base
	if in.Glob != "" {
		in.Glob = normalizePattern(in.Glob)
		if base != root {
			if rel, rerr := filepath.Rel(root, base); rerr == nil {
				in.Glob = stripPathPrefix(in.Glob, rel)
			}
		}
		walkRoot = narrowBase(base, in.Glob)
	}

	var (
		lines    []string
		visited  int
		hitLimit bool
	)

	searchFile := func(p string) error {
		info, err := os.Stat(p)
		if err != nil || info.Size() > 4<<20 {
			// Over 4 MiB is usually a build artifact or data; skip it.
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()

		reader := bufio.NewReaderSize(f, 64*1024)
		// A NUL at the start means binary; skip it.
		head, _ := reader.Peek(8000)
		if bytes.IndexByte(head, 0) >= 0 {
			return nil
		}

		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			rel = p
		}
		rel = filepath.ToSlash(rel)

		lineNo := 0
		for {
			line, rerr := reader.ReadString('\n')
			if line != "" {
				lineNo++
				if re.MatchString(line) {
					text := strings.TrimRight(line, "\r\n")
					if len(text) > 300 {
						text = text[:300] + "…"
					}
					lines = append(lines, fmt.Sprintf("%s:%d: %s", rel, lineNo, strings.TrimSpace(text)))
					if len(lines) >= in.MaxResults {
						return fs.SkipAll
					}
				}
			}
			if rerr != nil {
				break
			}
		}
		return nil
	}

	walkErr := filepath.WalkDir(walkRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		visited++
		if visited > walkLimit {
			hitLimit = true
			return fs.SkipAll
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if in.Glob != "" {
			mrel, merr := filepath.Rel(base, p)
			if merr != nil || !matchGlob(in.Glob, filepath.ToSlash(mrel)) {
				return nil
			}
		}
		return searchFile(p)
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		return "", walkErr
	}

	if len(lines) == 0 {
		msg := "nothing matched."
		if hitLimit {
			msg += truncNote()
		}
		return msg, nil
	}
	out := strings.Join(lines, "\n")
	if hitLimit {
		out += truncNote()
	}
	return TruncateForModel(out), nil
}

// matchGlob matches across layers, like `**`.
// Only path.Match's single-segment matching is reused; the rest is expanded here.
func matchGlob(pattern, name string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegments(pat, seg []string) bool {
	for {
		if len(pat) == 0 {
			return len(seg) == 0
		}
		if pat[0] == "**" {
			for i := 0; i <= len(seg); i++ {
				if matchSegments(pat[1:], seg[i:]) {
					return true
				}
			}
			return false
		}
		if len(seg) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], seg[0])
		if err != nil || !ok {
			return false
		}
		pat, seg = pat[1:], seg[1:]
	}
}
