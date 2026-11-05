package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// HTTPError is what a provider returns on non-200.
//
// A type rather than fmt.Errorf because callers decide whether the error is worth retrying, and
// scraping "429" out of a string eventually matches the wrong thing.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.Status, strings.TrimSpace(e.Body))
}

// The provider's own words for "your max_tokens is above what this model can write".
//
// Parsing an error is a last resort, but the alternative is worse: a limit we do not know fails the turn
// with a wall of JSON, every session, forever. The number comes from the provider itself here, so it is
// more reliable than any table — the only risk is not matching, which leaves the error exactly as it was.
//
// Each pattern must name the *output* parameter. An overflow is the other 400 of the same shape and its
// numbers are the user's request, not a ceiling; a loose pattern would silently cap every reply at the
// size of a context overflow.
var outputCeilingPatterns = []*regexp.Regexp{
	// DeepSeek: "Invalid max_tokens value, the valid range of max_tokens is [1, 393216]".
	regexp.MustCompile(`valid range of max_tokens is \[\s*\d+\s*,\s*(\d+)\s*\]`),
	// OpenAI, OpenRouter, Azure and most OpenAI-compatible servers: "max_tokens is too large: 32768.
	// This model supports at most 4096 completion tokens, whereas you provided 32768."
	regexp.MustCompile(`(?i)supports at most (\d+) (?:completion|output) tokens`),
	// Anthropic: "max_tokens: 8192 > 4096, which is the maximum allowed number of output tokens for
	// claude-3-5-sonnet-20240620".
	regexp.MustCompile(`(?i)max_tokens:\s*\d+\s*>\s*(\d+),\s*which is the maximum allowed number of output tokens`),
}

// maxPlausibleCeiling guards the parse: a number beyond this is a misread, not a model.
const maxPlausibleCeiling = 10_000_000

// OutputCeilingOf reports the model's output ceiling when a rejected request stated it, which happens
// when max_tokens was above the model's maximum.
//
// Only 400s are read. A 429 or a 500 mentioning tokens is about something else, and a limit learned from
// a misread of one would outlive the mistake — it is written to config.
func OutputCeilingOf(err error) (int, bool) {
	var he *HTTPError
	if err == nil || !errors.As(err, &he) {
		return 0, false
	}
	if he.Status != http.StatusBadRequest && he.Status != http.StatusUnprocessableEntity {
		return 0, false
	}
	for _, re := range outputCeilingPatterns {
		m := re.FindStringSubmatch(he.Body)
		if m == nil {
			continue
		}
		limit, convErr := strconv.Atoi(m[1])
		if convErr != nil || limit <= 0 || limit > maxPlausibleCeiling {
			continue
		}
		return limit, true
	}
	return 0, false
}

// Retryable reports whether a retry could succeed.
//
// Only "the other side is busy" and connection trouble: 429, 5xx, and transport errors (disconnect,
// timeout, reset). Other 4xx (bad key, bad parameters, missing model) will not become right on the
// hundredth retry and only make the user wait.
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	// The user pressed stop, or a caller cancelled: do not retry.
	if errors.Is(err, ErrCancelled) || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, ErrNoAPIKey) {
		return false
	}

	var he *HTTPError
	if errors.As(err, &he) {
		return he.Status == 429 || he.Status >= 500
	}

	// A truncated stream: the connection broke, not an HTTP response saying no.
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}

	// The provider went quiet and the watchdog dropped the connection. Nothing arrived that made it a
	// refusal, so a second attempt is the right first move.
	if errors.Is(err, ErrStreamStalled) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	return false
}
