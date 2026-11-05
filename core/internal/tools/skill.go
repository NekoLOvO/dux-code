package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/NekoLOvO/dux-code/core/internal/llm"
	"github.com/NekoLOvO/dux-code/core/internal/skills"
)

// LoadSkill loads a skill's prose into the conversation.
//
// This is the only way to read one, and no "load everything" is offered on purpose: each skill is a few
// hundred lines of instructions, loading all into the system prompt means paying tokens every turn, and
// the model would see conflicting directives at once. Let it choose.
type LoadSkill struct{}

// Spec implements Tool.
func (LoadSkill) Spec() llm.Tool {
	return llm.NewTool(
		"load_skill",
		"Load the full instructions of a skill. The system prompt lists the available skills and what they are for; call this first when you need to follow a procedure.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "skill name, as listed in the system prompt",
				},
			},
			"required": []string{"name"},
		},
	)
}

// Run implements Tool.
func (LoadSkill) Run(ctx context.Context, args string) (string, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}

	s, doc, err := skills.Load(in.Name)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(doc) == "" {
		return fmt.Sprintf("skill %s has no instructions (its SKILL.md is only frontmatter).", s.Name), nil
	}

	// Tells this turn "a skill was loaded" and handles allowed-tools. Without a runtime it just cannot
	// narrow tools, and the prose still comes back.
	env := EnvFrom(ctx)
	if env.Skills != nil {
		env.Skills.Activate(s.Name, s.Tools)

		var note string
		if len(s.Tools) > 0 {
			note = "\n\n(this skill is limited to these tools: " + strings.Join(s.Tools, ", ") + ")"
		}
		return "# Skill: " + s.Name + "\n\n" + doc + note, nil
	}

	return "# Skill: " + s.Name + "\n\n" + doc, nil
}

// SkillsPrompt builds the skill list for the system prompt: name and purpose only.
//
// It has to be short — it is resent on every model call.
func SkillsPrompt() string {
	list, err := skills.List()
	if err != nil || len(list) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n\nAvailable skills (when you need to follow a procedure, call load_skill first for the full instructions):")
	for _, s := range list {
		b.WriteString("\n- ")
		b.WriteString(s.Name)
		if s.Description != "" {
			b.WriteString(": ")
			b.WriteString(s.Description)
		}
	}
	return b.String()
}
