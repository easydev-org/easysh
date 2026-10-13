package daemon

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestInteractiveStdin feeds a running job's stdin, mirroring the worker's
// JobStdin: start `cat`, write, close, read back.
func TestInteractiveStdin(t *testing.T) {
	s := newTestSession(t, time.Minute)
	j, err := s.StartJob(context.Background(), "cat", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Stdin([]byte("hello stdin\n"), false); err != nil {
		t.Fatal(err)
	}
	if err := j.Stdin(nil, true); err != nil { // close -> EOF
		t.Fatal(err)
	}
	j.Wait()
	out := string(j.Output(0))
	if !strings.Contains(out, "hello stdin") {
		t.Fatalf("interactive stdin not echoed: %q", out)
	}
}

// TestReadBuiltin checks the `read` builtin consumes fed stdin.
func TestReadBuiltin(t *testing.T) {
	s := newTestSession(t, time.Minute)
	j, err := s.StartJob(context.Background(), "read line\necho \"got $line\"", nil)
	if err != nil {
		t.Fatal(err)
	}
	j.Stdin([]byte("world\n"), true)
	j.Wait()
	if out := string(j.Output(0)); !strings.Contains(out, "got world") {
		t.Fatalf("read builtin: %q", out)
	}
}

// TestPipedCommandDoesNotDeadlock guards the os.Pipe-vs-io.Pipe bug: a piped
// external command must terminate even though the job has an open stdin pipe.
func TestPipedCommandDoesNotDeadlock(t *testing.T) {
	s := newTestSession(t, time.Minute)
	done := make(chan struct{})
	go func() {
		j, _ := s.StartJob(context.Background(), "cat /etc/hostname | wc -c", nil)
		j.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("piped command deadlocked (likely an io.Pipe stdin)")
	}
}
