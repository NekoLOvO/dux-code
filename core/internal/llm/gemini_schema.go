package llm

import (
	"encoding/json"
	"strings"
)

// geminiSchemaKeywords is the vocabulary of Gemini's Schema message. Any other JSON Schema keyword
// is rejected with a 400 naming the key, so a tool schema written for another provider, or published
// by an MCP server, cannot be forwarded as-is.
var geminiSchemaKeywords = map[string]bool{
	"type": true, "format": true, "title": true, "description": true, "nullable": true,
	"enum": true, "items": true, "properties": true, "required": true,
	"minItems": true, "maxItems": true, "minProperties": true, "maxProperties": true,
	"minimum": true, "maximum": true, "minLength": true, "maxLength": true,
	"pattern": true, "example": true, "anyOf": true, "propertyOrdering": true, "default": true,
}

// geminiParameters converts a tool schema into something Gemini accepts.
//
// params is whatever llm.Tool carries: a map for the built-in tools, a json.RawMessage for the MCP
// ones. An unreadable schema becomes the empty object schema, so the tool still has a valid shape.
func geminiParameters(params any) any {
	var decoded any
	switch v := params.(type) {
	case nil:
		return emptyObjectSchema()
	case json.RawMessage:
		if len(v) == 0 {
			return emptyObjectSchema()
		}
		if err := json.Unmarshal(v, &decoded); err != nil {
			return emptyObjectSchema()
		}
	default:
		decoded = params
	}
	return geminiSchema(decoded)
}

// geminiSchema is the recursive half; node is one schema (an object in JSON).
func geminiSchema(node any) any {
	obj, ok := node.(map[string]any)
	if !ok {
		// A schema has to be an object.
		return emptyObjectSchema()
	}

	out := make(map[string]any, len(obj))
	for key, value := range obj {
		if !geminiSchemaKeywords[key] {
			continue
		}
		switch key {
		case "properties":
			props, ok := value.(map[string]any)
			if !ok {
				continue
			}
			clean := make(map[string]any, len(props))
			for name, sub := range props {
				clean[name] = geminiSchema(sub)
			}
			out[key] = clean
		case "items":
			// Gemini has one Schema, so a tuple ("items" as a list) has no form and is left out.
			if _, tuple := value.([]any); tuple {
				continue
			}
			out[key] = geminiSchema(value)
		case "anyOf":
			list, ok := value.([]any)
			if !ok {
				continue
			}
			branches := make([]any, 0, len(list))
			for _, sub := range list {
				branches = append(branches, geminiSchema(sub))
			}
			out[key] = branches
		default:
			// Everything else holds values or name lists, not schemas, so nothing is descended into.
			out[key] = value
		}
	}

	if union, ok := out["type"].([]any); ok {
		names := make([]string, 0, len(union))
		for _, one := range union {
			name, _ := one.(string)
			if strings.EqualFold(name, "null") {
				out["nullable"] = true
				continue
			}
			if name != "" {
				names = append(names, name)
			}
		}
		switch len(names) {
		case 1:
			out["type"] = names[0]
		case 0:
			delete(out, "type")
		default:
			// One type per schema; the other types become anyOf branches.
			out["type"] = names[0]
			out["anyOf"] = appendAnyOf(out["anyOf"], names[1:])
		}
	}

	if len(out) == 0 {
		return emptyObjectSchema()
	}
	return out
}

// appendAnyOf folds extra type names into anyOf, keeping whatever anyOf was already there.
func appendAnyOf(existing any, types []string) []any {
	out, _ := existing.([]any)
	for _, t := range types {
		out = append(out, map[string]any{"type": t})
	}
	return out
}

func emptyObjectSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
