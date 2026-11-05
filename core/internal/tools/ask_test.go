package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeAsker struct {
	answer string
	err    error
	asked  int
	last   Question
}

func (f *fakeAsker) Ask(_ context.Context, q Question) (string, error) {
	f.asked++
	f.last = q
	return f.answer, f.err
}

func TestAskUserReturnsAnswer(t *testing.T) {
	ap := &fakeAsker{answer: "Postgres"}
	ctx := WithEnv(context.Background(), Env{Ask: ap})

	out, err := (AskUser{}).Run(ctx, `{"question":"Which database should we use?","options":[{"label":"Postgres","description":"match what we have now"},{"label":"SQLite"}]}`)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "Postgres") {
		t.Fatalf("the answer never reached the model: %q", out)
	}
	if ap.asked != 1 {
		t.Fatalf("should ask once, asked %d times", ap.asked)
	}
	if len(ap.last.Options) != 2 || ap.last.Options[1].Label != "SQLite" {
		t.Fatalf("options were passed wrong: %+v", ap.last.Options)
	}
	if ap.last.Options[1].Description != "" {
		t.Fatalf("with no description it should be empty: %q", ap.last.Options[1].Description)
	}
}

// TestAskUserDismissed records a decision: a dismissed dialog is not an error. An error would make the
// model think the tool is broken; a note tells it to carry on by itself.
func TestAskUserDismissed(t *testing.T) {
	ap := &fakeAsker{answer: ""}
	ctx := WithEnv(context.Background(), Env{Ask: ap})

	out, err := (AskUser{}).Run(ctx, `{"question":"Should we continue?"}`)
	if err != nil {
		t.Fatalf("closing it is not an error: %v", err)
	}
	if !strings.Contains(out, "pick a reasonable approach") {
		t.Fatalf("the model must be told to decide for itself, got %q", out)
	}
}

func TestAskUserWithoutUI(t *testing.T) {
	ctx := WithEnv(context.Background(), Env{}) // no Ask
	_, err := (AskUser{}).Run(ctx, `{"question":"Are you there?"}`)
	if err == nil {
		t.Fatal("with no window it should error instead of inventing an answer")
	}
}

func TestAskUserRejectsEmptyQuestion(t *testing.T) {
	ap := &fakeAsker{answer: "x"}
	ctx := WithEnv(context.Background(), Env{Ask: ap})
	if _, err := (AskUser{}).Run(ctx, `{"question":"   "}`); err == nil {
		t.Fatal("an empty question should be rejected")
	}
	if ap.asked != 0 {
		t.Fatal("an empty question must not reach the user")
	}
}

// TestAskUserCapsOptions: more than five buttons is not a set of options, it is a list.
func TestAskUserCapsOptions(t *testing.T) {
	ap := &fakeAsker{answer: "a"}
	ctx := WithEnv(context.Background(), Env{Ask: ap})
	args := `{"question":"pick one","options":[{"label":"a"},{"label":"b"},{"label":"c"},{"label":"d"},{"label":"e"},{"label":"f"}]}`
	if _, err := (AskUser{}).Run(ctx, args); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(ap.last.Options) != 5 {
		t.Fatalf("should clamp to 5, got %d", len(ap.last.Options))
	}
}

func TestAskUserPropagatesAskError(t *testing.T) {
	ap := &fakeAsker{err: errors.New("the dialog was closed")}
	ctx := WithEnv(context.Background(), Env{Ask: ap})
	if _, err := (AskUser{}).Run(ctx, `{"question":"Are you there?"}`); err == nil {
		t.Fatal("an Ask failure should propagate, not be swallowed")
	}
}
