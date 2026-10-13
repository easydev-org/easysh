package exec

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/abcp-sdk/easysh/internal/flow"
)

// runCase is one table entry: a script and the exact stdout it must produce.
// Modeled on mvdan/sh's runTests: `in` is the program, `want` is stdout.
// A trailing "#..." comment in want is stripped (documents intent only).
type runCase struct {
	in   string
	want string
	code int // expected exit code (0 unless set)
}

var runCases = []runCase{
	// ── no-ops and trivial builtins ──
	{in: `true`},
	{in: `false`, code: 1},
	{in: `:`},
	{in: `echo`, want: "\n"},
	{in: `echo hello`, want: "hello\n"},
	{in: `echo a b c`, want: "a b c\n"},
	{in: `echo -n hi`, want: "hi"},
	{in: `echo "a  b"`, want: "a  b\n"},
	{in: `echo 'a  b'`, want: "a  b\n"},

	// ── variables ──
	{in: "let x = 1\necho $x", want: "1\n"},
	{in: "let x = hi\necho ${x}", want: "hi\n"},
	{in: "let x = a\necho \"pre-${x}-post\"", want: "pre-a-post\n"},
	{in: "mut n = 1\nn = 2\necho $n", want: "2\n"},
	{in: "let x = 1\nunset x\necho \"[$x]\"", want: "[]\n"},
	{in: `echo "[$NOPE]"`, want: "[]\n"},
	{in: "export FOO = bar\necho $FOO", want: "bar\n"},

	// ── quoting ──
	{in: `echo "a\tb"`, want: "a\\tb\n"},
	{in: `echo $'x'`, want: "x\n"},
	{in: `echo "quote\"inside"`, want: "quote\"inside\n"},
	{in: `echo 'single $x'`, want: "single $x\n"},
	{in: "let x = v\necho 'single $x'", want: "single $x\n"},
	{in: "let x = v\necho \"double $x\"", want: "double v\n"},

	// ── field splitting ──
	{in: "let x = \"a b c\"\necho $x", want: "a b c\n"},
	{in: "let x = \"a b c\"\necho \"$x\"", want: "a b c\n"},

	// ── arithmetic ──
	{in: "echo $((1 + 2))", want: "3\n"},
	{in: "echo $((2 * 3 + 1))", want: "7\n"},
	{in: "echo $((10 / 3))", want: "3\n"},
	{in: "echo $((10 % 3))", want: "1\n"},
	{in: "echo $((2 * (3 + 4)))", want: "14\n"},
	{in: "echo $((5 - 8))", want: "-3\n"},
	{in: "mut n = 5\nn = $((n * 2))\necho $n", want: "10\n"},

	// ── command substitution ──
	{in: "echo $(echo hi)", want: "hi\n"},
	{in: "let n = $(echo 42)\necho \"n=$n\"", want: "n=42\n"},
	{in: "echo \"[$(echo a b)]\"", want: "[a b]\n"},

	// ── pipelines ──
	{in: "echo hello | cat", want: "hello\n"},
	{in: "echo hi | cat | cat", want: "hi\n"},
	{in: "printf 'b\\na\\nc\\n' | sort", want: "a\nb\nc\n"},
	{in: "printf 'a\\nb\\nc\\n' | wc -l", want: "3\n"},
	{in: "echo 'one two three' | wc -w", want: "3\n"},

	// ── redirects ──
	{in: "echo hi > /dev/null"},
	{in: "echo a b | wc -w > /dev/null"},
	{in: "echo x 2> /dev/null", want: "x\n"},

	// ── logical operators ──
	{in: "true && echo yes", want: "yes\n"},
	{in: "false && echo no", code: 1},
	{in: "false || echo fallback", want: "fallback\n"},
	{in: "true || echo no"},
	{in: "false && echo a || echo b", want: "b\n"},
	{in: "true && echo a || echo b", want: "a\n"},
	{in: "echo one; echo two", want: "one\ntwo\n"},

	// ── if / elif / else ──
	{in: "if true then echo y end", want: "y\n"},
	{in: "if false then echo y end"},
	{in: "if false then echo a else echo b end", want: "b\n"},
	{in: "if false then echo a elif true then echo b else echo c end", want: "b\n"},
	{in: "if false then echo a elif false then echo b else echo c end", want: "c\n"},
	{in: "let x = 5\nif test $x -gt 3 then echo big else echo small end", want: "big\n"},

	// ── for ──
	{in: "for i in 1 2 3 do echo $i end", want: "1\n2\n3\n"},
	{in: "for x in a b do echo \"[$x]\" end", want: "[a]\n[b]\n"},
	{in: "for i in 1 2 3 do echo $i end", want: "1\n2\n3\n"},

	// ── while ──
	{in: "mut n = 3\nwhile test $n -gt 0 do echo $n\nn = $((n - 1)) end", want: "3\n2\n1\n"},

	// ── case ──
	{in: "let c = a\ncase $c in\n a => echo A\n * => echo other\nend", want: "A\n"},
	{in: "let c = b\ncase $c in\n a => echo A\n * => echo other\nend", want: "other\n"},
	{in: "let c = r\ncase $c in\n s | r => echo SR\n * => echo other\nend", want: "SR\n"},
	{in: "let c = z\ncase $c in\n a => echo A\n b => echo B\nend"},

	// ── functions ──
	{in: "fn f() { echo hi }\nf", want: "hi\n"},
	{in: "fn f(a) { echo $a }\nf x", want: "x\n"},
	{in: "fn add(a, b) { echo $((a + b)) }\nadd 2 3", want: "5\n"},
	{in: "fn f() { return 7 }\nf\necho $?", want: "7\n"},
	{in: "fn f(a, b) { echo $# }\nf x y z", want: "3\n"},

	// ── test / [ ──
	{in: "test -z '' && echo empty", want: "empty\n"},
	{in: "test -n x && echo nonempty", want: "nonempty\n"},
	{in: "test 1 = 1 && echo eq", want: "eq\n"},
	{in: "test 1 != 2 && echo ne", want: "ne\n"},
	{in: "test 5 -gt 3 && echo gt", want: "gt\n"},
	{in: "test 3 -lt 5 && echo lt", want: "lt\n"},
	{in: "[ 2 -eq 2 ] && echo eq2", want: "eq2\n"},

	// ── exit status ──
	{in: "exit 3", code: 3},
	{in: "false\necho $?", want: "1\n"},
	{in: "true\necho $?", want: "0\n"},
	{in: "exit 0\necho never"},
}

func TestRunTable(t *testing.T) {
	for _, c := range runCases {
		c := c
		t.Run("", func(t *testing.T) {
			t.Logf("input: %q", c.in)
			var out bytes.Buffer
			sink := flow.NewMemSink()
			r := New(Options{
				Dir:       t.TempDir(),
				Env:       []string{"HOME=/tmp", "PATH=/usr/bin:/bin"},
				Stdin:     strings.NewReader(""),
				Stdout:    &out,
				Stderr:    &out,
				Capture:   true,
				Sink:      sink,
				RingLines: 100,
			})
			res, err := r.Run(context.Background(), c.in)
			if err != nil {
				t.Fatalf("run(%q): %v", c.in, err)
			}
			want := c.want
			if i := strings.Index(want, " #"); i >= 0 {
				want = want[:i]
			}
			if got := out.String(); got != want {
				t.Fatalf("wrong output in %q:\nwant: %q\ngot:  %q", c.in, want, got)
			}
			if res.ExitCode != c.code {
				t.Fatalf("wrong exit code in %q: want %d got %d", c.in, c.code, res.ExitCode)
			}
		})
	}
}
