package syntax

import (
	"fmt"
	"strings"
)

// Parser builds an AST from a token stream. Recursive descent; one function per
// grammar production (see docs/GRAMMAR.md §3). ~2x smaller than the token-table
// approach because there is no bash grammar to disambiguate.
type Parser struct {
	lx  *Lexer
	tok Token
	err error
	buf []Token // token buffer for arbitrary lookahead
	// prevEnd is the exclusive end of the last consumed token, so a node can
	// record where it ended even after the parser has advanced past it.
	prevEnd Pos
}

// Parse parses src into a Script.
func Parse(src string) (*Script, error) {
	p := &Parser{lx: NewLexer(src)}
	p.next()
	if p.err != nil {
		return nil, p.err
	}
	s := &Script{At: Pos{Line: 1, Col: 1}}
	s.Stmts = p.stmtList()
	if p.err != nil {
		return nil, p.err
	}
	if p.tok.Kind != EOF {
		return nil, p.errf("unexpected %s", p.tok)
	}
	return s, nil
}

func (p *Parser) errf(format string, a ...any) error {
	if p.err == nil {
		p.err = fmt.Errorf("line %d: %s", p.tok.Line, fmt.Sprintf(format, a...))
	}
	return p.err
}

func (p *Parser) next() {
	if p.err != nil {
		return
	}
	// Remember where the token we are leaving ended.
	if p.tok.EndLine != 0 {
		p.prevEnd = Pos{Line: p.tok.EndLine, Col: p.tok.EndCol}
	}
	if len(p.buf) > 0 {
		p.tok = p.buf[0]
		p.buf = p.buf[1:]
		return
	}
	p.tok, p.err = p.lx.Next()
}

// peekN returns the token n positions ahead (peekN(1) is the next token).
func (p *Parser) peekN(n int) Token {
	for len(p.buf) < n {
		t, err := p.lx.Next()
		if err != nil {
			p.err = err
			p.buf = append(p.buf, Token{Kind: EOF})
			continue
		}
		p.buf = append(p.buf, t)
	}
	return p.buf[n-1]
}

// peek returns the next token without consuming it.
func (p *Parser) peek() Token {
	return p.peekN(1)
}

// peekIsAssign reports whether the token after the current is a bare `=`.
func (p *Parser) peekIsAssign() bool {
	t := p.peek()
	return t.Kind == WordTok && (t.Lit == "=" || strings.HasPrefix(t.Lit, "="))
}

// skipNewlines consumes statement separators (newlines / semicolons).
func (p *Parser) skipNewlines() {
	// Stop on error too: next() refuses to advance once p.err is set, so
	// looping here on a Newline/Semi token would spin forever.
	for p.err == nil && (p.tok.Kind == Newline || p.tok.Kind == Semi) {
		p.next()
	}
}

// ── statement lists ─────────────────────────────────────────────────────────

// stmtList parses statements until a terminator keyword (then/do/elif/else/end)
// or EOF. It does not consume the terminator.
func (p *Parser) stmtList() []*Stmt {
	var out []*Stmt
	p.skipNewlines()
	for p.err == nil && p.tok.Kind != EOF {
		if p.atTerminator() {
			break
		}
		st := p.stmt()
		if st == nil {
			break
		}
		out = append(out, st)
		// A statement must be followed by a separator, a block terminator
		// keyword, or the end of the list. A terminator keyword (then/do/
		// elif/else/end) acts as an implicit separator, so
		// `then echo x else` parses without a semicolon.
		if p.tok.Kind == Newline || p.tok.Kind == Semi {
			p.skipNewlines()
			continue
		}
		if p.atTerminator() {
			break
		}
		break
	}
	return out
}

// atArgStop reports whether the current token ends a command's argument list.
// Only easysh's own block keywords act as implicit separators; the bash
// spellings (done/fi/esac) require a real `;`/newline, so `echo done` is fine.
func (p *Parser) atArgStop() bool {
	if p.tok.Kind != WordTok {
		return false
	}
	switch p.tok.Lit {
	case "then", "do", "elif", "else", "end":
		return true
	}
	return false
}

