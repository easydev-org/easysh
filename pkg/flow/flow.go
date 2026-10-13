// Package flow is the heart of flowsh: every stream is an identified Flow with
// a producer, consumers, content, and a place in a directed graph. Because
// flowsh creates every pipe and redirect itself, the graph is known at
// construction time — no fstat, no inode comparison, no ptrace.
package flow

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sync"
	"time"
)

// FID identifies a flow within a job. 0 means "none".
type FID uint32

// Kind classifies a flow's origin/destination.
type Kind uint8

const (
	Pipe       Kind = iota // a | b          (kernel pipe)
	File                   // > f  < f  >> f (a file)
	Here                   // <<EOF  <<<      (here-doc/string)
	Dup                    // 2>&1            (alias of another flow)
	Null                   // >&-  </dev/null (discard)
	Capture                // $( ... )        (in-memory capture)
	Terminal               // fd0/1/2 at the job boundary
	FdEndpoint             // one stage's stdin/stdout/stderr (identity, no bytes)
)

func (k Kind) String() string {
	switch k {
	case Pipe:
		return "pipe"
	case File:
		return "file"
	case Here:
		return "here"
	case Dup:
		return "dup"
	case Null:
		return "null"
	case Capture:
		return "capture"
	case Terminal:
		return "terminal"
	case FdEndpoint:
		return "fd"
	default:
		return "unknown"
	}
}

// Slot is one of the three fds a stage has.
type Slot uint8

const (
	Stdin Slot = iota
	Stdout
	Stderr
)

func (s Slot) String() string {
	switch s {
	case Stdin:
		return "stdin"
	case Stdout:
		return "stdout"
	case Stderr:
		return "stderr"
	default:
		return "?"
	}
}

// CommandRef identifies the stage that produces or consumes a flow.
type CommandRef struct {
	Node int      // index in the job's stage list (parse order)
	Argv []string // resolved argv, or the builtin name
	Line int      // source line (1-based)
	Col  int      // source column (1-based)
	// EndCol/EndLine delimit the stage's source span [Line:Col, EndLine:EndCol),
	// so a UI can highlight exactly which segment of the pipeline this is.
	EndLine int
	EndCol  int
	// Iter is the loop-iteration path when this stage runs inside a loop, e.g.
	// [2] or [2,1] for nested loops (1-based). It distinguishes instances of the
	// SAME source segment across iterations — a `for` body has one span but many
	// instances. Empty when not in a loop.
	Iter []int
}

// IterLabel renders Iter as a "#2" / "#2.1" suffix ("" when not in a loop).
func (c *CommandRef) IterLabel() string {
	if c == nil || len(c.Iter) == 0 {
		return ""
	}
	s := "#"
	for i, n := range c.Iter {
		if i > 0 {
			s += "."
		}
		s += itoa(n)
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// Name returns a compact label for the graph.
func (c *CommandRef) Name() string {
	if c == nil || len(c.Argv) == 0 {
		return ""
	}
	n := c.Argv[0]
	for _, a := range c.Argv[1:] {
		n += " " + a
	}
	return n
}

// Flow is one identified stream.
type Flow struct {
	ID   FID
	Kind Kind
	// Fd is which descriptor this flow represents (0/1/2). Every stage gets its
	// own three fd flows, so a flow's identity is (owner, fd) — not just a
	// destination. This is what makes per-stage stdin/stdout/stderr observable.
	Fd Slot
	// Owner is the stage this fd belongs to. A pipe's write-end is owned by the
	// producer, its read-end by the consumer.
	Owner *CommandRef

	Inputs  []FID
	Outputs []FID
	DupOf   FID

	Producer *CommandRef
	Consumer *CommandRef

	Path string // file path / here-doc tag

	Data *TapBuffer

	Started time.Time
	Closed  time.Time

	mu     sync.Mutex
	bytes  int64
	lines  int64
	hash   hash
	closed bool
}

// Counts returns the byte/line totals observed so far.
func (f *Flow) Counts() (bytes, lines int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bytes, f.lines
}

func (f *Flow) addBytes(n int, nl int64) {
	f.mu.Lock()
	f.bytes += int64(n)
	f.lines += nl
	f.mu.Unlock()
}

func (f *Flow) finalizeHash() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hash == nil {
		return ""
	}
	return f.hash.sum()
}

// hash is a tiny interface so a flow can carry a rolling digest.
type hash interface {
	io.Writer
	sum() string
}

type sha struct {
	h interface {
		Sum([]byte) []byte
		Write([]byte) (int, error)
	}
}

func (s *sha) Write(p []byte) (int, error) { return s.h.Write(p) }
func (s *sha) sum() string                 { return hex.EncodeToString(s.h.Sum(nil)) }

func newHash() hash {
	return &sha{h: sha256.New()}
}

// ── registry ────────────────────────────────────────────────────────────────

// Registry allocates flows and their ids. It is the single chokepoint through
// which every stream is created, which is what makes the graph complete.
type Registry struct {
	mu    sync.Mutex
	next  FID
	flows map[FID]*Flow
	order []FID // creation order
	now   func() time.Time

	// onAlloc, when set, fires each time a flow is created, so a caller can
	// build the graph live instead of only at the end of the job.
	onAlloc func(*Flow)
	// onWrite, when set, receives every byte chunk written to any flow, tagged
	// by fid — the live per-flow tap (must be non-blocking).
	onWrite func(FID, []byte)
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{flows: map[FID]*Flow{}, now: time.Now}
}

func (r *Registry) alloc(f *Flow) *Flow {
	r.mu.Lock()
	r.next++
	f.ID = r.next
	f.Started = r.now()
	f.hash = newHash()
	r.flows[f.ID] = f
	r.order = append(r.order, f.ID)
	cb := r.onAlloc
	r.mu.Unlock()
	if cb != nil {
		cb(f)
	}
	return f
}

// OnAlloc sets a callback invoked whenever a flow is created.
func (r *Registry) OnAlloc(fn func(*Flow)) {
	r.mu.Lock()
	r.onAlloc = fn
	r.mu.Unlock()
}

// OnWrite sets a callback invoked with every chunk written to any flow.
func (r *Registry) OnWrite(fn func(FID, []byte)) {
	r.mu.Lock()
	r.onWrite = fn
	r.mu.Unlock()
}

// NotifyWrite fans a write out to the OnWrite callback (called by taps).
func (r *Registry) NotifyWrite(fid FID, p []byte) {
	r.mu.Lock()
	cb := r.onWrite
	r.mu.Unlock()
	if cb != nil {
		cb(fid, p)
	}
}

// Get returns a flow by id.
func (r *Registry) Get(id FID) *Flow {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.flows[id]
}

// Flows returns all flows in creation order.
func (r *Registry) Flows() []*Flow {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Flow, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.flows[id])
	}
	return out
}

// link records an edge from->to on both flows.
func (r *Registry) link(from, to *Flow) {
	if from == nil || to == nil || from == to {
		return
	}
	from.Outputs = append(from.Outputs, to.ID)
	to.Inputs = append(to.Inputs, from.ID)
}

// LinkByID records an edge between two flows by id (safe with 0/unknown ids).
func (r *Registry) LinkByID(from, to FID) {
	if from == 0 || to == 0 || from == to {
		return
	}
	r.link(r.Get(from), r.Get(to))
}
