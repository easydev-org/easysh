package daemon

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/easydev-org/easysh/pkg/proto"
	"github.com/easydev-org/easysh/pkg/syntax"
)

// keywords of the easysh language (block terminators included).
var keywords = map[string]bool{
	"if": true, "then": true, "elif": true, "else": true, "end": true,
	"for": true, "in": true, "do": true, "while": true, "case": true,
	"fn": true, "return": true, "let": true, "mut": true, "export": true,
	"unset": true, "break": true, "continue": true, "set": true,
}

// builtins of the easysh language.
var builtins = map[string]bool{
	"echo": true, "test": true, "[": true, "read": true, "cd": true,
	"pwd": true, "export": true, "unset": true, "exit": true, "return": true,
	"break": true, "continue": true, "true": true, "false": true, ":": true,
	"set": true,
}

// Highlight tokenizes a line with the real easysh lexer and returns colored
// spans. Because the parser is ours, highlighting needs no second lexer.
func Highlight(line string) []proto.Span {
	lx := syntax.NewLexer(line)
	var spans []proto.Span
	commandPos := true
	for {
		t, err := lx.Next()
		if err != nil || t.Kind == syntax.EOF {
			break
		}
		// Col is 1-based; convert to a byte offset by re-lexing is overkill, so
		// we track an offset by len(t.Lit) with the raw lit for operators.
		lit := t.Lit
		if lit == "\n" {
			continue
		}
		class := ""
		switch t.Kind {
		case syntax.SglQuoted, syntax.DblQuoted:
			class = "string"
		case syntax.ParamExp:
			class = "param"
		case syntax.CmdSubst:
			class = "cmdsubst"
		case syntax.ArithExp:
			class = "arith"
		case syntax.And, syntax.Or, syntax.Pipe, syntax.PipeErr, syntax.Semi,
			syntax.RedirectTok, syntax.LParen, syntax.RParen, syntax.LBrace, syntax.RBrace:
			class = "operator"
		case syntax.WordTok:
			if commandPos && keywords[lit] {
				class = "keyword"
			} else if commandPos && builtins[lit] {
				class = "builtin"
			} else if commandPos {
				class = "command"
			}
		}
		commandPos = t.Kind == syntax.Semi || t.Kind == syntax.Pipe ||
			t.Kind == syntax.PipeErr || t.Kind == syntax.And || t.Kind == syntax.Or ||
			t.Kind == syntax.LParen || t.Kind == syntax.LBrace
		if class == "" {
			continue
		}
		spans = append(spans, proto.Span{Start: t.Col - 1, End: t.Col - 1 + len(lit), Class: class})
	}
	return spans
}

// Complete returns completion candidates for a partial word: session variables
// and functions first, then builtins/keywords, then files in the cwd.
func (s *Session) Complete(prefix string) []proto.Completion {
	var out []proto.Completion
	seen := map[string]bool{}
	add := func(text, kind, desc string) {
		if text == "" || seen[text] || !strings.HasPrefix(text, prefix) {
			return
		}
		seen[text] = true
		out = append(out, proto.Completion{Text: text, Kind: kind, Desc: desc})
	}
	// variables + functions from the runner
	for name := range s.runner.Vars() {
		add(name, "variable", "")
	}
	for name := range s.runner.Functions() {
		add(name, "function", "")
	}
	for kw := range keywords {
		add(kw, "keyword", "")
	}
	for b := range builtins {
		add(b, "builtin", "")
	}
	// files in the session dir
	dir := s.Dir()
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				name += "/"
			}
			add(name, "file", filepath.Join(dir, e.Name()))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return kindRank(out[i].Kind) < kindRank(out[j].Kind)
		}
		return out[i].Text < out[j].Text
	})
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}

func kindRank(k string) int {
	switch k {
	case "variable":
		return 0
	case "function":
		return 1
	case "builtin":
		return 2
	case "keyword":
		return 3
	case "file":
		return 4
	default:
		return 5
	}
}