// atTerminator reports whether the current token ends a block.
func (p *Parser) atTerminator() bool {
	if p.tok.Kind != WordTok {
		return false
	}
	switch p.tok.Lit {
	case "then", "do", "elif", "else", "end", "done", "fi", "esac":
		return true
	}
	return false
}

// ── statements ──────────────────────────────────────────────────────────────

// stmt parses pipeline (&&| || pipeline)*.
func (p *Parser) stmt() *Stmt {
	pos := Pos{Line: p.tok.Line, Col: p.tok.Col}
	st := &Stmt{At: pos}
	if p.tok.Kind == Bang {
		st.Bang = true
		p.next()
	}
	first := p.pipeline()
	if first == nil {
		return nil
	}
	st.Chain = append(st.Chain, first)
	for p.err == nil && (p.tok.Kind == And || p.tok.Kind == Or) {
		op := p.tok.Lit
		p.next()
		p.skipNewlinesContinuation()
		pl := p.pipeline()
		if pl == nil {
			break
		}
		st.Ops = append(st.Ops, op)
		st.Chain = append(st.Chain, pl)
	}
	return st
}

// skipNewlinesContinuation consumes newlines that follow a trailing operator.
func (p *Parser) skipNewlinesContinuation() {
	for p.tok.Kind == Newline {
		p.next()
	}
}

// pipeline parses stage (| stage)*.
func (p *Parser) pipeline() *Pipeline {
	pl := &Pipeline{At: Pos{Line: p.tok.Line, Col: p.tok.Col}}
	st := p.stage()
	if st == nil {
		return nil
	}
	pl.Stages = append(pl.Stages, st)
	for p.err == nil && (p.tok.Kind == Pipe || p.tok.Kind == PipeErr) {
		pl.Errs = append(pl.Errs, p.tok.Kind == PipeErr)
		p.next()
		p.skipNewlinesContinuation()
		st := p.stage()
		if st == nil {
			break
		}
		pl.Stages = append(pl.Stages, st)
	}
	return pl
}

// stage parses [binding...] (compound | simple) [redirect...].
func (p *Parser) stage() *Stage {
	st := &Stage{At: Pos{Line: p.tok.Line, Col: p.tok.Col}}

	// Leading keyword bindings: `let x = 1 cmd ...` or a bare binding.
	for p.tok.Kind == WordTok && p.isBindingKeyword() {
		b := p.binding()
		if b == nil {
			return nil
		}
		st.Bindings = append(st.Bindings, b)
	}

	// POSIX assignment prefixes: `name=value` (possibly several). Whether they
	// persist or become a per-command environment depends on whether a command
	// follows, so tag them "prefix" for now and settle below.
	prefixes := 0
	for p.tok.Kind == WordTok && isAssignWord(p.tok.Lit) {
		st.Bindings = append(st.Bindings, p.assignWord())
		prefixes++
	}

	// `name = value` (spaced, no keyword) mutates an existing `mut` — the
	// easysh spelling kept for compatibility.
	if p.tok.Kind == WordTok && isIdent(p.tok.Lit) && p.peekIsAssign() {
		st.Bindings = append(st.Bindings, p.assign())
	}

	// A stage that is *only* bindings (no command word) is valid.
	if p.tok.Kind == Newline || p.tok.Kind == Semi || p.tok.Kind == Dsemi ||
		p.tok.Kind == EOF || p.tok.Kind == RParen || p.tok.Kind == RBrace ||
		p.atTerminator() {
		if len(st.Bindings) > 0 {
			// A lone `name=value` persists in the shell (POSIX assignment).
			for _, b := range st.Bindings {
				if b.Mode == "prefix" {
					b.Mode = "assign"
				}
			}
			return st
		}
		return nil
	}

	st.Cmd = p.command()
	if st.Cmd == nil {
		return nil
	}

	// Trailing redirects.
	for p.tok.Kind == RedirectTok {
		r := p.redirect()
		if r == nil {
			return nil
		}
		st.Redirs = append(st.Redirs, r)
	}
	st.At.EndLine, st.At.EndCol = p.prevEnd.Line, p.prevEnd.Col
	return st
}

