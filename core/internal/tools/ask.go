package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/NekoLOvO/dux-code/core/internal/llm"
)

// Question is one question the model wants to ask the user.
//
// Options may be empty (free text only) or hold a few suggested answers. The user can always type
// their own — a preset rarely matches the sentence they want.
type Question struct {
	Question string
	Options  []QuestionOption
}

// QuestionOption is one suggested answer. Label is the button text, Description a one-line note.
type QuestionOption struct {
	Label       string
	Description string
}

// Asker puts a question in front of the user and waits for an answer.
//
// An interface so the tools layer does not know what the UI looks like: a CLI, a phone, or a fake
// answer in tests is just another implementation.
type Asker interface {
	Ask(ctx context.Context, q Question) (string, error)
}

// AskUser lets the model stop and ask instead of picking a direction itself.
type AskUser struct{}

// Spec implements Tool.
func (AskUser) Spec() llm.Tool {
	return llm.NewTool(
		"ask_user",
		"Ask the user and wait for the answer. Good for: a request with more than one reasonable approach, an irreversible choice, or information only the user has. Not for: anything you can look up, or a mere check that you should continue. Offer 2 to 4 options; the user can always type their own.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{
					"type":        "string",
					"description": "the question. One sentence, no background essay.",
				},
				"options": map[string]any{
					"type":        "array",
					"description": "2 to 4 suggested answers. The user can always type their own, so no catch-all option is needed.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"label": map[string]any{
								"type":        "string",
								"description": "the button label; as short as possible (two or three words)",
							},
							"description": map[string]any{
								"type":        "string",
								"description": "one line on what this option means; only needed when there is a trade-off",
							},
						},
						"required": []string{"label"},
					},
				},
			},
			"required": []string{"question"},
		},
	)
}

// Run implements Tool.
//
// A dismissed dialog is not an error: it means "I would rather not answer", and the model should pick
// a path itself rather than ask again. So this returns a note saying it may continue.
func (AskUser) Run(ctx context.Context, args string) (string, error) {
	var in struct {
		Question string `json:"question"`
		Options  []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Question) == "" {
		return "", errors.New("question is empty")
	}

	// More than five buttons is not a set of options, it is a list.
	if len(in.Options) > 5 {
		in.Options = in.Options[:5]
	}

	q := Question{Question: strings.TrimSpace(in.Question)}
	for _, o := range in.Options {
		label := strings.TrimSpace(o.Label)
		if label == "" {
			continue
		}
		q.Options = append(q.Options, QuestionOption{
			Label:       label,
			Description: strings.TrimSpace(o.Description),
		})
	}

	env := EnvFrom(ctx)
	if env.Ask == nil {
		return "", errors.New("cannot ask: no window can answer (in CLI mode the model cannot ask questions)")
	}

	answer, err := env.Ask.Ask(ctx, q)
	if err != nil {
		return "", fmt.Errorf("no answer was received: %v", err)
	}

	answer = strings.TrimSpace(answer)
	if answer == "" {
		return "The user closed this without answering. Do not ask again: pick a reasonable approach and continue, and say in your reply what you chose.", nil
	}
	return "The user answered: " + answer, nil
}
