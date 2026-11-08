package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

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

// Cutting between a tool pair is a 400, so pickCut returns the current turn's own message and folds
// everything before it.
func TestPickCutLandsOnTheCurrentMessage(t *testing.T) {
	hist := []store.Message{
		{ID: "u1", Role: "user", Text: "first"},
		{ID: "a1", Role: "assistant", Text: "answer"},
		{ID: "u2", Role: "user", Text: "second"},
		{ID: "a2", Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "t1", Function: llm.FunctionCall{Name: "read_file"}}}},
		{ID: "t1", Role: "tool", ToolCallID: "t1", Name: "read_file", Text: "contents"},
		{ID: "a3", Role: "assistant", Text: "done"},
		{ID: "u3", Role: "user", Text: "the message that triggered this"},
	}
	if got := pickCut(hist); got != 6 {
		t.Errorf("cut = %d, want 6 (the last user message); everything before it is what gets folded", got)
	}
	if hist[pickCut(hist)].Role != "user" {
		t.Error("the cut must land on a user message, or tool_calls are separated from their results")
	}
}

// A tool loop is compacted at the start of a turn, so nothing of the current turn is ever dropped: the
// kept tail is the current turn's own messages, which keeps the calls and their results paired.
func TestPickCutKeepsTheCurrentTurnWhole(t *testing.T) {
	hist := []store.Message{
		{ID: "u1", Role: "user", Text: "old"},
		{ID: "a1", Role: "assistant", Text: "old answer"},
		{ID: "u2", Role: "user", Text: "current turn"},
		{ID: "a2", Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "t1", Function: llm.FunctionCall{Name: "bash"}}}},
		{ID: "t1", Role: "tool", ToolCallID: "t1", Name: "bash", Text: "output"},
	}
	cut := pickCut(hist)
	if cut != 2 {
		t.Fatalf("cut = %d, want 2", cut)
	}
	// Nothing between the cut and the end may be an unpaired tool result.
	if hist[cut].Role != "user" {
		t.Errorf("the kept tail starts with %q, so a tool result lost its call", hist[cut].Role)
	}
}

