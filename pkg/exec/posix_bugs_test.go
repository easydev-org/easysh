package exec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/easydev-org/easysh/pkg/flow"
)

// runIn runs src with the session's working directory set to dir (the process
// cwd is left untouched — this is the whole point of the bugs below).
func runIn(t *testing.T, dir, src string) (string, int) {
	t.Helper()
	var out strings.Builder
	r := New(Options{
		Dir:       dir,
		Env:       []string{"HOME=/tmp"},
		Stdin:     strings.NewReader(""),
		Stdout:    &out,
		Stderr:    &out,
		Capture:   true,
		Sink:      flow.NewMemSink(),
		RingLines: 100,
	})
	res, err := r.Run(context.Background(), src)
	if err != nil {
		t.Fatalf("run(%q): %v", src, err)
	}
	return out.String(), res.ExitCode
}

// Bug 1: glob must expand relative to the SESSION dir (st.dir), not the daemon
// process's cwd. Here the process cwd has NO *.js, the session dir has two.
func TestGlobUsesSessionDir(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.js", "b.js"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, code := runIn(t, dir, `echo *.js`)
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	got := strings.Fields(out)
	if len(got) != 2 || got[0] != "a.js" || got[1] != "b.js" {
		t.Fatalf("glob did not expand against the session dir: %q", out)
	}
}

// Bug 2: a redirect applies to ONE command only; it must not leak onto the
// following statements.
func TestRedirectDoesNotLeak(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "r.txt")
	out, code := runIn(t, dir, `echo AAA > `+target+`; echo BBB; echo CCC`)
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if strings.Contains(out, "AAA") {
		t.Fatalf("AAA leaked to the terminal: %q", out)
	}
	if !strings.Contains(out, "BBB") || !strings.Contains(out, "CCC") {
		t.Fatalf("BBB/CCC should go to the terminal: %q", out)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "AAA\n" {
		t.Fatalf("redirected file must contain only AAA, got %q", string(data))
	}
}

// Bug 3: test -f/-d must resolve relative paths against the SESSION dir.
func TestTestBuiltinUsesSessionDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := runIn(t, dir, `test -f package.json; echo $?`)
	if strings.TrimSpace(out) != "0" {
		t.Fatalf("test -f should be true in the session dir, got %q", strings.TrimSpace(out))
	}
	out, _ = runIn(t, dir, `test -d .; echo $?`)
	if strings.TrimSpace(out) != "0" {
		t.Fatalf("test -d . should be true in the session dir, got %q", strings.TrimSpace(out))
	}
	out, _ = runIn(t, dir, `test -f nope.txt; echo $?`)
	if strings.TrimSpace(out) != "1" {
		t.Fatalf("test -f nope.txt should be false, got %q", strings.TrimSpace(out))
	}
}
