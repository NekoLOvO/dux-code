// Package search is the web-search backend.
//
// A backend does one thing: query in, results out. Key storage, UI and caching live elsewhere.
// The three implementations share one file on purpose — they differ only in request shape and
// response fields, and splitting them would mean opening three files to see the difference.
package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Result is one hit. Snippet may be empty (some backends return titles only).
type Result struct {
	Title   string
	URL     string
	Snippet string
}

// Kind describes a backend type so the settings page can list it.
type Kind struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	NeedURL bool   `json:"needUrl"` // self-hosted instances need a URL
	NeedKey bool   `json:"needKey"` // hosted services need a key
	Hint    string `json:"hint"`
}

// Kinds are the supported backends, in the order the settings page shows them: keyless first, so
// self-hosters need not sign up for an account.
func Kinds() []Kind {
	return []Kind{
		{
			ID:      "searxng",
			Label:   "SearXNG",
			NeedURL: true,
			Hint:    "URL of your own instance, e.g. http://localhost:8888. The instance must enable JSON output.",
		},
		{
			ID:      "brave",
			Label:   "Brave Search",
			NeedKey: true,
			Hint:    "subscription token for api.search.brave.com. Returns titles and descriptions only; fetch the page for the body.",
		},
		{
			ID:      "tavily",
			Label:   "Tavily",
			NeedKey: true,
			Hint:    "Tavily API key from tavily.com. Returns extracted page snippets, so it needs the fewest round trips.",
		},
	}
}

// KindByID lets callers validate input.
func KindByID(id string) (Kind, bool) {
	for _, k := range Kinds() {
		if k.ID == id {
			return k, true
		}
	}
	return Kind{}, false
}

// Client is a configured backend.
type Client struct {
	Kind    string
	BaseURL string
	APIKey  string
}

// Official endpoints. They are variables only so tests can point them at httptest — with the
// hosts inlined, tests would need the real internet.
var (
	braveEndpoint  = "https://api.search.brave.com/res/v1/web/search"
	tavilyEndpoint = "https://api.tavily.com/search"
)

// Twenty seconds: search feeds a conversation, and making the model wait forty to then hear
// "nothing found" is worse than failing early.
const requestTimeout = 20 * time.Second

// Search runs one query. limit is clamped to 1..10.
func (c Client) Search(ctx context.Context, query string, limit int) ([]Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("the query string is empty")
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 10 {
		limit = 10
	}

	switch c.Kind {
	case "searxng":
		return c.searxng(ctx, query, limit)
	case "brave":
		return c.brave(ctx, query, limit)
	case "tavily":
		return c.tavily(ctx, query, limit)
	}
	return nil, fmt.Errorf("unsupported search backend: %q", c.Kind)
}

func (c Client) httpClient() *http.Client {
	return &http.Client{Timeout: requestTimeout}
}

// do performs the request and turns the usual failures into something readable.
//
// These messages surface in the settings page's "test" button and in the model's tool results,
// so they must be correct and point at the next step.
func (c Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot connect: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read the response: %v", err)
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("the key was rejected (HTTP %d)", resp.StatusCode)
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, errors.New("rate limited or out of quota (HTTP 429)")
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, excerpt(body))
	}
	return body, nil
}

func excerpt(b []byte) string {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "(empty response)"
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// ---- SearXNG ----
//
// The self-hosted instance's /search?format=json. Plenty of instances leave JSON off and answer
// with HTML, so the error has to say to fix settings.yml instead of just "parse failed".

func (c Client) searxng(ctx context.Context, query string, limit int) ([]Result, error) {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return nil, errors.New("SearXNG needs an instance URL")
	}
	u := base + "/search?" + url.Values{
		"q":          {query},
		"format":     {"json"},
		"safesearch": {"0"},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	body, err := c.do(req)
	if err != nil {
		return nil, err
	}

	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, errors.New("parse failed: the instance probably has JSON output disabled (search.formats in settings.yml)")
	}

	results := make([]Result, 0, len(out.Results))
	for _, r := range out.Results {
		if len(results) >= limit {
			break
		}
		results = append(results, Result{
			Title:   clean(r.Title),
			URL:     r.URL,
			Snippet: clean(r.Content),
		})
	}
	return results, nil
}

// ---- Brave ----
//
// GET api.search.brave.com/res/v1/web/search with the key in X-Subscription-Token.

func (c Client) brave(ctx context.Context, query string, limit int) ([]Result, error) {
	if strings.TrimSpace(c.APIKey) == "" {
		return nil, errors.New("Brave needs an API key")
	}
	u := braveEndpoint + "?" + url.Values{
		"q":     {query},
		"count": {strconv.Itoa(limit)},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", strings.TrimSpace(c.APIKey))

	body, err := c.do(req)
	if err != nil {
		return nil, err
	}

	var out struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("failed to parse the response: %v", err)
	}

	results := make([]Result, 0, len(out.Web.Results))
	for _, r := range out.Web.Results {
		if len(results) >= limit {
			break
		}
		results = append(results, Result{
			Title:   clean(r.Title),
			URL:     r.URL,
			Snippet: clean(r.Description),
		})
	}
	return results, nil
}

// ---- Tavily ----
//
// POST /search with Authorization: Bearer. The returned content is already extracted text, so
// Snippet runs much longer than Brave's — which is why it suits an agent.

func (c Client) tavily(ctx context.Context, query string, limit int) ([]Result, error) {
	if strings.TrimSpace(c.APIKey) == "" {
		return nil, errors.New("Tavily needs an API key")
	}
	payload, err := json.Marshal(map[string]any{
		"query":        query,
		"max_results":  limit,
		"search_depth": "basic",
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tavilyEndpoint, strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.APIKey))

	body, err := c.do(req)
	if err != nil {
		return nil, err
	}

	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("failed to parse the response: %v", err)
	}

	results := make([]Result, 0, len(out.Results))
	for _, r := range out.Results {
		if len(results) >= limit {
			break
		}
		results = append(results, Result{
			Title:   clean(r.Title),
			URL:     r.URL,
			Snippet: clean(r.Content),
		})
	}
	return results, nil
}

// tagRe strips HTML from snippets (Brave's description carries <strong>).
var tagRe = regexp.MustCompile(`<[^>]*>`)

func clean(s string) string {
	s = tagRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&#39;", "'")
	return strings.TrimSpace(s)
}