// isAssignWord reports whether a bare word is a POSIX assignment `name=...`
// (the name must be a valid identifier; `a=b` yes, `a.b=c` no).
func isAssignWord(lit string) bool {
	i := strings.IndexByte(lit, '=')
	if i <= 0 {
		return false
	}
	return isIdent(lit[:i])
}

// assignWord parses `name=value` from the current Word token.
func (p *Parser) assignWord() *Binding {
	pos := Pos{Line: p.tok.Line, Col: p.tok.Col}
	lit := p.tok.Lit
	i := strings.IndexByte(lit, '=')
	name, rest := lit[:i], lit[i+1:]
	b := &Binding{At: pos, Mode: "prefix", Name: name}
	p.next()
	// The value may be glued into the same token (a=b) or be the following
	// word(s) (a= b). Reconstruct a Word from the remainder + any following
	// word tokens that belong to this assignment (no space before `=`).
	if rest != "" {
		b.Value = &Word{At: pos, Parts: splitParts(rest, pos)}
	} else if p.tok.Kind == WordTok && !isAssignWord(p.tok.Lit) && !p.atTerminator() {
		b.Value = p.word()
	} else {
		b.Value = &Word{At: pos, Parts: []WordPart{&Lit{At: pos, Value: "", Quoted: true}}}
	}
	return b
}

// ── bindings ────────────────────────────────────────────────────────────────

func (p *Parser) isBindingKeyword() bool {
	switch p.tok.Lit {
	case "let", "mut", "export":
		// Only a binding if followed by IDENT `=`. Otherwise it is a command name.
		return true
	}
	return false
}

// binding parses `let x = word` / `mut x = word` / `export x = word`, and the
// POSIX glued form `export X=1` / `export PATH="$P"`.
func (p *Parser) binding() *Binding {
	mode := p.tok.Lit
	pos := Pos{Line: p.tok.Line, Col: p.tok.Col}
	p.next()
	if p.tok.Kind != WordTok {
		p.errf("%s: expected a name, got %s", mode, p.tok)
		return nil
	}
	// Glued `name=value` (export X=1): the lexer kept it as one word.
	if isAssignWord(p.tok.Lit) {
		b := p.assignWord()
		b.Mode = mode
		return b
	}
	if !isIdent(p.tok.Lit) {
		p.errf("%s: expected a name, got %s", mode, p.tok)
		return nil
	}
	name := p.tok.Lit
	p.next()
	var val *Word
	if p.tok.Kind == WordTok && p.tok.Lit == "=" {
		p.next()
		val = p.word()
	} else if p.tok.Kind == WordTok && strings.HasPrefix(p.tok.Lit, "=") {
		// `x=1` glued: split the leading '='.
		rest := strings.TrimPrefix(p.tok.Lit, "=")
		p.tok.Lit = rest
		val = p.word()
	} else {
		p.errf("%s %s: expected '=', got %s", mode, name, p.tok)
		return nil
	}
	return &Binding{At: pos, Mode: mode, Name: name, Value: val}
}

// assign parses a bare `name = value` (mutation of an existing `mut`).
func (p *Parser) assign() *Binding {
	pos := Pos{Line: p.tok.Line, Col: p.tok.Col}
	name := p.tok.Lit
	p.next()
	// current token is `=` (possibly glued like `=value`)
	if p.tok.Lit == "=" {
		p.next()
	} else if strings.HasPrefix(p.tok.Lit, "=") {
		p.tok.Lit = strings.TrimPrefix(p.tok.Lit, "=")
	}
	val := p.word()
	return &Binding{At: pos, Mode: "set", Name: name, Value: val}
}

// ── commands ────────────────────────────────────────────────────────────────

// command dispatches on the leading token: a compound keyword or a simple call.
func (p *Parser) command() Node {
	if p.tok.Kind == LParen {
		return p.block(true)
	}
	if p.tok.Kind == LBrace {
		return p.block(false)
	}
	if p.tok.Kind == WordTok {
		switch p.tok.Lit {
		case "if":
			return p.ifCmd()
		case "for":
			return p.forCmd()
		case "while":
			return p.whileCmd()
		case "until":
			return p.untilCmd()
		case "case":
			return p.caseCmd()
		case "fn":
			return p.funcDef()
		}
		// `f()` / `f ()` : a POSIX function definition.
		if isIdent(p.tok.Lit) && p.peekIsLParenRParen() {
			return p.bashFuncDef()
		}
	}
	return p.simple()
}

