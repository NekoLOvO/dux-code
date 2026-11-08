package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NekoLOvO/dux-code/core/internal/applog"
	"github.com/NekoLOvO/dux-code/core/internal/config"
	"github.com/NekoLOvO/dux-code/core/internal/llm"
	"github.com/NekoLOvO/dux-code/core/internal/store"
)

// Context compaction.
//
// Only the outgoing copy is affected: store messages are untouched, and historyMessages substitutes
// "summary + messages after the cut point", so the visible history stays complete and a failed
// compaction damages no data.
const (
	// summaryChunkFloor / summaryChunkCap bound one summarization request's material, in characters.
	// The limit is otherwise derived from the model's window (a quarter of it), so a small model gets
	// small chunks and a large one is not asked to read a million characters in one go.
	summaryChunkFloor = 8000
	summaryChunkCap   = 400000
	// summaryChunkChars is the limit when the model's window is unknown.
	summaryChunkChars = 40000
	// maxSummaryChunks caps the requests one compaction may make. Reaching the cap means the oldest
	// material is folded without being read, which the summary then says out loud — a summary that
	// claims to cover a range it never saw is worse than one that admits the gap.
	maxSummaryChunks = 8
	// summaryMessageChars truncates a single message's contribution. One pasted log must not eat the
	// whole chunk.
	summaryMessageChars = 2000
)

// summaryMessage is the one message that stands in for the compacted prefix.
//
// It is a function because the text that is sent and the text whose size decides whether to compact
// have to be the same text.
func summaryMessage(c *store.Compaction) llm.Message {
	return llm.Message{
		Role:    llm.RoleUser,
		Content: fmt.Sprintf("(%d earlier messages were compressed into the summary below; continue from it)\n\n%s", c.Count, c.Summary),
	}
}

// sentCost is how big the next request's history will be: the summary message plus everything from the
// cut point on. from is the cut point's index in hist, or -1 when the record names a message that is no
// longer there — in that case the whole history is sent, which is what historyMessages does too.
func sentCost(hist []store.Message, from int, c *store.Compaction) int {
	if c == nil || from <= 0 || from >= len(hist) {
		return histTokens(hist)
	}
	return estimateTokens(summaryMessage(c).Content) + 4 + histTokens(hist[from:])
}

// estimateTokens is a rough estimate, used only to decide "compact or not". Real usage always comes
// from the API's usage, never from here.
func estimateTokens(s string) int {
	ascii, wide := 0, 0
	for _, r := range s {
		if r < 128 {
			ascii++
		} else {
			wide++
		}
	}
	return ascii/4 + wide
}

func messageCost(m store.Message) int {
	n := estimateTokens(m.Text)
	for _, tc := range m.ToolCalls {
		n += estimateTokens(tc.Function.Arguments) + 8
	}
	return n + 4
}

// pickCut returns the message to send verbatim from: the current turn's own message.
//
// Everything before it folds into the summary. It has to be a user message: an assistant's tool_calls
// and the matching tool_result are one unit, and cutting between them is a 400 on both APIs. It is
// also the boundary the user can see, so the summary card begins where the current turn begins.
func pickCut(hist []store.Message) int {
	if len(hist) < 2 {
		return 0
	}
	for i := len(hist) - 1; i > 0; i-- {
		if hist[i].Role == "user" {
			return i
		}
	}
	// No user message past the first: the whole history is one turn, so there is no boundary to cut at.
	return 0
}

// histTokens is how big the history about to be sent is.
func histTokens(hist []store.Message) int {
	total := 0
	for _, m := range hist {
		total += messageCost(m)
	}
	return total
}

// summaryText is one message's contribution to a summary request: who said it, and what it said,
// capped so a single pasted log cannot take the whole chunk.
func summaryText(m store.Message) string {
	text := m.Text
	if runes := []rune(text); len(runes) > summaryMessageChars {
		// By runes, not bytes: cutting a multi-byte character in half produces a replacement character
		// in the prompt, and this text is sent to a model that then has to read it.
		text = string(runes[:summaryMessageChars]) + "… (truncated)"
	}
	var who string
	switch m.Role {
	case "user":
		who = "User"
	case "assistant":
		who = "Assistant"
	case "tool":
		who = "Output of tool " + m.Name
	default:
		who = m.Role
	}
	return who + ": " + text
}

// summaryChunkLimit is how many characters of material one summarization request may carry: a quarter
// of the model's window, so the prompt, the carried summary and the answer all fit alongside it.
func summaryChunkLimit(model config.Resolved) int {
	if model.Context <= 0 {
		return summaryChunkChars
	}
	n := model.Context
	if n < summaryChunkFloor {
		return summaryChunkFloor
	}
	if n > summaryChunkCap {
		return summaryChunkCap
	}
	return n
}

