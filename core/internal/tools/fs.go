package tools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/NekoLOvO/dux-code/core/internal/llm"
)

// ── read_file ───────────────────────────────────────────────────────

// ReadFile reads a slice of a text file. Read-only, no approval needed.
type ReadFile struct{}

// Spec implements Tool.
func (ReadFile) Spec() llm.Tool {
	return llm.NewTool(
		"read_file",
		"Read a text file inside the working directory. By default it returns 2000 lines starting at line 1; for long files pass start_line to page through it.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "file path relative to the working directory (an absolute path works too, but it must stay inside the working directory)",
				},
				"start_line": map[string]any{
					"type":        "integer",
					"description": "first line to read (1-based, default 1)",
				},
				"line_count": map[string]any{
					"type":        "integer",
					"description": "maximum lines to read (default 2000)",
				},
			},
			"required": []string{"path"},
		},
	)
}

// Run implements Tool.
func (ReadFile) Run(ctx context.Context, args string) (string, error) {
	var in struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		LineCount int    `json:"line_count"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}

	env := EnvFrom(ctx)
	abs, err := env.Resolve(in.Path)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(abs)
	if err != nil {
		return "", errorText(err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory, not a file; use glob to list a directory", in.Path)
	}

	start := in.StartLine
	if start < 1 {
		start = 1
	}
	limit := in.LineCount
	if limit <= 0 {
		limit = 2000
	}

	f, err := os.Open(abs)
	if err != nil {
		return "", errorText(err)
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 64*1024)
	var b strings.Builder

	lineNo := 0
	// Skip to the starting line first.
	for lineNo < start-1 {
		if _, err := reader.ReadString('\n'); err != nil {
			if errors.Is(err, io.EOF) {
				return "", fmt.Errorf("the file has only %d lines, so line %d does not exist", lineNo, start)
			}
			return "", errorText(err)
		}
		lineNo++
	}

	read := 0
	truncatedByBudget := false
	for read < limit {
		line, err := reader.ReadString('\n')
		if line != "" {
			if b.Len()+len(line) > maxModelBytes*2 {
				// Leave some room for the truncation note; stop here.
				truncatedByBudget = true
				break
			}
			b.WriteString(line)
			read++
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", errorText(err)
		}
	}

	out := b.String()
	if out == "" {
		return "(empty file)", nil
	}

	endLine := start + read - 1
	if truncatedByBudget || read == limit {
		// Tell the model there is more, so it does not think the file ends here.
		if _, err := reader.Peek(1); err == nil {
			out += fmt.Sprintf("\n… (more follows; these are lines %d-%d, pass start_line=%d for the rest)", start, endLine, endLine+1)
		}
	}
	return TruncateForModel(out), nil
}

// ── write_file ──────────────────────────────────────────────────────

// WriteFile writes a file (creating or overwriting it whole). Needs approval.
type WriteFile struct{}

// Spec implements Tool.
func (WriteFile) Spec() llm.Tool {
	return llm.NewTool(
		"write_file",
		"Write the whole content to a file, overwriting what was there. To change a few lines use edit_file instead of rewriting the file.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "file path relative to the working directory",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "the complete new content",
				},
			},
			"required": []string{"path", "content"},
		},
	)
}

// Run implements Tool.
func (WriteFile) Run(ctx context.Context, args string) (string, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}

	env := EnvFrom(ctx)
	abs, err := env.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		return "", fmt.Errorf("%s is a directory, not something to write to", in.Path)
	}

	action := "create a new file"
	oldLines := 0
	if old, err := os.ReadFile(abs); err == nil {
		oldLines = strings.Count(string(old), "\n")
		action = fmt.Sprintf("overwrite an existing file (%d lines before)", oldLines+1)
	}
	newLines := strings.Count(in.Content, "\n") + 1

	if err := env.ask(ctx, Approval{
		Kind:   "write",
		Title:  action + ": " + shortPath(env, abs),
		Detail: fmt.Sprintf("%s\n\nnew content, %d lines / %d bytes:\n%s", abs, newLines, len(in.Content), preview(in.Content, 30)),
		Paths:  []string{abs},
	}); err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", errorText(err)
	}
	if err := os.WriteFile(abs, []byte(in.Content), 0o644); err != nil {
		return "", errorText(err)
	}
	return fmt.Sprintf("wrote %s (%d lines / %d bytes)", shortPath(env, abs), newLines, len(in.Content)), nil
}

// ── edit_file ───────────────────────────────────────────────────────

// EditFile does an exact string replacement. Needs approval.
type EditFile struct{}

// Spec implements Tool.
func (EditFile) Spec() llm.Tool {
	return llm.NewTool(
		"edit_file",
		"Replace old_string with new_string in a file. old_string must appear exactly once; set replace_all to change every occurrence.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "file path relative to the working directory",
				},
				"old_string": map[string]any{
					"type":        "string",
					"description": "the text to replace; it must match the file byte for byte, indentation included",
				},
				"new_string": map[string]any{
					"type":        "string",
					"description": "what to put in its place. An empty string deletes it.",
				},
				"replace_all": map[string]any{
					"type":        "boolean",
					"description": "true replaces every occurrence (default false, which requires a unique match)",
				},
			},
			"required": []string{"path", "old_string", "new_string"},
		},
	)
}

// Run implements Tool.
func (EditFile) Run(ctx context.Context, args string) (string, error) {
	var in struct {
		Path       string `json:"path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if in.OldString == "" {
		return "", errors.New("old_string is empty; use write_file to rewrite the whole file")
	}
	if in.OldString == in.NewString {
		return "", errors.New("old_string and new_string are identical, so there is nothing to change")
	}

	env := EnvFrom(ctx)
	abs, err := env.Resolve(in.Path)
	if err != nil {
		return "", err
	}

	raw, err := os.ReadFile(abs)
	if err != nil {
		return "", errorText(err)
	}
	content := string(raw)

	count := strings.Count(content, in.OldString)
	if count == 0 {
		return "", fmt.Errorf("old_string was not found in %s. Read the file first and copy the exact text, indentation and line breaks included", in.Path)
	}
	if count > 1 && !in.ReplaceAll {
		return "", fmt.Errorf("old_string appears %d times in %s, so it is ambiguous. Extend it until it is unique, or set replace_all=true", count, in.Path)
	}

	var updated string
	if in.ReplaceAll {
		updated = strings.ReplaceAll(content, in.OldString, in.NewString)
	} else {
		updated = strings.Replace(content, in.OldString, in.NewString, 1)
	}

	if err := env.ask(ctx, Approval{
		Kind:  "write",
		Title: fmt.Sprintf("edit %s (%d places)", shortPath(env, abs), count),
		Detail: fmt.Sprintf("%s\n\nreplace:\n%s\n\nwith:\n%s",
			abs, preview(in.OldString, 20), preview(in.NewString, 20)),
		Paths: []string{abs},
	}); err != nil {
		return "", err
	}

	// Keep the original mode when writing back.
	info, statErr := os.Stat(abs)
	mode := os.FileMode(0o644)
	if statErr == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(abs, []byte(updated), mode); err != nil {
		return "", errorText(err)
	}

	return fmt.Sprintf("changed %s (%d places)", shortPath(env, abs), count), nil
}

// --- small helpers ---

// shortPath shortens an absolute path relative to the working directory, for readable messages.
func shortPath(env Env, abs string) string {
	if root, err := env.RootDir(); err == nil {
		if rel, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return abs
}

// preview takes the first n lines and marks when there are more.
func preview(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + fmt.Sprintf("\n… (%d more lines)", len(lines)-n)
}

// errorText turns filesystem errors into wording the model understands.
func errorText(err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return errors.New("the file or directory does not exist")
	case errors.Is(err, os.ErrPermission):
		return errors.New("permission denied for this path")
	default:
		return err
	}
}