// simple parses a command word followed by argument words.
func (p *Parser) simple() *Simple {
	if p.tok.Kind != WordTok {
		p.errf("expected a command, got %s", p.tok)
		return nil
	}
	s := &Simple{At: Pos{Line: p.tok.Line, Col: p.tok.Col}}
	s.Args = append(s.Args, p.word())
	for p.tok.Kind == WordTok && !p.atArgStop() {
		s.Args = append(s.Args, p.word())
	}
	return s
}

// ifCmd parses if/elif/else/end.
func (p *Parser) ifCmd() *IfCmd {
	c := &IfCmd{At: Pos{Line: p.tok.Line, Col: p.tok.Col}}
	p.next() // if
	c.Cond = p.stmtList()
	if !p.expect("then") {
		return nil
	}
	c.Then = p.stmtList()
	for p.tok.Kind == WordTok && p.tok.Lit == "elif" {
		p.next()
		cond := p.stmtList()
		if !p.expect("then") {
			return nil
		}
		body := p.stmtList()
		c.Elifs = append(c.Elifs, IfBranch{Cond: cond, Then: body})
	}
	if p.tok.Kind == WordTok && p.tok.Lit == "else" {
		p.next()
		c.Else = p.stmtList()
	}
	if !p.expectEnd() {
		return nil
	}
	return c
}

// forCmd parses `for NAME in words do ... end`.
func (p *Parser) forCmd() *ForCmd {
	c := &ForCmd{At: Pos{Line: p.tok.Line, Col: p.tok.Col}}
	p.next() // for
	if p.tok.Kind != WordTok || !isIdent(p.tok.Lit) {
		p.errf("for: expected a name, got %s", p.tok)
		return nil
	}
	c.Name = p.tok.Lit
	p.next()
	if !p.expect("in") {
		return nil
	}
	for p.tok.Kind == WordTok && !p.isKeyword("do") {
		c.In = append(c.In, p.word())
	}
	// A separator may sit between the `in` list and `do` (for i in a b; do ...).
	p.skipNewlines()
	if !p.expect("do") {
		return nil
	}
	c.Body = p.stmtList()
	if !p.expectEnd() {
		return nil
	}
	return c
}

// whileCmd parses `while cond do ... end`.
func (p *Parser) whileCmd() *WhileCmd {
	c := &WhileCmd{At: Pos{Line: p.tok.Line, Col: p.tok.Col}}
	p.next() // while
	c.Cond = p.stmtList()
	if !p.expect("do") {
		return nil
	}
	c.Body = p.stmtList()
	if !p.expectEnd() {
		return nil
	}
	return c
}

// caseCmd parses `case word in pat => body ... end`, and also the POSIX form
// `case word in pat) body ;; ... esac` (patterns close with `)`, arms with
// `;;`).
func (p *Parser) caseCmd() *CaseCmd {
	c := &CaseCmd{At: Pos{Line: p.tok.Line, Col: p.tok.Col}}
	p.next() // case
	c.Word = p.word()
	if !p.expect("in") {
		return nil
	}
	p.skipNewlines()
	for p.err == nil && !p.isEnd() && p.tok.Kind != EOF {
		arm := CaseArm{}
		for {
			arm.Pats = append(arm.Pats, p.word())
			if p.tok.Kind == Pipe {
				p.next()
				continue
			}
			break
		}
		// A POSIX arm closes its pattern list with `)`; easysh uses `=>`.
		if p.tok.Kind == RParen {
			p.next()
		} else if !p.expect("=>") {
			return nil
		}
		arm.Body = p.caseBody()
		c.Arms = append(c.Arms, arm)
		// POSIX arms end with `;;` (or the next pattern / esac).
		if p.tok.Kind == Dsemi {
			p.next()
		}
		p.skipNewlines()
	}
	if !p.expectEnd() {
		return nil
	}
	return c
}

