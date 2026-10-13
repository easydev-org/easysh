package syntax

import (
	"fmt"
	"strings"
)

// Lexer turns source text into tokens. It is a hand-written scanner; there is no
// separate token-generation step (a big chunk of mvdan's ~2k lines of token
// tables and their generators).
type Lexer struct {
	src  string
	off  int
	line int
	col  int

	// pending is a token buffered by peek/unread.
	pending *Token
	// depth of ( { so newlines inside them do not terminate statements.
	parenDepth int
	// lastKind lets us decide "command position" (start of a stage) for
	// keyword promotion and for `#` comment recognition.
	lastKind Kind
	lastSet  bool
}

// NewLexer creates a lexer over src.
func NewLexer(src string) *Lexer {
	return &Lexer{src: src, line: 1, col: 1}
}

// Lexer position helpers.
func (l *Lexer) eof() bool { return l.off >= len(l.src) }

func (l *Lexer) peekByte() byte {
	if l.eof() {
		return 0
	}
	return l.src[l.off]
}

func (l *Lexer) peekAt(n int) byte {
	if l.off+n >= len(l.src) {
		return 0
	}
	return l.src[l.off+n]
}

func (l *Lexer) advance() byte {
	c := l.src[l.off]
	l.off++
	if c == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return c
}

// at returns the current position.
func (l *Lexer) at() Pos { return Pos{Line: l.line, Col: l.col} }

// Next returns the next token, or a token with Kind == EOF at end of input.
func (l *Lexer) Next() (Token, error) {
	if l.pending != nil {
		t := *l.pending
		l.pending = nil
		l.lastKind, l.lastSet = t.Kind, true
		return t, nil
	}
	t, err := l.scan()
	if err != nil {
		return Token{}, err
	}
	t.EndLine, t.EndCol = l.line, l.col
	l.lastKind, l.lastSet = t.Kind, true
	return t, nil
}

// Peek returns the next token without consuming it.
func (l *Lexer) Peek() (Token, error) {
	if l.pending == nil {
		t, err := l.scan()
		if err != nil {
			return Token{}, err
		}
		t.EndLine, t.EndCol = l.line, l.col
		l.pending = &t
	}
	return *l.pending, nil
}

// commandPos reports whether the next word starts a command (so keywords and
// `#` comments apply).
func (l *Lexer) commandPos() bool {
	if !l.lastSet {
		return true
	}
	switch l.lastKind {
	case Semi, And, Or, Pipe, PipeErr, Newline, LParen, LBrace:
		return true
	default:
		return false
	}
}

// atWordStart reports whether the lexer is positioned at the start of a word.
// scan() only reaches the `#` check between tokens (scanWord consumes word-
// internal `#`), so a `#` seen here is always at a word start.
func (l *Lexer) atWordStart() bool { return true }

// scan produces the next raw token.
func (l *Lexer) scan() (Token, error) {
	for {
		// skip horizontal whitespace
		for !l.eof() {
			c := l.peekByte()
			if c == ' ' || c == '\t' {
				l.advance()
				continue
			}
			// line continuation: backslash-newline
			if c == '\\' && l.peekAt(1) == '\n' {
				l.advance()
				l.advance()
				continue
			}
			break
		}
		if l.eof() {
			return Token{Kind: EOF, Line: l.line, Col: l.col}, nil
		}
		c := l.peekByte()

		// A `#` starts a comment when it begins a word (start of line or right
		// after whitespace/operator) — so `a#b` is one word but `a #b` is not.
		if c == '#' && l.atWordStart() {
			for !l.eof() && l.peekByte() != '\n' {
				l.advance()
			}
			continue
		}
		if c == '\n' {
			pos := l.at()
			l.advance()
			// Inside ( ) { } a newline is just whitespace.
			if l.parenDepth > 0 {
				continue
			}
			return Token{Kind: Newline, Lit: "\n", Line: pos.Line, Col: pos.Col}, nil
		}
		return l.scanOpOrWord()
	}
}

