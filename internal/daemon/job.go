package daemon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"github.com/abcp-sdk/easysh/internal/flow"
	"github.com/abcp-sdk/easysh/internal/store"
)

// errNoStdin is returned when a job has no stdin pipe (already finished).
var errNoStdin = errors.New("job stdin is closed")

// Job is one running or finished command.
type Job struct {
	ID      string
	Command string

	mu       sync.Mutex
	state    string // running | detached | done | failed | killed
	exitCode int
	started  time.Time
	ended    time.Time
	detached bool
	out      bytes.Buffer // full captured stdout+stderr (bounded by store)
	sink     *jobSink
	graph    []flowSnapshot // creation order, for stable display
	byFid    map[int64]int  // fid -> index in graph
	edges    [][2]int64
	flowLive map[int64]*liveFlow // per-flow stream subscribers
	reg      *flow.Registry      // the live registry (nil until run)

	onOut   func(Event)
	cancel  context.CancelFunc
	session *Session

	// stdinW is the write end of the job's stdin pipe. The daemon writes to it
	// (interactive input: `cat`, `read`, a REPL); the executor reads the other
	// end as the job's stdin.
	stdinW io.WriteCloser
	stdinR io.ReadCloser
}

// flowSnapshot is a serializable flow row.
type flowSnapshot struct {
	FID      int64
	Kind     string
	Fd       string // stdin/stdout/stderr, for fd endpoints
	Owner    string // the stage this fd belongs to
	Producer string
	Consumer string
	Path     string
	Bytes    int64
	Lines    int64
	// Source span of the owning stage, so the UI can highlight which segment of
	// the pipeline this flow belongs to.
	Line    int
	Col     int
	EndLine int
	EndCol  int
	// Iter is the loop-iteration label ("#2", "#2.1") when the stage ran inside a
	// loop, distinguishing instances of one source segment.
	Iter string
}

// jobSink persists a job's flow chunks and forwards live output.
type jobSink struct {
	job       *Job
	store     storeWriter
	seq       map[int64]int64
	mu        sync.Mutex
	stream    int
	lineSeq   int64
	stdoutFID int64 // the fid of the job's own stdout (set per run)
}

type storeWriter interface {
	EnqueueChunk(jobID string, fid int64, seq int64, data []byte)
	EnqueueLine(jobID string, seq int64, stream int, data []byte)
}

// Enqueue implements flow.Sink: it both persists the chunk and (for the job's
// own stdout/stderr fid) forwards it live to the client.
func (js *jobSink) Enqueue(fid flow.FID, p []byte) {
	js.mu.Lock()
	n := js.seq[int64(fid)]
	js.seq[int64(fid)] = n + 1
	isStdout := js.stdoutFID != 0 && int64(fid) == js.stdoutFID
	js.mu.Unlock()
	if js.store != nil {
		js.store.EnqueueChunk(js.job.ID, int64(fid), n, append([]byte(nil), p...))
	}
	// Forward the job's own stdout live; intermediate flows are only persisted.
	if isStdout {
		js.job.appendOut(p, 1)
	}
}

// SetStdoutFID implements flow.StdoutAware.
func (js *jobSink) SetStdoutFID(fid flow.FID) {
	js.mu.Lock()
	js.stdoutFID = int64(fid)
	js.mu.Unlock()
}

// newJob constructs a job bound to its session.
func newJob(id, command string, s *Session, detach time.Duration, onOut func(Event)) *Job {
	j := &Job{
		ID:       id,
		Command:  command,
		state:    "running",
		started:  time.Now(),
		onOut:    onOut,
		session:  s,
		byFid:    map[int64]int{},
		flowLive: map[int64]*liveFlow{},
	}
	j.sink = &jobSink{job: j, seq: map[int64]int64{}}
	// Only store the store when it is a real value: assigning a nil *Store to
	// the interface would make `store != nil` true while the pointer is nil.
	if s.store != nil {
		j.sink.store = s.store
	}
	// A pipe for the job's stdin: the daemon writes j.stdinW, the executor reads
	// j.stdinR. This is what lets a client feed `cat`/`read`/a REPL at run time.
	//
	// It MUST be an os.Pipe, not an io.Pipe: os/exec passes an *os.File straight
	// to the child (no copying goroutine), whereas a non-*os.File stdin makes
	// exec spawn an io.Copy goroutine that never returns until the reader hits
	// EOF — which deadlocks every piped command, because that reader is never
	// closed while a sibling stage waits on the pipe.
	pr, pw, err := os.Pipe()
	if err != nil {
		// Fall back to an in-memory pipe (no interactive stdin).
		j.stdinR, j.stdinW = io.Pipe()
	} else {
		j.stdinR, j.stdinW = pr, pw
	}
	return j
}

