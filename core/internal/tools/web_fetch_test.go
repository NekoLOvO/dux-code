package tools

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// withPrivateAllowed turns on the "allow local targets" test switch.
// httptest only binds 127.0.0.1; without this the normal path is untestable.
func withPrivateAllowed(t *testing.T) {
	t.Helper()
	old := allowPrivateTargets
	allowPrivateTargets = true
	t.Cleanup(func() { allowPrivateTargets = old })
}

func TestWebFetchExtractsText(t *testing.T) {
	withPrivateAllowed(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>Title &amp; such</title>
			<style>.x{color:red}</style>
			<script>var a = "<p>must not appear</p>";</script>
			</head><body>
			<h1>Heading</h1><p>First paragraph &lt;code&gt;</p>
			<p>Second paragraph</p>
			</body></html>`))
	}))
	defer srv.Close()

	out, err := (WebFetch{}).Run(context.Background(), `{"url":"`+srv.URL+`/"}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "# Title & such") {
		t.Errorf("the title was not captured or not decoded: %q", out)
	}
	if !strings.Contains(out, "Heading") || !strings.Contains(out, "First paragraph <code>") {
		t.Errorf("the body is incomplete: %q", out)
	}
	// script and style must disappear, or the page is all noise.
	if strings.Contains(out, "must not appear") || strings.Contains(out, "color:red") {
		t.Errorf("script/style was not stripped: %q", out)
	}
	// the untrusted-content notice
	if !strings.Contains(out, "reference material only") {
		t.Errorf("missing the untrusted-content warning: %q", out)
	}
}

func TestWebFetchBlocksPrivateTargets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html><body>secret</body></html>"))
	}))
	defer srv.Close()

	_, err := (WebFetch{}).Run(context.Background(), `{"url":"`+srv.URL+`/"}`)
	if err == nil {
		t.Fatal("127.0.0.1 should be blocked")
	}
	// Compare the sentinel rather than the string: wording changes, the check must not break with it.
	if !errors.Is(err, errBlockedTarget) {
		t.Errorf("should report that the address is not allowed, got: %v", err)
	}
}

func TestWebFetchRejectsNonHTTP(t *testing.T) {
	for _, u := range []string{`file:///etc/passwd`, `ftp://example.com/x`} {
		if _, err := (WebFetch{}).Run(context.Background(), `{"url":"`+u+`"}`); err == nil {
			t.Errorf("%s should not be accepted", u)
		}
	}
}

func TestWebFetchMissingURL(t *testing.T) {
	if _, err := (WebFetch{}).Run(context.Background(), `{"url":"   "}`); err == nil {
		t.Fatal("an empty URL should be rejected")
	}
}

func TestWebFetchHTTPError(t *testing.T) {
	withPrivateAllowed(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("gone"))
	}))
	defer srv.Close()

	_, err := (WebFetch{}).Run(context.Background(), `{"url":"`+srv.URL+`/"}`)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("should return HTTP 404, got %v", err)
	}
}

// TestWebFetchEmptyAfterStrip: an SPA comes back as nothing but script, and the reason has to be said.
// An empty string would make the model think "this page has nothing" and start guessing.
func TestWebFetchEmptyAfterStrip(t *testing.T) {
	withPrivateAllowed(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><script src="/app.js"></script></head><body><div id="root"></div></body></html>`))
	}))
	defer srv.Close()

	_, err := (WebFetch{}).Run(context.Background(), `{"url":"`+srv.URL+`/"}`)
	if err == nil || !strings.Contains(err.Error(), "JavaScript") {
		t.Fatalf("should say it renders via JavaScript, got %v", err)
	}
}

func TestWebFetchJSONPassthrough(t *testing.T) {
	withPrivateAllowed(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	out, err := (WebFetch{}).Run(context.Background(), `{"url":"`+srv.URL+`/"}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, `{"ok":true}`) {
		t.Errorf("JSON should pass through unchanged: %q", out)
	}
}

func TestHTMLToTextCollapsesWhitespace(t *testing.T) {
	_, text := htmlToText("<div>  a   b  </div><div></div><div>c</div>")
	if strings.Contains(text, "  ") {
		t.Errorf("runs of whitespace should collapse: %q", text)
	}
	if strings.Contains(text, "\n\n\n") {
		t.Errorf("no more than two newlines in a row: %q", text)
	}
}
