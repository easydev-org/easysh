package exec

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// isFile / isDir back the test builtin.
func isFile(p, dir string) bool {
	st, err := os.Stat(resolvePath(p, dir))
	return err == nil && !st.IsDir()
}

func isDir(p, dir string) bool {
	st, err := os.Stat(resolvePath(p, dir))
	return err == nil && st.IsDir()
}

// resolvePath resolves a possibly-relative path against the session dir (dir),
// so `test -f package.json` sees the file in the shell's cwd — not the daemon
// process's cwd.
func resolvePath(p, dir string) string {
	if p == "" || filepath.IsAbs(p) || dir == "" {
		return p
	}
	return filepath.Join(dir, p)
}

// globMatch matches name against a glob pattern (* ? [..]).
func globMatch(pat, name string) bool {
	ok, err := filepath.Match(pat, name)
	return err == nil && ok
}

// withPWD returns env with PWD set to dir (replacing any existing PWD), so an
// external command sees the right working directory.
func withPWD(env []string, dir string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, "PWD=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "PWD="+dir)
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
