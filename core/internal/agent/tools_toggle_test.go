package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/NekoLOvO/dux-code/core/internal/config"
)

// fakeBrowser creates an executable fake agent-browser so toolAllowed can find it.
func fakeBrowser(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "agent-browser")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// The composer's one switch governs three tools: search, page fetch and the browser.
//
// Three separate switches would produce "I turned web off, why is it still fetching pages", and if any
// one of them escapes the switch, the switch's promise is a lie.
func TestWebToolsShareOneToggle(t *testing.T) {
	on, off := true, false
	bin := fakeBrowser(t)

	cfg := &config.Config{
		Tools:   config.ToolSwitches{WebSearch: &on},
		Browser: config.BrowserSettings{Path: bin},
		Search: map[string]config.SearchProvider{
			"s": {Type: "tavily", APIKey: "k", Enabled: true},
		},
	}

	for _, name := range []string{"web_search", "web_fetch", "browser"} {
		if !toolAllowed(name, cfg) {
			t.Errorf("with network on, %s should be available", name)
		}
	}

	cfg.Tools.WebSearch = &off
	for _, name := range []string{"web_search", "web_fetch", "browser"} {
		if toolAllowed(name, cfg) {
			t.Errorf("with network off, %s must not appear", name)
		}
	}
}

// The browser has no switch of its own, but "no executable found" is a separate condition: handing
// the model a tool that always fails only makes it retry with different wording.
func TestBrowserNeedsExecutableNotJustToggle(t *testing.T) {
	on := true
	cfg := &config.Config{
		Tools:   config.ToolSwitches{WebSearch: &on},
		Browser: config.BrowserSettings{},
	}
	// Point PATH at an empty directory so the system's real copy cannot be used.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("DUXCODE_AGENT_BROWSER", "")

	if toolAllowed("browser", cfg) {
		t.Error("no executable means no browser tool")
	}

	// After setting the path it must appear — which also proves a settings change needs no restart.
	cfg.Browser.Path = fakeBrowser(t)
	if !toolAllowed("browser", cfg) {
		t.Error("pointing at an executable should make it appear")
	}
}

// Page fetch needs no search backend, search does. Without a backend only web_search drops and the
// other two still work — otherwise "no backend" would lock the browser out too.
func TestWebFetchSurvivesWithoutSearchBackend(t *testing.T) {
	on := true
	cfg := &config.Config{
		Tools:   config.ToolSwitches{WebSearch: &on},
		Browser: config.BrowserSettings{Path: fakeBrowser(t)},
	}

	if toolAllowed("web_search", cfg) {
		t.Error("no search backend means no web_search")
	}
	if !toolAllowed("web_fetch", cfg) {
		t.Error("web_fetch needs no backend")
	}
	if !toolAllowed("browser", cfg) {
		t.Error("browser needs no backend")
	}
}
