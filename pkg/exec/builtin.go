package exec

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
)

// builtin runs a builtin command. It returns (code, true) when name is a
// builtin, else (0, false).
func (r *Runner) builtin(ctx context.Context, name string, args []string, st *state) (int, bool) {
	out := writerOf(st.stdout, r)
	errw := writerOf(st.stderr, r)
	switch name {
	case "echo":
		noNewline := false
		rest := args
		if len(rest) > 0 && rest[0] == "-n" {
			noNewline = true
			rest = rest[1:]
		}
		io.WriteString(out, strings.Join(rest, " "))
		if !noNewline {
			io.WriteString(out, "\n")
		}
		return 0, true
	case "true":
		return 0, true
	case "false":
		return 1, true
	case ":":
		return 0, true
	case "cd":
		dir := ""
		if len(args) > 0 {
			dir = args[0]
		} else {
			dir = r.param("HOME", st)
		}
		if dir == "" {
			dir = "/"
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(st.dir, dir)
		}
		st.dir = dir
		return 0, true
	case "pwd":
		fmt.Fprintln(out, st.dir)
		return 0, true
	case "exit":
		code := 0
		if len(args) > 0 {
			if n, err := strconv.Atoi(args[0]); err == nil {
				code = n
			}
		}
		r.exiting = true
		r.retCode = code
		return code, true
	case "return":
		code := 0
		if len(args) > 0 {
			if n, err := strconv.Atoi(args[0]); err == nil {
				code = n
			}
		}
		r.returning = true
		r.retCode = code
		return code, true
	case "unset":
		for _, a := range args {
			delete(r.vars, a)
		}
		return 0, true
	case "test", "[":
		return r.test(args, st.dir), true
	case "read":
		return r.read(args, st), true
	case "export":
		for _, a := range args {
			if i := strings.IndexByte(a, '='); i >= 0 {
				st.env = append(st.env, a[:i]+"="+a[i+1:])
				r.vars[a[:i]] = a[i+1:]
			}
		}
		return 0, true
	case "set":
		return 0, true
	case "break", "continue":
		r.loopCtl = name
		return 0, true
	default:
		_ = errw
		_ = ctx
		return 0, false
	}
}

// test implements a minimal `test`/`[` (ints and strings, -f -d -z -n).
func (r *Runner) test(args []string, dir string) int {
	if len(args) > 0 && args[len(args)-1] == "]" {
		args = args[:len(args)-1]
	}
	if len(args) == 1 {
		if args[0] == "" {
			return 1
		}
		return 0
	}
	if len(args) == 2 {
		switch args[0] {
		case "-z":
			if args[1] == "" {
				return 0
			}
			return 1
		case "-n":
			if args[1] != "" {
				return 0
			}
			return 1
		case "-f":
			if isFile(args[1], dir) {
				return 0
			}
			return 1
		case "-d":
			if isDir(args[1], dir) {
				return 0
			}
			return 1
		case "!":
			if args[1] == "" {
				return 0
			}
			return 1
		}
		return 1
	}
	if len(args) == 3 {
		a, op, b := args[0], args[1], args[2]
		switch op {
		case "=", "==":
			if a == b {
				return 0
			}
			return 1
		case "!=":
			if a != b {
				return 0
			}
			return 1
		case "-eq", "-ne", "-lt", "-le", "-gt", "-ge":
			x, e1 := strconv.Atoi(a)
			y, e2 := strconv.Atoi(b)
			if e1 != nil || e2 != nil {
				return 1
			}
			ok := false
			switch op {
			case "-eq":
				ok = x == y
			case "-ne":
				ok = x != y
			case "-lt":
				ok = x < y
			case "-le":
				ok = x <= y
			case "-gt":
				ok = x > y
			case "-ge":
				ok = x >= y
			}
			if ok {
				return 0
			}
			return 1
		}
	}
	return 1
}

// read reads one line from stdin into a variable.
func (r *Runner) read(args []string, st *state) int {
	name := "REPLY"
	if len(args) > 0 {
		name = args[0]
	}
	rd := readerOf(st.stdin, r)
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := rd.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				break
			}
			b.WriteByte(buf[0])
		}
		if err != nil {
			break
		}
	}
	r.vars[name] = b.String()
	if b.Len() == 0 {
		return 1
	}
	return 0
}
