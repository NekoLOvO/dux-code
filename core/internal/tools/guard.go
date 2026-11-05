package tools

import (
	"context"
	"fmt"
	"strings"
)

// ask is the only entry point for "needs a nod".
//
// Policy converges here rather than in each tool: a tool only says "I want to do this, touching these
// paths", and whether to ask is Env.Approval's decision.
//
// No approver (a CLI, or no UI) counts as a refusal — deliberately: better that a tool cannot act than
// that it decides for itself that it may.
func (e Env) ask(ctx context.Context, a Approval) error {
	switch e.Approval {
	case ApprovalAuto:
		return nil
	case ApprovalWorkspace:
		if e.insideWorkspace(a) {
			return nil
		}
	}

	if e.Approve == nil {
		return fmt.Errorf("%w: no window is available to ask", ErrDenied)
	}
	ok, err := e.Approve.Ask(ctx, a)
	if err != nil {
		return fmt.Errorf("%w (%v)", ErrDenied, err)
	}
	if !ok {
		return ErrDenied
	}
	return nil
}

// insideWorkspace reports whether this action clearly only touches things inside the working
// directory.
//
// File writes look at Paths: Resolve already guaranteed those are under Root, so this is really
// "confirm there is no exception". The real difficulty is bash — a shell is Turing-complete, and
// `cd $(mktemp -d)` does not look like it goes to /tmp. So commands are checked literally only:
// `rm -rf /`, `cat ../x` and `curl -o /tmp/x` are caught, and what cannot be caught cannot be caught.
// That line has to be stated to the user and never called a sandbox.
func (e Env) insideWorkspace(a Approval) bool {
	root, err := e.RootDir()
	if err != nil {
		return false
	}

	if a.Kind == "bash" {
		cmd := strings.TrimSpace(a.Command)
		if cmd == "" {
			// Not knowing what will run leaves nothing to judge; hand it to a human.
			return false
		}
		return commandStaysInside(root, cmd)
	}

	if len(a.Paths) == 0 {
		return false
	}
	for _, p := range a.Paths {
		if !within(root, realPathBestEffort(p)) {
			return false
		}
	}
	return true
}
