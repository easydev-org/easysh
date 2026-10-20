package exec

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/easydev-org/easysh/pkg/flow"
	"github.com/easydev-org/easysh/pkg/syntax"
)

// expandOne expands a word to exactly one string (no field splitting, no glob).
// Used for redirect targets, binding values, and case subjects/patterns.
func (r *Runner) expandOne(w *syntax.Word, st *state) string {
	if w == nil {
		return ""
	}
	// Tilde expansion: a leading unquoted `~` (as the whole first segment or
	// `~/...`) becomes $HOME.
	if len(w.Parts) > 0 {
		if l, ok := w.Parts[0].(*syntax.Lit); ok && !l.Quoted {
			if l.Value == "~" || strings.HasPrefix(l.Value, "~/") {
				home := r.home(st)
				if home != "" {
					rest := strings.TrimPrefix(l.Value, "~")
					var b strings.Builder
					b.WriteString(home)
					b.WriteString(rest)
					for _, p := range w.Parts[1:] {
						b.WriteString(r.partText(p, st))
					}
					return b.String()
				}
			}
		}
	}
	var b strings.Builder
	for _, p := range w.Parts {
		switch v := p.(type) {
		case *syntax.Lit:
			b.WriteString(v.Value)
		case *syntax.Param:
			b.WriteString(r.paramExp(v, st))
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
		out = append(out, r.glob(f, st.dir)...)
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
// stays literal (no nullglob in v0). A relative pattern is resolved against the
// SESSION dir (dir), not the daemon process's cwd — otherwise `cd x; echo *.go`
// would glob the wrong directory.
func (r *Runner) glob(f, dir string) []string {
	if !hasGlobMeta(f) {
		return []string{f}
	}
	pattern := f
	if !filepath.IsAbs(pattern) && dir != "" {
		pattern = filepath.Join(dir, pattern)
	}
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return []string{f}
	}
	// Present matches relative to the session dir when the pattern was relative,
	// so `echo *.go` prints "a.go b.go" (not "/abs/dir/a.go").
	if !filepath.IsAbs(f) && dir != "" {
		rel := make([]string, 0, len(matches))
		for _, m := range matches {
			if rr, rerr := filepath.Rel(dir, m); rerr == nil {
				rel = append(rel, rr)
				continue
			}
			rel = append(rel, m)
		}
		return rel
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
	case "PWD":
		// The working directory, always live (it follows `cd`), like a real
		// shell's $PWD.
		return st.dir
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

// partText expands a single word part to text (no field splitting).
func (r *Runner) partText(p syntax.WordPart, st *state) string {
	switch v := p.(type) {
	case *syntax.Lit:
		return v.Value
	case *syntax.Param:
		return r.paramExp(v, st)
	case *syntax.CmdSub:
		return r.cmdSubst(v.Body, st)
	case *syntax.Arith:
		return strconv.FormatInt(r.arith(v.Raw, st), 10)
	}
	return ""
}

// home resolves $HOME (or the passwd-style default) for tilde expansion.
func (r *Runner) home(st *state) string {
	if h := r.param("HOME", st); h != "" {
		return h
	}
	return os.Getenv("HOME")
}

// paramExp resolves a Param, applying a POSIX expansion operator if present.
func (r *Runner) paramExp(p *syntax.Param, st *state) string {
	val := r.param(p.Name, st)
	set := r.isSet(p.Name, st)
	switch p.Op {
	case "":
		return val
	case "#len":
		return strconv.Itoa(len(val))
	case ":-":
		if !set || val == "" {
			return r.expandOne(p.Word, st)
		}
		return val
	case "-":
		if !set {
			return r.expandOne(p.Word, st)
		}
		return val
	case ":=", "=":
		if (!set || val == "") && p.Op == ":=" || (!set && p.Op == "=") {
			nv := r.expandOne(p.Word, st)
			r.vars[p.Name] = nv
			return nv
		}
		return val
	case ":?":
		if !set || val == "" {
			return ""
		}
		return val
	case ":+":
		if set && val != "" {
			return r.expandOne(p.Word, st)
		}
		return ""
	case "+":
		if set {
			return r.expandOne(p.Word, st)
		}
		return ""
	case "#":
		return trimPrefixShortest(val, r.expandOne(p.Word, st))
	case "##":
		return trimPrefixLongest(val, r.expandOne(p.Word, st))
	case "%":
		return trimSuffixShortest(val, r.expandOne(p.Word, st))
	case "%%":
		return trimSuffixLongest(val, r.expandOne(p.Word, st))
	case "/", "//":
		return substPattern(val, r.expandOne(p.Word, st), p.Op == "//")
	case ":":
		return substrExp(val, r.expandOne(p.Word, st))
	}
	return val
}

// isSet reports whether a parameter is defined (vs expanding to empty).
func (r *Runner) isSet(name string, st *state) bool {
	switch name {
	case "?", "@", "#", "PWD":
		return true
	}
	if n, err := strconv.Atoi(name); err == nil {
		return n >= 1 && n <= len(r.args)
	}
	if _, ok := r.vars[name]; ok {
		return true
	}
	for _, kv := range st.env {
		if strings.HasPrefix(kv, name+"=") {
			return true
		}
	}
	return false
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