// caseBody parses an arm body: statements until `end` or the start of the next
// arm (a line whose first token is a pattern followed by `=>`). Because a case
// arm's body has no explicit terminator, we stop when the upcoming tokens look
// like `word... =>` or when we reach `end`.
func (p *Parser) caseBody() []*Stmt {
	var out []*Stmt
	p.skipNewlines()
	for p.err == nil && p.tok.Kind != EOF && !p.isEnd() && p.tok.Kind != Dsemi {
		if p.looksLikeArmStart() {
			break
		}
		st := p.stmt()
		if st == nil {
			break
		}
		out = append(out, st)
		if p.tok.Kind == Newline || p.tok.Kind == Semi {
			p.skipNewlines()
			continue
		}
		if p.isEnd() || p.looksLikeArmStart() {
			break
		}
		break
	}
	return out
}

// looksLikeArmStart reports whether the current position begins a new case arm:
// one or more words then `=>`. It peeks without consuming.
func (p *Parser) looksLikeArmStart() bool {
	if p.tok.Kind != WordTok {
		return false
	}
	// Scan ahead on the same logical line: words and `|` then `=>`.
	for i := 1; i <= 64; i++ {
		t := p.peekN(i)
		switch {
		case t.Kind == WordTok && t.Lit == "=>":
			return true
		case t.Kind == Pipe:
			continue
		case t.Kind == WordTok && (isIdent(t.Lit) || strings.HasPrefix(t.Lit, "*")):
			continue
		default:
			return false
		}
	}
	return false
}

// block parses ( ... ) or { ... }.
func (p *Parser) block(subshell bool) *Block {
	b := &Block{At: Pos{Line: p.tok.Line, Col: p.tok.Col}, Subshell: subshell}
	open, close := "}", "{"
	if subshell {
		open, close = ")", "("
	}
	_ = open
	p.next() // ( or {
	b.Body = p.stmtList()
	if p.tok.Kind != RParen && p.tok.Kind != RBrace {
		p.errf("expected %s, got %s", close, p.tok)
		return nil
	}
	p.next()
	return b
}

// funcDef parses `fn name(p1, p2) { ... }`.
func (p *Parser) funcDef() *FuncDef {
	f := &FuncDef{At: Pos{Line: p.tok.Line, Col: p.tok.Col}}
	p.next() // fn
	if p.tok.Kind != WordTok || !isIdent(p.tok.Lit) {
		p.errf("fn: expected a name, got %s", p.tok)
		return nil
	}
	f.Name = p.tok.Lit
	p.next()
	if p.tok.Kind != LParen {
		p.errf("fn %s: expected '(', got %s", f.Name, p.tok)
		return nil
	}
	p.next() // (
	// Params are `a, b, c`; the lexer keeps `a,` as one word, so split on `,`.
	for p.tok.Kind == WordTok && p.tok.Lit != ")" {
		for _, part := range strings.Split(p.tok.Lit, ",") {
			part = strings.TrimSpace(part)
			if isIdent(part) {
				f.Params = append(f.Params, part)
			}
		}
		p.next()
	}
	if p.tok.Kind != RParen {
		p.errf("fn %s: expected ')', got %s", f.Name, p.tok)
		return nil
	}
	p.next() // )
	if p.tok.Kind != LBrace {
		p.errf("fn %s: expected '{', got %s", f.Name, p.tok)
		return nil
	}
	blk := p.block(false)
	if blk == nil {
		return nil
	}
	f.Body = blk.Body
	return f
}

// peekIsLParenRParen reports whether the next two tokens are `(` `)` — the
// `name()` prefix of a POSIX function definition.
func (p *Parser) peekIsLParenRParen() bool {
	return p.peekN(1).Kind == LParen && p.peekN(2).Kind == RParen
}

// untilCmd parses `until cond do ... end` (POSIX: loop while cond is false).
func (p *Parser) untilCmd() *WhileCmd {
	c := &WhileCmd{At: Pos{Line: p.tok.Line, Col: p.tok.Col}, Until: true}
	p.next() // until
	c.Cond = p.stmtList()
	if !p.expect("do") {
		return nil
	}
	c.Body = p.stmtList()
	if !p.expectEnd() {
		return nil
	}
	return c
}