// Stdin writes to the job's stdin, optionally closing it (EOF). It mirrors the
// worker's JobStdin so interactive commands work identically.
func (j *Job) Stdin(data []byte, closeAfter bool) error {
	j.mu.Lock()
	w := j.stdinW
	j.mu.Unlock()
	if w == nil {
		return errNoStdin
	}
	if len(data) > 0 {
		if _, err := w.Write(data); err != nil {
			return err
		}
	}
	if closeAfter {
		return w.Close()
	}
	return nil
}

// run executes the command and applies the detach policy.
func (j *Job) run(ctx context.Context) {
	// The session's runner (and its variable/function tables) is shared, so jobs
	// in one session execute one at a time. Detach still returns the client's
	// prompt immediately; a *new* command in the same session waits for the
	// detached job to finish.
	j.session.execMu.Lock()
	defer j.session.execMu.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	j.mu.Lock()
	j.cancel = cancel
	j.mu.Unlock()
	defer cancel()

	// Watchdog: if still running after the threshold, mark detached. The job
	// keeps running; only its status changes, so the client gets its prompt back.
	detachTimer := time.AfterFunc(j.session.DetachAfter(), func() {
		j.mu.Lock()
		if j.state == "running" {
			j.detached = true
			j.state = "detached"
			if j.session.store != nil {
				j.session.store.MarkDetached(j.ID)
			}
			if j.onOut != nil {
				j.onOut(Event{Kind: "detached", JobID: j.ID})
			}
		}
		j.mu.Unlock()
	})
	defer detachTimer.Stop()

	// Build the graph live and stream per-flow writes: the daemon sets the hook
	// so the WebUI can show flows as they appear and each flow's growth.
	j.session.runner.SetRegistryHook(func(reg *flow.Registry) {
		j.mu.Lock()
		j.reg = reg
		j.mu.Unlock()
		reg.OnAlloc(func(f *flow.Flow) {
			j.addFlow(f)
		})
		reg.OnWrite(func(fid flow.FID, p []byte) {
			j.onFlowWrite(fid, p)
		})
	})
	res, err := j.session.runner.RunWithIO(ctx, j.Command, j.sink, j.stdinR)
	j.mu.Lock()
	j.exitCode = res.ExitCode
	j.ended = time.Now()
	// Close the stdin pipe ends: nothing more will be written, and a lingering
	// os.Pipe read end would otherwise keep a stage's reader alive.
	if j.stdinR != nil {
		j.stdinR.Close()
	}
	if j.stdinW != nil {
		j.stdinW.Close()
	}
	if j.state == "running" || j.state == "detached" {
		if res.ExitCode == 0 {
			j.state = "done"
		} else {
			j.state = "failed"
		}
	}
	// Close every per-flow live stream so watchers see EOF.
	for _, lf := range j.flowLive {
		close(lf.ch)
	}
	j.flowLive = map[int64]*liveFlow{}
	final := j.state
	code := j.exitCode
	snapshot := append([]flowSnapshot(nil), j.graph...)
	var edges [][2]int64
	if res.Registry != nil {
		edges = append(edges, j.edgesFor(res.Registry)...)
	}
	j.mu.Unlock()

	// Persist (outside the job lock: the store is its own thing).
	if j.session.store != nil {
		for _, fr := range snapshot {
			j.session.store.PutFlow(j.ID, store.FlowRow{
				FID: fr.FID, Kind: fr.Kind, Fd: fr.Fd, Owner: fr.Owner,
				Producer: fr.Producer, Consumer: fr.Consumer,
				Path: fr.Path, Bytes: fr.Bytes, Lines: fr.Lines,
				StartedAt: j.started.UnixMilli(), ClosedAt: j.ended.UnixMilli(),
			})
		}
		for _, e := range edges {
			j.session.store.PutEdge(j.ID, e[0], e[1])
		}
		// Emit every flow's trailing partial line (a stream not ending in '\n'),
		// so nothing is lost.
		for _, fr := range snapshot {
			j.session.store.EnqueueFlushFlow(j.ID, fr.FID)
		}
		j.session.store.FinishJob(j.ID, final, code, time.Now().UnixMilli())
	}
	if j.onOut != nil {
		j.onOut(Event{Kind: "done", JobID: j.ID, Code: code})
	}
	_ = err
}

