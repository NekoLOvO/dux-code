package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NekoLOvO/dux-code/core/internal/config"
	"github.com/NekoLOvO/dux-code/core/internal/ipc"
	"github.com/NekoLOvO/dux-code/core/internal/tools"
)

// writeSkill creates a skill under a fake HOME.
func writeSkill(t *testing.T, dir, frontmatter, body string) {
	t.Helper()
	skillDir := filepath.Join(dir, ".agents", "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\n" + frontmatter + "\n---\n\n" + body
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadSkillInjectsBodyAndRestrictsTools is the core of the skill feature: after loading, the prose
// comes back and allowed-tools really narrows the next step's tools.
func TestLoadSkillInjectsBodyAndRestrictsTools(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSkill(t,
		home,
		"name: fix bug\ndescription: change code to match the spec\nallowed-tools: [\"bash\", \"read_file\"]",
		"## Steps\n1. run the tests\n2. then change the code")

	// Env must carry the run as SkillRuntime, or the restriction cannot be tested.
	rn := &run{}
	rn.env.Skills = rn
	ctx := tools.WithEnv(context.Background(), rn.env)

	out, err := (tools.LoadSkill{}).Run(ctx, `{"name":"fix bug"}`)
	if err != nil {
		t.Fatalf("load_skill: %v", err)
	}
	if !strings.Contains(out, "run the tests") {
		t.Errorf("the body never came back: %s", out)
	}
	if strings.Contains(out, "name:") {
		t.Errorf("frontmatter leaked through: %s", out)
	}

	// narrowed
	cfg := &config.Config{}
	r := New(ipc.NewEventBus(), nil, cfg)
	names := map[string]bool{}
	for _, s := range r.allowedSpecs(rn, cfg) {
		names[s.Function.Name] = true
	}
	if !names["bash"] || !names["read_file"] {
		t.Errorf("tools listed in allowed-tools must not be dropped: %v", names)
	}
	if names["write_file"] || names["glob"] {
		t.Errorf("tools outside allowed-tools should be hidden: %v", names)
	}
	if !names["load_skill"] {
		t.Error("load_skill must always stay, or the model is locked into one skill")
	}
}

// TestSkillWithoutAllowedToolsDoesNotRestrict checks that no allowed-tools means "unrestricted", not
// "nothing usable" — a stray nil would cripple the model.
func TestSkillWithoutAllowedToolsDoesNotRestrict(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSkill(t, home, "name: just looking\ndescription: read only", "look only")

	rn := &run{}
	rn.env.Skills = rn
	ctx := tools.WithEnv(context.Background(), rn.env)

	if _, err := (tools.LoadSkill{}).Run(ctx, `{"name":"just looking"}`); err != nil {
		t.Fatalf("load_skill: %v", err)
	}

	// This test is about whether the skill narrows anything, so the search backend has to be on;
	// otherwise web_search is filtered out by config and the count is one lower anyway.
	//
	// Browser likewise appears only with a usable executable, so a fake one is provided — otherwise the
	// test becomes "is agent-browser installed on this machine".
	bin := filepath.Join(t.TempDir(), "agent-browser")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Search: map[string]config.SearchProvider{
			"s": {Type: "tavily", APIKey: "k", Enabled: true},
		},
		Browser: config.BrowserSettings{Path: bin},
	}
	r := New(ipc.NewEventBus(), nil, cfg)
	if got := len(r.allowedSpecs(rn, cfg)); got != len(r.tools.Specs()) {
		t.Errorf("no allowed-tools means no restriction: %d vs %d", got, len(r.tools.Specs()))
	}
}

// TestTwoSkillsUnion checks that loading two skills takes the union, or the second would remove what
// the first allowed.
func TestTwoSkillsUnion(t *testing.T) {
	rn := &run{}
	rn.Activate("a", []string{"bash"})
	rn.Activate("b", []string{"read_file"})

	cfg := &config.Config{}
	r := New(ipc.NewEventBus(), nil, cfg)
	names := map[string]bool{}
	for _, s := range r.allowedSpecs(rn, cfg) {
		names[s.Function.Name] = true
	}
	if !names["bash"] || !names["read_file"] {
		t.Errorf("the tools of both skills should be unioned: %v", names)
	}
	if names["edit_file"] {
		t.Errorf("tools that were not allowed must not appear: %v", names)
	}
}

// TestAllowedSpecsAlwaysIncludesLoadSkill is a guard: if the narrowed list happened to exclude
// load_skill, the model could never load another skill.
func TestAllowedSpecsAlwaysIncludesLoadSkill(t *testing.T) {
	rn := &run{}
	rn.Activate("x", []string{"echo"})

	cfg := &config.Config{}
	r := New(ipc.NewEventBus(), nil, cfg)
	found := false
	for _, s := range r.allowedSpecs(rn, cfg) {
		if s.Function.Name == "load_skill" {
			found = true
		}
	}
	if !found {
		t.Fatal("load_skill disappeared")
	}
}

// TestSkillsPromptListsNameAndDescription checks that the system prompt lists them, or the model does
// not know skills exist.
func TestSkillsPromptListsNameAndDescription(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSkill(t, home, "name: deploy\ndescription: push things live", "steps")

	p := tools.SkillsPrompt()
	if !strings.Contains(p, "deploy") || !strings.Contains(p, "push things live") {
		t.Fatalf("the skill list is incomplete: %q", p)
	}
	if strings.Contains(p, "steps") {
		t.Errorf("the system prompt must not carry skill bodies (that is the job of load_skill): %q", p)
	}
}

// TestSkillsPromptEmptyWhenNoSkills checks that with nothing installed no empty shell appears.
func TestSkillsPromptEmptyWhenNoSkills(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if p := tools.SkillsPrompt(); p != "" {
		t.Errorf("with no skills this should be empty, got %q", p)
	}
}

// TestLoadSkillByDirName checks that the folder name works too — folder demo, name "fix bug", both
// must resolve.
func TestLoadSkillByDirName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSkill(t, home, "name: fix bug\ndescription: x", "body text")

	out, err := (tools.LoadSkill{}).Run(context.Background(), `{"name":"demo"}`)
	if err != nil {
		t.Fatalf("loading by folder name failed: %v", err)
	}
	if !strings.Contains(out, "body text") {
		t.Errorf("unexpected content: %s", out)
	}
}

