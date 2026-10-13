package exec

import (
	"os"
	"path/filepath"
	"strconv"
)

// isFile / isDir back the test builtin.
func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// globMatch matches name against a glob pattern (* ? [..]).
func globMatch(pat, name string) bool {
	ok, err := filepath.Match(pat, name)
	return err == nil && ok
}

type arithParser struct {
	s       string
	i       int
	resolve func(name string) int64 // resolves a bare identifier to a value
}

func (p *arithParser) ws() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t') {
		p.i++
	}
}

func (p *arithParser) expr() int64 { return p.add() }

func (p *arithParser) add() int64 {
	v := p.mul()
	for {
		p.ws()
		if p.i >= len(p.s) {
			return v
		}
		switch p.s[p.i] {
		case '+':
			p.i++
			v += p.mul()
		case '-':
			p.i++
			v -= p.mul()
		default:
			return v
		}
	}
}

func (p *arithParser) mul() int64 {
	v := p.atom()
	for {
		p.ws()
		if p.i >= len(p.s) {
			return v
		}
		switch p.s[p.i] {
		case '*':
			p.i++
			v *= p.atom()
		case '/':
			p.i++
			d := p.atom()
			if d != 0 {
				v /= d
			}
		case '%':
			p.i++
			d := p.atom()
			if d != 0 {
				v %= d
			}
		default:
			return v
		}
	}
}

func (p *arithParser) atom() int64 {
	p.ws()
	if p.i >= len(p.s) {
		return 0
	}
	if p.s[p.i] == '(' {
		p.i++
		v := p.expr()
		p.ws()
		if p.i < len(p.s) && p.s[p.i] == ')' {
			p.i++
		}
		return v
	}
	if p.s[p.i] == '-' {
		p.i++
		return -p.atom()
	}
	start := p.i
	for p.i < len(p.s) {
		c := p.s[p.i]
		if (c >= '0' && c <= '9') || c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			p.i++
			continue
		}
		break
	}
	tok := p.s[start:p.i]
	if tok == "" {
		return 0
	}
	if n, err := strconv.ParseInt(tok, 10, 64); err == nil {
		return n
	}
	if p.resolve != nil {
		return p.resolve(tok)
	}
	return 0
}
