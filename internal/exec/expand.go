package exec

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/abcp-sdk/easysh/internal/flow"
	"github.com/abcp-sdk/easysh/internal/syntax"
)

// expandOne expands a word to exactly one string (no field splitting, no glob).
// Used for redirect targets, binding values, and case subjects/patterns.
func (r *Runner) expandOne(w *syntax.Word, st *state) string {
	if w == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range w.Parts {
		switch v := p.(type) {
		case *syntax.Lit:
			b.WriteString(v.Value)
		case *syntax.Param:
			b.WriteString(r.param(v.Name, st))
		case *syntax.CmdSub:
			b.WriteString(r.cmdSubst(v.Body, st))
		case *syntax.Arith:
			b.WriteString(strconv.FormatInt(r.arith(v.Raw, st), 10))
		}
	}
	return b.String()
}

// expandFields expands a word to zero or more fields: it splits unquoted
// expansions on the fixed separators (space/tab/newline) and globs each field.
// Quoted words are never split. This replaces IFS with one predictable rule.
func (r *Runner) expandFields(w *syntax.Word, st *state) []string {
	if w == nil {
		return nil
	}
	// A word that is exactly `$@` yields one field per positional argument.
	if len(w.Parts) == 1 {
		if p, ok := w.Parts[0].(*syntax.Param); ok && p.Name == "@" {
			return append([]string(nil), r.args...)
		}
	}
	s := r.expandOne(w, st)
	if wordHasQuoted(w) {
		return []string{s}
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil
	}
	var out []string
	for _, f := range fields {
		out = append(out, r.glob(f)...)
	}
	return out
}

// wordHasQuoted reports whether any part of the word came from quotes.
func wordHasQuoted(w *syntax.Word) bool {
	for _, p := range w.Parts {
		if l, ok := p.(*syntax.Lit); ok && l.Quoted {
			return true
		}
	}
	return false
}

// glob expands a field containing glob metacharacters; a non-matching pattern
// stays literal (no nullglob in v0).
func (r *Runner) glob(f string) []string {
	if !hasGlobMeta(f) {
		return []string{f}
	}
	matches, err := filepath.Glob(f)
	if err != nil || len(matches) == 0 {
		return []string{f}
	}
	return matches
}

func hasGlobMeta(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

// arith evaluates an integer expression, resolving bare identifiers as
// variables (so `$((n - 1))` works).
func (r *Runner) arith(expr string, st *state) int64 {
	p := &arithParser{s: strings.TrimSpace(expr), resolve: func(name string) int64 {
		v := r.param(name, st)
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}}
	return p.expr()
}

// param resolves a variable reference.
func (r *Runner) param(name string, st *state) string {
	switch name {
	case "?":
		return strconv.Itoa(r.lastExit)
	case "@":
		return strings.Join(r.args, " ")
	case "#":
		return strconv.Itoa(len(r.args))
	}
	if name == "" {
		return ""
	}
	// positional $1..$9 (and ${10} via name digits)
	if n, err := strconv.Atoi(name); err == nil {
		if n >= 1 && n <= len(r.args) {
			return r.args[n-1]
		}
		return ""
	}
	if v, ok := r.vars[name]; ok {
		return v
	}
	for _, kv := range st.env {
		if strings.HasPrefix(kv, name+"=") {
			return kv[len(name)+1:]
		}
	}
	return ""
}

// cmdSubst runs a $( ... ) body, capturing stdout as a capture flow.
func (r *Runner) cmdSubst(body []*syntax.Stmt, st *state) string {
	ref := &flow.CommandRef{Node: r.nextNode(), Argv: []string{"$()"}}
	stream, buf := r.reg.NewCapture(ref, r.activeSink, r.opts.RingLines)
	sub := *st
	sub.stdout = stream
	r.runStmts(context.Background(), body, &sub)
	return buf.String()
}