// bashFuncDef parses `name() { ... }` / `name () { ... }` (POSIX).
func (p *Parser) bashFuncDef() *FuncDef {
	f := &FuncDef{At: Pos{Line: p.tok.Line, Col: p.tok.Col}}
	f.Name = p.tok.Lit
	p.next() // name
	p.next() // (
	p.next() // )
	p.skipNewlines()
	if p.tok.Kind != LBrace {
		p.errf("%s(): expected '{', got %s", f.Name, p.tok)
		return nil
	}
	blk := p.block(false)
	if blk == nil {
		return nil
	}
	f.Body = blk.Body
	return f
}

// ── redirects ───────────────────────────────────────────────────────────────

// redirect parses one redirection.
func (p *Parser) redirect() *Redirect {
	op := p.tok.Lit
	r := &Redirect{At: Pos{Line: p.tok.Line, Col: p.tok.Col}, Op: op}
	switch op {
	case ">", ">>", "&>", "&>>":
		r.Fd = 1
	case "2>", "2>>":
		r.Fd = 2
	case "<":
		r.Fd = 0
	case ">&", "<&":
		r.Fd = 1
	case "2>&":
		r.Fd = 2
	case "<<", "<<<":
		r.Here = true
		r.Fd = 0
	}
	p.next()
	// `>&-` / `<&-` carry no target.
	if op == ">&-" || op == "2>&-" || op == "<&-" {
		return r
	}
	if p.tok.Kind != WordTok {
		p.errf("%s: expected a target, got %s", op, p.tok)
		return nil
	}
	r.Target = p.word()
	return r
}

// ── words ───────────────────────────────────────────────────────────────────

// expect consumes a keyword and advances.
func (p *Parser) expect(kw string) bool {
	if p.tok.Kind == WordTok && p.tok.Lit == kw {
		p.next()
		p.skipNewlines()
		return true
	}
	p.errf("expected %q, got %s", kw, p.tok)
	return false
}

func (p *Parser) isKeyword(kw string) bool {
	return p.tok.Kind == WordTok && p.tok.Lit == kw
}

// endKeywords: easysh closes blocks with `end`, but we also accept the bash
// spellings (done/fi/esac) so AI-generated / pasted bash runs unmodified.
var endKeywords = map[string]bool{"end": true, "done": true, "fi": true, "esac": true}

// isEnd reports whether the current token closes a block.
func (p *Parser) isEnd() bool {
	return p.tok.Kind == WordTok && endKeywords[p.tok.Lit]
}

// expectEnd consumes the block terminator (end, or its bash alias).
func (p *Parser) expectEnd() bool {
	if !p.isEnd() {
		p.errf("expected a block terminator (end/done/fi/esac), got %s", p.tok)
		return false
	}
	p.next()
	return true
}

// word parses one word from a single Word token, splitting it into parts.
func (p *Parser) word() *Word {
	if p.tok.Kind != WordTok {
		p.errf("expected a word, got %s", p.tok)
		return nil
	}
	w := &Word{At: Pos{Line: p.tok.Line, Col: p.tok.Col}}
	w.Parts = splitParts(p.tok.Lit, w.At)
	p.next()
	return w
}

