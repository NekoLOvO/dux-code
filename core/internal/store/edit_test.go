package store

import (
	"strings"
	"testing"
)

// newSessionWithTurns gives every test the same shape: two turns, so a write aimed at the
// first one can be told apart from a write aimed at the last one.
func newSessionWithTurns(t *testing.T, s *Store) Session {
	t.Helper()
	sess, err := s.Create("Edit tests")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	mustAppend(
		t, s, sess.ID,
		Message{ID: "u1", Role: "user", Text: "first question", CreatedAt: 100},
		Message{ID: "a1", Role: "assistant", Text: "first answer", CreatedAt: 200},
		Message{ID: "u2", Role: "user", Text: "second question", CreatedAt: 300},
		Message{ID: "a2", Role: "assistant", Text: "second answer", CreatedAt: 400},
	)
	return sess
}

// Rewriting the last user message must not depend on its position: the UI drops and merges
// rows, so an index from the transcript points somewhere else.
func TestRewriteUserAddressesMessageByID(t *testing.T) {
	s := newTestStore(t)
	sess := newSessionWithTurns(t, s)

	if err := s.RewriteUser(sess.ID, "u2", "rewritten"); err != nil {
		t.Fatalf("RewriteUser: %v", err)
	}

	got, ok := s.Get(sess.ID)
	if !ok {
		t.Fatal("session disappeared")
	}
	if len(got.Messages) != 3 {
		t.Fatalf("kept %d messages, want 3 (the answer after the edited prompt is dropped)", len(got.Messages))
	}
	if got.Messages[2].ID != "u2" || got.Messages[2].Text != "rewritten" {
		t.Errorf("edited message = %+v, want u2/rewritten", got.Messages[2])
	}
	if got.Messages[1].ID != "a1" {
		t.Errorf("message before the edit changed: %+v", got.Messages[1])
	}
}

func TestRewriteUserRejectsNonUserMessage(t *testing.T) {
	s := newTestStore(t)
	sess := newSessionWithTurns(t, s)

	err := s.RewriteUser(sess.ID, "a1", "not allowed")
	if err == nil || !strings.Contains(err.Error(), "only user messages") {
		t.Fatalf("err = %v, want the only-user-messages error", err)
	}
}

func TestRewriteUserUnknownID(t *testing.T) {
	s := newTestStore(t)
	sess := newSessionWithTurns(t, s)

	err := s.RewriteUser(sess.ID, "missing", "x")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want a not-found error", err)
	}
}

func TestTruncateAfterKeepsThroughMessage(t *testing.T) {
	s := newTestStore(t)
	sess := newSessionWithTurns(t, s)

	if err := s.TruncateAfter(sess.ID, "a1"); err != nil {
		t.Fatalf("TruncateAfter: %v", err)
	}

	got, _ := s.Get(sess.ID)
	if len(got.Messages) != 2 {
		t.Fatalf("kept %d messages, want 2", len(got.Messages))
	}
	if got.Messages[1].ID != "a1" {
		t.Errorf("last kept message = %s, want a1", got.Messages[1].ID)
	}
}

func TestBranchCopiesThroughMessage(t *testing.T) {
	s := newTestStore(t)
	sess := newSessionWithTurns(t, s)

	src, ok := s.Get(sess.ID)
	if !ok {
		t.Fatal("source session disappeared")
	}

	branch, err := s.Branch(sess.ID, "u2", "")
	if err != nil {
		t.Fatalf("Branch: %v", err)
	}
	if len(branch.Messages) != 3 {
		t.Fatalf("branch holds %d messages, want 3", len(branch.Messages))
	}
	if branch.Messages[2].Text != "second question" {
		t.Errorf("last branch message = %+v", branch.Messages[2])
	}
	// Ids are reissued so the two sessions never share a message identity.
	for i := range branch.Messages {
		if branch.Messages[i].ID == "" {
			t.Fatalf("branch message %d has no id", i)
		}
		if branch.Messages[i].ID == src.Messages[i].ID {
			t.Errorf("branch kept the original id %s", branch.Messages[i].ID)
		}
	}
}

// Sessions written before messages carried ids get one on load; without it nothing in the
// transcript can be edited or branched.
func TestOpenFillsMissingMessageIDs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	s, _, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	sess, err := s.Create("Legacy")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	mustAppend(t, s, sess.ID, Message{Role: "user", Text: "no id here", CreatedAt: 1})

	// Reopen the same directory: the message on disk has no id.
	reopened, _, err := Open()
	if err != nil {
		t.Fatalf("Open again: %v", err)
	}
	got, ok := reopened.Get(sess.ID)
	if !ok {
		t.Fatal("session not reloaded")
	}
	if got.Messages[0].ID == "" {
		t.Fatal("message id was not filled in on load")
	}
}
