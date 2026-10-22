package exec

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/easydev-org/easysh/pkg/flow"
)

// run executes src with captured stdout and returns (stdout, registry, code).
func run(t *testing.T, src string) (string, *flow.Registry, int) {
	t.Helper()
	var out bytes.Buffer
	sink := flow.NewMemSink()
	r := New(Options{
		Dir:       t.TempDir(),
		Env:       []string{"HOME=/tmp"},
		Stdin:     strings.NewReader(""),
		Stdout:    &out,
		Stderr:    &out,
		Capture:   true,
		Sink:      sink,
		RingLines: 100,
	})
	res, err := r.Run(context.Background(), src)
	if err != nil {
		t.Fatalf("run(%q): %v", src, err)
	}
	return out.String(), res.Registry, res.ExitCode
}

func TestEcho(t *testing.T) {
	out, _, code := run(t, `echo hello world`)
	if code != 0 || out != "hello world\n" {
		t.Fatalf("out=%q code=%d", out, code)
	}
}

// TestExitDoesNotPoisonSession is a regression test for a bug that made a whole
// worker useless after one job.
//
// The Runner is a session: it deliberately outlives a Run so `cd`, `export` and
// function definitions persist. But `exit`, `return`, `break` and `continue` are
// per-run control flow, and their sentinels were only ever set, never cleared.
// One job that ran `exit 3` therefore left exiting == true for the lifetime of
// the process, and every later job returned at the top of runStmts having
// executed nothing at all. From outside the daemon looked healthy — it accepted
// job ids — while the shell silently did nothing, and only a restart recovered
// it. That is exactly the failure this test pins down.
func TestExitDoesNotPoisonSession(t *testing.T) {
	var out bytes.Buffer
	r := New(Options{
		Dir:     t.TempDir(),
		Env:     []string{"HOME=/tmp"},
		Stdin:   strings.NewReader(""),
		Stdout:  &out,
		Stderr:  &out,
		Capture: true,
	})

	// 1. `exit` with a code reports that code...
	res, err := r.Run(context.Background(), `echo before; exit 3`)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("first run exit code = %d, want 3", res.ExitCode)
	}

	// 2. ...and the NEXT run on the same session still executes commands.
	// Before the fix this produced empty output and code 3, because runStmts
	// returned the stale retCode before running anything.
	out.Reset()
	res, err = r.Run(context.Background(), `echo after`)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if out.String() != "after\n" {
		t.Fatalf("second run output = %q, want %q (session was poisoned by exit)",
			out.String(), "after\n")
	}
	if res.ExitCode != 0 {
		t.Fatalf("second run exit code = %d, want 0", res.ExitCode)
	}

	// 3. The same must hold for `return` inside a function call and for a
	//    `break` that never left a loop, which set the same kind of sentinel.
	out.Reset()
	if _, err := r.Run(context.Background(), `f() { return 4; }; f`); err != nil {
		t.Fatalf("return run: %v", err)
	}
	out.Reset()
	if _, err := r.Run(context.Background(), `echo still-alive`); err != nil {
		t.Fatalf("after return: %v", err)
	}
	if out.String() != "still-alive\n" {
		t.Fatalf("after return output = %q, want %q", out.String(), "still-alive\n")
	}
}

func TestVarExpand(t *testing.T) {
	out, _, _ := run(t, "let x = hi\necho \"$x there\"")
	if out != "hi there\n" {
		t.Fatalf("out=%q", out)
	}
}

func TestFieldSplitting(t *testing.T) {
	// An unquoted expansion splits on whitespace; a quoted one does not.
	out, _, _ := run(t, "let x = \"a b c\"\necho $x")
	if out != "a b c\n" {
		t.Fatalf("unquoted out=%q", out)
	}
}

func TestArith(t *testing.T) {
	out, _, _ := run(t, "mut n = 3\nn = $((n * 4 - 2))\necho $n")
	if out != "10\n" {
		t.Fatalf("out=%q", out)
	}
}