// edgesFor returns the current edges from a registry (caller must hold j.mu).
func (j *Job) edgesFor(reg *flow.Registry) [][2]int64 {
	var edges [][2]int64
	for _, f := range reg.Flows() {
		for _, o := range f.Outputs {
			edges = append(edges, [2]int64{int64(f.ID), int64(o)})
		}
	}
	return edges
}

// appendOut records output and, when not detached, streams it live.
func (j *Job) appendOut(p []byte, stream int) {
	j.mu.Lock()
	j.out.Write(p)
	seq := j.sink.lineSeq
	j.sink.lineSeq++
	onOut := j.onOut
	detached := j.detached
	j.mu.Unlock()
	if j.session.store != nil {
		j.session.store.EnqueueLine(j.ID, seq, stream, append([]byte(nil), p...))
	}
	if onOut != nil && !detached {
		onOut(Event{Kind: "stdout", JobID: j.ID, Data: p})
	}
}

// Info returns a snapshot for the jobs list.
func (j *Job) Info() (id, cmd, state string, code int, started, ended int64, detached bool, lines, nbytes int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	nl := bytes.Count(j.out.Bytes(), []byte{'\n'})
	return j.ID, j.Command, j.state, j.exitCode,
		j.started.UnixMilli(), j.ended.UnixMilli(), j.detached,
		nl, j.out.Len()
}

// Output returns the captured output (optionally only the last tail lines).
func (j *Job) Output(tail int) []byte {
	j.mu.Lock()
	defer j.mu.Unlock()
	b := j.out.Bytes()
	if tail <= 0 {
		return append([]byte(nil), b...)
	}
	lines := bytes.Split(b, []byte{'\n'})
	if len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	return bytes.Join(lines, []byte{'\n'})
}

// Graph returns the flow rows and edges.
func (j *Job) Graph() ([]flowSnapshot, [][2]int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	flows := append([]flowSnapshot(nil), j.graph...)
	// Refresh byte/line counts and edges from the live registry, so a running
	// job's graph is accurate (not just at the end).
	if j.reg != nil {
		idx := map[int64]int{}
		for i := range flows {
			idx[flows[i].FID] = i
		}
		for _, f := range j.reg.Flows() {
			if i, ok := idx[int64(f.ID)]; ok {
				nb, nl := f.Counts()
				flows[i].Bytes = nb
				flows[i].Lines = nl
			}
		}
		var edges [][2]int64
		for _, f := range j.reg.Flows() {
			for _, o := range f.Outputs {
				edges = append(edges, [2]int64{int64(f.ID), int64(o)})
			}
		}
		return flows, edges
	}
	return flows, append([][2]int64(nil), j.edges...)
}

// Kill cancels a running job.
func (j *Job) Kill() {
	j.mu.Lock()
	if j.cancel != nil {
		j.cancel()
	}
	j.state = "killed"
	j.mu.Unlock()
}

// Wait blocks until the job finishes.
func (j *Job) Wait() int {
	for {
		j.mu.Lock()
		st := j.state
		code := j.exitCode
		j.mu.Unlock()
		if st != "running" && st != "detached" {
			return code
		}
		time.Sleep(10 * time.Millisecond)
	}
}
