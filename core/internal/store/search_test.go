package store

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s, skipped, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(skipped) != 0 {
		t.Fatalf("an empty directory must report no bad files: %v", skipped)
	}
	return s
}

func TestSearchFindsAcrossSessions(t *testing.T) {
	s := newTestStore(t)

	a, _ := s.Create("Session A")
	b, _ := s.Create("Session B")

	mustAppend(t, s, a.ID,
		Message{ID: "a1", Role: "user", Text: "show me how chroma highlights code", CreatedAt: 100},
		Message{ID: "a2", Role: "assistant", Text: "chroma is the syntax highlighter for Go", CreatedAt: 200},
	)
	mustAppend(t, s, b.ID,
		Message{ID: "b1", Role: "user", Text: "Chroma in capitals must match too", CreatedAt: 300},
	)

	hits := s.Search("chroma", 10)
	if len(hits) != 3 {
		t.Fatalf("got %d hits, want 3 (across sessions, case-insensitive)", len(hits))
	}

	// Newest first: b1 has the latest timestamp.
	if hits[0].MessageID != "b1" {
		t.Errorf("first hit is %s, want b1 (newest first)", hits[0].MessageID)
	}
	// Carries enough to navigate by.
	if hits[0].SessionID != b.ID || hits[0].SessionTitle != "Session B" {
		t.Errorf("wrong session info: %+v", hits[0])
	}
	for _, h := range hits {
		if h.MessageID == "" {
			t.Error("every hit needs a messageId, or the UI cannot jump to it")
		}
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	s := newTestStore(t)
	sess, _ := s.Create("A")
	mustAppend(t, s, sess.ID, Message{ID: "m1", Role: "user", Text: "anything", CreatedAt: 1})

	for _, q := range []string{"", "   ", "\n"} {
		if hits := s.Search(q, 10); len(hits) != 0 {
			t.Errorf("query %q must return nothing (an empty query is not match-all), got %d", q, len(hits))
		}
	}
}

func TestSearchLimit(t *testing.T) {
	s := newTestStore(t)
	sess, _ := s.Create("A")
	for i := 0; i < 10; i++ {
		mustAppend(t, s, sess.ID, Message{
			ID: "m" + string(rune('a'+i)), Role: "user", Text: "repeated keyword", CreatedAt: int64(i),
		})
	}
	if hits := s.Search("keyword", 3); len(hits) != 3 {
		t.Errorf("limit 3 returned %d hits", len(hits))
	}
	// No limit means the default, but it must not blow up.
	if hits := s.Search("keyword", 0); len(hits) != 10 {
		t.Errorf("limit 0 should use the default (all 10 here), got %d", len(hits))
	}
}

// Cutting a snippet mid-CJK produces invalid UTF-8, which Qt shows as �.
func TestSearchSnippetKeepsRunesIntact(t *testing.T) {
	s := newTestStore(t)
	sess, _ := s.Create("A")
	long := strings.Repeat("this is a rather long description of the matter, ", 8) + "the needle is here" + strings.Repeat(" and there is more content after it,", 8)
	mustAppend(t, s, sess.ID, Message{ID: "m1", Role: "user", Text: long, CreatedAt: 1})

	hits := s.Search("needle", 5)
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	snippet := hits[0].Snippet

	if !utf8.ValidString(snippet) {
		t.Fatalf("snippet is not valid UTF-8 (cut mid-rune): %q", snippet)
	}
	if strings.ContainsRune(snippet, utf8.RuneError) {
		t.Errorf("snippet contains a replacement character: %q", snippet)
	}
	if !strings.Contains(snippet, "the needle is here") {
		t.Errorf("snippet does not contain the match: %q", snippet)
	}
	// Both ends were trimmed, so both need an ellipsis.
	if !strings.HasPrefix(snippet, "…") || !strings.HasSuffix(snippet, "…") {
		t.Errorf("snippet should have an ellipsis at both ends: %q", snippet)
	}
	if strings.Contains(snippet, "\n") {
		t.Error("snippet must be collapsed to a single line")
	}
}

// Navigation relies on messageId, so it must reach JSON under exactly that key.
func TestSearchHitJSONShape(t *testing.T) {
	h := SearchHit{
		SessionID: "s1", SessionTitle: "Title", MessageID: "m1",
		Role: "user", Index: 2, Snippet: "…", CreatedAt: 9,
	}
	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, key := range []string{"sessionId", "sessionTitle", "messageId", "role", "index", "snippet", "createdAt"} {
		if !strings.Contains(string(data), `"`+key+`"`) {
			t.Errorf("JSON is missing field %q: %s", key, data)
		}
	}
}

func mustAppend(t *testing.T, s *Store, id string, msgs ...Message) {
	t.Helper()
	if err := s.Append(id, msgs...); err != nil {
		t.Fatalf("Append: %v", err)
	}
}
