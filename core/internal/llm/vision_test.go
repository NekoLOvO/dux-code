package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// A 1x1 PNG: small and still a real image (for sniffing).
var tinyImage = Image{MimeType: "image/png", Data: []byte("\x89PNG\r\n\x1a\nfake")}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// OpenAI-compatible layer: content goes from a string to an image_url array, url is a data URI.
func TestOpenAIContentCarriesDataURI(t *testing.T) {
	got := mustJSON(t, chatContent("look at this", []Image{tinyImage}))
	if !strings.Contains(got, `"type":"image_url"`) {
		t.Fatalf("no image_url: %s", got)
	}
	if !strings.Contains(got, `"url":"data:image/png;base64,`) {
		t.Errorf("url should be a data URI: %s", got)
	}
	if !strings.Contains(got, `"text":"look at this"`) {
		t.Errorf("the text is missing: %s", got)
	}

	// Without images it stays a plain string: widest compatibility and no needless wrapping.
	if s, ok := chatContent("plain text", nil).(string); !ok || s != "plain text" {
		t.Errorf("without images it should still be a string")
	}
}

// Responses: input_image's image_url is a string, not an object.
func TestResponsesContentUsesInputImage(t *testing.T) {
	got := mustJSON(t, responsesContent("look", []Image{tinyImage}))
	if !strings.Contains(got, `"type":"input_image"`) {
		t.Fatalf("no input_image: %s", got)
	}
	if strings.Contains(got, `"image_url":{`) {
		t.Errorf("Responses image_url must be a plain string: %s", got)
	}
	if !strings.Contains(got, `"type":"input_text"`) {
		t.Errorf("text must be wrapped as input_text: %s", got)
	}
}

// Anthropic: an image block plus a source object, data is plain base64 (no data: prefix). Images go
// before the text.
func TestAnthropicImageBlock(t *testing.T) {
	out := anthropicMessages([]Message{{Role: RoleUser, Content: "look", Images: []Image{tinyImage}}})
	if len(out) != 1 || out[0].Role != "user" {
		t.Fatalf("unexpected message shape: %#v", out)
	}
	blocks := out[0].Content
	if len(blocks) != 2 {
		t.Fatalf("want two blocks, image + text, got %d", len(blocks))
	}
	if blocks[0].Type != "image" || blocks[1].Type != "text" {
		t.Errorf("the image must come before the text, got %s then %s", blocks[0].Type, blocks[1].Type)
	}
	if blocks[0].Source == nil || blocks[0].Source.Type != "base64" {
		t.Fatalf("unexpected source: %#v", blocks[0].Source)
	}
	if strings.HasPrefix(blocks[0].Source.Data, "data:") {
		t.Error("Claude does not accept a data: prefix")
	}
	if blocks[0].Source.MediaType != "image/png" {
		t.Errorf("unexpected media_type: %q", blocks[0].Source.MediaType)
	}
}

// Gemini: inline_data plus plain base64.
func TestGeminiInlineData(t *testing.T) {
	out := geminiContents([]Message{{Role: RoleUser, Content: "look", Images: []Image{tinyImage}}})
	if len(out) != 1 {
		t.Fatalf("wrong number of messages: %d", len(out))
	}
	parts := out[0].Parts
	if len(parts) != 2 {
		t.Fatalf("want text + inline_data, got %d", len(parts))
	}
	if parts[1].InlineData == nil {
		t.Fatal("no inline_data")
	}
	if parts[1].InlineData.MimeType != "image/png" {
		t.Errorf("unexpected mime_type: %q", parts[1].InlineData.MimeType)
	}
	if strings.HasPrefix(parts[1].InlineData.Data, "data:") {
		t.Error("Gemini does not accept a data: prefix")
	}
}

// An image-only message must not be dropped for having empty text.
func TestImageOnlyMessageSurvives(t *testing.T) {
	if got := anthropicMessages([]Message{{Role: RoleUser, Images: []Image{tinyImage}}}); len(got) != 1 {
		t.Errorf("the image-only message was dropped: %#v", got)
	}
	if got := geminiContents([]Message{{Role: RoleUser, Images: []Image{tinyImage}}}); len(got) != 1 {
		t.Errorf("the image-only message was dropped: %#v", got)
	}
}
