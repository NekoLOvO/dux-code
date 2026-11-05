// Package store is the session persistence layer.
//
// One rule carried over from the rest of the project: only finished content is
// persisted. Streaming output stays in memory and is written on done / cancelled, so a
// crash never leaves half a message.
//
// One JSON file per session instead of SQLite: hundreds of sessions, not millions, and
// the files can be read in an editor and diffed in git.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/NekoLOvO/dux-code/core/internal/llm"
	"sort"
	"strings"
	"sync"
	"time"
)

// Message is one completed message.
type Message struct {
	ID        string `json:"id"`
	Role      string `json:"role"` // user | assistant
	Text      string `json:"text"`
	Model     string `json:"model,omitempty"`
	CreatedAt int64  `json:"createdAt"` // Unix milliseconds
	ElapsedMs int64  `json:"elapsedMs,omitempty"`
	// Attachments are files the user attached (absolute paths). Recorded only; reading
	// the content needs the tools layer.
	Attachments []string `json:"attachments,omitempty"`
	// Tools: an assistant message asking for calls carries ToolCalls (content may be
	// empty); results come back as Role == "tool" with ToolCallID, and Name is only for
	// display.
	ToolCalls  []llm.ToolCall `json:"toolCalls,omitempty"`
	ToolCallID string         `json:"toolCallId,omitempty"`
	Name       string         `json:"name,omitempty"`
	// ToolArgs is the raw arguments of this call (JSON). It rides on the result message
	// too, so a reopened session can show what the tool was asked instead of only which
	// tool ran.
	ToolArgs string `json:"toolArgs,omitempty"`
	// Usage is this turn's total across tool steps: what the turn cost.
	Usage *Usage `json:"usage,omitempty"`
	// LastUsage is what the API reported for the last request, verbatim. Its input is the
	// current context size, so it really drops after compaction; this is the pair the UI
	// shows, not the running total.
	LastUsage *Usage `json:"lastUsage,omitempty"`
	// Reasoning is this message's thinking. Model-generated like the answer, just not part
	// of the body; storing it lets a reopened session show it again.
	Reasoning string `json:"reasoning,omitempty"`
	// Thinking is the returnable form of that reasoning (text + signature). Only Anthropic
	// needs it: with thinking on and a tool call in between, the next request has to send
	// the thinking block back verbatim. Not for humans — it keeps the tool loop going.
	Thinking []llm.ThinkingBlock `json:"thinking,omitempty"`
	// Truncated means the output limit cut this message off (finish_reason == length). It
	// is almost never a complete answer, so the UI must say so rather than let it pass as
	// finished.
	Truncated bool `json:"truncated,omitempty"`
}

// Usage is token usage.
type Usage struct {
	Input  int `json:"input"`
	Output int `json:"output"`
}

// Session is a conversation.
type Session struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	Pinned    bool   `json:"pinned,omitempty"`
	// WorkingDirectory is this session's working directory: the cwd the model is told about
	// and the one the tools use.
	WorkingDirectory string `json:"workingDirectory,omitempty"`
	// SmartNamed means auto-naming already ran; it does not run again.
	SmartNamed bool `json:"smartNamed,omitempty"`
	// Compaction records that the earlier part has been summarised. Messages themselves
	// are untouched — the history the user sees stays complete; only what goes to the
	// model is replaced by the summary plus the messages after UpToID.
	Compaction *Compaction `json:"compaction,omitempty"`
	// TotalUsage is every token this session has spent, summed over requests: the sum of
	// each message's Usage plus the side requests (compaction summary, title) and the turns
	// that failed before producing a message. It only ever grows — the provider billed it,
	// so editing, truncating or compacting the history does not refund any of it. Always
	// API-reported, never estimated.
	TotalUsage *Usage    `json:"totalUsage,omitempty"`
	Messages   []Message `json:"messages"`
}

// Compaction is one summarisation.
type Compaction struct {
	// UpToID means "send from this message on verbatim"; everything before it is replaced
	// by Summary.
	UpToID  string `json:"upToId"`
	Summary string `json:"summary"`
	// Count is how many messages Summary stands in for — an absolute position in the
	// transcript, not "how many this compaction dropped". Only one summary is ever in play
	// (a later compaction recomputes the cut against the whole history and overwrites this
	// record), so the numbers are not meant to be summed. The UI states it as the size of
	// the section above the seam.
	Count int   `json:"count"`
	At    int64 `json:"at"`
}

