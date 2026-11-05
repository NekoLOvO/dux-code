package search

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearXNGGetsJSON(t *testing.T) {
	var gotQuery string
	var gotFormat string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		gotFormat = r.URL.Query().Get("format")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"title":"Quickshell","url":"https://quickshell.org","content":"A toolkit"},
			{"title":"Layer shell","url":"https://example.org/l","content":"<b>bold</b> text"}
		]}`))
	}))
	defer srv.Close()

	c := Client{Kind: "searxng", BaseURL: srv.URL}
	res, err := c.Search(context.Background(), "quickshell", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotQuery != "quickshell" {
		t.Errorf("the query was not passed through: %q", gotQuery)
	}
	// An instance without format=json answers HTML — the most common misconfiguration.
	if gotFormat != "json" {
		t.Errorf("json is required: %q", gotFormat)
	}
	if len(res) != 2 {
		t.Fatalf("want 2, got %d", len(res))
	}
	if res[0].URL != "https://quickshell.org" {
		t.Errorf("wrong URL: %+v", res[0])
	}
	if strings.Contains(res[1].Snippet, "<b>") {
		t.Errorf("tags in the snippet should be stripped: %q", res[1].Snippet)
	}
}

func TestSearXNGHTMLMeansNoJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not json</html>"))
	}))
	defer srv.Close()

	c := Client{Kind: "searxng", BaseURL: srv.URL}
	_, err := c.Search(context.Background(), "x", 3)
	if err == nil {
		t.Fatal("returning HTML should be an error")
	}
	// The message must name the real cause instead of just "parse failed".
	if !strings.Contains(err.Error(), "JSON") {
		t.Errorf("the error must mention the JSON setting: %v", err)
	}
}

func TestSearXNGNeedsURL(t *testing.T) {
	c := Client{Kind: "searxng"}
	if _, err := c.Search(context.Background(), "x", 3); err == nil {
		t.Fatal("no URL should be an error")
	}
}

func TestBraveSendsKeyAndParses(t *testing.T) {
	old := braveEndpoint
	defer func() { braveEndpoint = old }()

	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Subscription-Token")
		_, _ = w.Write([]byte(`{"web":{"results":[
			{"title":"A","url":"https://a.example","description":"desc <strong>hi</strong>"}
		]}}`))
	}))
	defer srv.Close()
	braveEndpoint = srv.URL

	c := Client{Kind: "brave", APIKey: "secret"}
	res, err := c.Search(context.Background(), "x", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotKey != "secret" {
		t.Errorf("the key was not passed through: %q", gotKey)
	}
	if len(res) != 1 || res[0].Snippet != "desc hi" {
		t.Errorf("unexpected parse result: %+v", res)
	}
}

func TestBraveNeedsKey(t *testing.T) {
	c := Client{Kind: "brave"}
	if _, err := c.Search(context.Background(), "x", 3); err == nil {
		t.Fatal("no key should be an error")
	}
}

func TestTavilyPostsAndParses(t *testing.T) {
	old := tavilyEndpoint
	defer func() { tavilyEndpoint = old }()

	var gotAuth string
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		_, _ = w.Write([]byte(`{"results":[{"title":"T","url":"https://t.example","content":"long content"}]}`))
	}))
	defer srv.Close()
	tavilyEndpoint = srv.URL

	c := Client{Kind: "tavily", APIKey: "tvly"}
	res, err := c.Search(context.Background(), "what to ask", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotAuth != "Bearer tvly" {
		t.Errorf("wrong Authorization: %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"max_results":2`) {
		t.Errorf("the count never reached the body: %s", gotBody)
	}
	if len(res) != 1 || res[0].Snippet != "long content" {
		t.Errorf("unexpected parse result: %+v", res)
	}
}

// TestStatusMapping covers the three common failures: these lines appear on the settings page's
// test button, where the user decides what to do next.
func TestStatusMapping(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "key"},
		{http.StatusTooManyRequests, "rate limited"},
		{http.StatusInternalServerError, "HTTP 500"},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte("nope"))
		}))
		c := Client{Kind: "searxng", BaseURL: srv.URL}
		_, err := c.Search(context.Background(), "x", 1)
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("the message for HTTP %d should mention %q, got %v", tc.status, tc.want, err)
		}
	}
}

func TestLimitClamp(t *testing.T) {
	if _, err := (Client{Kind: "nope"}).Search(context.Background(), "x", 3); err == nil {
		t.Fatal("an unknown kind should be an error")
	}
	if _, err := (Client{Kind: "searxng", BaseURL: "http://x"}).Search(context.Background(), "  ", 3); err == nil {
		t.Fatal("an empty query should be an error")
	}
}
