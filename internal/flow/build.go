package flow

import (
	"io"
	"os"
)

// This file adds the graph-building constructors to Registry. Each one creates
// the flow AND returns the stream(s), so identity and wiring are atomic.

// Stream is an fd target bound to a flow.
type Stream struct {
	FID    FID
	W      io.Writer // set when the stage writes here
	R      io.Reader // set when the stage reads here
	File   *os.File  // set for real files (so Close can be called)
	Closer io.Closer // the pipe/file write end to close on stage exit
}

// tapFor builds a tap writer for a flow if capture is on.
func (r *Registry) tapFor(f *Flow, capture bool, sink Sink, ringLines int) io.Writer {
	if !capture || sink == nil {
		return nil
	}
	f.Data = NewTapBuffer(f, ringLines, sink)
	f.Data.BindRegistry(r)
	return f.Data
}

// NewPipe creates a pipe flow connecting producer l to consumer c.
// It returns the write end (for l's stdout) and the read end (for c's stdin).
func (r *Registry) NewPipe(l, c *CommandRef, capture bool, sink Sink, ringLines int) (Stream, Stream) {
	f := r.alloc(&Flow{Kind: Pipe, Producer: l, Consumer: c, Owner: l})
	// A real OS pipe: the kernel buffer (64 KiB) means a writer does not block
	// until the reader catches up, and both ends are ordinary *os.File — the
	// same thing a shell uses.
	pr, pw, err := os.Pipe()
	if err != nil {
		// Fall back to an in-memory pipe if the OS refuses (fd exhaustion).
		mpr, mpw := io.Pipe()
		return Stream{FID: f.ID, W: mpw, Closer: mpw}, Stream{FID: f.ID, R: mpr}
	}
	var w io.Writer = pw
	if tw := r.tapFor(f, capture, sink, ringLines); tw != nil {
		w = io.MultiWriter(pw, toSink(f, sink))
		_ = tw
	}
	return Stream{FID: f.ID, W: w, Closer: pw}, Stream{FID: f.ID, R: pr}
}

// toSink wraps the tap as a plain writer (kept for the multiwriter path).
func toSink(f *Flow, sink Sink) io.Writer {
	if f.Data == nil {
		return io.Discard
	}
	return f.Data
}

// NewFile creates a file flow and opens the underlying file.
func (r *Registry) NewFile(path string, flags int, mode os.FileMode, prod *CommandRef, write bool) (Stream, error) {
	f := r.alloc(&Flow{Kind: File, Path: path, Producer: prod, Owner: prod})
	fh, err := os.OpenFile(path, flags, mode)
	if err != nil {
		return Stream{}, err
	}
	var w io.Writer = fh
	if write && f.Data == nil {
		// For file flows the file IS the content; no tap needed. Counts come
		// from a thin counting writer so bytes/lines are still tracked.
		w = &countingWriter{w: fh, f: f}
	}
	s := Stream{FID: f.ID, File: fh}
	if write {
		s.W = w
	} else {
		s.R = fh
	}
	return s, nil
}

// NewHere creates a here-doc / here-string flow pre-filled with body.
func (r *Registry) NewHere(tag string, body []byte, prod *CommandRef, sink Sink, ringLines int) Stream {
	f := r.alloc(&Flow{Kind: Here, Path: tag, Producer: prod})
	if sink != nil {
		f.Data = NewTapBuffer(f, ringLines, sink)
		f.Data.BindRegistry(r)
		f.Data.Write(body) // pre-fill content + counts
	}
	return Stream{FID: f.ID, R: newBytesReader(body)}
}

// NewNull creates a null (discard) flow.
func (r *Registry) NewNull(write bool) Stream {
	f := r.alloc(&Flow{Kind: Null})
	s := Stream{FID: f.ID}
	if write {
		s.W = io.Discard
	} else {
		s.R = emptyReader{}
	}
	return s
}

