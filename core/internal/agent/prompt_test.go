package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/NekoLOvO/dux-code/core/internal/config"
)

// The model cannot count its own tokens, so the only thing to do is give it an executable instruction:
// "wrap up when it will not fit". Without that line it writes until cut off.
func TestSystemPromptMentionsOutputBudget(t *testing.T) {
	cfg := &config.Config{MaxOutputTokens: 4096}
	got := systemPromptFor(cfg, cfg.MaxOutputTokens, "/tmp/ws", "", "")

	if !strings.Contains(got, "4096") {
		t.Errorf("the system prompt never mentions the output cap: %q", got)
	}
	if !strings.Contains(got, "Working directory: /tmp/ws") {
		t.Errorf("the working-directory section was dropped: %q", got)
	}
}

func TestSystemPromptWithoutBudgetStaysClean(t *testing.T) {
	cfg := &config.Config{}
	got := systemPromptFor(cfg, cfg.MaxOutputTokens, "", "", "")
	if strings.Contains(got, "capped at") {
		t.Errorf("with no cap set the budget section must not appear: %q", got)
	}
}

// Project rules must override the general description: what is said nearer the end is followed more
// reliably.
func TestProjectInstructionsWinByBeingLast(t *testing.T) {
	got := systemPromptFor(&config.Config{}, 0, "/tmp/ws", "", "- do not touch DCommon/")
	if !strings.Contains(got, "do not touch DCommon/") {
		t.Fatalf("AGENTS.md never made it into the system prompt: %q", got)
	}
	if strings.Index(got, "do not touch DCommon/") < strings.Index(got, "Working directory") {
		t.Error("project instructions belong after the generic guidance (later wins)")
	}
}

func TestProjectInstructionsReadFromWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, instructionsFile), []byte("- indent with tabs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := projectInstructions(dir); !strings.Contains(got, "indent with tabs") {
		t.Errorf("could not read AGENTS.md: %q", got)
	}

	// No file means none; do not guess other names.
	if got := projectInstructions(t.TempDir()); got != "" {
		t.Errorf("without AGENTS.md this should be empty, got %q", got)
	}
	if got := projectInstructions(""); got != "" {
		t.Errorf("without a working directory this should be empty, got %q", got)
	}
}

// An oversized file must be truncated with a note, not silently stuffed into the system prompt.
func TestProjectInstructionsTruncate(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("a", instructionsLimit+1) // one byte over the limit is enough to truncate
	if err := os.WriteFile(filepath.Join(dir, instructionsFile), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	got := projectInstructions(dir)
	if len(got) > instructionsLimit*2 {
		t.Errorf("nothing was truncated: length %d", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Error("truncation must be announced, or the model thinks that is all")
	}
	if !utf8.ValidString(got) {
		t.Error("truncation cut a multi-byte character in half")
	}
}
