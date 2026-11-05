package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestApprovalModeDefaults checks that unreadable values fall back to asking every time. This field
// decides whether tools may touch files by themselves, and erring loose is too expensive.
func TestApprovalModeDefaults(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "ask"},    // older config
		{"ask", "ask"}, // explicit
		{"workspace", "workspace"},
		{"auto", "auto"},
		{"yes", "ask"},  // nonsense
		{"AUTO", "ask"}, // case is not guessed
	}
	for _, c := range cases {
		cfg := &Config{ApprovalMode: c.in}
		cfg.FixDefaults()
		if cfg.ApprovalMode != c.want {
			t.Errorf("%q → %q, want %q", c.in, cfg.ApprovalMode, c.want)
		}
	}
}

// TestActiveSearch covers how the backend to use is chosen. The point is stable ordering: map
// iteration differs every time, and picking a different backend from the same config looks broken.
func TestActiveSearch(t *testing.T) {
	cfg := &Config{Search: map[string]SearchProvider{
		"b-tavily": {Type: "tavily", APIKey: "k", Enabled: true, MaxResults: 3},
		"a-brave":  {Type: "brave", APIKey: "k", Enabled: true, MaxResults: 3},
	}}
	if id, _, ok := cfg.ActiveSearch(); !ok || id != "a-brave" {
		t.Fatalf("with no explicit choice, want the first by id, got %q", id)
	}

	cfg.SearchDefault = "b-tavily"
	if id, _, _ := cfg.ActiveSearch(); id != "b-tavily" {
		t.Fatalf("an explicit choice must win, got %q", id)
	}

	// The default is disabled → fall back to the other enabled one.
	p := cfg.Search["b-tavily"]
	p.Enabled = false
	cfg.Search["b-tavily"] = p
	if id, _, _ := cfg.ActiveSearch(); id != "a-brave" {
		t.Fatalf("with the default disabled, fall back to the other one, got %q", id)
	}

	// All disabled → no usable backend.
	p = cfg.Search["a-brave"]
	p.Enabled = false
	cfg.Search["a-brave"] = p
	if _, _, ok := cfg.ActiveSearch(); ok {
		t.Fatal("with everything disabled there must be no backend")
	}
}

// TestSearchDefaults checks that loading fills in MaxResults and points the default at something
// usable.
func TestSearchDefaults(t *testing.T) {
	cfg := &Config{Search: map[string]SearchProvider{
		"x": {Type: "searxng", BaseURL: "http://localhost:8888", Enabled: true},
	}}
	cfg.FixDefaults()
	p := cfg.Search["x"]
	if p.MaxResults != 5 {
		t.Errorf("MaxResults should default to 5, got %d", p.MaxResults)
	}
	if p.Label != "searxng" {
		t.Errorf("with no label, use the kind name, got %q", p.Label)
	}
	if cfg.SearchDefault != "x" {
		t.Errorf("the default should point at the only enabled backend, got %q", cfg.SearchDefault)
	}
}