// NewCapture creates an in-memory capture flow ($( ... )).
func (r *Registry) NewCapture(prod *CommandRef, sink Sink, ringLines int) (Stream, *CaptureBuf) {
	f := r.alloc(&Flow{Kind: Capture, Producer: prod})
	buf := &CaptureBuf{f: f}
	f.Data = NewTapBuffer(f, ringLines, sink)
	f.Data.BindRegistry(r)
	buf.tap = f.Data
	return Stream{FID: f.ID, W: buf}, buf
}

// Dup creates a dup flow aliasing src (2>&1).
func (r *Registry) Dup(src Stream, cmd *CommandRef) Stream {
	f := r.alloc(&Flow{Kind: Dup, DupOf: src.FID, Producer: cmd})
	if s := r.Get(src.FID); s != nil {
		r.link(s, f)
	}
	// A dup shares the source's writer; a write to it is a write to src.
	return Stream{FID: f.ID, W: src.W, R: src.R, File: src.File}
}

// Terminal creates a job-boundary flow (fd0/1/2 of the whole job).
func (r *Registry) Terminal(slot Slot, w io.Writer, rd io.Reader) Stream {
	f := r.alloc(&Flow{Kind: FdEndpoint, Fd: slot, Path: slot.String()})
	return Stream{FID: f.ID, W: w, R: rd}
}

// TerminalTap is Terminal for a writer that must also be captured: the bytes a
// stage writes to the job's own stdout/stderr are teed into the sink, so the
// daemon observes them like any other flow. stderr (rd == nil) is captured but
// its live bytes are not forwarded (they belong to fid 3).
func (r *Registry) TerminalTap(slot Slot, w io.Writer, rd io.Reader, sink Sink, ringLines int) Stream {
	f := r.alloc(&Flow{Kind: FdEndpoint, Fd: slot, Path: slot.String()})
	if w == nil {
		return Stream{FID: f.ID, R: rd}
	}
	if sink != nil {
		f.Data = NewTapBuffer(f, ringLines, sink)
		f.Data.BindRegistry(r)
		return Stream{FID: f.ID, W: io.MultiWriter(w, f.Data)}
	}
	return Stream{FID: f.ID, W: w}
}

// NewFd creates an fd-endpoint flow for one stage: the thing that *is* the
// stage's stdin/stdout/stderr. It carries no data of its own — it names where
// that fd points (a pipe end, a file, a terminal) and is linked by edges to the
// data-carrying flows. This is what makes per-stage fds observable: every stage
// gets three of these, so `cmd`'s stderr is distinguishable from the job's.
func (r *Registry) NewFd(slot Slot, owner *CommandRef) *Flow {
	f := r.alloc(&Flow{Kind: FdEndpoint, Fd: slot, Owner: owner, Producer: owner})
	return f
}

// Link connects two flows with an edge (upstream -> downstream).
func (r *Registry) Link(from, to *Flow) { r.link(from, to) }

// Finalize marks every flow closed and computes hashes.
func (r *Registry) Finalize() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range r.flows {
		if !f.closed {
			f.closed = true
			f.Closed = r.now()
		}
	}
}

// ── small helpers ───────────────────────────────────────────────────────────

// countingWriter tallies bytes/lines for a flow whose data lives in a file.
type countingWriter struct {
	w io.Writer
	f *Flow
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	var nl int64
	for _, b := range p[:n] {
		if b == '\n' {
			nl++
		}
	}
	c.f.addBytes(n, nl)
	return n, err
}

// CaptureBuf accumulates a $() substitution's output.
type CaptureBuf struct {
	f   *Flow
	tap *TapBuffer
	buf []byte
}

// Write implements io.Writer.
func (c *CaptureBuf) Write(p []byte) (int, error) {
	c.buf = append(c.buf, p...)
	if c.tap != nil {
		_, _ = c.tap.Write(p)
	}
	return len(p), nil
}

// String returns the captured text with trailing newlines stripped.
func (c *CaptureBuf) String() string {
	s := string(c.buf)
	for len(s) > 0 && s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
	}
	return s
}
