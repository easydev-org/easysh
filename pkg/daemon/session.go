// Package daemon implements the easysh server: long-lived sessions that keep
// variables/functions/cwd across commands, an async job scheduler that detaches
// long-running foreground jobs, and SQLite persistence.
package daemon

import (
	"context"
	"io"
	"os"
	"sync"
	"time"

	"github.com/easydev-org/easysh/pkg/exec"
	"github.com/easydev-org/easysh/pkg/flow"
	"github.com/easydev-org/easysh/pkg/store"
)

// DefaultDetachAfter is how long a foreground job may run before it is detached
// to the background (configurable per session).
const DefaultDetachAfter = 10 * time.Second

// Session is one persistent shell context. Variables, functions and cwd persist
// across Run calls because the same exec.Runner is reused.
type Session struct {
	Name string

	mu      sync.Mutex
	runner  *exec.Runner
	execMu  sync.Mutex // serializes job execution within the session
	dir     string
	env     []string
	jobs    map[string]*Job
	order   []string
	hist    []string // typed commands, shared across terminals of this session
	seq     uint64
	store   *store.Store
	detach  time.Duration
	onEvent func(Event)
}

// Event is a streamed notification to a connected client.
type Event struct {
	Kind  string // stdout | stderr | detached | done | job
	JobID string
	Data  []byte
	Code  int
}

// NewSession creates a session writing to a shared store.
func NewSession(name, dir string, env []string, st *store.Store, detach time.Duration) *Session {
	if detach <= 0 {
		detach = DefaultDetachAfter
	}
	s := &Session{
		Name:   name,
		dir:    dir,
		env:    env,
		jobs:   map[string]*Job{},
		store:  st,
		detach: detach,
	}
	s.runner = exec.New(exec.Options{
		Dir: dir,
		Env: env,
		// The job's own stdout/stderr are teed into the sink by TerminalTap, so
		// the raw writers here only need to exist (not nil — MultiWriter would
		// panic on a nil writer).
		Stdout:    io.Discard,
		Stderr:    io.Discard,
		Capture:   true,
		Sink:      nil, // per-job sink is set in Job
		RingLines: 10000,
	})
	// Load persisted history for this session (shared across terminals).
	if st != nil {
		if h, err := st.History(name, 1000); err == nil {
			s.hist = h
		}
	}
	return s
}

// AddHistory records a typed command and persists it.
func (s *Session) AddHistory(command string) {
	s.mu.Lock()
	s.hist = append(s.hist, command)
	if len(s.hist) > 10000 {
		s.hist = s.hist[len(s.hist)-10000:]
	}
	st := s.store
	s.mu.Unlock()
	if st != nil {
		_, _ = st.AppendHistory(s.Name, command, time.Now().UnixMilli())
	}
}

// History returns the session's typed commands, oldest first.
func (s *Session) History() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.hist...)
}

// SetDetachAfter changes the detach threshold.
func (s *Session) SetDetachAfter(d time.Duration) {
	s.mu.Lock()
	if d > 0 {
		s.detach = d
	}
	s.mu.Unlock()
}

// DetachAfter returns the current threshold.
func (s *Session) DetachAfter() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detach
}

// Dir returns the session's current working directory.
func (s *Session) Dir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dir
}

// StartJob begins running a script as a job and returns immediately. The job's
// stdout/stderr and flow chunks are streamed to onOut and persisted to the store.
// After detachAfter the job is marked detached and keeps running in the
// background; the caller is notified via onEvent("detached").
func (s *Session) StartJob(ctx context.Context, command string, onOut func(Event)) (*Job, error) {
	s.mu.Lock()
	s.seq++
	id := jobID(s.Name, s.seq)
	detach := s.detach
	s.mu.Unlock()

	j := newJob(id, command, s, detach, onOut)
	s.mu.Lock()
	s.jobs[id] = j
	s.order = append(s.order, id)
	s.mu.Unlock()

	if s.store != nil {
		s.store.EnqueueJob(id, command, s.Name, time.Now().UnixMilli())
	}

	go j.run(ctx)
	return j, nil
}

// Jobs returns the session's jobs, newest last.
func (s *Session) Jobs() []*Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Job, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.jobs[id])
	}
	return out
}

// Job returns a job by id.
func (s *Session) Job(id string) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jobs[id]
}

// jobID builds a stable id: <session>-<seq>.
func jobID(session string, seq uint64) string {
	return session + "-" + itoa(seq)
}

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// ensure imports stay used in this file's revisions.
var _ = io.Discard
var _ = os.Getenv
var _ = flow.Pipe
