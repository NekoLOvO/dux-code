package agent

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/NekoLOvO/dux-code/core/internal/config"
	"github.com/NekoLOvO/dux-code/core/internal/llm"
	"github.com/NekoLOvO/dux-code/core/internal/tools"
)

// capRecorder answers at once and remembers what the cap was on each request.
type capRecorder struct{ maxTokens []int }

func (c *capRecorder) Stream(_ context.Context, req llm.Request, _ llm.DeltaFunc) (llm.Result, error) {
	c.maxTokens = append(c.maxTokens, req.MaxTokens)
	return llm.Result{Content: "done", Usage: llm.Usage{Input: fakeUsageIn, Output: fakeUsageOut}, StopReason: "stop"}, nil
}

// runWithCap runs one turn against a model that reports this output ceiling.
func runWithCap(t *testing.T, chosen, ceiling int) *capRecorder {
	t.Helper()
	cfg := quietConfig(0)
	cfg.MaxOutputTokens = chosen
	r, sid, _ := newTestRunner(t, cfg)
	fake := &capRecorder{}
	model := config.Resolved{ID: "fake:m", Provider: "fake", Model: "m", MaxOutput: ceiling}
	rn := r.newRun(sid, model, tools.Env{}, "", false)
	r.execute(context.Background(), rn, fake, model, "hi")
	return fake
}

// A cap above the model's own ceiling is a 400, not a generous setting: it has to be lowered on the
// way out, and the system prompt must state the number that is actually enforced.
func TestOutputCapIsLoweredToTheModelCeiling(t *testing.T) {
	fake := runWithCap(t, 262144, 16384)
	if len(fake.maxTokens) == 0 {
		t.Fatal("the model was never called")
	}
	if fake.maxTokens[0] != 16384 {
		t.Errorf("a 262144 cap on a 16384 ceiling must be sent as 16384, got %d", fake.maxTokens[0])
	}
}

// "Off" means the provider decides, so nothing is sent. The ceiling only becomes the number when it is
// the only thing keeping the request legal (Anthropic), which is a client-side concern.
func TestOutputCapOffSendsNoNumber(t *testing.T) {
	fake := runWithCap(t, 0, 0)
	if len(fake.maxTokens) == 0 {
		t.Fatal("the model was never called")
	}
	if fake.maxTokens[0] != 0 {
		t.Errorf("with the cap off and no known ceiling the request must carry 0, got %d", fake.maxTokens[0])
	}
}

// The prompt and the wire must agree. Telling the model "capped at 262144" while the request carries
// 16384 is the one mistake the model cannot see through.
func TestSystemPromptStatesTheEffectiveCap(t *testing.T) {
	cfg := quietConfig(0)
	cfg.MaxOutputTokens = 262144
	r, sid, _ := newTestRunner(t, cfg)
	fake := &capRecorder{}
	model := config.Resolved{ID: "fake:m", Provider: "fake", Model: "m", MaxOutput: 16384}
	rn := r.newRun(sid, model, tools.Env{}, "", false)
	r.execute(context.Background(), rn, fake, model, "hi")

	prompt := systemPromptFor(cfg, cfg.OutputCap(model), "/tmp/ws", "", "")
	if !strings.Contains(prompt, strconv.Itoa(16384)) {
		t.Errorf("the prompt must state the enforced cap: %q", prompt)
	}
	if strings.Contains(prompt, "262144") {
		t.Errorf("the prompt repeats the user's setting instead of the real cap: %q", prompt)
	}
}

// ceiled refuses the first request in the provider's own words, then accepts whatever it is given.
type ceiled struct {
	body string
	caps []int
}

func (c *ceiled) Stream(_ context.Context, req llm.Request, _ llm.DeltaFunc) (llm.Result, error) {
	c.caps = append(c.caps, req.MaxTokens)
	if len(c.caps) == 1 {
		return llm.Result{}, &llm.HTTPError{Status: 400, Body: c.body}
	}
	return llm.Result{Content: "ok", Usage: llm.Usage{Input: 1, Output: 1}, StopReason: "stop"}, nil
}

// deepseekRefusal is the wording api.deepseek.com really answers with when max_tokens is above the
// maximum (captured live); only the number is lowered here, to a ceiling below the cap under test.
const deepseekRefusal = `{"error":{"message":"Invalid max_tokens value, the valid range of max_tokens is [1, 16384]","type":"invalid_request_error"}}`

// configWithProvider writes a config.json holding one provider and model, and returns the config the
// runner will hold. The agent re-reads the file when it records a ceiling, so the file has to be real.
func configWithProvider(t *testing.T, chosen, ceiling int) *config.Config {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := quietConfig(0)
	cfg.Version = 3
	cfg.MaxOutputTokens = chosen
	cfg.Providers = map[string]config.Provider{
		"fake": {
			Type:    "openai",
			BaseURL: "http://127.0.0.1:1",
			Models:  map[string]config.ModelEntry{"m": {Enabled: true, MaxOutput: ceiling}},
		},
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// The provider names its ceiling in the 400. That is a fact, and the useful thing to do with a fact is
// keep it and send the same step again — the alternative is losing the turn and learning nothing.
func TestCeilingFromA400IsLearnedAndTheStepRetried(t *testing.T) {
	cfg := configWithProvider(t, 262144, 0) // ceiling unknown, as for a provider whose model list omits it
	r, sid, events := newTestRunner(t, cfg)
	fake := &ceiled{body: deepseekRefusal}
	model := config.Resolved{ID: "fake:m", Provider: "fake", Model: "m"}
	rn := r.newRun(sid, model, tools.Env{}, "", false)
	r.execute(context.Background(), rn, fake, model, "hi")

	if len(fake.caps) != 2 {
		t.Fatalf("the step should be sent twice (refused, then accepted), got %d calls: %v", len(fake.caps), fake.caps)
	}
	if fake.caps[0] != 262144 || fake.caps[1] != 16384 {
		t.Errorf("the retry must carry the ceiling the provider stated, got %v", fake.caps)
	}
	kinds := kindsOf(*events)
	if kinds[len(kinds)-1] != "run.finished" {
		t.Errorf("the turn should finish, not fail: %v", kinds)
	}

	// Written down, or every session pays for the same discovery.
	back, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Providers["fake"].Models["m"].MaxOutput; got != 16384 {
		t.Errorf("the learned ceiling should be in config.json, got %d", got)
	}
}

// One retry, not a loop: if the provider refuses the number it just gave us, the error is real and the
// turn should end with it rather than hammering the endpoint.
func TestCeilingRetryHappensOnce(t *testing.T) {
	cfg := configWithProvider(t, 262144, 0)
	r, sid, events := newTestRunner(t, cfg)
	// Always refuses, even at the ceiling it named.
	always := &alwaysRefusing{}
	model := config.Resolved{ID: "fake:m", Provider: "fake", Model: "m"}
	rn := r.newRun(sid, model, tools.Env{}, "", false)
	r.execute(context.Background(), rn, always, model, "hi")

	if len(always.caps) != 2 {
		t.Fatalf("should give up after one retry, got %d calls: %v", len(always.caps), always.caps)
	}
	kinds := kindsOf(*events)
	if kinds[len(kinds)-1] != "run.failed" {
		t.Errorf("a refusal that survives the retry must surface, got %v", kinds)
	}
}

type alwaysRefusing struct{ caps []int }

func (c *alwaysRefusing) Stream(_ context.Context, req llm.Request, _ llm.DeltaFunc) (llm.Result, error) {
	c.caps = append(c.caps, req.MaxTokens)
	return llm.Result{}, &llm.HTTPError{Status: 400, Body: deepseekRefusal}
}
