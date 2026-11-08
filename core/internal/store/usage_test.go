package store

import (
	"os"
	"path/filepath"
	"testing"
)

// The chip in the composer is "how many tokens has this conversation spent", so the total has to be
// the sum of what the provider reported for every turn — not the last turn, and not an estimate.
func TestAppendAddsTurnUsageToTotal(t *testing.T) {
	s := newTestStore(t)
	sess, err := s.Create("t")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	mustAppend(
		t, s, sess.ID,
		Message{ID: "u1", Role: "user", Text: "one"},
		Message{ID: "a1", Role: "assistant", Text: "answer one", Usage: &Usage{Input: 30000, Output: 1000}},
		Message{ID: "u2", Role: "user", Text: "two"},
		Message{ID: "a2", Role: "assistant", Text: "answer two", Usage: &Usage{Input: 20000, Output: 500}},
	)

	if got := s.TotalUsage(sess.ID); got.Input != 50000 || got.Output != 1500 {
		t.Errorf("total = %+v, want 50000/1500 (both turns added, not the last one)", got)
	}
}

// A user message, a tool result and a turn that reported nothing must not move the total.
func TestUsageLessMessagesLeaveTheTotalAlone(t *testing.T) {
	s := newTestStore(t)
	sess, _ := s.Create("t")

	mustAppend(
		t, s, sess.ID,
		Message{ID: "u1", Role: "user", Text: "hi"},
		Message{ID: "t1", Role: "tool", Text: "output"},
	)
	if got := s.TotalUsage(sess.ID); got.Input != 0 || got.Output != 0 {
		t.Errorf("total = %+v, want zero", got)
	}
	if got, _ := s.Get(sess.ID); got.TotalUsage != nil {
		t.Errorf("an empty total should stay nil, got %+v", got.TotalUsage)
	}
}

// Editing or truncating a conversation drops messages. The tokens were still billed, so the number the
// user reads must not go down — that is the whole reason it is stored instead of summed on demand.
func TestTotalSurvivesTruncation(t *testing.T) {
	s := newTestStore(t)
	sess := newSessionWithTurns(t, s)
	mustAppend(t, s, sess.ID, Message{
		ID: "a2", Role: "assistant", Text: "answer", Usage: &Usage{Input: 9000, Output: 400},
	})
	before := s.TotalUsage(sess.ID)

	if err := s.RewriteUser(sess.ID, "u2", "rewritten"); err != nil {
		t.Fatalf("RewriteUser: %v", err)
	}
	if after := s.TotalUsage(sess.ID); after != before {
		t.Errorf("total changed from %+v to %+v after an edit", before, after)
	}
	if after := s.TotalUsage(sess.ID); after.Input != 9000 || after.Output != 400 {
		t.Errorf("total = %+v, want the 9000/400 that was spent", after)
	}
}

// A summary request and a title request spend tokens without producing a message. Leaving them out
// would make the total quietly too small.
func TestSpendCountsRequestsThatLeaveNoMessage(t *testing.T) {
	s := newTestStore(t)
	sess, _ := s.Create("t")

	if err := s.Spend(sess.ID, 4000, 900); err != nil {
		t.Fatalf("Spend: %v", err)
	}
	if err := s.Spend(sess.ID, 0, 0); err != nil {
		t.Fatalf("Spend with nothing to add: %v", err)
	}
	if got := s.TotalUsage(sess.ID); got.Input != 4000 || got.Output != 900 {
		t.Errorf("total = %+v, want 4000/900", got)
	}
	if err := s.Spend("missing", 1, 1); err == nil {
		t.Error("spending on an unknown session should fail")
	}
}

// Branching copies the messages, so the copy carries their Usage. That money was spent in the session
// that made the calls; the branch starts at zero and must keep reading zero after a reload.
func TestBranchStartsAtZeroAndStaysThere(t *testing.T) {
	s := newTestStore(t)
	sess := newSessionWithTurns(t, s)
	mustAppend(t, s, sess.ID, Message{
		ID: "a2", Role: "assistant", Text: "answer", Usage: &Usage{Input: 7777, Output: 111},
	})

	branch, err := s.Branch(sess.ID, "a2", "")
	if err != nil {
		t.Fatalf("Branch: %v", err)
	}
	if got := s.TotalUsage(branch.ID); got.Input != 0 || got.Output != 0 {
		t.Errorf("branch total = %+v, want zero", got)
	}

	// Reload from disk: a nil total would be re-derived from the copied messages' Usage.
	reopened, _, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := reopened.TotalUsage(branch.ID); got.Input != 0 || got.Output != 0 {
		t.Errorf("after a reload the branch total = %+v, want zero", got)
	}
	if got := reopened.TotalUsage(sess.ID); got.Input != 7777 || got.Output != 111 {
		t.Errorf("the source session lost its total: %+v", got)
	}
}

// Files written before totalUsage existed still carry every turn's count in the messages, so the
// total is recoverable instead of showing nothing until the next turn.
func TestOpenRecoversTotalFromOlderFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	sessions := filepath.Join(dir, "dcode", "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	legacy := `{
  "id": "old",
  "title": "old chat",
  "createdAt": 1,
  "updatedAt": 2,
  "messages": [
    {"id": "u1", "role": "user", "text": "hi"},
    {"id": "a1", "role": "assistant", "text": "hello", "usage": {"input": 1200, "output": 300}},
    {"id": "u2", "role": "user", "text": "more"},
    {"id": "a2", "role": "assistant", "text": "sure", "usage": {"input": 800, "output": 100}}
  ]
}`
	if err := os.WriteFile(filepath.Join(sessions, "old.json"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s, skipped, err := Open()
	if err != nil || len(skipped) != 0 {
		t.Fatalf("Open: %v (skipped %v)", err, skipped)
	}
	if got := s.TotalUsage("old"); got.Input != 2000 || got.Output != 400 {
		t.Errorf("recovered total = %+v, want 2000/400", got)
	}
}
