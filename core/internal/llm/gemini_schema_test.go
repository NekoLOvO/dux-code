package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// The schema that made this necessary, cut down from GitHub's list_tags: an MCP server marks the
// argument that travels as an HTTP header with an "x-" extension, and Gemini answers the whole
// request with 400 "Unknown name "x-mcp-header" ... Cannot find field". Nothing about the message or
// the model; the tool list alone kills the turn.
func TestGeminiParametersDropsExtensionKeys(t *testing.T) {
	in := json.RawMessage(`{
		"type": "object",
		"properties": {
			"owner": {"type": "string", "description": "Repository owner", "x-mcp-header": "owner"},
			"repo":  {"type": "string", "description": "Repository name", "x-mcp-header": "repo"},
			"page":  {"type": "number", "minimum": 1}
		},
		"required": ["owner", "repo"]
	}`)

	out, err := json.Marshal(geminiParameters(in))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)
	if strings.Contains(got, "x-mcp-header") {
		t.Errorf("an extension key survived: %s", got)
	}
	// The arguments themselves must all still be there: dropping the key is the fix, dropping the
	// property is a different bug that leaves the model with a tool it cannot call.
	for _, want := range []string{`"owner"`, `"repo"`, `"page"`, `"required"`, `"minimum"`} {
		if !strings.Contains(got, want) {
			t.Errorf("%s was dropped with the extension: %s", want, got)
		}
	}
}

// additionalProperties is a real JSON Schema keyword and has no counterpart in Gemini's Schema
// message, so it earns the same 400.
func TestGeminiParametersDropsAdditionalProperties(t *testing.T) {
	in := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"labels": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		},
	}
	got := mustJSON(t, geminiParameters(in))
	if strings.Contains(got, "additionalProperties") {
		t.Errorf("additionalProperties survived: %s", got)
	}
	if !strings.Contains(got, `"labels"`) {
		t.Errorf("the property holding it was dropped too: %s", got)
	}
}

// A JSON Schema type union is read as an invalid enum value, not as a list, so it has to become one
// type plus nullable.
func TestGeminiParametersFoldsTypeUnion(t *testing.T) {
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"maybe":  map[string]any{"type": []any{"string", "null"}},
			"either": map[string]any{"type": []any{"string", "number"}},
		},
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(mustJSON(t, geminiParameters(in))), &out); err != nil {
		t.Fatal(err)
	}
	props := out["properties"].(map[string]any)

	maybe := props["maybe"].(map[string]any)
	if maybe["type"] != "string" || maybe["nullable"] != true {
		t.Errorf("string|null should become type=string nullable=true, got %#v", maybe)
	}

	// Two real types cannot both be the type: keep one and express the other with anyOf, which is
	// the one way Gemini can say "either".
	either := props["either"].(map[string]any)
	if either["type"] != "string" {
		t.Errorf("type = %#v, want the first name", either["type"])
	}
	if _, ok := either["anyOf"]; !ok {
		t.Errorf("the second type was lost: %#v", either)
	}
}

// A tuple ("items" as a list) has no equivalent, so it is dropped rather than sent as nonsense.
func TestGeminiParametersDropsTupleItems(t *testing.T) {
	in := map[string]any{
		"type":  "array",
		"items": []any{map[string]any{"type": "string"}, map[string]any{"type": "number"}},
	}
	got := mustJSON(t, geminiParameters(in))
	if strings.Contains(got, "items") {
		t.Errorf("a tuple items survived: %s", got)
	}
}

// enum / default / example / required hold values, not schemas. A filter that descends into them
// eats the values ("LEFT" is not a keyword) and the enum arrives empty.
func TestGeminiParametersKeepsLiteralValues(t *testing.T) {
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"side": map[string]any{
				"type":    "string",
				"enum":    []any{"LEFT", "RIGHT"},
				"default": "RIGHT",
				"example": "LEFT",
			},
		},
		"required": []any{"side"},
	}
	got := mustJSON(t, geminiParameters(in))
	for _, want := range []string{`"LEFT"`, `"RIGHT"`, `"default":"RIGHT"`, `"required":["side"]`} {
		if !strings.Contains(got, want) {
			t.Errorf("%s was lost: %s", want, got)
		}
	}
}

// Anything unreadable becomes the empty object schema: a tool with no arguments is usable, while a
// broken schema fails the request and says nothing about which tool did it.
func TestGeminiParametersFallsBackToEmptyObject(t *testing.T) {
	for _, in := range []any{nil, json.RawMessage(nil), json.RawMessage(`not json`), "a string", 42} {
		got := mustJSON(t, geminiParameters(in))
		if got != `{"properties":{},"type":"object"}` {
			t.Errorf("geminiParameters(%#v) = %s, want the empty object schema", in, got)
		}
	}
}

// Gemini's own vocabulary is passed through untouched: this is a filter, not a rewriter.
func TestGeminiParametersKeepsNestedObjectSchema(t *testing.T) {
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"filter": map[string]any{
				"type":        "object",
				"description": "narrow it down",
				"properties": map[string]any{
					"from": map[string]any{"type": "string", "format": "date"},
				},
				"required":      []any{"from"},
				"minProperties": 1,
			},
		},
	}
	got := mustJSON(t, geminiParameters(in))
	for _, want := range []string{`"format":"date"`, `"required":["from"]`, `"minProperties":1`} {
		if !strings.Contains(got, want) {
			t.Errorf("%s was lost: %s", want, got)
		}
	}
}
