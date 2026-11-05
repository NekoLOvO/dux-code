package store

import "testing"

// A summary describes "everything before this message is replaced". Once that message is gone the
// sentence has no meaning, and the record would go on naming a boundary that no longer exists —
// including the count the UI shows next to a transcript it does not describe.
func TestCompactionIsDroppedWhenItsCutPointIsTruncated(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s, _, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	sess := newSessionWithTurns(t, s)
	if err := s.SetCompaction(sess.ID, &Compaction{UpToID: "u2", Summary: "so far", Count: 2}); err != nil {
		t.Fatalf("SetCompaction: %v", err)
	}

	// Regenerating the first answer keeps u1 and a1, which is before the cut point.
	if err := s.TruncateAfter(sess.ID, "a1"); err != nil {
		t.Fatalf("TruncateAfter: %v", err)
	}

	got, _ := s.Get(sess.ID)
	if got.Compaction != nil {
		t.Errorf("a compaction whose cut point was truncated must be forgotten, got %+v", got.Compaction)
	}
	// The disk copy has to agree, or a restart brings the dead summary back.
	again, _, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if reloaded, ok := again.Get(sess.ID); !ok || reloaded.Compaction != nil {
		t.Errorf("the dropped record came back on reload: %+v", reloaded.Compaction)
	}
}

// The same for editing: rewriting an earlier prompt drops the rest of the transcript, which can
// take the cut point with it.
func TestCompactionIsDroppedWhenEditingPastItsCutPoint(t *testing.T) {
	s := newTestStore(t)
	sess := newSessionWithTurns(t, s)
	if err := s.SetCompaction(sess.ID, &Compaction{UpToID: "u2", Summary: "so far", Count: 2}); err != nil {
		t.Fatalf("SetCompaction: %v", err)
	}

	if err := s.RewriteUser(sess.ID, "u1", "different question"); err != nil {
		t.Fatalf("RewriteUser: %v", err)
	}

	got, _ := s.Get(sess.ID)
	if got.Compaction != nil {
		t.Errorf("editing before the cut point must forget the summary, got %+v", got.Compaction)
	}
}

// The record must survive an edit that leaves its cut point in place: that is the ordinary case
// (edit the last prompt of a compacted session), and forgetting the summary there would resend
// history the model has already been told to drop.
func TestCompactionSurvivesAnEditThatKeepsItsCutPoint(t *testing.T) {
	s := newTestStore(t)
	sess := newSessionWithTurns(t, s)
	if err := s.SetCompaction(sess.ID, &Compaction{UpToID: "u2", Summary: "so far", Count: 2}); err != nil {
		t.Fatalf("SetCompaction: %v", err)
	}

	if err := s.RewriteUser(sess.ID, "u2", "second question, reworded"); err != nil {
		t.Fatalf("RewriteUser: %v", err)
	}

	got, _ := s.Get(sess.ID)
	if got.Compaction == nil || got.Compaction.UpToID != "u2" {
		t.Errorf("the summary still applies and must be kept, got %+v", got.Compaction)
	}
}