// Store is the in-memory index plus the file directory.
type Store struct {
	dir      string
	mu       sync.RWMutex
	sessions map[string]*Session
}

// Dir is the session directory ~/.local/share/dcode/sessions.
func Dir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "dcode", "sessions")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "dcode", "sessions")
}

// Open loads every session in the directory. Unreadable files are skipped and listed in
// skipped — one bad file must not take the daemon down.
func Open() (*Store, []error, error) {
	dir := Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create session directory: %w", err)
	}
	s := &Store{dir: dir, sessions: make(map[string]*Session)}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}

	var skipped []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			skipped = append(skipped, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		var sess Session
		if err := json.Unmarshal(data, &sess); err != nil {
			skipped = append(skipped, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		if sess.ID == "" {
			sess.ID = strings.TrimSuffix(e.Name(), ".json")
		}
		if sess.Messages == nil {
			sess.Messages = []Message{}
		}
		// Sessions written before totalUsage existed have every turn's count in the messages
		// (a message's Usage is the cost of its turn), so the total is recoverable rather than
		// lost. Anything saved from now on carries the field, and Branch pins it, so this only
		// runs for the old files.
		if sess.TotalUsage == nil {
			sess.TotalUsage = summedUsage(sess.Messages)
		}
		// Sessions written before messages carried ids have none. Editing, branching and
		// regenerating address a message by id, so fill the gaps on load; the next save
		// persists them.
		for i := range sess.Messages {
			if sess.Messages[i].ID == "" {
				sess.Messages[i].ID = NewID()
			}
		}
		s.sessions[sess.ID] = &sess
	}
	return s, skipped, nil
}

func (s *Store) pathLocked(id string) string {
	return filepath.Join(s.dir, id+".json")
}

// List returns copies of every session, most recently updated first.
func (s *Store) List() []Session {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		cp := *sess
		cp.Messages = append([]Message(nil), sess.Messages...)
		out = append(out, cp)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out
}

// Get returns a copy of one session.
func (s *Store) Get(id string) (Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	if !ok {
		return Session{}, false
	}
	cp := *sess
	cp.Messages = append([]Message(nil), sess.Messages...)
	return cp, true
}

// Create makes an empty session.
func (s *Store) Create(title string) (Session, error) {
	now := time.Now().UnixMilli()
	sess := &Session{
		ID:        NewID(),
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
		Messages:  []Message{},
	}
	if sess.Title == "" {
		sess.Title = "New chat"
	}

	s.mu.Lock()
	s.sessions[sess.ID] = sess
	s.mu.Unlock()

	if err := s.save(sess.ID); err != nil {
		return Session{}, err
	}
	cp := *sess
	return cp, nil
}

// Rename sets the title.
func (s *Store) Rename(id, title string) error {
	if strings.TrimSpace(title) == "" {
		title = "New chat"
	}
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("session %s not found", id)
	}
	sess.Title = title
	sess.UpdatedAt = time.Now().UnixMilli()
	s.mu.Unlock()
	return s.save(id)
}

// SetPinned pins or unpins.
func (s *Store) SetPinned(id string, pinned bool) error {
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("session %s not found", id)
	}
	sess.Pinned = pinned
	sess.UpdatedAt = time.Now().UnixMilli()
	s.mu.Unlock()
	return s.save(id)
}

// Delete removes one session.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	delete(s.sessions, id)
	path := s.pathLocked(id)
	s.mu.Unlock()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Append adds finished messages and persists them. A default title is replaced with the
// first user message.
//
// A message's Usage is the cost of the turn that produced it, so appending one also adds it to
// the session's running total: two independent counters would drift.
func (s *Store) Append(id string, msgs ...Message) error {
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("session %s not found", id)
	}
	sess.Messages = append(sess.Messages, msgs...)
	for _, m := range msgs {
		if m.Usage != nil {
			sess.addUsage(*m.Usage)
		}
	}
	sess.UpdatedAt = time.Now().UnixMilli()
	if shouldRetitle(sess) {
		for _, m := range msgs {
			if m.Role == "user" && strings.TrimSpace(m.Text) != "" {
				sess.Title = deriveTitle(m.Text)
				break
			}
		}
	}
	s.mu.Unlock()
	return s.save(id)
}