// planChunks splits the folded range into requests, oldest chunk first so a rolling summary reads in
// order.
//
// skipped counts the messages left unread when the chunk count hits its cap; the caller states that
// gap in the summary itself.
func planChunks(msgs []store.Message, limit int) (chunks [][]store.Message, skipped int) {
	if len(msgs) == 0 {
		return nil, 0
	}
	if limit < 1 {
		limit = 1
	}

	// Walk backwards, starting a new chunk whenever the current one is full. cuts[k] is the first index
	// of the k-th chunk counted from the end, so cuts is descending and always ends with 0.
	cuts := make([]int, 0, 8)
	cuts = append(cuts, len(msgs))
	used := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		n := len(summaryText(msgs[i]))
		// used > 0 keeps an oversized single message in its own chunk instead of looping forever.
		if used > 0 && used+n > limit {
			cuts = append(cuts, i+1)
			used = 0
		}
		used += n
	}
	cuts = append(cuts, 0)

	total := len(cuts) - 1
	keep := total
	if keep > maxSummaryChunks {
		keep = maxSummaryChunks
		skipped = cuts[keep]
	}

	chunks = make([][]store.Message, 0, keep)
	// cuts[keep] .. cuts[0], oldest kept chunk first.
	for k := keep - 1; k >= 0; k-- {
		chunks = append(chunks, msgs[cuts[k+1]:cuts[k]])
	}
	return chunks, skipped
}

// maybeCompact replaces the earlier part with a summary when the request that would go out is too big.
// A failure does not affect this turn.
func (r *Runner) maybeCompact(ctx context.Context, rn *run, client llm.Client, model config.Resolved, cfg *config.Config) {
	if cfg.CompactAt <= 0 {
		return
	}
	hist := r.store.History(rn.sessionID)
	// The threshold is about the request, not the transcript: the stored history never shrinks, so
	// measuring it would keep reporting "too big" and pay for a summary identical to the last one.
	// What is measured is exactly what historyMessages would build.
	threshold := cfg.CompactThreshold(model)
	existing := r.store.Compaction(rn.sessionID)
	from := -1
	if existing != nil {
		for i, m := range hist {
			if m.ID == existing.UpToID {
				from = i
				break
			}
		}
	}
	sent := sentCost(hist, from, existing)
	if sent <= threshold {
		return
	}

	cut := pickCut(hist)
	// Nothing would be gained: a cut at or before the current one replaces the summary with the same
	// text at the same boundary, at the price of another request.
	if cut <= 0 || cut <= from {
		// "Compaction is on but nothing happened" is a question only this line can answer, and it was
		// silent: a session at 80% of its window with no summary looked like a bug in the UI.
		applog.Infof("session %s is over the compaction threshold (%d > %d) but there is nothing to fold (cut %d, current cut %d)",
			rn.sessionID, sent, threshold, cut, from)
		return
	}

	// Announced before the first summary request, not after the last: folding a long history is
	// several model calls, and until it finishes the turn shows nothing but "working", which reads
	// as a hang. The count is what will be folded, so the message can say how much.
	r.publish(map[string]any{
		"kind":      "context.compacting",
		"sessionId": rn.sessionID,
		"count":     cut,
	})

	summary, err := r.summarize(ctx, client, model, rn.sessionID, hist[:cut], existing)
	if err != nil {
		// A failed summary must not make the turn worse: send as-is. The limit it would have hit is still
		// there, but at least the turn does not fail because compaction broke.
		applog.Warnf("compaction failed (session %s): %v", rn.sessionID, err)
		return
	}

	c := &store.Compaction{
		UpToID:  hist[cut].ID,
		Summary: summary,
		Count:   cut,
		At:      time.Now().UnixMilli(),
	}
	applog.Infof("compacted session %s: %d messages folded into a summary, request %d -> %d tokens (threshold %d)",
		rn.sessionID, cut, sent, sentCost(hist, cut, c), threshold)

	if err := r.store.SetCompaction(rn.sessionID, c); err != nil {
		applog.Warnf("failed to save the compaction (session %s): %v", rn.sessionID, err)
		return
	}

	// The UI draws a divider at the cut point and can show the summary itself, so both travel with the
	// event. Fetching them with a session.get would reload the transcript under a running turn.
	r.publish(map[string]any{
		"kind":      "context.compacted",
		"sessionId": rn.sessionID,
		"count":     cut,
		"before":    sent,
		"upToId":    c.UpToID,
		"summary":   summary,
	})
}