// One turn and nothing before it: no boundary to cut at, so no compaction.
func TestPickCutReturnsZeroForASingleTurn(t *testing.T) {
	if got := pickCut([]store.Message{{ID: "u1", Role: "user", Text: strings.Repeat("long ", 2000)}}); got != 0 {
		t.Errorf("a single message has nothing to fold, got %d", got)
	}
	if got := pickCut(nil); got != 0 {
		t.Errorf("empty history, got %d", got)
	}
	// The first message is the only user message: still nothing to fold.
	twoFromStart := []store.Message{
		{ID: "u1", Role: "user", Text: "only request"},
		{ID: "a1", Role: "assistant", Text: "answer"},
	}
	if got := pickCut(twoFromStart); got != 0 {
		t.Errorf("nothing precedes the only user message, got %d", got)
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
	_ = s.Append(
		sess.ID,
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

// summarizer answers the compaction request with a fixed handover note, and keeps the requests it
// was given so a test can say what the summarizer was actually asked for.
type summarizer struct {
	calls    int
	requests []llm.Request
}

func (s *summarizer) Stream(_ context.Context, req llm.Request, _ llm.DeltaFunc) (llm.Result, error) {
	s.calls++
	s.requests = append(s.requests, req)
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
	if err := s.Append(
		sess.ID,
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
		if err := s.Append(
			id,
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

// A reasoning model that spends the whole budget thinking returns no content. That is a retryable
// outcome: without the second attempt the turn silently does not compact, which looks exactly like
// compaction being broken.
func TestSummarizeRetriesAnEmptyAnswer(t *testing.T) {
	var summarizer emptyThenText
	r, sid, _ := newTestRunner(t, nil)
	mustAppendAgent(t, r.store, sid, store.Message{ID: "m1", Role: "user", Text: "hello", CreatedAt: 1})

	out, err := r.summarize(context.Background(), &summarizer,
		config.Resolved{ID: "fake:m", Model: "m"}, sid,
		[]store.Message{{ID: "m1", Role: "user", Text: "hello"}}, nil)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if out != "a real summary" {
		t.Fatalf("got %q", out)
	}
	if summarizer.calls != 2 {
		t.Fatalf("expected 2 attempts, got %d", summarizer.calls)
	}
}

// emptyThenText answers with nothing first (a reasoning model out of budget) and text after.
type emptyThenText struct{ calls int }

func (e *emptyThenText) Stream(_ context.Context, _ llm.Request, onDelta llm.DeltaFunc) (llm.Result, error) {
	e.calls++
	if e.calls == 1 {
		// The shape of the failure: tokens spent, reasoning produced, no content.
		return llm.Result{Reasoning: "thinking...", StopReason: "length"}, nil
	}
	onDelta("a real summary")
	return llm.Result{Content: "a real summary", StopReason: "stop"}, nil
}

// recorder keeps every summarization prompt, so a test can assert what the summarizer was actually
// shown rather than what it produced.
type recorder struct {
	prompts []string
	reply   string
}

func (rec *recorder) Stream(_ context.Context, req llm.Request, _ llm.DeltaFunc) (llm.Result, error) {
	if len(req.Messages) > 0 {
		rec.prompts = append(rec.prompts, req.Messages[0].Content)
	}
	out := rec.reply
	if out == "" {
		out = "a summary"
	}
	return llm.Result{Content: out}, nil
}

// Every folded message has to reach the summarizer exactly once, across however many requests it
// takes.
func TestSummarizeReadsTheWholeFoldedRange(t *testing.T) {
	r, sid, _ := newTestRunner(t, nil)

	dropped := make([]store.Message, 0, 120)
	for i := 0; i < 120; i++ {
		// A unique marker per message, padded so the chunk limit splits them into several requests.
		dropped = append(dropped, store.Message{
			ID:   fmt.Sprintf("m%d", i),
			Role: "user",
			Text: fmt.Sprintf("MARKER-%03d %s", i, strings.Repeat("x", 900)),
		})
	}

	rec := &recorder{}
	if _, err := r.summarize(context.Background(), rec, config.Resolved{ID: "fake:m", Model: "m", Context: 40000}, sid, dropped, nil); err != nil {
		t.Fatalf("summarize: %v", err)
	}

	seen := strings.Join(rec.prompts, "\n")
	for i := 0; i < 120; i++ {
		if want := fmt.Sprintf("MARKER-%03d", i); !strings.Contains(seen, want) {
			t.Errorf("%s never reached the summarizer: the summary would claim to cover it", want)
		}
	}
	if len(rec.prompts) < 2 {
		t.Errorf("120 messages of ~900 characters should need more than one request, got %d", len(rec.prompts))
	}
}

// When the chunk cap is reached, the oldest material is folded without being read. The summary then
// says how much, because a summary that reads as complete is what makes a model confidently wrong
// about its own history.
func TestSummarizeAdmitsWhatItCouldNotRead(t *testing.T) {
	r, sid, _ := newTestRunner(t, nil)

	dropped := make([]store.Message, 0, 200)
	for i := 0; i < 200; i++ {
		dropped = append(dropped, store.Message{
			ID:   fmt.Sprintf("m%d", i),
			Role: "user",
			Text: fmt.Sprintf("MARKER-%03d %s", i, strings.Repeat("x", 900)),
		})
	}

	rec := &recorder{}
	// A tiny window forces many chunks, and the cap keeps only the newest ones.
	got, err := r.summarize(context.Background(), rec, config.Resolved{ID: "fake:m", Model: "m", Context: 8000}, sid, dropped, nil)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if len(rec.prompts) != maxSummaryChunks {
		t.Errorf("requests = %d, want the cap %d", len(rec.prompts), maxSummaryChunks)
	}
	if !strings.Contains(got, "folded without being read") {
		t.Errorf("the summary must state the gap, got %q", got)
	}
	// The newest message is always read; the oldest is the one dropped.
	seen := strings.Join(rec.prompts, "\n")
	if !strings.Contains(seen, "MARKER-199") {
		t.Error("the newest message must always be summarized")
	}
	if strings.Contains(seen, "MARKER-000") {
		t.Error("with the cap reached the oldest message is the one that goes unread")
	}
}

// Compaction happens repeatedly over a long session. If the previous summary is not carried forward,
// each round re-derives from raw history and loses whatever falls outside the reading budget, so the
// conversation is forgotten in layers instead of compacted once.
func TestSummarizeCarriesThePreviousSummary(t *testing.T) {
	r, sid, _ := newTestRunner(t, nil)
	prev := &store.Compaction{
		UpToID:  "old",
		Summary: "EARLIER: the user is building dux-code and asked for xAI support.",
		Count:   900,
	}

	rec := &recorder{}
	if _, err := r.summarize(context.Background(), rec, config.Resolved{ID: "fake:m", Model: "m"}, sid,
		[]store.Message{{ID: "m1", Role: "user", Text: "and now the next part"}}, prev); err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if len(rec.prompts) == 0 {
		t.Fatal("no request was made")
	}
	if !strings.Contains(rec.prompts[0], "the user is building dux-code") {
		t.Errorf("the earlier summary was not carried into the new one: %q", rec.prompts[0])
	}
	if !strings.Contains(rec.prompts[0], "and now the next part") {
		t.Errorf("the new material is missing: %q", rec.prompts[0])
	}
}

// An empty answer is not a summary. Saving one would replace the history with a blank card.
func TestSummarizeRejectsAnEmptyAnswer(t *testing.T) {
	r, sid, _ := newTestRunner(t, nil)
	if _, err := r.summarize(context.Background(), &alwaysSilent{}, config.Resolved{ID: "fake:m", Model: "m"}, sid,
		[]store.Message{{ID: "m1", Role: "user", Text: "hello"}}, nil); err == nil {
		t.Fatal("an empty summary must be an error, not a record")
	}
}

// alwaysSilent never answers, which is what a reasoning model out of budget looks like.
type alwaysSilent struct{}

func (alwaysSilent) Stream(_ context.Context, _ llm.Request, _ llm.DeltaFunc) (llm.Result, error) {
	return llm.Result{Reasoning: "thinking...", StopReason: "length"}, nil
}

// A message's contribution is truncated by runes: splitting a multi-byte character produces a
// replacement character in the prompt, which the model then has to read.
func TestSummaryTextTruncatesByRunes(t *testing.T) {
	// A wide character (one rune, three bytes) is the case that breaks a byte-wise cut.
	got := summaryText(store.Message{Role: "user", Text: strings.Repeat("\u4e2d", summaryMessageChars+50)})
	if !utf8.ValidString(got) {
		t.Error("truncation cut a multi-byte character in half")
	}
	if !strings.Contains(got, "truncated") {
		t.Error("the truncation must be announced")
	}
}

// Messages are grouped into requests without losing any, and the oldest chunk goes first so a rolling
// summary reads in order.
func TestPlanChunksKeepsOrderAndEverything(t *testing.T) {
	msgs := make([]store.Message, 0, 10)
	for i := 0; i < 10; i++ {
		msgs = append(msgs, store.Message{ID: fmt.Sprintf("m%d", i), Role: "user", Text: strings.Repeat("y", 1000)})
	}
	chunks, skipped := planChunks(msgs, 3200)
	if skipped != 0 {
		t.Errorf("nothing should be skipped with room to spare, got %d", skipped)
	}
	var flat []string
	for _, c := range chunks {
		if len(c) == 0 {
			t.Error("an empty chunk would be a wasted request")
		}
		for _, m := range c {
			flat = append(flat, m.ID)
		}
	}
	if len(flat) != len(msgs) {
		t.Fatalf("chunks cover %d of %d messages", len(flat), len(msgs))
	}
	for i, id := range flat {
		if want := fmt.Sprintf("m%d", i); id != want {
			t.Fatalf("chunk %d holds %s, want %s: the rolling summary would read out of order", i, id, want)
		}
	}
}

// Folding a long history is several model calls, and until it finishes the turn sends no text of its
// own — so the UI is told it started, with the count, instead of showing "working" for a minute.
func TestCompactionSaysItStartedBeforeSummarizing(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s, _, err := store.Open()
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	sess, err := s.Create("t")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	appendTurns(t, s, sess.ID, 3, 1500, "c")
	if err := s.Append(sess.ID, store.Message{ID: "q", Role: "user", Text: "now the last question"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	var events []map[string]any
	r := New(ipc.NewEventBus(), s, quietConfig(0))
	r.OnEvent = func(ev map[string]any) { events = append(events, ev) }

	cfg := quietConfig(0)
	cfg.CompactAt = 4000
	model := config.Resolved{ID: "fake:m", Model: "m"}
	rn := r.newRun(sess.ID, model, tools.Env{}, "", false)
	r.maybeCompact(context.Background(), rn, &summarizer{}, model, cfg)

	kinds := kindsOf(events)
	started, done := indexOfKind(events, "context.compacting"), indexOfKind(events, "context.compacted")
	if started < 0 || done < 0 {
		t.Fatalf("want both a start and a finish, got %v", kinds)
	}
	if started > done {
		t.Errorf("the start has to be published before the summary is written, got %v", kinds)
	}
	count, _ := events[started]["count"].(int)
	if count <= 0 {
		t.Errorf("the start event says %v messages will be folded; the UI prints that number", events[started]["count"])
	}
	if count != events[done]["count"] {
		t.Errorf("announced %d messages, folded %v", count, events[done]["count"])
	}
}

// indexOfKind is the position of the first event of that kind, or -1.
func indexOfKind(events []map[string]any, kind string) int {
	for i, ev := range events {
		if ev["kind"] == kind {
			return i
		}
	}
	return -1
}

// Summarizing is a mechanical rewrite, so it asks for no thinking where the model allows that and the
// shallowest level where it does not. The default (send nothing) means "auto", which on a model with
// adaptive thinking is the slowest possible choice for the least useful work.
// summaryRequests runs one compaction for a model and returns the summary requests it produced.
func summaryRequests(t *testing.T, model config.Resolved) []llm.Request {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s, _, err := store.Open()
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	sess, err := s.Create("t")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	appendTurns(t, s, sess.ID, 3, 1500, "d")
	if err := s.Append(sess.ID, store.Message{ID: "q", Role: "user", Text: "last"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	cfg := quietConfig(0)
	cfg.CompactAt = 4000
	model.ID, model.Model = "fake:m", "m"
	sum := &summarizer{}
	r := New(ipc.NewEventBus(), s, cfg)
	r.maybeCompact(context.Background(), r.newRun(sess.ID, model, tools.Env{}, "", false), sum, model, cfg)
	if len(sum.requests) == 0 {
		t.Fatal("no summary request was made")
	}
	return sum.requests
}

func TestSummarizeAsksForTheCheapestDepth(t *testing.T) {
	all := []string{config.ReasoningNone, config.ReasoningLow, config.ReasoningMedium, config.ReasoningHigh}

	t.Run("off when the model takes it", func(t *testing.T) {
		for i, req := range summaryRequests(t, config.Resolved{Reasoning: all}) {
			if req.Reasoning != config.ReasoningNone {
				t.Errorf("request %d asked for depth %q, want %q", i+1, req.Reasoning, config.ReasoningNone)
			}
		}
	})

	t.Run("low when off is refused", func(t *testing.T) {
		levels := []string{config.ReasoningLow, config.ReasoningHigh}
		for i, req := range summaryRequests(t, config.Resolved{Reasoning: levels}) {
			if req.Reasoning != config.ReasoningLow {
				t.Errorf("request %d asked for depth %q, want %q", i+1, req.Reasoning, config.ReasoningLow)
			}
		}
	})

	// An OpenAI-compatible endpoint we know nothing about: the level table is empty, so there is no
	// value to send. Sending one anyway is a 400 on a server that never heard of reasoning_effort, and
	// a compaction that fails is worse than one that thinks.
	t.Run("nothing when the model has no table entry", func(t *testing.T) {
		for i, req := range summaryRequests(t, config.Resolved{}) {
			if req.Reasoning != "" {
				t.Errorf("request %d carried depth %q for a model with no levels", i+1, req.Reasoning)
			}
		}
	})

	// The per-model override is the way in: declaring the levels is what makes "off" available, and the
	// summary then picks it up like any other model's.
	t.Run("the override turns it on for such a model", func(t *testing.T) {
		for i, req := range summaryRequests(t, config.Resolved{Reasoning: []string{config.ReasoningNone}}) {
			if req.Reasoning != config.ReasoningNone {
				t.Errorf("request %d asked for depth %q, want %q", i+1, req.Reasoning, config.ReasoningNone)
			}
		}
	})

}

// The choice itself, without a store: off if offered, low if not, nothing if neither — sending a level
// the model does not list is a 400, so an empty table means an empty request.
func TestSummaryReasoning(t *testing.T) {
	for _, tc := range []struct {
		name   string
		levels []string
		want   string
	}{
		{"off first", []string{"none", "low", "medium", "high"}, config.ReasoningNone},
		{"low when off is refused", []string{"low", "medium", "high"}, config.ReasoningLow},
		{"nothing when neither is offered", []string{"minimal", "high"}, ""},
		{"nothing when the model has no levels", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := summaryReasoning(config.Resolved{Reasoning: tc.levels})
			if got != tc.want {
				t.Errorf("summaryReasoning(%v) = %q, want %q", tc.levels, got, tc.want)
			}
		})
	}
}

// The depth table is a guess (config.ReasoningLevels says so itself), so a refusal costs one level and
// one retry, never the summary: the second request goes out with no depth at all, which no provider
// rejects.
func TestSummarizeRetriesWithoutADepthItRefused(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s, _, err := store.Open()
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	sess, err := s.Create("t")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	msgs := make([]store.Message, 0, 6)
	for i := 0; i < 3; i++ {
		msgs = append(msgs,
			store.Message{ID: fmt.Sprintf("u%d", i), Role: "user", Text: padded(1500)},
			store.Message{ID: fmt.Sprintf("a%d", i), Role: "assistant", Text: padded(1500)})
	}
	if err := s.Append(sess.ID, msgs...); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// A client that refuses any depth at all, the way a model whose table entry is wrong would.
	refusing := &refusesDepth{}

	r := &Runner{store: s}
	out, err := r.summarizeChunk(context.Background(), refusing, config.Resolved{Model: "m", Reasoning: []string{"none", "low"}},
		sess.ID, "", msgs, 1, 1)
	if err != nil {
		t.Fatalf("summarizeChunk: %v", err)
	}
	if out == "" {
		t.Error("the summary came back empty")
	}
	if len(refusing.depths) != 2 || refusing.depths[0] != "none" || refusing.depths[1] != "" {
		t.Errorf("depths sent = %v, want [none, \"\"]", refusing.depths)
	}
}

// refusesDepth rejects any request that carries a reasoning level, and answers the rest.
type refusesDepth struct{ depths []string }

func (c *refusesDepth) Stream(_ context.Context, req llm.Request, _ llm.DeltaFunc) (llm.Result, error) {
	c.depths = append(c.depths, req.Reasoning)
	if req.Reasoning != "" {
		return llm.Result{}, &llm.HTTPError{Status: 400, Body: "reasoning_effort is not supported"}
	}
	return llm.Result{Content: "a summary"}, nil
}
