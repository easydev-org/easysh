package exec

import (
	"context"

	"github.com/easydev-org/easysh/pkg/syntax"
)

// runIf evaluates the condition and runs the matching branch.
func (r *Runner) runIf(ctx context.Context, c *syntax.IfCmd, st *state) int {
	if r.runStmts(ctx, c.Cond, st) == 0 {
		return r.runStmts(ctx, c.Then, st)
	}
	for _, e := range c.Elifs {
		if r.runStmts(ctx, e.Cond, st) == 0 {
			return r.runStmts(ctx, e.Then, st)
		}
	}
	if c.Else != nil {
		return r.runStmts(ctx, c.Else, st)
	}
	return 0
}

// runFor iterates the `in` words, binding NAME each time.
func (r *Runner) runFor(ctx context.Context, c *syntax.ForCmd, st *state) int {
	code := 0
	// POSIX: the `in` list undergoes field splitting, so `for i in $(seq 1 3)`
	// iterates 1,2,3. Collect the fields across all words first.
	var items []string
	for _, w := range c.In {
		items = append(items, r.expandFields(w, st)...)
	}
	if len(items) == 0 {
		items = r.args // `for x; do` (POSIX: iterate the positional params)
	}
	for iter, item := range items {
		r.vars[c.Name] = item
		r.iterStack = append(r.iterStack, iter+1)
		code = r.runStmts(ctx, c.Body, st)
		r.iterStack = r.iterStack[:len(r.iterStack)-1]
		if r.loopCtl == "break" {
			r.loopCtl = ""
			break
		}
		if r.loopCtl == "continue" {
			r.loopCtl = ""
			continue
		}
	}
	return code
}

// runWhile loops while the condition is true (bounded to avoid hangs).
func (r *Runner) runWhile(ctx context.Context, c *syntax.WhileCmd, st *state) int {
	code := 0
	for i := 0; i < 1_000_000; i++ {
		cond := r.runStmts(ctx, c.Cond, st)
		// while loops while the condition succeeds; until loops while it fails.
		if c.Until {
			if cond == 0 {
				break
			}
		} else if cond != 0 {
			break
		}
		r.iterStack = append(r.iterStack, i+1)
		code = r.runStmts(ctx, c.Body, st)
		r.iterStack = r.iterStack[:len(r.iterStack)-1]
		if r.loopCtl == "break" {
			r.loopCtl = ""
			break
		}
		if r.loopCtl == "continue" {
			r.loopCtl = ""
			continue
		}
	}
	return code
}

// runCase matches the word against glob patterns.
func (r *Runner) runCase(ctx context.Context, c *syntax.CaseCmd, st *state) int {
	word := r.expandOne(c.Word, st)
	for _, arm := range c.Arms {
		for _, p := range arm.Pats {
			pat := r.expandOne(p, st)
			if globMatch(pat, word) {
				return r.runStmts(ctx, arm.Body, st)
			}
		}
	}
	return 0
}
