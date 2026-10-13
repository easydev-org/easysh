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

// atTerminator reports whether the current token ends a block.
func (p *Parser) atTerminator() bool {
	if p.tok.Kind != WordTok {
		return false
	}
	switch p.tok.Lit {
	case "then", "do", "elif", "else", "end":
		return true
	}
	return false
}

// ── statements ──────────────────────────────────────────────────────────────

// stmt parses pipeline (&&| || pipeline)*.
func (p *Parser) stmt() *Stmt {
	pos := Pos{Line: p.tok.Line, Col: p.tok.Col}
	st := &Stmt{At: pos}
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

	// Leading bindings: `let x = 1 cmd ...` (rare but legal) or a bare binding.
	for p.tok.Kind == WordTok && p.isBindingKeyword() {
		b := p.binding()
		if b == nil {
			return nil
		}
		st.Bindings = append(st.Bindings, b)
	}

	// A bare assignment `name = value` (no keyword) mutates an existing `mut`.
	if p.tok.Kind == WordTok && isIdent(p.tok.Lit) && p.peekIsAssign() {
		st.Bindings = append(st.Bindings, p.assign())
	}

	// A stage that is *only* bindings (no command word) is valid.
	if p.tok.Kind == Newline || p.tok.Kind == Semi ||
		p.tok.Kind == EOF || p.tok.Kind == RParen || p.atTerminator() {
		if len(st.Bindings) > 0 {
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

// ── bindings ────────────────────────────────────────────────────────────────

func (p *Parser) isBindingKeyword() bool {
	switch p.tok.Lit {
	case "let", "mut", "export":
		// Only a binding if followed by IDENT `=`. Otherwise it is a command name.
		return true
	}
	return false
}

// binding parses `let x = word` / `mut x = word` / `export x = word`.
func (p *Parser) binding() *Binding {
	mode := p.tok.Lit
	pos := Pos{Line: p.tok.Line, Col: p.tok.Col}
	p.next()
	if p.tok.Kind != WordTok || !isIdent(p.tok.Lit) {
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
		case "case":
			return p.caseCmd()
		case "fn":
			return p.funcDef()
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
	for p.tok.Kind == WordTok && !p.atTerminator() {
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
	if !p.expect("end") {
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
	if !p.expect("end") {
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
	if !p.expect("end") {
		return nil
	}
	return c
}

// caseCmd parses `case word in pat => body ... end`.
func (p *Parser) caseCmd() *CaseCmd {
	c := &CaseCmd{At: Pos{Line: p.tok.Line, Col: p.tok.Col}}
	p.next() // case
	c.Word = p.word()
	if !p.expect("in") {
		return nil
	}
	p.skipNewlines()
	for p.err == nil && !p.isKeyword("end") && p.tok.Kind != EOF {
		arm := CaseArm{}
		for {
			arm.Pats = append(arm.Pats, p.word())
			if p.tok.Kind == Pipe {
				p.next()
				continue
			}
			break
		}
		if !p.expect("=>") {
			return nil
		}
		arm.Body = p.caseBody()
		c.Arms = append(c.Arms, arm)
		p.skipNewlines()
	}
	if !p.expect("end") {
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
	for p.err == nil && p.tok.Kind != EOF && !p.isKeyword("end") {
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
		if p.isKeyword("end") || p.looksLikeArmStart() {
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
	case "<<", "<<<":
		r.Here = true
		r.Fd = 0
	}
	p.next()
	// `>&-` / `<&-` carry no target.
	if op == ">&-" || op == "<&-" {
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
					name, n := readParam(raw, j+1)
					parts = append(parts, &Param{At: base, Name: name})
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
			name, n := readParam(raw, i+1)
			flush(false)
			parts = append(parts, &Param{At: base, Name: name})
			i = n
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

// readParam reads a variable name (or special char) starting at i (after '$').
func readParam(s string, i int) (string, int) {
	if i >= len(s) {
		return "", i
	}
	c := s[i]
	// special one-char params
	if c == '?' || c == '#' || c == '@' || c == '$' || (c >= '0' && c <= '9') {
		return string(c), i + 1
	}
	if c == '{' {
		j := i + 1
		for j < len(s) && s[j] != '}' {
			j++
		}
		name := s[i+1 : j]
		if j < len(s) {
			j++
		}
		return name, j
	}
	j := i
	for j < len(s) && isIdentByte(s[j], j == i) {
		j++
	}
	return s[i:j], j
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