// splitParts re-scans the raw text of a word into Lit / Param / CmdSub / Arith
// parts. (The lexer already validated quoting; this is a second, cheap pass.)
func splitParts(raw string, base Pos) []WordPart {
	var parts []WordPart
	var lit strings.Builder
	flush := func(quoted bool) {
		if lit.Len() > 0 {
			parts = append(parts, &Lit{At: base, Value: lit.String(), Quoted: quoted})
			lit.Reset()
		}
	}
	i := 0
	for i < len(raw) {
		c := raw[i]
		switch {
		case c == '\'':
			j := i + 1
			for j < len(raw) && raw[j] != '\'' {
				j++
			}
			parts = append(parts, &Lit{At: base, Value: raw[i+1 : j], Quoted: true})
			i = j + 1
		case c == '"':
			j := i + 1
			var sub strings.Builder
			flushSub := func() {
				if sub.Len() > 0 {
					parts = append(parts, &Lit{At: base, Value: sub.String(), Quoted: true})
					sub.Reset()
				}
			}
			for j < len(raw) && raw[j] != '"' {
				if raw[j] == '\\' && j+1 < len(raw) {
					// Inside double quotes only " $ \ ` (and newline) are
					// escapable; anything else keeps the backslash (so
					// "\t" stays a literal backslash-t, as in bash).
					switch raw[j+1] {
					case '"', '$', '\\', '`':
						sub.WriteByte(raw[j+1])
					default:
						sub.WriteByte('\\')
						sub.WriteByte(raw[j+1])
					}
					j += 2
					continue
				}
				if raw[j] == '$' && j+1 < len(raw) && raw[j+1] == '(' {
					flushSub()
					k := matchingParen(raw, j+1)
					bodyStart := posAt(raw, j+2, base)
					parts = append(parts, &CmdSub{At: posAt(raw, j, base), Body: parseInline(raw[j+2:k], bodyStart)})
					j = k + 1
					continue
				}
				if raw[j] == '$' {
					flushSub()
					pp, n := readParamPart(raw, j+1, posAt(raw, j, base))
					parts = append(parts, pp)
					j = n
					continue
				}
				sub.WriteByte(raw[j])
				j++
			}
			flushSub()
			i = j + 1
		case c == '$' && i+1 < len(raw) && raw[i+1] == '(':
			// $(( = arithmetic, $( = command substitution
			if i+2 < len(raw) && raw[i+2] == '(' {
				flush(false)
				k := matchingArith(raw, i+2)
				parts = append(parts, &Arith{At: base, Raw: raw[i+3 : k]})
				i = k + 1
				continue
			}
			flush(false)
			k := matchingParen(raw, i+1)
			bodyStart := posAt(raw, i+2, base)
			parts = append(parts, &CmdSub{At: posAt(raw, i, base), Body: parseInline(raw[i+2:k], bodyStart)})
			i = k + 1
		case c == '$':
			pp, n := readParamPart(raw, i+1, posAt(raw, i, base))
			flush(false)
			parts = append(parts, pp)
			i = n
		case c == '`':
			// Backtick command substitution: run the body like $(...).
			flush(false)
			j := i + 1
			var body strings.Builder
			for j < len(raw) && raw[j] != '`' {
				if raw[j] == '\\' && j+1 < len(raw) {
					body.WriteByte(raw[j+1])
					j += 2
					continue
				}
				body.WriteByte(raw[j])
				j++
			}
			parts = append(parts, &CmdSub{At: posAt(raw, i, base), Body: parseInline(body.String(), posAt(raw, i+1, base))})
			i = j + 1
		case c == '\\' && i+1 < len(raw):
			lit.WriteByte(raw[i+1])
			i += 2
		default:
			lit.WriteByte(c)
			i++
		}
	}
	flush(false)
	if len(parts) == 0 {
		parts = append(parts, &Lit{At: base, Value: "", Quoted: true})
	}
	return parts
}

// readParamPart reads a parameter reference starting at i (after '$') and
// returns a Param plus the index just past it. Handles `$x`, `$1`, `$?` and the
// braced forms `${x}` and POSIX expansions `${x:-def}`, `${#x}`, `${x#p}`,
// `${x%%p}`, `${x/a/b}`, `${x:off:len}`.
func readParamPart(s string, i int, base Pos) (*Param, int) {
	if i >= len(s) {
		return &Param{At: base}, i
	}
	c := s[i]
	if c != '{' {
		// special one-char params
		if c == '?' || c == '#' || c == '@' || c == '$' || c == '*' || c == '!' || c == '-' || (c >= '0' && c <= '9') {
			return &Param{At: base, Name: string(c)}, i + 1
		}
		j := i
		for j < len(s) && isIdentByte(s[j], j == i) {
			j++
		}
		return &Param{At: base, Name: s[i:j]}, j
	}
	// ${ ... }: find the matching close brace (nesting ${}).
	j := i + 1
	depth := 1
	for j < len(s) && depth > 0 {
		switch s[j] {
		case '{':
			depth++
		case '}':
			depth--
		}
		j++
	}
	inner := s[i+1 : j-1]
	p := parseBracedParam(inner, base)
	return p, j
}

