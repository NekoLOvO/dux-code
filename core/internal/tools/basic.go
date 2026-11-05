package tools

import (
	"context"
	"time"

	"github.com/NekoLOvO/dux-code/core/internal/llm"
)

// Echo exists to exercise the whole tool chain: no side effects, but it goes through
// "model asks for a call → event → result back → model answers again".
type Echo struct{}

// Spec implements Tool.
func (Echo) Spec() llm.Tool {
	return llm.NewTool(
		"echo",
		"Echo the text back unchanged. Used to check that the tool channel works.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{
					"type":        "string",
					"description": "the text to echo",
				},
			},
			"required": []string{"text"},
		},
	)
}

// Run implements Tool.
func (Echo) Run(_ context.Context, args string) (string, error) {
	var in struct {
		Text string `json:"text"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if in.Text == "" {
		return "(empty)", nil
	}
	return in.Text, nil
}

// Clock returns the current local time. For "what day is it" questions,
// and handy for checking that tool results really reach the context.
type Clock struct{}

// Spec implements Tool.
func (Clock) Spec() llm.Tool {
	return llm.NewTool(
		"now",
		"Current local date and time, including the time zone.",
		map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	)
}

// Run implements Tool.
func (Clock) Run(_ context.Context, _ string) (string, error) {
	now := time.Now()
	weekday := [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}[int(now.Weekday())]
	return now.Format("2006-01-02 15:04:05 MST") + " (" + weekday + ")", nil
}
