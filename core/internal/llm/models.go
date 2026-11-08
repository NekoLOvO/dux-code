package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/NekoLOvO/dux-code/core/internal/config"
)

// ModelInfo is one model fetched from a provider.
//
// Only id is guaranteed; the rest differ per provider, filled in when present and zero otherwise.
type ModelInfo struct {
	ID string `json:"id"`
	// Name is a human-readable name, provided only by some upstreams (OpenRouter, Ollama). Empty when
	// absent, and callers fall back to ID.
	Name    string `json:"name,omitempty"`
	OwnedBy string `json:"ownedBy,omitempty"`
	// Context and MaxOutput are the model's limits in tokens, reported only by some upstreams (OpenRouter,
	// DeepSeek, Gemini). 0 means "not said", which callers keep rather than overwrite.
	Context   int `json:"contextWindow,omitempty"`
	MaxOutput int `json:"maxOutput,omitempty"`
}

// Title is the name to show: Name when present, otherwise ID.
func (m ModelInfo) Title() string {
	if strings.TrimSpace(m.Name) != "" {
		return m.Name
	}
	return m.ID
}

// FetchModels asks a provider which models it has.
//
// It uses OpenAI's GET /models (Ollama, llama.cpp and OpenRouter all speak it). The call does not
// block for long, so 20 seconds is plenty.
func FetchModels(ctx context.Context, p config.Provider) ([]ModelInfo, error) {
	key, _ := p.ResolveKey()
	if key == "" && p.NeedsKey() {
		return nil, ErrNoAPIKey
	}

	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		return nil, fmt.Errorf("no endpoint URL")
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	// Gemini returns 50 models per page by default, which is fewer than it publishes: the rest of the
	// list would be missing from the picker with no sign that it was cut off.
	listURL := base + "/models"
	gemini := strings.EqualFold(p.Type, "gemini")
	if gemini {
		listURL += "?pageSize=1000"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	switch {
	case strings.EqualFold(p.Type, "anthropic"):
		// Anthropic uses x-api-key plus a version header; a Bearer token is rejected.
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", anthropicVersion)
	case gemini:
		// Gemini's native endpoint reads the key from x-goog-api-key. A Bearer token is only for the
		// OpenAI-compatible /v1beta/openai front end, and this provider deliberately does not use that
		// one (it has no google_search) — sending Bearer here is answered with "Request had invalid
		// authentication credentials. Expected OAuth 2 access token".
		req.Header.Set("x-goog-api-key", key)
	case key != "":
		req.Header.Set("Authorization", "Bearer "+key)
	}

	client := sharedClient
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	// The body is read to the end below; a close error afterwards is the connection's, not the list's.
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{Status: resp.StatusCode, Body: snippet(body)}
	}

	models, err := parseModels(body)
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("endpoint answered but returned no models")
	}

	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

// parseModels accepts a few common shapes:
//
//	{"data":[{"id":"gpt-4o"}]}          OpenAI / Ollama / OpenRouter
//	{"models":[{"name":"llama3"}]}      Ollama native
//	["gpt-4o"]                          a few self-hosted servers
func parseModels(body []byte) ([]ModelInfo, error) {
	var envelope struct {
		// Several spellings of the same two numbers exist: OpenRouter says context_length plus
		// top_provider.max_completion_tokens, DeepSeek sends context_window / max_output_tokens,
		// Gemini sends inputTokenLimit / outputTokenLimit. All are read; absent stays 0.
		Data []struct {
			ID               string `json:"id"`
			Name             string `json:"name"`
			OwnedBy          string `json:"owned_by"`
			ContextLength    int    `json:"context_length"`
			ContextWindow    int    `json:"context_window"`
			MaxOutputTokens  int    `json:"max_output_tokens"`
			InputTokenLimit  int    `json:"inputTokenLimit"`
			OutputTokenLimit int    `json:"outputTokenLimit"`
			TopProvider      struct {
				MaxCompletionTokens int `json:"max_completion_tokens"`
			} `json:"top_provider"`
		} `json:"data"`
		Models []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
			// Gemini's native list is the one upstream that puts the limits under models[] rather
			// than data[]: {"models":[{"name":"models/gemini-2.5-pro","displayName":"…",
			// "inputTokenLimit":1048576,"outputTokenLimit":65536}]}.
			InputTokenLimit  int `json:"inputTokenLimit"`
			OutputTokenLimit int `json:"outputTokenLimit"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil {
		out := make([]ModelInfo, 0, len(envelope.Data)+len(envelope.Models))
		for _, m := range envelope.Data {
			id := m.ID
			if id == "" {
				id = m.Name
			}
			if id == "" {
				continue
			}
			// OpenRouter's data[].name is a title like "Anthropic: Claude …", while the id is
			// anthropic/claude-…. Both are kept so the UI can show the title large and the id small.
			out = append(out, ModelInfo{
				ID:        id,
				Name:      m.Name,
				OwnedBy:   m.OwnedBy,
				Context:   firstPositive(m.ContextLength, m.ContextWindow, m.InputTokenLimit),
				MaxOutput: firstPositive(m.TopProvider.MaxCompletionTokens, m.MaxOutputTokens, m.OutputTokenLimit),
			})
		}
		for _, m := range envelope.Models {
			id := m.ID
			if id == "" {
				id = m.Name
			}
			if id == "" {
				continue
			}
			// Gemini names a model "models/gemini-2.5-pro". The prefix is a resource path, not part of
			// the name: chat already trims it, and leaving it on the id would put "models/" in the
			// model picker and in config.json.
			id = strings.TrimPrefix(id, "models/")
			// displayName is Gemini's title ("Gemini 2.5 Pro"); Ollama has none and falls back to the id.
			out = append(out, ModelInfo{
				ID:        id,
				Name:      firstNonEmpty(m.DisplayName, m.Name),
				Context:   m.InputTokenLimit,
				MaxOutput: m.OutputTokenLimit,
			})
		}
		if len(out) > 0 {
			return dedupe(out), nil
		}
	}

	var plain []string
	if err := json.Unmarshal(body, &plain); err == nil && len(plain) > 0 {
		out := make([]ModelInfo, 0, len(plain))
		for _, id := range plain {
			out = append(out, ModelInfo{ID: id})
		}
		return out, nil
	}

	return nil, fmt.Errorf("unrecognized /models response")
}

func dedupe(in []ModelInfo) []ModelInfo {
	seen := make(map[string]bool, len(in))
	out := make([]ModelInfo, 0, len(in))
	for _, m := range in {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, m)
	}
	return out
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// firstNonEmpty returns the first value that is not blank.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// firstPositive returns the first number greater than zero; 0 when none is.
func firstPositive(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}