func shouldRetitle(sess *Session) bool {
	t := strings.TrimSpace(sess.Title)
	return t == "" || t == "New chat"
}

func deriveTitle(text string) string {
	clean := strings.Join(strings.Fields(text), " ")
	if clean == "" {
		return "New chat"
	}
	runes := []rune(clean)
	if len(runes) > 42 {
		return string(runes[:42]) + "…"
	}
	return clean
}

// History returns the message sequence to send to the LLM.
func (s *Store) History(id string) []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil
	}
	out := make([]Message, 0, len(sess.Messages))
	for _, m := range sess.Messages {
		// An assistant message asking for a tool call may have empty content and must not be
		// filtered out for that. Neither may a tool result: "ran and said nothing" is still the
		// answer to a call, and dropping it leaves the call unanswered in the request — which both
		// APIs reject, permanently, since the transcript is what is sent.
		if m.Role != "tool" && strings.TrimSpace(m.Text) == "" && len(m.ToolCalls) == 0 {
			continue
		}
		out = append(out, m)
	}
	return out
}

// Compaction returns the current record, or nil.
func (s *Store) Compaction(id string) *Compaction {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sess, ok := s.sessions[id]; ok {
		return sess.Compaction
	}
	return nil
}

// SetCompaction writes the record (nil clears it) and persists.
func (s *Store) SetCompaction(id string, c *Compaction) error {
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("session %s not found", id)
	}
	sess.Compaction = c
	sess.UpdatedAt = time.Now().UnixMilli()
	s.mu.Unlock()
	return s.save(id)
}

// TotalUsage is what this session has spent so far. An unknown session reads as zero.
func (s *Store) TotalUsage(id string) Usage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sess, ok := s.sessions[id]; ok && sess.TotalUsage != nil {
		return *sess.TotalUsage
	}
	return Usage{}
}

// Spend records tokens that no message carries: a compaction summary, a title request, or a turn
// that failed before it produced one. They are billed like anything else, so leaving them out
// would make the total quietly too small.
func (s *Store) Spend(id string, in, out int) error {
	if in == 0 && out == 0 {
		return nil
	}
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("session %s not found", id)
	}
	sess.addUsage(Usage{Input: in, Output: out})
	sess.UpdatedAt = time.Now().UnixMilli()
	s.mu.Unlock()
	return s.save(id)
}

// addUsage folds one count into the running total. Caller holds the lock.
func (sess *Session) addUsage(u Usage) {
	if u.Input == 0 && u.Output == 0 {
		return
	}
	if sess.TotalUsage == nil {
		sess.TotalUsage = &Usage{}
	}
	sess.TotalUsage.Input += u.Input
	sess.TotalUsage.Output += u.Output
}

// summedUsage recovers a session total from the messages of a file written before the field
// existed. Nil when there is nothing to recover, so a fresh session does not store a pair of zeros.
func summedUsage(msgs []Message) *Usage {
	total := Usage{}
	for _, m := range msgs {
		if m.Usage != nil {
			total.Input += m.Usage.Input
			total.Output += m.Usage.Output
		}
	}
	if total.Input == 0 && total.Output == 0 {
		return nil
	}
	return &total
}

func (s *Store) save(id string) error {
	s.mu.RLock()
	sess, ok := s.sessions[id]
	if !ok {
		s.mu.RUnlock()
		return fmt.Errorf("session %s not found", id)
	}
	// Marshal inside the lock so Append cannot race.
	data, err := json.MarshalIndent(sess, "", "  ")
	path := s.pathLocked(id)
	s.mu.RUnlock()
	if err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// NewID returns a short, human-readable id.
func NewID() string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	const n = 10
	seed := time.Now().UnixNano()
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[seed%int64(len(alphabet))]
		seed /= int64(len(alphabet))
	}
	return string(b)
}

// SetWorkingDirectory sets the session's working directory.
func (s *Store) SetWorkingDirectory(id, dir string) error {
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("session %s not found", id)
	}
	sess.WorkingDirectory = dir
	sess.UpdatedAt = time.Now().UnixMilli()
	s.mu.Unlock()
	return s.save(id)
}

