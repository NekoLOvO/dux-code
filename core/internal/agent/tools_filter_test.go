package agent

import (
	"testing"

	"github.com/NekoLOvO/dux-code/core/internal/config"
)

// TestToolAllowed is the only thing backing those two settings switches. Without it, turning off
// "file access" would only change the UI while the model keeps reading and writing.
func TestToolAllowed(t *testing.T) {
	on := true
	off := false

	allOpen := &config.Config{}
	for _, name := range []string{"read_file", "write_file", "edit_file", "glob", "grep", "bash"} {
		if !toolAllowed(name, allOpen) {
			t.Errorf("everything should be on by default, but %s was blocked", name)
		}
	}
	// Without a search backend web_search must not appear.
	if toolAllowed("web_search", allOpen) {
		t.Error("no search backend means no web_search")
	}

	noFiles := &config.Config{Tools: config.ToolSwitches{FileAccess: &off}}
	for _, name := range []string{"read_file", "write_file", "edit_file", "glob", "grep"} {
		if toolAllowed(name, noFiles) {
			t.Errorf("%s is still on after file access was disabled", name)
		}
	}
	if !toolAllowed("bash", noFiles) {
		t.Error("disabling files must not disable bash")
	}

	noShell := &config.Config{Tools: config.ToolSwitches{Shell: &off}}
	if toolAllowed("bash", noShell) {
		t.Error("bash is still offered after being disabled")
	}
	if !toolAllowed("read_file", noShell) {
		t.Error("disabling bash must not disable file reads")
	}

	withSearch := &config.Config{
		Search: map[string]config.SearchProvider{"s": {Type: "tavily", APIKey: "k", Enabled: true}},
	}
	if !toolAllowed("web_search", withSearch) {
		t.Error("with a search backend web_search should be offered")
	}
	_ = on

	// The composer's switch: a backend exists but the user turned it off, so it must not be offered.
	offSearch := false
	switchedOff := &config.Config{
		Tools:  config.ToolSwitches{WebSearch: &offSearch},
		Search: map[string]config.SearchProvider{"s": {Type: "tavily", APIKey: "k", Enabled: true}},
	}
	if toolAllowed("web_search", switchedOff) {
		t.Error("the toggle off means no web_search even with a backend")
	}

	// Unknown tools (future MCP and such) pass by default: this only handles the known switches.
	if !toolAllowed("some_future_tool", allOpen) {
		t.Error("an unknown tool must not be filtered here")
	}
}
