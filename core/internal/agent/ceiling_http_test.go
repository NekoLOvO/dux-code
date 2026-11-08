package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/NekoLOvO/dux-code/core/internal/config"
	"github.com/NekoLOvO/dux-code/core/internal/llm"
	"github.com/NekoLOvO/dux-code/core/internal/tools"
)

// TestOutputCeilingLearnedOverRealHTTP is the whole chain end to end: a real client, a real HTTP
// response, the parse of the provider's wording, the lowered cap on the retry, and the write to
// config.json.
//
// The unit tests each cover one link. This covers the joints — the error body a client actually stores,
// the config a fresh Load actually returns — which is where the pieces were most likely to disagree.
func TestOutputCeilingLearnedOverRealHTTP(t *testing.T) {
	const ceiling = 16384
	// The handler runs on its own goroutines; the socket is not a happens-before edge the race detector
	// understands.
	var (
		mu   sync.Mutex
		caps []int
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		caps = append(caps, body.MaxTokens)
		mu.Unlock()

		if body.MaxTokens > ceiling {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			// The wording api.deepseek.com really answers with; only the number is chosen for this test.
			// Write errors go to the test client, which is what would report them anyway.
			_, _ = fmt.Fprintf(w, `{"error":{"message":"Invalid max_tokens value, the valid range of max_tokens is [1, %d]","type":"invalid_request_error"}}`, ceiling)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{
			`{"choices":[{"index":0,"delta":{"content":"ok"}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`,
		} {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	cfg := configWithProvider(t, 262144, 0)
	provider := cfg.Providers["fake"]
	provider.BaseURL = srv.URL
	cfg.Providers["fake"] = provider
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	client, err := llm.New(provider)
	if err != nil {
		t.Fatal(err)
	}

	r, sid, events := newTestRunner(t, cfg)
	model := config.Resolved{ID: "fake:m", Provider: "fake", Model: "m"}
	rn := r.newRun(sid, model, tools.Env{}, "", false)
	r.execute(context.Background(), rn, client, model, "hi")

	mu.Lock()
	defer mu.Unlock()
	if len(caps) != 2 || caps[0] != 262144 || caps[1] != ceiling {
		t.Fatalf("want the step refused at 262144 then accepted at %d, got %v", ceiling, caps)
	}
	kinds := kindsOf(*events)
	if kinds[len(kinds)-1] != "run.finished" {
		t.Errorf("the turn should finish rather than fail: %v", kinds)
	}

	back, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Providers["fake"].Models["m"].MaxOutput; got != ceiling {
		t.Errorf("the ceiling should have been written to config.json, got %d", got)
	}
	// The user's own number is kept: it is a wish this model cannot grant, and the cap is the smaller of
	// the two. Overwriting it would quietly change what they asked for.
	if back.MaxOutputTokens != 262144 {
		t.Errorf("the user's cap must survive, got %d", back.MaxOutputTokens)
	}

	// The same number has to reach the two readers that would otherwise go on stating the old one: the
	// settings page (through the event) and the next turn (through the runner's live config). Without the
	// event the row keeps claiming 262k; without SetConfig the next turn pays for the same 400 again.
	changed := onlyKind(*events, "config.changed")
	if len(changed) != 1 {
		t.Fatalf("want one config.changed event, got %v", kinds)
	}
	if live := r.Config(); live.Providers["fake"].Models["m"].MaxOutput != ceiling {
		t.Errorf("the runner's config must carry the learned ceiling, got %d",
			live.Providers["fake"].Models["m"].MaxOutput)
	}
	if resolved, _, ok := r.Config().Resolve("fake:m"); !ok {
		t.Error("the model should still resolve from the live config")
	} else if got := r.Config().OutputCap(resolved); got != ceiling {
		t.Errorf("the next turn should send %d, got %d", ceiling, got)
	}
}

// TestCeilingNotWrittenForAnUnconfiguredModel: the model was resolved from somewhere other than
// config.json, and inventing a model entry there would put something the user never enabled into their
// settings. The ceiling is still used for this turn — the turn is what matters — it is just not kept.
func TestCeilingNotWrittenForAnUnconfiguredModel(t *testing.T) {
	cfg := configWithProvider(t, 262144, 0)
	// Drop the provider from the file but keep it in the runner's in-memory copy, which is the shape of
	// "selected from a list that config.json no longer has".
	stripped := *cfg
	stripped.Providers = map[string]config.Provider{}
	if err := stripped.Save(); err != nil {
		t.Fatal(err)
	}

	r, sid, events := newTestRunner(t, cfg)
	fake := &ceiled{body: deepseekRefusal}
	model := config.Resolved{ID: "fake:m", Provider: "fake", Model: "m"}
	rn := r.newRun(sid, model, tools.Env{}, "", false)
	r.execute(context.Background(), rn, fake, model, "hi")

	if len(fake.caps) != 2 || fake.caps[1] != 16384 {
		t.Errorf("the step should still be retried at the ceiling it was given, got %v", fake.caps)
	}
	kinds := kindsOf(*events)
	if kinds[len(kinds)-1] != "run.finished" {
		t.Errorf("a write that cannot be made must not fail the turn: %v", kinds)
	}
	back, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := back.Providers["fake"]; ok {
		t.Error("an unconfigured model's provider was invented in config.json")
	}
	// Nothing changed on disk, so nothing may claim it did: the settings page would re-fetch for nothing.
	if changed := onlyKind(*events, "config.changed"); len(changed) != 0 {
		t.Errorf("no config.changed may be published when nothing was written, got %v", kinds)
	}
}