func TestFuncParams(t *testing.T) {
	out, _, _ := run(t, "fn add(a, b) { echo \"$a + $b\" }\nadd 1 2")
	if out != "1 + 2\n" {
		t.Fatalf("out=%q", out)
	}
}

func TestCmdSubst(t *testing.T) {
	out, _, _ := run(t, "let n = $(echo 42)\necho \"n=$n\"")
	if out != "n=42\n" {
		t.Fatalf("out=%q", out)
	}
}

func TestForIfCase(t *testing.T) {
	src := `for f in x y z do
  if test "$f" = "y" then echo "found y" else echo "got $f" end
end`
	out, _, _ := run(t, src)
	want := "got x\nfound y\ngot z\n"
	if out != want {
		t.Fatalf("out=%q want=%q", out, want)
	}
}

func TestCase(t *testing.T) {
	src := `let c = run
case "$c" in
  start | run => echo starting
  * => echo other
end`
	out, _, _ := run(t, src)
	if out != "starting\n" {
		t.Fatalf("out=%q", out)
	}
}

func TestAndOr(t *testing.T) {
	out, _, _ := run(t, "true && echo yes")
	if out != "yes\n" {
		t.Fatalf("and out=%q", out)
	}
	out, _, _ = run(t, "false || echo fallback")
	if out != "fallback\n" {
		t.Fatalf("or out=%q", out)
	}
}

func TestExitCode(t *testing.T) {
	_, _, code := run(t, "false")
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
}

// TestPipelineFlowGraph is the point of flowsh: a 3-stage pipeline yields 3
// pipe flows wired end to end, each with its intermediate content captured.
func TestPipelineFlowGraph(t *testing.T) {
	var out bytes.Buffer
	sink := flow.NewMemSink()
	r := New(Options{
		Dir:       t.TempDir(),
		Env:       []string{"PATH=/usr/bin:/bin"},
		Stdout:    &out,
		Stderr:    &out,
		Capture:   true,
		Sink:      sink,
		RingLines: 100,
	})
	res, err := r.Run(context.Background(), `echo "a b c d e" | wc -w`)
	if err != nil {
		t.Fatal(err)
	}
	// Find the pipe flow.
	var pipe *flow.Flow
	for _, f := range res.Registry.Flows() {
		if f.Kind == flow.Pipe {
			pipe = f
		}
	}
	if pipe == nil {
		t.Fatal("no pipe flow recorded")
	}
	if pipe.Producer == nil || pipe.Producer.Argv[0] != "echo" {
		t.Fatalf("pipe producer = %v", pipe.Producer)
	}
	if pipe.Consumer == nil || pipe.Consumer.Argv[0] != "wc" {
		t.Fatalf("pipe consumer = %v", pipe.Consumer)
	}
	got := strings.TrimSpace(string(sink.Bytes(pipe.ID)))
	if got != "a b c d e" {
		t.Fatalf("captured intermediate = %q", got)
	}
}

// TestRedirectFlowGraph checks that `> file` becomes a File flow.
func TestRedirectFlowGraph(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	sink := flow.NewMemSink()
	r := New(Options{Dir: dir, Stdout: &out, Stderr: &out, Capture: true, Sink: sink, RingLines: 100})
	res, err := r.Run(context.Background(), `echo persisted > out.txt`)
	if err != nil {
		t.Fatal(err)
	}
	var file *flow.Flow
	for _, f := range res.Registry.Flows() {
		if f.Kind == flow.File {
			file = f
		}
	}
	if file == nil {
		t.Fatal("no file flow recorded")
	}
	if !strings.HasSuffix(file.Path, "out.txt") {
		t.Fatalf("file path = %q", file.Path)
	}
	if b, _ := file.Counts(); b != 10 {
		t.Fatalf("file bytes = %d", b)
	}
}