// The step cap has to reach config.json: clamping only in memory leaves the UI showing 100 while the
// file says 0, and the next person has to work out why the numbers disagree.
func TestMaxToolStepsRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg := &Config{MaxToolSteps: 16}
	cfg.FixDefaults()
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	back, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if back.MaxToolSteps != 16 {
		t.Errorf("save and reload must keep 16, got %d", back.MaxToolSteps)
	}

	// 0 in a v2 file meant "unset": it has to be pinned to the default during migration, or upgrading
	// would silently remove the cap.
	if err := os.WriteFile(Path(), []byte(`{"version":2,"maxToolSteps":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	back, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if back.MaxToolSteps != DefaultMaxToolSteps {
		t.Errorf("v2 with 0 must fall back to the default %d, got %d", DefaultMaxToolSteps, back.MaxToolSteps)
	}
	// From v3 on, 0 is the user turning the fuse off and must survive.
	if err := os.WriteFile(Path(), []byte(`{"version":3,"maxToolSteps":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	back, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := back.ToolStepLimit(); got != 0 {
		t.Errorf("v3 with 0 means no cap, got %d", got)
	}
	if err := os.WriteFile(Path(), []byte(`{"version":3,"maxToolSteps":99999}`), 0o600); err != nil {
		t.Fatal(err)
	}
	back, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := back.ToolStepLimit(); got != MaxToolStepsLimit {
		t.Errorf("an absurd value must clamp to %d, got %d", MaxToolStepsLimit, got)
	}
}

// TestMaxOutputTokensOff: 0 is legal and is the default ("no cap; the provider decides"), so a file
// that never mentioned the field reads as off rather than as a hidden 4096.
func TestMaxOutputTokensOff(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	back, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if back.MaxOutputTokens != 0 {
		t.Errorf("a file without the field must be off, got %d", back.MaxOutputTokens)
	}

	if err := os.WriteFile(Path(), []byte(`{"version":3,"maxOutputTokens":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	back, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if back.MaxOutputTokens != 0 {
		t.Errorf("v3 with 0 means off, got %d", back.MaxOutputTokens)
	}
}

// TestToolSwitchesDefaultOn: an absent field counts as on. With a plain bool, reading an older config
// would switch every tool off.
func TestToolSwitchesDefaultOn(t *testing.T) {
	var t0 ToolSwitches
	if !t0.FileEnabled() || !t0.ShellEnabled() {
		t.Error("the zero value should enable both")
	}

	no := false
	yes := true
	if got := (ToolSwitches{FileAccess: &no}).FileEnabled(); got {
		t.Error("an explicit false must stay false")
	}
	if got := (ToolSwitches{FileAccess: &yes, Shell: &no}).ShellEnabled(); got {
		t.Error("an explicit false for shell must stay false")
	}
}

// TestOutputCap: the number that reaches the wire. A cap above the model's own ceiling is not
// generous, it is a 400, so it has to come down; everything else is the user's choice.
func TestOutputCap(t *testing.T) {
	cases := []struct {
		name   string
		chosen int
		model  int
		want   int
	}{
		{"off, ceiling unknown", 0, 0, 0},
		{"off, ceiling known", 0, 64000, 0},
		{"below the ceiling", 32000, 64000, 32000},
		{"exactly the ceiling", 64000, 64000, 64000},
		{"above the ceiling", 256000, 64000, 64000},
		{"ceiling unknown, cap set", 32000, 0, 32000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &Config{MaxOutputTokens: c.chosen}
			if got := cfg.OutputCap(Resolved{MaxOutput: c.model}); got != c.want {
				t.Errorf("OutputCap(%d chosen, %d model) = %d, want %d", c.chosen, c.model, got, c.want)
			}
		})
	}

	var nilCfg *Config
	if got := nilCfg.OutputCap(Resolved{MaxOutput: 8000}); got != 0 {
		t.Errorf("a nil config has no cap, got %d", got)
	}
}

// TestMaxOutputTokensClamped: the dropdown tops out at MaxOutputCap, so a hand-edited value above it
// must not survive as a request no option in the UI can explain or undo.
func TestMaxOutputTokensClamped(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(`{"version":3,"maxOutputTokens":900000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	back, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if back.MaxOutputTokens != MaxOutputCap {
		t.Errorf("an absurd cap must clamp to %d, got %d", MaxOutputCap, back.MaxOutputTokens)
	}
}

// A compaction threshold above the model's window can never fire in time: the request that would cross
// it is already too large, and an overflow is a 400 that is not retried. It has to come down.
func TestCompactThresholdFitsTheContextWindow(t *testing.T) {
	cases := []struct {
		name       string
		configured int
		context    int
		want       int
	}{
		{"off stays off", 0, 1048576, 0},
		{"well inside the window", 65536, 1048576, 65536},
		{"window unknown keeps the number", 524288, 0, 524288},
		{"above the window comes down", 524288, 131072, 104857}, // 131072 * 0.8
		{"equal to the window comes down", 131072, 131072, 104857},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &Config{CompactAt: c.configured}
			got := cfg.CompactThreshold(Resolved{Context: c.context})
			if got != c.want {
				t.Errorf("CompactThreshold(%d, window %d) = %d, want %d", c.configured, c.context, got, c.want)
			}
		})
	}
}