// messageIndex maps a message id to its position, or -1 when no message carries it.
//
// Every caller that acts on "the message the user clicked" has to address it this way:
// the UI drops and merges rows (empty assistant messages, consecutive tool results), so
// a row index in the transcript is not an index into Messages.
func messageIndex(messages []Message, id string) int {
	for i := range messages {
		if messages[i].ID == id {
			return i
		}
	}
	return -1
}

// dropStaleCompaction forgets a summary whose cut point is no longer in the transcript.
//
// Editing or regenerating an old message drops everything after it, which can include the
// message the summary takes over from. Requesting that cannot be honoured: sending starts
// from a message that is gone. historyMessages already falls back to the whole history in
// that case, but leaving the record behind keeps a summary and a count in the file (and on
// screen) that describe a boundary that no longer exists.
func (s *Session) dropStaleCompaction() {
	if s.Compaction != nil && messageIndex(s.Messages, s.Compaction.UpToID) < 0 {
		s.Compaction = nil
	}
}

// TruncateAfter keeps messages up to and including the one with messageID and drops the
// rest. Used by regenerate.
func (s *Store) TruncateAfter(id, messageID string) error {
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("session %s not found", id)
	}
	index := messageIndex(sess.Messages, messageID)
	if index < 0 {
		s.mu.Unlock()
		return fmt.Errorf("message %s not found", messageID)
	}
	sess.Messages = sess.Messages[:index+1]
	sess.dropStaleCompaction()
	sess.UpdatedAt = time.Now().UnixMilli()
	s.mu.Unlock()
	return s.save(id)
}

// RewriteUser replaces the user message with messageID and drops everything after it.
// This is "edit and rerun": the later answers were produced from the old question and
// would only contradict.
func (s *Store) RewriteUser(id, messageID, text string) error {
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("session %s not found", id)
	}
	index := messageIndex(sess.Messages, messageID)
	if index < 0 {
		s.mu.Unlock()
		return fmt.Errorf("message %s not found", messageID)
	}
	if sess.Messages[index].Role != "user" {
		s.mu.Unlock()
		return fmt.Errorf("only user messages can be edited")
	}
	sess.Messages[index].Text = text
	sess.Messages = sess.Messages[:index+1]
	sess.dropStaleCompaction()
	sess.UpdatedAt = time.Now().UnixMilli()
	s.mu.Unlock()
	return s.save(id)
}

// Branch copies the messages up to and including the one with messageID into a new
// session. Message ids are reissued — sharing them would make per-message operations hit
// both sessions.
func (s *Store) Branch(id, messageID, title string) (Session, error) {
	s.mu.Lock()
	src, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		return Session{}, fmt.Errorf("session %s not found", id)
	}
	index := messageIndex(src.Messages, messageID)
	if index < 0 {
		s.mu.Unlock()
		return Session{}, fmt.Errorf("message %s not found", messageID)
	}

	msgs := make([]Message, index+1)
	copy(msgs, src.Messages[:index+1])
	for i := range msgs {
		msgs[i].ID = NewID()
	}

	now := time.Now().UnixMilli()
	if title == "" {
		title = strings.TrimSpace(src.Title) + " (branch)"
	}
	sess := &Session{
		ID:               NewID(),
		Title:            title,
		CreatedAt:        now,
		UpdatedAt:        now,
		WorkingDirectory: src.WorkingDirectory,
		SmartNamed:       true, // title carried over; no need to ask the model again
		Messages:         msgs,
		// The copied messages carry their own Usage, but that money was spent in the session
		// that made the calls. Zero (not nil) so the copy does not quietly pick the parent's
		// total back up when the file is read again.
		TotalUsage: &Usage{},
	}
	s.sessions[sess.ID] = sess
	s.mu.Unlock()

	if err := s.save(sess.ID); err != nil {
		return Session{}, err
	}
	return *sess, nil
}

// MarkSmartNamed records that auto-naming ran, so it is not asked again every turn.
func (s *Store) MarkSmartNamed(id string) error {
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("session %s not found", id)
	}
	sess.SmartNamed = true
	s.mu.Unlock()
	return s.save(id)
}
