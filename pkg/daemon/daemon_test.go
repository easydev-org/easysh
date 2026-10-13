package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/easydev-org/easysh/pkg/store"
)

// newTestSession makes a session with a short detach threshold and no store.
func newTestSession(t *testing.T, detach time.Duration) *Session {
	t.Helper()
	return NewSession("test", t.TempDir(), []string{"HOME=/tmp", "PATH=/usr/bin:/bin"}, nil, detach)
}

// collect runs a command synchronously and returns its stdout.
func collect(t *testing.T, s *Session, cmd string) (string, int) {
	t.Helper()
	var sb strings.Builder
	j, err := s.StartJob(context.Background(), cmd, func(e Event) {
		if e.Kind == "stdout" {
			sb.Write(e.Data)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	code := j.Wait()
	return sb.String(), code
}

func TestSessionVariablesPersist(t *testing.T) {
	s := newTestSession(t, time.Minute)
	if out, _ := collect(t, s, "let x = hello"); out != "" {
		t.Fatalf("let output=%q", out)
	}
	out, _ := collect(t, s, `echo "x=$x"`)
	if out != "x=hello\n" {
		t.Fatalf("persisted var: %q", out)
	}
}

func TestSessionCwdPersists(t *testing.T) {
	s := newTestSession(t, time.Minute)
	collect(t, s, "cd /tmp")
	out, _ := collect(t, s, "pwd")
	if strings.TrimSpace(out) != "/tmp" {
		t.Fatalf("cwd not persisted: %q", out)
	}
}

func TestSessionFunctionsPersist(t *testing.T) {
	s := newTestSession(t, time.Minute)
	collect(t, s, "fn greet(n) { echo \"hi $n\" }")
	out, _ := collect(t, s, "greet bob")
	if out != "hi bob\n" {
		t.Fatalf("persisted fn: %q", out)
	}
}

func TestPipelineGraph(t *testing.T) {
	s := newTestSession(t, time.Minute)
	j, err := s.StartJob(context.Background(), `echo a b c d e | wc -w`, nil)
	if err != nil {
		t.Fatal(err)
	}
	j.Wait()
	flows, edges := j.Graph()
	var pipe *flowSnapshot
	for i := range flows {
		if flows[i].Kind == "pipe" {
			pipe = &flows[i]
		}
	}
	if pipe == nil {
		t.Fatalf("no pipe flow in graph: %+v", flows)
	}
	if pipe.Producer != "echo a b c d e" {
		t.Fatalf("pipe producer = %q", pipe.Producer)
	}
	if pipe.Consumer != "wc -w" {
		t.Fatalf("pipe consumer = %q", pipe.Consumer)
	}
	if len(edges) == 0 {
		t.Fatal("no edges recorded")
	}
}

func TestDetach(t *testing.T) {
	// Threshold 100ms; the job sleeps 1s, so it must detach and the job keeps
	// running to completion in the background.
	s := newTestSession(t, 100*time.Millisecond)
	detached := make(chan struct{}, 1)
	j, err := s.StartJob(context.Background(), "sleep 1; echo late", func(e Event) {
		if e.Kind == "detached" {
			select {
			case detached <- struct{}{}:
			default:
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-detached:
	case <-time.After(2 * time.Second):
		t.Fatal("job was not detached")
	}
	// It must still finish and capture output.
	j.Wait()
	if _, _, st, _, _, _, det, _, _ := j.Info(); st != "done" || !det {
		t.Fatalf("after detach: state=%s detached=%v", st, det)
	}
	if out := string(j.Output(0)); !strings.Contains(out, "late") {
		t.Fatalf("detached output missing: %q", out)
	}
}

func TestShortJobDoesNotDetach(t *testing.T) {
	s := newTestSession(t, 5*time.Second)
	j, _ := s.StartJob(context.Background(), "echo quick", nil)
	j.Wait()
	if _, _, st, _, _, _, det, _, _ := j.Info(); det || st != "done" {
		t.Fatalf("short job: state=%s detached=%v", st, det)
	}
}

func TestPersistence(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/jobs.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := NewSession("s", t.TempDir(), []string{"PATH=/usr/bin:/bin"}, st, time.Minute)
	j, _ := s.StartJob(context.Background(), "echo persisted | wc -c", nil)
	j.Wait()
	time.Sleep(400 * time.Millisecond) // let the store flush
	// Query the store directly.
	n := st.CountJobs()
	if n < 1 {
		t.Fatalf("expected >=1 persisted job, got %d", n)
	}
}

func TestComplete(t *testing.T) {
	s := newTestSession(t, time.Minute)
	collect(t, s, "let myvariable = 1")
	got := s.Complete("myvar")
	found := false
	for _, c := range got {
		if c.Text == "myvariable" && c.Kind == "variable" {
			found = true
		}
	}
	if !found {
		t.Fatalf("variable not completed: %+v", got)
	}
}

func TestHighlight(t *testing.T) {
	spans := Highlight("echo $x | wc")
	classes := map[string]bool{}
	for _, sp := range spans {
		classes[sp.Class] = true
	}
	for _, want := range []string{"builtin", "operator", "command"} {
		if !classes[want] {
			t.Fatalf("missing %q class in %+v", want, spans)
		}
	}
}
