package tools

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestWebFetchLive hits a real site and is skipped by default (offline runs and CI must not depend on
// the internet).
// When needed: DUXCODE_LIVE=1 go test ./internal/tools -run Live
func TestWebFetchLive(t *testing.T) {
	if os.Getenv("DUXCODE_LIVE") != "1" {
		t.Skip("set DUXCODE_LIVE=1 to run")
	}
	out, err := (WebFetch{}).Run(context.Background(), `{"url":"https://quickshell.org/docs/v0.2.1/guide/"}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out) < 500 {
		t.Fatalf("the content is too short (%d bytes)", len(out))
	}
	if !strings.Contains(out, "Quickshell") {
		t.Errorf("no site name, maybe the wrong content was fetched: %q", out[:200])
	}
	t.Logf("length %d, starts: %q", len(out), out[:160])
}
