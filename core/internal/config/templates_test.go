package config

import "testing"

// These two templates use the native endpoints on purpose: only the native APIs have built-in search.
// Switching back to the compatible endpoint silently makes google_search / web_search disappear.
func TestTemplatesUseNativeEndpoints(t *testing.T) {
	google, ok := TemplateByID("google")
	if !ok {
		t.Fatal("google template not found")
	}
	if google.Type != "gemini" {
		t.Errorf("google template type = %q, want gemini (compat endpoints have no google_search)", google.Type)
	}
	if !SupportsNativeSearch(google.Type) {
		t.Errorf("gemini should support native search")
	}

	openai, ok := TemplateByID("openai")
	if !ok {
		t.Fatal("openai template not found")
	}
	if openai.Type != "openai-responses" {
		t.Errorf("openai template type = %q, want openai-responses", openai.Type)
	}
	if !SupportsNativeSearch(openai.Type) {
		t.Errorf("openai-responses should support native search")
	}

	// Compatible endpoints stay as they are: no built-in search for deepseek / openrouter / custom.
	for _, id := range []string{"deepseek", "openrouter", "custom"} {
		tpl, ok := TemplateByID(id)
		if !ok {
			t.Fatalf("template %s not found", id)
		}
		if tpl.Type != "openai" {
			t.Errorf("template %s type = %q, want openai", id, tpl.Type)
		}
		if SupportsNativeSearch(tpl.Type) {
			t.Errorf("%s must not be treated as having native search", id)
		}
	}
}
