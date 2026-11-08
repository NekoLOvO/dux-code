package store

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// SearchHit is one match inside messages.
type SearchHit struct {
	SessionID    string `json:"sessionId"`
	SessionTitle string `json:"sessionTitle"`
	// MessageID is the only thing the UI can navigate by. Index is for debugging only:
	// loading a session drops assistant messages that carry nothing but toolCalls and
	// merges consecutive tool results into one row, so the store's index does not match
	// the row number on screen.
	MessageID string `json:"messageId"`
	Role      string `json:"role"`
	Index     int    `json:"index"`
	Snippet   string `json:"snippet"`
	CreatedAt int64  `json:"createdAt"`
}

// Search looks for query in every session's messages, newest first.
//
// No index: a few hundred sessions and tens of thousands of messages scan in
// milliseconds, while an index would have to be kept in sync across Append /
// Truncate / RewriteUser / compaction for little gain. Revisit if it gets slow
// (SQLite, probably).
func (s *Store) Search(query string, limit int) []SearchHit {
	q := strings.TrimSpace(query)
	if q == "" {
		return []SearchHit{}
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	lowerQ := strings.ToLower(q)
	hits := make([]SearchHit, 0, limit)

	for _, sess := range s.sessions {
		for i, m := range sess.Messages {
			if m.Text == "" {
				continue
			}
			pos := indexFold(m.Text, q, lowerQ)
			if pos < 0 {
				continue
			}
			hits = append(hits, SearchHit{
				SessionID:    sess.ID,
				SessionTitle: sess.Title,
				MessageID:    m.ID,
				Role:         m.Role,
				Index:        i,
				Snippet:      snippetAround(m.Text, pos, len(q)),
				CreatedAt:    m.CreatedAt,
			})
		}
	}

	sort.SliceStable(hits, func(i, j int) bool {
		return hits[i].CreatedAt > hits[j].CreatedAt
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// indexFold returns the byte offset of a match, or -1.
//
// Try the exact form first, then lowercase. Folding is not length-preserving in
// general (İ → i̇), so a position found that way can be a few bytes off in the
// original — it is only used to cut a snippet, and a rune-boundary cut makes that
// harmless. Not worth a full case-insensitive matcher.
func indexFold(text, query, lowerQuery string) int {
	if i := strings.Index(text, query); i >= 0 {
		return i
	}
	if i := strings.Index(strings.ToLower(text), lowerQuery); i >= 0 {
		if i > len(text) {
			return -1
		}
		return i
	}
	return -1
}

// snippetAround takes a slice either side of the match and folds it onto one line.
//
// Cuts must land on rune boundaries: a CJK character is three bytes, and a byte
// cut leaves half a character that Qt renders as �.
func snippetAround(text string, pos, qlen int) string {
	const radius = 40

	start := pos - radius
	if start < 0 {
		start = 0
	}
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}

	end := pos + qlen + radius
	if end > len(text) {
		end = len(text)
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
	}

	s := strings.ReplaceAll(text[start:end], "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if start > 0 {
		s = "…" + s
	}
	if end < len(text) {
		s += "…"
	}
	return s
}