// scanOpOrWord reads an operator token if one starts here, else a word.
func (l *Lexer) scanOpOrWord() (Token, error) {
	pos := l.at()
	c := l.peekByte()

	two := func(s string) bool { return strings.HasPrefix(l.src[l.off:], s) }

	switch {
	case two(";;"):
		l.advance()
		l.advance()
		return Token{Kind: Dsemi, Lit: ";;", Line: pos.Line, Col: pos.Col}, nil
	case two("=>"):
		l.advance()
		l.advance()
		return Token{Kind: WordTok, Lit: "=>", Line: pos.Line, Col: pos.Col}, nil
	case two("&&"):
		l.advance()
		l.advance()
		return Token{Kind: And, Lit: "&&", Line: pos.Line, Col: pos.Col}, nil
	case two("||"):
		l.advance()
		l.advance()
		return Token{Kind: Or, Lit: "||", Line: pos.Line, Col: pos.Col}, nil
	case two("|&"):
		l.advance()
		l.advance()
		return Token{Kind: PipeErr, Lit: "|&", Line: pos.Line, Col: pos.Col}, nil
	case two(">>"):
		l.advance()
		l.advance()
		return Token{Kind: RedirectTok, Lit: ">>", Line: pos.Line, Col: pos.Col}, nil
	case two("&>"):
		l.advance()
		l.advance()
		return Token{Kind: RedirectTok, Lit: "&>", Line: pos.Line, Col: pos.Col}, nil
	case two("&>>"):
		l.advance()
		l.advance()
		l.advance()
		return Token{Kind: RedirectTok, Lit: "&>>", Line: pos.Line, Col: pos.Col}, nil
	case two(">&"):
		l.advance()
		l.advance()
		if l.peekByte() == '-' {
			l.advance()
			return Token{Kind: RedirectTok, Lit: ">&-", Line: pos.Line, Col: pos.Col}, nil
		}
		return Token{Kind: RedirectTok, Lit: ">&", Line: pos.Line, Col: pos.Col}, nil
	case two("<&"):
		l.advance()
		l.advance()
		if l.peekByte() == '-' {
			l.advance()
			return Token{Kind: RedirectTok, Lit: "<&-", Line: pos.Line, Col: pos.Col}, nil
		}
		return Token{Kind: RedirectTok, Lit: "<&", Line: pos.Line, Col: pos.Col}, nil
	case two("<<"):
		l.advance()
		l.advance()
		if l.peekByte() == '<' { // here-string
			l.advance()
			return Token{Kind: RedirectTok, Lit: "<<<", Line: pos.Line, Col: pos.Col}, nil
		}
		return Token{Kind: RedirectTok, Lit: "<<", Line: pos.Line, Col: pos.Col}, nil
	case two("2>") && !l.wordCharAfter(2):
		l.advance()
		l.advance()
		// `2>&1` / `2>&-` : fd duplication, not a file redirect.
		if l.peekByte() == '&' {
			l.advance()
			if l.peekByte() == '-' {
				l.advance()
				return Token{Kind: RedirectTok, Lit: "2>&-", Line: pos.Line, Col: pos.Col}, nil
			}
			// 2>&1 duplicates stderr onto the target fd; keep it distinct from
			// >&1 so applyRedirect knows which fd is the source.
			return Token{Kind: RedirectTok, Lit: "2>&", Line: pos.Line, Col: pos.Col}, nil
		}
		if l.peekByte() == '>' { // 2>>
			l.advance()
			return Token{Kind: RedirectTok, Lit: "2>>", Line: pos.Line, Col: pos.Col}, nil
		}
		return Token{Kind: RedirectTok, Lit: "2>", Line: pos.Line, Col: pos.Col}, nil
	}

	switch c {
	case ';':
		l.advance()
		return Token{Kind: Semi, Lit: ";", Line: pos.Line, Col: pos.Col}, nil
	case '!':
		// `!` is the negation operator only when it stands alone (next char is
		// whitespace/operator/EOF); `!=`/`!x` are ordinary word characters.
		if c2 := l.peekAt(1); c2 != ' ' && c2 != '\t' && c2 != '\n' && c2 != 0 &&
			c2 != ';' && c2 != '|' && c2 != '&' && c2 != '<' && c2 != '>' &&
			c2 != '(' && c2 != ')' && c2 != '{' && c2 != '}' {
			return l.scanWord()
		}
		l.advance()
		return Token{Kind: Bang, Lit: "!", Line: pos.Line, Col: pos.Col}, nil
	case '|':
		l.advance()
		return Token{Kind: Pipe, Lit: "|", Line: pos.Line, Col: pos.Col}, nil
	case '>':
		l.advance()
		return Token{Kind: RedirectTok, Lit: ">", Line: pos.Line, Col: pos.Col}, nil
	case '<':
		l.advance()
		return Token{Kind: RedirectTok, Lit: "<", Line: pos.Line, Col: pos.Col}, nil
	case '(':
		l.advance()
		l.parenDepth++
		return Token{Kind: LParen, Lit: "(", Line: pos.Line, Col: pos.Col}, nil
	case ')':
		l.advance()
		if l.parenDepth > 0 {
			l.parenDepth--
		}
		return Token{Kind: RParen, Lit: ")", Line: pos.Line, Col: pos.Col}, nil
	case '{':
		l.advance()
		l.parenDepth++
		return Token{Kind: LBrace, Lit: "{", Line: pos.Line, Col: pos.Col}, nil
	case '}':
		l.advance()
		if l.parenDepth > 0 {
			l.parenDepth--
		}
		return Token{Kind: RBrace, Lit: "}", Line: pos.Line, Col: pos.Col}, nil
	}
	return l.scanWord()
}

