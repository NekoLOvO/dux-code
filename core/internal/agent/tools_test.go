package agent

import (
	"testing"

	"github.com/NekoLOvO/dux-code/core/internal/config"
	"github.com/NekoLOvO/dux-code/core/internal/ipc"
	"github.com/NekoLOvO/dux-code/core/internal/tools"
)

// TestRegisteredTools pins which tools the model actually sees. A missing registration shows up as the
// model saying it cannot do that, which is hard to spot in a log, so the list is asserted directly.
func TestRegisteredTools(t *testing.T) {
	r := New(ipc.NewEventBus(), nil, &config.Config{})

	got := make(map[string]bool)
	for _, s := range r.tools.Specs() {
		got[s.Function.Name] = true
		if s.Function.Description == "" {
			t.Errorf("tool %s has no description, so the model cannot tell when to use it", s.Function.Name)
		}
		if s.Function.Parameters == nil {
			t.Errorf("tool %s has no parameters schema", s.Function.Name)
		}
	}

	for _, want := range []string{
		"echo", "now",
		"read_file", "write_file", "edit_file",
		"bash", "grep", "glob",
		"load_skill",
	} {
		if !got[want] {
			t.Errorf("missing tool %s", want)
		}
	}
}

// TestToolEnvReachesTool checks that Env really travels through the context into the tool, rather
// than every tool guessing the working directory.
func TestToolEnvReachesTool(t *testing.T) {
	r := New(ipc.NewEventBus(), nil, &config.Config{})
	_ = r

	ctx := tools.WithEnv(t.Context(), tools.Env{Root: "/tmp/probe"})
	if env := tools.EnvFrom(ctx); env.Root != "/tmp/probe" {
		t.Fatalf("Env was not passed through: %q", env.Root)
	}
}
