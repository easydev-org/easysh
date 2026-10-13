package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/easydev-org/easysh/pkg/proto"
	"github.com/easydev-org/easysh/pkg/store"
)

// Server owns all sessions and serves the wire protocol over a unix socket
// (and optionally TCP).
type Server struct {
	mu       sync.Mutex
	sessions map[string]*Session
	store    *store.Store
	dir      string
	env      []string
	detach   time.Duration
}

// NewServer creates a server. store may be nil (no persistence).
func NewServer(dir string, env []string, st *store.Store, detach time.Duration) *Server {
	if detach <= 0 {
		detach = DefaultDetachAfter
	}
	return &Server{
		sessions: map[string]*Session{},
		store:    st,
		dir:      dir,
		env:      env,
		detach:   detach,
	}
}

// Session returns (creating if needed) a named session.
func (s *Server) Session(name string) *Session {
	if name == "" {
		name = "default"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[name]
	if sess == nil {
		sess = NewSession(name, s.dir, s.env, s.store, s.detach)
		s.sessions[name] = sess
	}
	return sess
}

// SessionNames returns the names of sessions created so far.
func (s *Server) SessionNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.sessions))
	for n := range s.sessions {
		out = append(out, n)
	}
	return out
}

// FindJob returns a job by id across all sessions, or nil.
func (s *Server) FindJob(id string) *Job {
	s.mu.Lock()
	sessions := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()
	for _, sess := range sessions {
		if j := sess.Job(id); j != nil {
			return j
		}
	}
	return nil
}

// ServeUnix serves the protocol on a unix socket until ctx is done.
func (s *Server) ServeUnix(ctx context.Context, path string) error {
	os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return nil
		}
		go s.handle(ctx, conn)
	}
}

// ServeTCP serves the protocol on a TCP address (for the worker).
func (s *Server) ServeTCP(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return nil
		}
		go s.handle(ctx, conn)
	}
}

// handle serves one connection: one JSON request per line, replies streamed.
func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	dec := json.NewDecoder(bufio.NewReader(conn))
	enc := json.NewEncoder(conn)

	// Replies go through a bounded channel and a single writer goroutine, so a
	// slow or dead client can never block the job that is producing output
	// (which would otherwise hold the session's exec lock forever). When the
	// buffer is full we drop the event — the job keeps running and its output is
	// still persisted.
	out := make(chan proto.Response, 256)
	writerDone := make(chan struct{})
	var closedMu sync.Mutex
	closedFlag := false
	go func() {
		defer close(writerDone)
		for r := range out {
			if err := enc.Encode(&r); err != nil {
				return
			}
		}
	}()
	send := func(r proto.Response) {
		closedMu.Lock()
		done := closedFlag
		closedMu.Unlock()
		if done {
			return
		}
		defer func() { _ = recover() }() // a concurrent close may win the race
		select {
		case out <- r:
		default:
			// client is not draining; drop rather than block the job.
		}
	}
	defer func() {
		closedMu.Lock()
		closedFlag = true
		closedMu.Unlock()
		close(out)
		<-writerDone
	}()

	for {
		var req proto.Request
		if err := dec.Decode(&req); err != nil {
			return
		}
		s.dispatch(ctx, req, send)
	}
}

// dispatch runs one request.
func (s *Server) dispatch(ctx context.Context, req proto.Request, send func(proto.Response)) {
	sess := s.Session(req.Session)

	switch req.Op {
	case "ping":
		send(proto.Response{Event: "pong"})

	case "run":
		detach := s.detach
		if req.DetachAfterMs > 0 {
			detach = time.Duration(req.DetachAfterMs) * time.Millisecond
		}
		sess.SetDetachAfter(detach)

		j, err := sess.StartJob(ctx, req.Script, func(e Event) {
			switch e.Kind {
			case "stdout":
				send(proto.Response{Event: "stdout", JobID: e.JobID, Data: string(e.Data)})
			case "stderr":
				send(proto.Response{Event: "stderr", JobID: e.JobID, Data: string(e.Data)})
			case "detached":
				send(proto.Response{Event: "detached", JobID: e.JobID})
			case "done":
				send(proto.Response{Event: "done", JobID: e.JobID, Code: e.Code})
			}
		})
		if err != nil {
			send(proto.Response{Event: "error", Err: err.Error()})
			return
		}
		send(proto.Response{Event: "started", JobID: j.ID})

	case "jobs", "list":
		var out []proto.JobInfo
		for _, j := range sess.Jobs() {
			id, cmd, state, code, st, en, det, lines, nb := j.Info()
			out = append(out, proto.JobInfo{
				ID: id, Command: cmd, State: state, ExitCode: code,
				StartedAt: st, EndedAt: en, Detached: det, Lines: lines, Bytes: nb,
			})
		}
		send(proto.Response{Jobs: out})

	case "output":
		j := sess.Job(req.JobID)
		if j == nil {
			send(proto.Response{Event: "error", Err: "no such job"})
			return
		}
		send(proto.Response{Event: "output", JobID: j.ID, Data: string(j.Output(req.Tail))})

	case "wait":
		j := sess.Job(req.JobID)
		if j == nil {
			send(proto.Response{Event: "error", Err: "no such job"})
			return
		}
		code := j.Wait()
		send(proto.Response{Event: "done", JobID: j.ID, Code: code})

	case "kill":
		j := sess.Job(req.JobID)
		if j == nil {
			send(proto.Response{Event: "error", Err: "no such job"})
			return
		}
		j.Kill()
		send(proto.Response{Event: "killed", JobID: j.ID})

	case "stdin":
		j := sess.Job(req.JobID)
		if j == nil {
			send(proto.Response{Event: "error", Err: "no such job"})
			return
		}
		if err := j.Stdin([]byte(req.Data), req.Close); err != nil {
			send(proto.Response{Event: "error", JobID: j.ID, Err: err.Error()})
			return
		}
		send(proto.Response{Event: "stdin-ok", JobID: j.ID})

	case "graph":
		j := sess.Job(req.JobID)
		if j == nil {
			send(proto.Response{Event: "error", Err: "no such job"})
			return
		}
		fl, edges := j.Graph()
		b, _ := json.Marshal(struct {
			Flows []flowSnapshot `json:"flows"`
			Edges [][2]int64     `json:"edges"`
		}{fl, edges})
		send(proto.Response{Event: "graph", JobID: j.ID, Data: string(b)})

	case "complete":
		send(proto.Response{Completions: sess.Complete(req.Prefix)})

	case "highlight":
		send(proto.Response{Spans: Highlight(req.Line)})

	case "history":
		send(proto.Response{History: sess.History()})

	case "history-add":
		sess.AddHistory(req.Command)
		send(proto.Response{Event: "history-ok"})

	case "shutdown":
		send(proto.Response{Event: "bye"})
		os.Exit(0)

	default:
		send(proto.Response{Event: "error", Err: fmt.Sprintf("unknown op %q", req.Op)})
	}
}
