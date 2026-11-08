package agent

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/NekoLOvO/dux-code/core/internal/config"
	"github.com/NekoLOvO/dux-code/core/internal/store"
)

// Replays a real session through the compaction path, and reports how much of the folded range was
// actually read. Skipped unless DCODE_REPLAY points at a session file: it is a measurement, not a unit
// test.
func TestReplayCompactionOnARealSession(t *testing.T) {
	path := os.Getenv("DCODE_REPLAY")
	if path == "" {
		t.Skip("set DCODE_REPLAY=<session.json> to replay a real session")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var sess store.Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		t.Fatal(err)
	}
	msgs := sess.Messages

	cut := pickCut(msgs)
	if cut == 0 {
		t.Fatalf("nothing to fold in %d messages", len(msgs))
	}
	if msgs[cut].Role != "user" {
		t.Fatalf("cut %d lands on %s", cut, msgs[cut].Role)
	}

	dropped := msgs[:cut]
	window := 1048576 // a 1M-token model
	chunks, skipped := planChunks(dropped, summaryChunkLimit(config.Resolved{Context: window}))

	covered := 0
	chars := 0
	for _, c := range chunks {
		covered += len(c)
		for _, m := range c {
			chars += len(summaryText(m))
		}
	}
	t.Logf("messages: %d total, %d folded, %d kept verbatim", len(msgs), cut, len(msgs)-cut)
	t.Logf("cut lands on the current request: %q", firstRunes(msgs[cut].Text, 60))
	t.Logf("requests: %d (cap %d), material %d chars (~%d tokens)", len(chunks), maxSummaryChunks, chars, chars/4)
	t.Logf("read: %d of %d folded messages (%.1f%%), unread %d", covered, len(dropped), 100*float64(covered)/float64(len(dropped)), skipped)

	if covered+skipped != len(dropped) {
		t.Errorf("coverage math is off: %d read + %d skipped != %d folded", covered, skipped, len(dropped))
	}
	if len(chunks) == 0 {
		t.Fatal("no chunks produced")
	}
	// The oldest kept chunk must start where the skipped ones end, and chunks must not overlap.
	prevEnd := skipped
	for i, c := range chunks {
		if len(c) == 0 {
			t.Errorf("chunk %d is empty", i)
		}
		prevEnd += len(c)
	}
	if prevEnd != len(dropped) {
		t.Errorf("chunks end at %d, want %d", prevEnd, len(dropped))
	}
}

func firstRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
