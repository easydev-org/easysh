package flow

import (
	"bytes"
	"io"
	"sync"
)

// Ring is a bounded line ring (last N lines), for live view / tail. It mirrors
// the design proven in the Go worker's LineBuffer.
type Ring struct {
	mu   sync.Mutex
	max  int
	recs []string
	cur  bytes.Buffer
}

// NewRing makes a ring holding at most max complete lines.
func NewRing(max int) *Ring {
	if max <= 0 {
		max = 10000
	}
	return &Ring{max: max}
}

// Write splits p into lines and appends the completed ones.
func (r *Ring) Write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range p {
		if b == '\n' {
			r.push(r.cur.String())
			r.cur.Reset()
			continue
		}
		r.cur.WriteByte(b)
	}
}

func (r *Ring) push(s string) {
	r.recs = append(r.recs, s)
	if len(r.recs) > r.max {
		r.recs = r.recs[1:]
	}
}

// Tail returns the last n complete lines.
func (r *Ring) Tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 || n > len(r.recs) {
		n = len(r.recs)
	}
	out := make([]string, n)
	copy(out, r.recs[len(r.recs)-n:])
	return out
}

// Sink receives captured bytes for persistence + live fanout. Implementations
// must be non-blocking.
type Sink interface {
	Enqueue(fid FID, p []byte)
}

// StdoutAware is an optional Sink extension: the executor calls SetStdoutFID
// once per run with the fid of the job's own stdout, so the sink can stream
// those bytes live (as opposed to every intermediate flow, which is only
// persisted).
type StdoutAware interface {
	SetStdoutFID(fid FID)
}

// Discard drops everything (used when capture is disabled).
type Discard struct{}

func (Discard) Enqueue(FID, []byte) {}

// MemSink keeps everything in memory (tests, small jobs).
type MemSink struct {
	mu   sync.Mutex
	data map[FID]*bytes.Buffer
}

// NewMemSink creates an in-memory sink.
func NewMemSink() *MemSink { return &MemSink{data: map[FID]*bytes.Buffer{}} }

// Enqueue appends p to the flow's buffer.
func (m *MemSink) Enqueue(fid FID, p []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.data[fid]
	if b == nil {
		b = &bytes.Buffer{}
		m.data[fid] = b
	}
	b.Write(p)
}

// Bytes returns the captured content of a flow.
func (m *MemSink) Bytes(fid FID) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b := m.data[fid]; b != nil {
		return append([]byte(nil), b.Bytes()...)
	}
	return nil
}

// TapBuffer is an io.Writer that keeps a bounded line-ring and spools the full
// byte stream to a Sink. It never blocks the pipeline.
type TapBuffer struct {
	fid  FID
	inv  *Flow
	ring *Ring
	sink Sink
	reg  *Registry // for live per-flow notification (OnWrite)
}

// NewTapBuffer creates a tap for a flow.
func NewTapBuffer(inv *Flow, ringLines int, sink Sink) *TapBuffer {
	return &TapBuffer{fid: inv.ID, inv: inv, ring: NewRing(ringLines), sink: sink}
}

// BindRegistry attaches the registry so writes can be fanned out live.
func (t *TapBuffer) BindRegistry(r *Registry) { t.reg = r }

// Write implements io.Writer: ring + count + sink, never blocking.
func (t *TapBuffer) Write(p []byte) (int, error) {
	if t == nil {
		return len(p), nil
	}
	t.ring.Write(p)
	var nl int64
	for _, b := range p {
		if b == '\n' {
			nl++
		}
	}
	t.inv.addBytes(len(p), nl)
	if t.sink != nil && t.inv != nil {
		t.sink.Enqueue(t.fid, p)
	}
	if t.reg != nil {
		t.reg.NotifyWrite(t.fid, p)
	}
	return len(p), nil
}

// Tail returns the recent lines for live display.
func (t *TapBuffer) Tail(n int) []string {
	if t == nil {
		return nil
	}
	return t.ring.Tail(n)
}

// TeeWriter returns an io.Writer that writes to w and captures into t.
func TeeWriter(w io.Writer, t *TapBuffer) io.Writer {
	if t == nil {
		return w
	}
	return io.MultiWriter(w, t)
}
