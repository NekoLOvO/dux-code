package llm

import "testing"

// TestParseModelsKeepsDisplayName: OpenRouter returns both id and name, and the UI wants the name
// large and the id small. Dropping the name leaves only unreadable strings like
// anthropic/claude-sonnet-4.
func TestParseModelsKeepsDisplayName(t *testing.T) {
	body := []byte(`{"data":[
		{"id":"anthropic/claude-sonnet-4","name":"Anthropic: Claude Sonnet 4"},
		{"id":"gpt-4o"}
	]}`)
	models, err := parseModels(body)
	if err != nil {
		t.Fatalf("parseModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("want 2, got %d", len(models))
	}
	if models[0].Name != "Anthropic: Claude Sonnet 4" || models[0].Title() != "Anthropic: Claude Sonnet 4" {
		t.Errorf("name was dropped: %+v", models[0])
	}
	// Without a name, fall back to the id.
	if models[1].Title() != "gpt-4o" {
		t.Errorf("without a name it should fall back to the id: %q", models[1].Title())
	}
}

// TestParseModelsOllamaNative: Ollama's /api/tags uses models[].name.
func TestParseModelsOllamaNative(t *testing.T) {
	models, err := parseModels([]byte(`{"models":[{"name":"llama3:8b"}]}`))
	if err != nil {
		t.Fatalf("parseModels: %v", err)
	}
	if len(models) != 1 || models[0].ID != "llama3:8b" {
		t.Fatalf("unexpected parse result: %+v", models)
	}
}

// TestParseModelsKeepsLimits: three wire spellings of the same two numbers are in the wild — OpenRouter
// (context_length + top_provider.max_completion_tokens), DeepSeek (context_window + max_output_tokens,
// as api.deepseek.com actually answers) and Gemini (inputTokenLimit + outputTokenLimit). They are what
// keep a "256k" output cap from being sent to a model whose maximum is smaller. Missing fields must
// stay 0 (unknown), not become a guessed number.
func TestParseModelsKeepsLimits(t *testing.T) {
	body := []byte(`{"data":[
		{"id":"a","context_length":128000,"top_provider":{"max_completion_tokens":16384}},
		{"id":"b","context_window":1048576,"max_output_tokens":393216},
		{"id":"c","inputTokenLimit":2000000,"outputTokenLimit":65536},
		{"id":"d"}
	]}`)
	models, err := parseModels(body)
	if err != nil {
		t.Fatalf("parseModels: %v", err)
	}
	if models[0].Context != 128000 || models[0].MaxOutput != 16384 {
		t.Errorf("OpenRouter limits were dropped: %+v", models[0])
	}
	if models[1].Context != 1048576 || models[1].MaxOutput != 393216 {
		t.Errorf("DeepSeek limits were dropped: %+v", models[1])
	}
	if models[2].Context != 2000000 || models[2].MaxOutput != 65536 {
		t.Errorf("Gemini limits were dropped: %+v", models[2])
	}
	if models[3].Context != 0 || models[3].MaxOutput != 0 {
		t.Errorf("an unstated limit must stay 0: %+v", models[3])
	}
}
