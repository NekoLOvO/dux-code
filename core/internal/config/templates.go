package config

// Template is one entry in the "add provider" list.
//
// Only what needs pre-filling is here (endpoint, which client), along with the blurb, so the UI does
// not keep its own copy of the table.
type Template struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Type        string `json:"type"`
	BaseURL     string `json:"baseUrl"`
	Icon        string `json:"icon"`
	Description string `json:"description"`
	NeedsKey    bool   `json:"needsKey"`
	// Multiple means this template can be added several times, each with its own name. Currently only
	// the OpenAI-compatible endpoint needs that: running Ollama and a company vLLM side by side is
	// normal.
	Multiple bool `json:"multiple,omitempty"`
}

// The order is the order in the UI: the first five by name, with the catch-all generic entry last.
//
// Ollama gets no slot of its own: it (like llama.cpp, vLLM, LM Studio) is an OpenAI-compatible
// endpoint and only the default address differs. An extra slot is an extra description to maintain
// with no behavioural difference.
var templates = []Template{
	{
		ID:          "anthropic",
		Label:       "Anthropic",
		Type:        "anthropic",
		BaseURL:     "https://api.anthropic.com/v1",
		Icon:        "anthropic",
		Description: "Claude models. Uses the native Messages API.",
		NeedsKey:    true,
	},
	{
		ID:          "deepseek",
		Label:       "DeepSeek",
		Type:        "openai",
		BaseURL:     "https://api.deepseek.com/v1",
		Icon:        "deepseek",
		Description: "DeepSeek's own API. Cheap, and good at code.",
		NeedsKey:    true,
	},
	{
		ID:    "google",
		Label: "Google Gemini",
		Type:  "gemini",
		// Native endpoint. The compatible one (/v1beta/openai) has no google_search — Google states that
		// grounding is unsupported in compatibility mode — so built-in search needs this one.
		BaseURL:     "https://generativelanguage.googleapis.com/v1beta",
		Icon:        "google",
		Description: "Gemini through its native API, with built-in Google search.",
		NeedsKey:    true,
	},
	{
		ID:    "openai",
		Label: "OpenAI",
		Type:  "openai-responses",
		// Goes through /v1/responses: only that endpoint has built-in web_search, and reasoning depth moves
		// from a top-level reasoning_effort to a nested reasoning:{effort}. Compatible endpoints (deepseek,
		// openrouter, Ollama…) keep the "openai" type.
		BaseURL:     "https://api.openai.com/v1",
		Icon:        "openai",
		Description: "GPT models through the Responses API, with built-in web search.",
		NeedsKey:    true,
	},
	{
		ID:          "openrouter",
		Label:       "OpenRouter",
		Type:        "openai",
		BaseURL:     "https://openrouter.ai/api/v1",
		Icon:        "openrouter",
		Description: "One key for many providers, including free models.",
		NeedsKey:    true,
	},
	{
		ID:          "custom",
		Label:       "OpenAI-compatible",
		Type:        "openai",
		BaseURL:     "",
		Icon:        "custom",
		Description: "Any OpenAI-compatible endpoint: Ollama, llama.cpp, vLLM, LM Studio.",
		// A local endpoint needs no key; provider.add uses IsLocalURL, so the true here only affects the
		// card's hint text.
		NeedsKey: true,
		// Several of these usually coexist (a local Ollama, a company vLLM), so it must be addable more
		// than once, each with its own name.
		Multiple: true,
	},
}

// Templates returns the selectable provider templates.
func Templates() []Template {
	out := make([]Template, len(templates))
	copy(out, templates)
	return out
}

// TemplateByID finds one template.
func TemplateByID(id string) (Template, bool) {
	for _, t := range templates {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}
