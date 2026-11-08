package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NekoLOvO/dux-code/core/internal/config"
)

// TestSearchViewHidesKeys is the most important one here: a key must never appear in an IPC
// response, or it escapes through event streams, logs, and screenshots.
func TestSearchViewHidesKeys(t *testing.T) {
	cfg := &config.Config{
		Search: map[string]config.SearchProvider{
			"b": {Type: "brave", Label: "Brave", APIKey: "super-secret", Enabled: true, MaxResults: 3},
			"a": {Type: "searxng", Label: "SearXNG", BaseURL: "http://localhost:8888", Enabled: true},
		},
		SearchDefault: "b",
	}
	view := searchView(cfg)

	providers, _ := view["providers"].([]map[string]any)
	if len(providers) != 2 {
		t.Fatalf("want 2, got %d", len(providers))
	}
	// Order is fixed: the UI must not reshuffle on every repaint.
	if providers[0]["id"] != "a" || providers[1]["id"] != "b" {
		t.Errorf("the order should follow the ids: %v %v", providers[0]["id"], providers[1]["id"])
	}
	for _, p := range providers {
		if _, leaked := p["apiKey"]; leaked {
			t.Fatal("the key must not appear in the response")
		}
		for _, v := range p {
			if s, ok := v.(string); ok && strings.Contains(s, "super-secret") {
				t.Fatalf("the key leaked in a field: %v", p)
			}
		}
	}
	if providers[1]["hasKey"] != true {
		t.Error("hasKey should flag that a key exists for the UI without being the key itself")
	}
	if providers[1]["active"] != true || providers[0]["active"] != false {
		t.Errorf("wrong active markers: %v %v", providers[0]["active"], providers[1]["active"])
	}
}

// TestSearchAdapterUsesActiveBackend: the daemon really queries the configured backend, rather than
// only validating the settings shape.
func TestSearchAdapterUsesActiveBackend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" {
			t.Errorf("SearXNG needs format=json, got %q", r.URL.Query().Get("format"))
		}
		_, _ = w.Write([]byte(`{"results":[{"title":"T","url":"https://e.x","content":"c"}]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{Search: map[string]config.SearchProvider{
		"s": {Type: "searxng", BaseURL: srv.URL, Enabled: true, MaxResults: 2},
	}, SearchDefault: "s"}

	got, err := searchAdapter{cfg: cfg}.Search(context.Background(), "x", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].URL != "https://e.x" {
		t.Fatalf("the results were not converted: %+v", got)
	}
}

func TestSearchAdapterNoBackend(t *testing.T) {
	if _, err := (searchAdapter{cfg: &config.Config{}}).Search(context.Background(), "x", 0); err == nil {
		t.Fatal("no backend should be an error")
	}
	if _, err := (searchAdapter{cfg: nil}).Search(context.Background(), "x", 0); err == nil {
		t.Fatal("nil config should error, not panic")
	}
}
