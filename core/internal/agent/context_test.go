package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/NekoLOvO/dux-code/core/internal/config"
	"github.com/NekoLOvO/dux-code/core/internal/ipc"
	"github.com/NekoLOvO/dux-code/core/internal/llm"
	"github.com/NekoLOvO/dux-code/core/internal/store"
	"github.com/NekoLOvO/dux-code/core/internal/tools"
)

func TestEstimateTokensCountsWideCharsHeavier(t *testing.T) {
	if got := estimateTokens("abcdefgh"); got != 2 {
		t.Errorf("8 ASCII characters are about 2 tokens, got %d", got)
	}
	if got := estimateTokens("\u4e2d\u6587\u5b57"); got != 3 {
		t.Errorf("3 CJK characters are about 3 tokens, got %d", got)
	}
	if got := estimateTokens(""); got != 0 {
		t.Errorf("an empty string should be 0, got %d", got)
	}
}

// Cutting between a tool pair is a 400 — the easiest place for compaction to break.
func TestPickCutOnlyLandsOnUserMessages(t *testing.T) {
	hist := []store.Message{
		{ID: "u1", Role: "user", Text: strings.Repeat("long text ", 50)},
		{ID: "a1", Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "t1", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"a"}`}}}},
		{ID: "t1", Role: "tool", ToolCallID: "t1", Name: "read_file", Text: strings.Repeat("x", 4000)},
		{ID: "a2", Role: "assistant", Text: "done looking"},
		{ID: "u2", Role: "user", Text: "change that too"},
		{ID: "a3", Role: "assistant", Text: "ok"},
		{ID: "u3", Role: "user", Text: "thanks"},
	}

	for keep := 1; keep < 3000; keep += 97 {
		cut := pickCut(hist, keep)
		if cut == 0 {
			continue // not compacting is also a valid outcome
		}
		if hist[cut].Role != "user" {
			t.Fatalf("keep=%d would cut at %q (role=%s), separating tool_calls from tool_result", keep, hist[cut].ID, hist[cut].Role)
		}
	}
}

func TestPickCutSkipsWhenEverythingFits(t *testing.T) {
	hist := []store.Message{
		{ID: "u1", Role: "user", Text: "hi"},
		{ID: "a1", Role: "assistant", Text: "hello"},
	}
	if got := pickCut(hist, 100000); got != 0 {
		t.Errorf("nothing should be cut when it all fits, got %d", got)
	}
}

func TestPickCutReturnsZeroWhenNothingWouldRemain(t *testing.T) {
	// A single very long user message: cutting leaves nothing, so better not to compact.
	hist := []store.Message{{ID: "u1", Role: "user", Text: strings.Repeat("long ", 2000)}}
	if got := pickCut(hist, 10); got != 0 {
		t.Errorf("should return 0 when nothing would remain after the cut, got %d", got)
	}
}

// Compaction only changes the outgoing copy; the stored history must keep every message.
func TestHistoryMessagesReplacesPrefixButKeepsStore(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s, _, err := store.Open()
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	sess, err := s.Create("t")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	msgs := []store.Message{
		{ID: "u1", Role: "user", Text: "an early question"},
		{ID: "a1", Role: "assistant", Text: "an early answer"},
		{ID: "u2", Role: "user", Text: "a new question"},
		{ID: "a2", Role: "assistant", Text: "a new answer"},
	}
	if err := s.Append(sess.ID, msgs...); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := s.SetCompaction(sess.ID, &store.Compaction{
		UpToID: "u2", Summary: "the user started with an early question", Count: 2,
	}); err != nil {
		t.Fatalf("SetCompaction: %v", err)
	}

	r := &Runner{store: s}
	got := r.historyMessages(sess.ID)

	if len(got) != 3 {
		t.Fatalf("want summary + 2 messages = 3, got %d: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Content, "the user started with an early question") {
		t.Errorf("the first message should be the summary, got %q", got[0].Content)
	}
	if got[1].Content != "a new question" || got[2].Content != "a new answer" {
		t.Errorf("messages after the cut are wrong: %+v", got[1:])
	}

	// The persisted history must not be touched.
	if full := s.History(sess.ID); len(full) != 4 {
		t.Errorf("the stored history was mutated: %d messages", len(full))
	}
}

func TestHistoryMessagesFallsBackWhenCutPointMissing(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s, _, err := store.Open()
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	sess, _ := s.Create("t")
	_ = s.Append(sess.ID,
		store.Message{ID: "u1", Role: "user", Text: "a"},
		store.Message{ID: "a1", Role: "assistant", Text: "b"},
	)
	// The cut point names a message that no longer exists (the session was truncated).
	_ = s.SetCompaction(sess.ID, &store.Compaction{UpToID: "gone", Summary: "x", Count: 1})

	r := &Runner{store: s}
	got := r.historyMessages(sess.ID)
	if len(got) != 2 {
		t.Fatalf("with no cut point the full history should be sent, got %d messages", len(got))
	}
}

// summarizer answers the compaction request with a fixed handover note.
type summarizer struct{ calls int }

func (s *summarizer) Stream(_ context.Context, _ llm.Request, _ llm.DeltaFunc) (llm.Result, error) {
	s.calls++
	return llm.Result{Content: "the user asked for a thing and half of it is done"}, nil
}

// The UI draws the boundary from the event alone: it needs the cut point to know which row the
// summary takes over from, and the summary itself — fetching them with a session.get would reload
// the transcript under a running turn.
func TestCompactionEventNamesTheCutPointAndCarriesTheSummary(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s, _, err := store.Open()
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	sess, err := s.Create("t")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Four turns of padding: enough to cross a 4000-token threshold, and the cut lands on u3.
	big := strings.Repeat("some earlier thinking ", 500)
	if err := s.Append(sess.ID,
		store.Message{ID: "u1", Role: "user", Text: big},
		store.Message{ID: "a1", Role: "assistant", Text: big},
		store.Message{ID: "u2", Role: "user", Text: big},
		store.Message{ID: "a2", Role: "assistant", Text: big},
		store.Message{ID: "u3", Role: "user", Text: "and now the last question"},
		store.Message{ID: "a3", Role: "assistant", Text: "the last answer"},
	); err != nil {
		t.Fatalf("Append: %v", err)
	}

	var events []map[string]any
	r := New(ipc.NewEventBus(), s, quietConfig(0))
	r.OnEvent = func(ev map[string]any) { events = append(events, ev) }

	cfg := quietConfig(0)
	cfg.CompactAt = 4000
	rn := r.newRun(sess.ID, config.Resolved{ID: "fake:m", Model: "m"}, tools.Env{}, "", false)
	r.maybeCompact(context.Background(), rn, &summarizer{}, config.Resolved{ID: "fake:m", Model: "m"}, cfg)

	compacted := onlyKind(events, "context.compacted")
	if len(compacted) != 1 {
		t.Fatalf("want one compaction, got %v", kindsOf(events))
	}
	ev := compacted[0]
	if ev["summary"] != "the user asked for a thing and half of it is done" {
		t.Errorf("the event must carry the summary the model will actually be given, got %q", ev["summary"])
	}
	record := s.Compaction(sess.ID)
	if record == nil {
		t.Fatal("no record was saved")
	}
	if ev["upToId"] != record.UpToID || ev["upToId"] == "" {
		t.Errorf("the event's cut point %q must match the saved one %q", ev["upToId"], record.UpToID)
	}
	if ev["count"] != record.Count {
		t.Errorf("the event says %v messages, the record %d", ev["count"], record.Count)
	}
}

// padded is a body of roughly n tokens: estimateTokens counts ASCII at four characters each.
func padded(tokens int) string { return strings.Repeat("x", tokens*4) }

// appendTurns writes n user/assistant pairs of the given size.
func appendTurns(t *testing.T, s *store.Store, id string, n, tokens int, tag string) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := s.Append(id,
			store.Message{ID: fmt.Sprintf("%s-u%d", tag, i), Role: "user", Text: padded(tokens)},
			store.Message{ID: fmt.Sprintf("%s-a%d", tag, i), Role: "assistant", Text: padded(tokens)},
		); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
}

// The threshold is about the request, not the transcript. The transcript only grows, so measuring it
// means every later turn reports "too big" and pays for a summary identical to the last one — which
// is what this test exists to prevent from coming back.
func TestCompactionDoesNotRepeatWhileTheRequestStillFits(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s, _, err := store.Open()
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	sess, _ := s.Create("t")
	r := New(ipc.NewEventBus(), s, quietConfig(0))

	cfg := quietConfig(0)
	cfg.CompactAt = 4000
	model := config.Resolved{ID: "fake:m", Model: "m"}
	sum := &summarizer{}

	// 6 x ~1004 tokens = ~6000, over a 4000 threshold.
	appendTurns(t, s, sess.ID, 3, 1000, "a")

	rn := r.newRun(sess.ID, model, tools.Env{}, "", false)
	r.maybeCompact(context.Background(), rn, sum, model, cfg)
	if sum.calls != 1 {
		t.Fatalf("the first over-threshold turn should compact once, got %d calls", sum.calls)
	}
	first := s.Compaction(sess.ID)
	if first == nil {
		t.Fatal("no compaction was saved")
	}

	// Three more turns with nothing new. The outgoing copy is the summary plus the kept tail, which
	// fits, so there is nothing to do and nothing to pay for.
	for turn := 0; turn < 3; turn++ {
		rn := r.newRun(sess.ID, model, tools.Env{}, "", false)
		r.maybeCompact(context.Background(), rn, sum, model, cfg)
	}
	if sum.calls != 1 {
		t.Errorf("a fitting request must not be re-summarized, got %d calls", sum.calls)
	}
	if now := s.Compaction(sess.ID); now == nil || now.UpToID != first.UpToID || now.Count != first.Count {
		t.Errorf("the record changed without anything to compact: %+v", now)
	}
}

// Once the kept tail grows past the threshold again, compaction does fire — and Count reports the new
// boundary, not how many messages this round added to it.
func TestCompactionFiresAgainWhenTheSentTailGrows(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s, _, err := store.Open()
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	sess, _ := s.Create("t")
	r := New(ipc.NewEventBus(), s, quietConfig(0))

	cfg := quietConfig(0)
	cfg.CompactAt = 4000
	model := config.Resolved{ID: "fake:m", Model: "m"}
	sum := &summarizer{}

	appendTurns(t, s, sess.ID, 3, 1000, "a")
	rn := r.newRun(sess.ID, model, tools.Env{}, "", false)
	r.maybeCompact(context.Background(), rn, sum, model, cfg)
	first := s.Compaction(sess.ID)
	if first == nil {
		t.Fatal("no compaction was saved")
	}

	// The kept tail was ~2000 tokens of a 4000 threshold; one more pair takes the request over it.
	appendTurns(t, s, sess.ID, 1, 1000, "b")
	rn = r.newRun(sess.ID, model, tools.Env{}, "", false)
	r.maybeCompact(context.Background(), rn, sum, model, cfg)

	if sum.calls != 2 {
		t.Fatalf("a request over the threshold should compact again, got %d calls", sum.calls)
	}
	second := s.Compaction(sess.ID)
	if second == nil || second.UpToID == first.UpToID {
		t.Fatalf("the cut should have moved forward, was %q now %+v", first.UpToID, second)
	}
	if second.Count <= first.Count {
		t.Errorf("the count must grow with the boundary: %d then %d", first.Count, second.Count)
	}
	// The label describes the seam, so the number is the position in the transcript, not the two
	// messages that were newly dropped.
	if second.Count != len(s.History(sess.ID))-2 {
		t.Errorf("count %d should name the boundary (%d messages above it), not the delta",
			second.Count, len(s.History(sess.ID))-2)
	}
}

// A record whose cut point is gone (an older file, a hand-edit) must be treated as "nothing is
// compacted": historyMessages sends the whole history then, and the measurement has to agree or the
// two disagree about which request is being made.
func TestSentCostFallsBackWhenTheCutPointIsGone(t *testing.T) {
	hist := []store.Message{
		{ID: "u1", Role: "user", Text: padded(100)},
		{ID: "a1", Role: "assistant", Text: padded(100)},
	}
	whole := histTokens(hist)
	record := &store.Compaction{UpToID: "gone", Summary: "a summary", Count: 1}
	if got := sentCost(hist, -1, record); got != whole {
		t.Errorf("a missing cut point means the whole history is sent: got %d, want %d", got, whole)
	}
	if got := sentCost(hist, 0, record); got != whole {
		t.Errorf("a cut at the very first message sends everything: got %d, want %d", got, whole)
	}
	// With a real cut, the summary takes the place of what is above it.
	if got := sentCost(hist, 1, record); got >= whole {
		t.Errorf("the summary should replace the prefix, got %d against %d", got, whole)
	}
}