// TestLoadSkillNotFoundListsAvailable checks that a mistyped name also tells the model what is
// available, so it can correct itself.
func TestLoadSkillNotFoundListsAvailable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSkill(t, home, "name: deploy\ndescription: x", "steps")

	_, err := (tools.LoadSkill{}).Run(context.Background(), `{"name":"nope"}`)
	if err == nil {
		t.Fatal("this should fail")
	}
	if !strings.Contains(err.Error(), "deploy") {
		t.Errorf("the error should list the available skills: %v", err)
	}
}

// TestSystemPromptComposition is a pure-function test: the working directory and the skill list must
// both reach the prompt, in a fixed order (directory first, skills after).
func TestSystemPromptComposition(t *testing.T) {
	cfg := &config.Config{SystemPrompt: "You are an assistant."}
	got := systemPromptFor(cfg, 0, "/tmp/work", "\n\nAvailable skills…", "")

	if !strings.Contains(got, "You are an assistant.") {
		t.Error("the custom prompt disappeared")
	}
	if !strings.Contains(got, "/tmp/work") {
		t.Error("the working directory disappeared")
	}
	if !strings.Contains(got, "Available skills") {
		t.Error("the skill list disappeared")
	}
	if strings.Index(got, "/tmp/work") > strings.Index(got, "Available skills") {
		t.Error("the working directory should come before the skill list")
	}

	// Without a working directory no empty shell may remain.
	empty := systemPromptFor(&config.Config{}, 0, "", "", "")
	if empty != "" {
		t.Errorf("with everything empty this should be empty, got %q", empty)
	}
}