// summarize folds the material about to be dropped into one handover summary.
//
// The previous summary is carried into every request, so a second compaction extends the first instead
// of re-deriving it from raw history. The folded range is read in chunks, each carrying the running
// summary, so the whole range is read once and in order.
func (r *Runner) summarize(ctx context.Context, client llm.Client, model config.Resolved, sessionID string, dropped []store.Message, previous *store.Compaction) (string, error) {
	carry := ""
	if previous != nil {
		carry = strings.TrimSpace(previous.Summary)
	}

	chunks, skipped := planChunks(dropped, summaryChunkLimit(model))
	if len(chunks) == 0 {
		// Nothing new to fold. The summary we already have still stands (maybeCompact will not save an
		// empty one, see below).
		return carry, nil
	}

	for i, chunk := range chunks {
		next, err := r.summarizeChunk(ctx, client, model, sessionID, carry, chunk, i+1, len(chunks))
		if err != nil {
			return "", err
		}
		carry = next
	}

	if strings.TrimSpace(carry) == "" {
		// A model that answered nothing is a retryable outcome, not a summary; saving it would replace
		// the history with an empty card.
		return "", fmt.Errorf("the summary is empty")
	}

	if skipped > 0 {
		// Say so rather than let the summary read as complete: the model will act on this text, and a
		// silent gap is what makes it confidently wrong about its own history.
		carry = fmt.Sprintf("%s\n\n(Note: the %d oldest messages of this session were folded without being read, because the summary budget ran out. Anything before this summary is not recoverable from it.)", carry, skipped)
	}
	return carry, nil
}

// summaryReasoning is the depth the summarizer runs at: off when the model offers it, low otherwise.
//
// Summarizing is a mechanical rewrite of text the model itself produced — there is nothing to reason
// about, and thinking is the slowest part of the request. Leaving it at the model's default ("auto")
// made a compaction of a few thousand messages take a minute or more on a model with adaptive
// thinking, during which the turn looks hung.
//
// The chosen level has to be one the model actually accepts, or the request is a 400 that loses the
// summary: the levels come from config.ReasoningLevels, the same table the menu is built from. Empty
// means the model has no reasoning control at all, and the client omits the field.
func summaryReasoning(model config.Resolved) string {
	for _, wanted := range []string{config.ReasoningNone, config.ReasoningLow} {
		for _, level := range model.Reasoning {
			if level == wanted {
				return wanted
			}
		}
	}
	return ""
}

// summarizeChunk folds one slice of the conversation, merging it into everything summarized so far.
func (r *Runner) summarizeChunk(ctx context.Context, client llm.Client, model config.Resolved, sessionID, carry string, chunk []store.Message, part, parts int) (string, error) {
	parts2 := make([]string, 0, len(chunk))
	for _, m := range chunk {
		parts2 = append(parts2, summaryText(m))
	}

	var b strings.Builder
	if carry != "" {
		b.WriteString("This session has already been compressed once. That summary is below; merge it into yours, so the new text stands in for the whole conversation rather than only the new part.\n\n")
		b.WriteString("--- summary of everything before this point ---\n")
		b.WriteString(carry)
		b.WriteString("\n--- end of the earlier summary ---\n\n")
	}
	if parts > 1 {
		fmt.Fprintf(&b, "Now the messages that follow it, part %d of %d:\n\n", part, parts)
	} else {
		b.WriteString("Now the messages that follow it:\n\n")
	}
	b.WriteString(strings.Join(parts2, "\n\n"))

	prompt := "Compress the conversation below into a summary of at most 400 words," +
		" so someone who has not seen it can take over directly:\n\n" + b.String()

	// Two attempts, for two different reasons: an empty answer is worth asking again (a reasoning model
	// can spend the whole budget thinking and return nothing), and a refused depth is worth asking
	// without it (the levels come from a table that is a guess, and a 400 here would cost the summary
	// rather than one option).
	depth := summaryReasoning(model)
	for attempt := 0; attempt < 2; attempt++ {
		res, err := client.Stream(ctx, llm.Request{
			Model: model.Model,
			System: "You compress conversations into a handover summary." +
				" Keep: the user's goals and constraints, decisions already made and why, which files were touched, and what is still unfinished." +
				" No pleasantries, no narrative of the process, no decoration beyond headings.",
			Messages: []llm.Message{{
				Role:    llm.RoleUser,
				Content: prompt,
			}},
			// Off (or low) rather than the turn's own depth: nothing here needs thinking, and a deep
			// model spends most of a minute on it.
			Reasoning: depth,
			// The budget has to cover reasoning *and* the answer. 900 was enough for the answer alone,
			// which is why a reasoning model came back empty: it thought until the cap and stopped.
			MaxTokens: 3000,
		}, func(string) {})
		// The summary is a request like any other and is billed. Nobody sees its usage in the
		// transcript, so without this it would silently be missing from the session total.
		if spendErr := r.store.Spend(sessionID, res.Usage.Input, res.Usage.Output); spendErr != nil {
			applog.Warnf("failed to record usage of session %s: %v", sessionID, spendErr)
		}
		if err != nil {
			if attempt == 0 && depth != "" && !llm.Retryable(err) {
				applog.Warnf("summary request for session %s rejected depth %q (%v); asking again without it",
					sessionID, depth, err)
				depth = ""
				continue
			}
			return "", err
		}
		if out := strings.TrimSpace(res.Content); out != "" {
			return out, nil
		}
		applog.Warnf("summary attempt %d for session %s came back empty (reasoning %d chars, stop %q); retrying",
			attempt+1, sessionID, len(res.Reasoning), res.StopReason)
	}
	return "", fmt.Errorf("the summary is empty")
}
