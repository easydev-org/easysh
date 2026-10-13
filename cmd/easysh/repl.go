package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/abcp-sdk/easysh/internal/proto"
)

// repl runs the interactive loop: a raw-mode line editor with history,
// syntax highlighting and tab completion, backed by the daemon's session.
func repl(c *Client, session string, detachMs int) {
	fmt.Printf("easysh — a flow-first shell (session %q).\n", session)
	fmt.Printf("  Tab completes · ↑/↓ history · Ctrl-D exits · >10s jobs detach\n")

	ed := newLineEditor("$ ",
		func(line string) []span { return remoteSpans(c, line) },
		func(prefix string) []completion { return remoteCompletions(c, session, prefix) },
	)
	// Seed history from the session so ↑ recalls commands typed in any terminal
	// attached to this session, not just this process.
	for _, h := range c.History(session) {
		ed.addHistory(h)
	}

	for {
		line, ok := ed.ReadLine()
		if !ok {
			return
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if trimmed == "exit" || trimmed == "quit" {
			return
		}
		ed.addHistory(line)
		c.AddHistory(session, line)

		code, jobID := c.RunOne(session, line, detachMs, true)
		if code < 0 {
			fmt.Fprintf(os.Stderr, "[job %s detached; `easysh jobs` to list, `easysh output %s` to read]\n", jobID, jobID)
		} else if code > 0 {
			fmt.Printf("[exit %d]\n", code)
		}
	}
}

// remoteSpans asks the daemon to highlight a line with its real lexer.
func remoteSpans(c *Client, line string) []span {
	if err := c.Send(proto.Request{Op: "highlight", Line: line}); err != nil {
		return nil
	}
	r, err := c.Recv()
	if err != nil {
		return nil
	}
	out := make([]span, 0, len(r.Spans))
	for _, sp := range r.Spans {
		out = append(out, span{start: sp.Start, end: sp.End, class: sp.Class})
	}
	return out
}

// remoteCompletions asks the daemon for completion candidates.
func remoteCompletions(c *Client, session, prefix string) []completion {
	if err := c.Send(proto.Request{Session: session, Op: "complete", Prefix: prefix}); err != nil {
		return nil
	}
	r, err := c.Recv()
	if err != nil {
		return nil
	}
	out := make([]completion, 0, len(r.Completions))
	for _, cp := range r.Completions {
		out = append(out, completion{text: cp.Text, kind: cp.Kind})
	}
	return out
}