// wordCharAfter reports whether the byte at offset n begins a word char (used to
// distinguish `2>` the redirect from `2>x` a word like `2x`).
func (l *Lexer) wordCharAfter(n int) bool {
	c := l.peekAt(n)
	if c == 0 || c == ' ' || c == '\t' || c == '\n' || c == ';' || c == '|' ||
		c == '&' || c == '<' || c == '>' || c == '(' || c == ')' {
		return false
	}
	// A digit or letter right after `2>` means it was part of a word (`2>x`).
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

// scanWord reads a single- or double-quoted or bare word plus its $-expansions,
// emitting a Word token. The literal text is kept; the parser expands parts.
//
// For the skeleton, a Word token carries the raw source slice and the parser
// re-tokenizes it into parts (see parser.go: wordParts).
func (l *Lexer) scanWord() (Token, error) {
	pos := l.at()
	start := l.off
	for !l.eof() {
		c := l.peekByte()
		if c == ' ' || c == '\t' || c == '\n' {
			break
		}
		if c == ';' || c == '|' || c == '&' || c == '<' || c == '>' ||
			c == '(' || c == ')' {
			break
		}
		if c == '\'' {
			l.advance()
			for !l.eof() && l.peekByte() != '\'' {
				l.advance()
			}
			if l.eof() {
				return Token{}, fmt.Errorf("line %d: unterminated single quote", pos.Line)
			}
			l.advance()
			continue
		}
		if c == '"' {
			l.advance()
			for !l.eof() && l.peekByte() != '"' {
				if l.peekByte() == '\\' {
					l.advance()
					if !l.eof() {
						l.advance()
					}
					continue
				}
				if l.peekByte() == '$' && l.peekAt(1) == '(' {
					l.skipCmdSubst()
					continue
				}
				l.advance()
			}
			if l.eof() {
				return Token{}, fmt.Errorf("line %d: unterminated double quote", pos.Line)
			}
			l.advance()
			continue
		}
		if c == '$' && l.peekAt(1) == '(' {
			l.skipCmdSubst()
			continue
		}
		if c == '$' && l.peekAt(1) == '{' {
			// ${name}: consume through the closing brace.
			l.advance() // $
			l.advance() // {
			for !l.eof() && l.peekByte() != '}' {
				l.advance()
			}
			if !l.eof() {
				l.advance() // }
			}
			continue
		}
		if c == '\\' {
			l.advance()
			if !l.eof() {
				l.advance()
			}
			continue
		}
		if c == '`' {
			// Backtick command substitution (POSIX): consume to the matching
			// backtick, honouring backslash escapes.
			l.advance()
			for !l.eof() && l.peekByte() != '`' {
				if l.peekByte() == '\\' {
					l.advance()
				}
				if !l.eof() {
					l.advance()
				}
			}
			if l.eof() {
				return Token{}, fmt.Errorf("line %d: unterminated backtick", pos.Line)
			}
			l.advance()
			continue
		}
		if c == '{' || c == '}' {
			break
		}
		l.advance()
	}
	lit := l.src[start:l.off]
	if lit == "" {
		return Token{}, fmt.Errorf("line %d: unexpected %q", pos.Line, string(l.peekByte()))
	}
	return Token{Kind: WordTok, Lit: lit, Line: pos.Line, Col: pos.Col}, nil
}

// skipCmdSubst consumes a balanced $( ... ) including nested quotes/parens.
func (l *Lexer) skipCmdSubst() {
	l.advance() // $
	l.advance() // (
	depth := 1
	for !l.eof() && depth > 0 {
		c := l.peekByte()
		switch c {
		case '(':
			depth++
			l.advance()
		case ')':
			depth--
			l.advance()
		case '\'':
			l.advance()
			for !l.eof() && l.peekByte() != '\'' {
				l.advance()
			}
			if !l.eof() {
				l.advance()
			}
		case '"':
			l.advance()
			for !l.eof() && l.peekByte() != '"' {
				if l.peekByte() == '\\' {
					l.advance()
				}
				if !l.eof() {
					l.advance()
				}
			}
			if !l.eof() {
				l.advance()
			}
		default:
			l.advance()
		}
	}
}
