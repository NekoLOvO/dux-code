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
// The point is that only the outgoing copy is affected: Messages in the store are untouched, so the
// history the user sees stays complete, and only historyMessages swaps in "summary + messages after
// the cut point". A failed compaction, or a later recomputation, therefore never damages the data.

// Each compaction goes down to about 40% of the threshold, measured on what is actually sent. Keeping
// too little leaves no headroom, so the next turn trips the threshold again immediately, and every
// compaction re-sends the dropped history — which costs more than not compacting at all.
const compactKeepRatio = 0.4

// summaryMessage is the one message that stands in for the compacted prefix.
//
// It is a function rather than a literal in two places because the text that is sent and the text whose
// size decides whether to compact at all have to be the same text: measuring something else is how the
// threshold ends up describing a request that is never made.
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

// The summary request costs too, so the dropped section only takes this many trailing characters (the
// nearer the present, the more relevant; earlier parts are already superseded).
const summarizeBudgetChars = 24000

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

// pickCut keeps the last keep tokens and returns "which message to send verbatim from".
//
// The cut must land on a user message: an assistant's tool_calls and the matching tool_result are one
// unit, and cutting between them is a 400 on both APIs. A user boundary leaves neither side short.
func pickCut(hist []store.Message, keep int) int {
	acc := 0
	i := len(hist)
	for i > 0 {
		i--
		acc += messageCost(hist[i])
		if acc >= keep {
			break
		}
	}
	for i < len(hist) && hist[i].Role != "user" {
		i++
	}
	// i == 0: everything fits; no need to compact.
	// i >= len: nothing is left after aligning; better not to compact.
	if i <= 0 || i >= len(hist) {
		return 0
	}
	return i
}

// histTokens is how big the history about to be sent is.
func histTokens(hist []store.Message) int {
	total := 0
	for _, m := range hist {
		total += messageCost(m)
	}
	return total
}

// maybeCompact replaces the earlier part with a summary when the request that would go out is too big.
// A failure does not affect this turn.
func (r *Runner) maybeCompact(ctx context.Context, rn *run, client llm.Client, model config.Resolved, cfg *config.Config) {
	if cfg.CompactAt <= 0 {
		return
	}
	hist := r.store.History(rn.sessionID)
	// "Too few messages" is not a guard: few messages that are each huge is exactly the case that most
	// needs compaction. pickCut decides — it returns 0 when cutting would leave nothing.
	//
	// The threshold is about the request, not the transcript. The stored history never shrinks (only
	// the outgoing copy changes), so measuring it would keep reporting "too big" on every later turn
	// and pay for a fresh summary that comes out the same as the last one. What is measured here is
	// exactly what historyMessages would build.
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

	cut := pickCut(hist, int(float64(threshold)*compactKeepRatio))
	// Nothing would be gained: a cut at or before the current one replaces the summary with the same
	// text at the same boundary, at the price of another request.
	if cut <= 0 || cut <= from {
		return
	}

	summary, err := r.summarize(ctx, client, model, rn.sessionID, hist[:cut])
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

// summarize asks the model to condense the section about to be dropped into a handover summary.
func (r *Runner) summarize(ctx context.Context, client llm.Client, model config.Resolved, sessionID string, dropped []store.Message) (string, error) {
	var parts []string
	used := 0
	// Take from the most recent backwards until the character budget is spent.
	for i := len(dropped) - 1; i >= 0 && used < summarizeBudgetChars; i-- {
		m := dropped[i]
		text := m.Text
		if len(text) > 2000 {
			text = text[:2000] + "… (truncated)"
		}
		used += len(text)
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
		parts = append(parts, who+": "+text)
	}
	// Collected in reverse; flip before sending.
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}

	res, err := client.Stream(ctx, llm.Request{
		Model: model.Model,
		System: "You compress conversations into a handover summary." +
			" Keep: the user's goals and constraints, decisions already made and why, which files were touched, and what is still unfinished." +
			" No pleasantries, no narrative of the process, no decoration beyond headings.",
		Messages: []llm.Message{{
			Role: llm.RoleUser,
			Content: "Compress the conversation below into a summary of at most 400 words," +
				" so someone who has not seen it can take over directly:\n\n" + strings.Join(parts, "\n\n"),
		}},
		MaxTokens: 900,
	}, func(string) {})
	// The summary is a request like any other and is billed. Nobody sees its usage in the transcript, so
	// without this it would silently be missing from the session total.
	if spendErr := r.store.Spend(sessionID, res.Usage.Input, res.Usage.Output); spendErr != nil {
		applog.Warnf("failed to record usage of session %s: %v", sessionID, spendErr)
	}
	if err != nil {
		return "", err
	}

	out := strings.TrimSpace(res.Content)
	if out == "" {
		return "", fmt.Errorf("the summary is empty")
	}
	return out, nil
}