// parseBracedParam parses the text between `${` and `}` into a Param.
func parseBracedParam(inner string, base Pos) *Param {
	p := &Param{At: base}
	// `${#x}` -> length of x, but only when what follows `#` is a bare parameter
	// name with nothing else (so `${#x}` is length while `${#*/}` is the prefix
	// operator with pattern `*/`).
	if strings.HasPrefix(inner, "#") && len(inner) > 1 {
		rest := inner[1:]
		if isParamName(rest) {
			p.Name = rest
			p.Op = "#len"
			return p
		}
	}
	// The name is the leading identifier / special char.
	ni := 0
	if inner == "" {
		return p
	}
	if inner[0] == '?' || inner[0] == '@' || inner[0] == '#' || inner[0] == '$' ||
		inner[0] == '*' || inner[0] == '!' || inner[0] == '-' ||
		(inner[0] >= '0' && inner[0] <= '9') {
		ni = 1
	} else {
		for ni < len(inner) && isIdentByte(inner[ni], ni == 0) {
			ni++
		}
	}
	p.Name = inner[:ni]
	rest := inner[ni:]
	if rest == "" {
		return p
	}
	// Operators (longest match first).
	for _, op := range []string{":-", ":=", ":?", ":+", "##", "%%", "//", "-", "=", "?", "+", "#", "%", "/", ":"} {
		if strings.HasPrefix(rest, op) {
			p.Op = op
			arg := rest[len(op):]
			if arg != "" {
				p.Word = &Word{At: base, Parts: splitParts(arg, base)}
			}
			return p
		}
	}
	return p
}

// isParamName reports whether s is exactly a parameter name (identifier, digit,
// or one of the special params) with no trailing operator.
func isParamName(s string) bool {
	if s == "" {
		return false
	}
	switch s[0] {
	case '?', '@', '#', '$', '*', '!', '-':
		return len(s) == 1
	}
	if s[0] >= '0' && s[0] <= '9' {
		for i := 0; i < len(s); i++ {
			if s[i] < '0' || s[i] > '9' {
				return false
			}
		}
		return true
	}
	return isIdent(s)
}

// isParamOpChar reports whether c can start an expansion operator (so `${#x}`
// is a length op but `${#}` is the `#` parameter).
func isParamOpChar(c byte) bool {
	switch c {
	case ':', '-', '=', '?', '+', '#', '%', '/':
		return true
	}
	return false
}

// matchingParen returns the index of the ')' matching the '(' at i.
func matchingParen(s string, i int) int {
	depth := 0
	for ; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(s)
}

// matchingArith returns the index of the ')' closing `$((` (the second one).
func matchingArith(s string, i int) int {
	// i points at the second '('.
	first := matchingParen(s, i)
	if first+1 < len(s) && s[first+1] == ')' {
		return first + 1
	}
	return first
}

// parseInline parses a sub-script for $( ... ) bodies. start is the absolute
// source position of src's first byte within the enclosing word, so the parsed
// body's positions are rebased to absolute coordinates (otherwise every $()
// body would report positions relative to its own substring). Errors are
// swallowed into an empty body (the outer lexer already balanced the parens).
func parseInline(src string, start Pos) []*Stmt {
	sc, err := Parse(src)
	if err != nil {
		return nil
	}
	rebaseStmts(sc.Stmts, start)
	return sc.Stmts
}

// isIdent reports whether s is a valid identifier.
func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isIdentByte(s[i], i == 0) {
			return false
		}
	}
	return true
}

func isIdentByte(c byte, first bool) bool {
	if c == '_' {
		return true
	}
	if c >= 'a' && c <= 'z' {
		return true
	}
	if c >= 'A' && c <= 'Z' {
		return true
	}
	if !first && c >= '0' && c <= '9' {
		return true
	}
	return false
}
