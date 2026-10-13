package exec

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// posixCases is a bash/POSIX compatibility matrix: the exact stdout a standard
// shell would produce. It guards the POSIX surface easysh must accept so that
// AI-generated / pasted bash runs unmodified.
var posixCases = []struct {
	in   string
	want string
}{
	// assignments
	{"x=1; echo $x", "1\n"},
	{"x=1; y=2; echo $x$y", "12\n"},
	{"FOO=bar sh -c 'echo $FOO'", "bar\n"},
	{"export X=1; sh -c 'echo $X'", "1\n"},
	// parameter expansion
	{"x=abc; echo ${#x}", "3\n"},
	{"echo ${x:-def}", "def\n"},
	{"x=hello.txt; echo ${x%.txt}", "hello\n"},
	{"x=/a/b/c.txt; echo ${x##*/}", "c.txt\n"},
	{"x=/a/b/c.txt; echo ${x#*/}", "a/b/c.txt\n"},
	{"x=hello.txt; echo ${x%%.*}", "hello\n"},
	{"x=abcabc; echo ${x/ab/XY}", "XYcabc\n"},
	{"x=abcabc; echo ${x//ab/XY}", "XYcXYc\n"},
	{"x=abcdef; echo ${x:2:3}", "cde\n"},
	{"x=abcdef; echo ${x:2}", "cdef\n"},
	{"x=abcdef; echo ${x: -2}", "ef\n"},
	{"x=abc; echo ${x:=def}; echo $x", "abc\nabc\n"},
	{"echo ${x:-d}", "d\n"},
	{"y=1; echo ${y:+s}", "s\n"},
	// command substitution + splitting
	{"echo `echo hi`", "hi\n"},
	{"x=`echo nested`; echo $x", "nested\n"},
	{"for i in $(seq 1 3); do echo $i; done", "1\n2\n3\n"},
	{"x=$(echo a b c); for w in $x; do echo [$w]; done", "[a]\n[b]\n[c]\n"},
	// functions
	{"f(){ echo hi; }; f", "hi\n"},
	{"f() { echo hi; }; f", "hi\n"},
	{"greet(){ echo \"hi $1\"; }; greet bob", "hi bob\n"},
	// case
	{"case b in a) echo A;; b) echo B;; esac", "B\n"},
	{"case x in a|b) echo ab;; *) echo other;; esac", "other\n"},
	// blocks / control
	{"(echo a; echo b)", "a\nb\n"},
	{"{ echo a; echo b; }", "a\nb\n"},
	{"if true; then echo yes; fi", "yes\n"},
	{"if false; then echo no; else echo yes; fi", "yes\n"},
	{"while false; do echo no; done; echo done", "done\n"},
	{"i=0; while test $i -lt 2; do echo w$i; i=$((i+1)); done", "w0\nw1\n"},
	{"until true; do echo once; break; done", ""},
	// operators
	{"true && echo ok || echo no", "ok\n"},
	{"false && echo a || echo b", "b\n"},
	{"! false; echo $?", "0\n"},
	{"! true; echo $?", "1\n"},
	// redirects
	{"echo hi 2>&1", "hi\n"},
	{"cat <<< hi", "hi\n"},
	{"echo done", "done\n"}, // `done` as an argument, not a keyword
	// misc
	{"echo ~/x", "/tmp/x\n"}, // HOME=/tmp in the harness
	{"echo one two three | wc -w", "3\n"},
	{"[ -f /etc/hostname ] && echo yes", "yes\n"},
}

func TestPOSIXCompat(t *testing.T) {
	for _, c := range posixCases {
		c := c
		t.Run("", func(t *testing.T) {
			var out bytes.Buffer
			r := New(Options{
				Dir:       t.TempDir(),
				Env:       []string{"HOME=/tmp", "PATH=/usr/bin:/bin"},
				Stdout:    &out,
				Stderr:    &out,
				Capture:   true,
				RingLines: 100,
			})
			res, err := r.RunWithEnv(context.Background(), c.in, "", []string{"HOME=/tmp", "PATH=/usr/bin:/bin"}, strings.NewReader(""), &out, &out)
			if err != nil {
				t.Fatalf("run(%q): %v", c.in, err)
			}
			if got := out.String(); got != c.want {
				t.Fatalf("run(%q):\nwant %q\ngot  %q", c.in, c.want, got)
			}
			_ = res
		})
	}
}
