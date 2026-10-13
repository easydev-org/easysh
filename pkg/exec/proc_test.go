package exec

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// TestRunWithEnv checks the worker-facing entry point: workdir, env, and
// separate stdout/stderr writers.
func TestRunWithEnv(t *testing.T) {
	r := New(Options{Capture: false, RingLines: 100})
	var out, errb bytes.Buffer
	dir := t.TempDir()
	res, err := r.RunWithEnv(context.Background(), `echo "in $PWD"`, dir, []string{"PATH=/usr/bin:/bin"}, strings.NewReader(""), &out, &errb)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit %d, stderr=%q", res.ExitCode, errb.String())
	}
	if !strings.Contains(out.String(), dir) {
		t.Fatalf("stdout %q should contain workdir %q", out.String(), dir)
	}
}

// TestKillTreeKillsGrandchildren is the point of process-group kill: a command
// that spawns a background child must have that child die when the job is
// cancelled. Without a process group, the grandchild is orphaned and keeps
// running.
func TestKillTreeKillsGrandchildren(t *testing.T) {
	r := New(Options{Capture: false, RingLines: 100})
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	// The shell command backgrounds a `sleep 30` and then waits; cancelling must
	// reap BOTH the shell and the sleep.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = r.RunWithEnv(ctx, `sleep 30 & echo $! > /tmp/easysh_gc.pid; wait`, t.TempDir(),
			[]string{"PATH=/usr/bin:/bin"}, strings.NewReader(""), &out, &out)
	}()
	// Give it a moment to spawn and record the grandchild pid.
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("job did not stop after cancel")
	}
	// The recorded grandchild must be gone.
	pid := readPID(t, "/tmp/easysh_gc.pid")
	if pid > 0 && processAlive(pid) {
		killTree(pid)
		t.Fatalf("grandchild pid %d survived the cancel (no process-group kill)", pid)
	}
}

// TestKillAll checks a runner reaps its tracked trees on shutdown.
func TestKillAll(t *testing.T) {
	r := New(Options{Capture: false, RingLines: 100})
	ctx := context.Background()
	var out bytes.Buffer
	go func() {
		_, _ = r.RunWithEnv(ctx, `sleep 30`, t.TempDir(), []string{"PATH=/usr/bin:/bin"}, strings.NewReader(""), &out, &out)
	}()
	time.Sleep(300 * time.Millisecond)
	r.KillAll()
	time.Sleep(300 * time.Millisecond)
	r.mu.Lock()
	n := len(r.pids)
	r.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d pids still tracked after KillAll", n)
	}
}
