package llm

import (
	"fmt"
	"net/http"
	"testing"
)

// TestOutputCeilingOf: every spelling here was taken from a provider's real answer, because a remembered
// wording is exactly how a parser ends up matching nothing in production.
func TestOutputCeilingOf(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{
			// Confirmed against api.deepseek.com in 2026-11.
			"deepseek",
			`{"error":{"message":"Invalid max_tokens value, the valid range of max_tokens is [1, 393216] (request_id: 9f1c9ac1)","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`,
			393216,
		},
		{
			"openai and openrouter",
			`{"error":{"message":"max_tokens is too large: 32768. This model supports at most 4096 completion tokens, whereas you provided 32768.","type":"invalid_request_error","param":"max_tokens","code":null}}`,
			4096,
		},
		{
			"anthropic",
			`{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: 8192 > 4096, which is the maximum allowed number of output tokens for claude-3-5-sonnet-20240620"}}`,
			4096,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			limit, ok := OutputCeilingOf(&HTTPError{Status: http.StatusBadRequest, Body: c.body})
			if !ok || limit != c.want {
				t.Errorf("OutputCeilingOf() = %d, %v; want %d, true", limit, ok, c.want)
			}
		})
	}
}

// TestOutputCeilingOfIgnoresOtherErrors: this is the dangerous half. A context overflow also arrives as a
// 400 full of token numbers, and those numbers are what the user asked for — learning one as a ceiling
// would write a wrong limit into config.json and cap every reply from then on.
func TestOutputCeilingOfIgnoresOtherErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{
			"context overflow, openai",
			http.StatusBadRequest,
			`{"error":{"message":"This model's maximum context length is 128000 tokens. However, you requested 141226 tokens (41226 in the messages, 100000 in the completion).","type":"invalid_request_error","code":"context_length_exceeded"}}`,
		},
		{
			"context overflow, anthropic",
			http.StatusBadRequest,
			`{"type":"error","error":{"type":"invalid_request_error","message":"input length and max_tokens exceed context limit: 189127 + 16000 > 200000, decrease input length or max_tokens and try again"}}`,
		},
		{
			"input overflow, gemini",
			http.StatusBadRequest,
			`{"error":{"code":400,"message":"The input token count (5911388) exceeds the maximum number of tokens allowed (1048576).","status":"INVALID_ARGUMENT"}}`,
		},
		{
			// The right numbers, but the wrong status: a 429 is about rate limits, and a limit learned
			// from it would be written to config and outlive the mistake.
			"a ceiling on a non-400",
			http.StatusTooManyRequests,
			`{"error":{"message":"max_tokens is too large: 32768. This model supports at most 4096 completion tokens, whereas you provided 32768."}}`,
		},
		{"empty body", http.StatusBadRequest, ``},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if limit, ok := OutputCeilingOf(&HTTPError{Status: c.status, Body: c.body}); ok {
				t.Errorf("a ceiling of %d was read out of an unrelated error", limit)
			}
		})
	}
}

// TestOutputCeilingOfNeedsAnHTTPError: a plain error with matching text is not a provider answer.
func TestOutputCeilingOfNeedsAnHTTPError(t *testing.T) {
	if limit, ok := OutputCeilingOf(fmt.Errorf("max_tokens is too large: 1. This model supports at most 2 completion tokens")); ok {
		t.Errorf("read a ceiling (%d) from a non-HTTP error", limit)
	}
	if _, ok := OutputCeilingOf(nil); ok {
		t.Error("a nil error has no ceiling")
	}
}
