// Package httpapi exposes the daemon over HTTP/JSON (+ SSE + WebSocket) so a
// browser can run easysh, watch jobs, and inspect every job's dataflow graph.
//
// It is a thin adapter over the same Session/Job API the unix-socket protocol
// uses — the socket stays the low-latency path for the CLI, HTTP is the path for
// the WebUI and the worker.
package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/easydev-org/easysh/pkg/daemon"
	"github.com/easydev-org/easysh/pkg/store"
)

// Server is the HTTP API.
type Server struct {
	Srv   *daemon.Server
	Store *store.Store
	Token string // bearer token; empty disables auth
	UI    http.Handler

	// ctx outlives individual requests: a job started via POST /run must keep
	// running after the HTTP response is written (the whole point of the async
	// job model).
	ctx context.Context
}

// WithContext sets the base context used for jobs (call before Handler).
func (h *Server) WithContext(ctx context.Context) *Server { h.ctx = ctx; return h }

func (h *Server) base() context.Context {
	if h.ctx != nil {
		return h.ctx
	}
	return context.Background()
}

// Handler builds the HTTP routes.
func (h *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// API
	mux.HandleFunc("/api/health", h.get(h.health))
	mux.HandleFunc("/api/sessions", h.sessionsRoute)
	mux.HandleFunc("/api/sessions/", h.sessionRoutes)
	mux.HandleFunc("/api/jobs", h.get(h.listJobs))
	mux.HandleFunc("/api/jobs/", h.jobRoutes)

	// WebUI at "/" (same origin), if provided.
	if h.UI != nil {
		mux.Handle("/", h.UI)
	}
	return h.auth(mux)
}

// auth enforces a bearer token when configured (same as the worker).
func (h *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.Token != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if got == "" {
				got = r.URL.Query().Get("token")
			}
			if got != h.Token {
				http.Error(w, "unauthenticated", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// get wraps a handler that only supports GET.
func (h *Server) get(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		fn(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"ok": true, "time": time.Now().UnixMilli()})
}

// ── sessions ────────────────────────────────────────────────────────────────

type sessionInfo struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
	Jobs int    `json:"jobs"`
}

// sessionsRoute handles GET (list) and POST (create) /api/sessions.
func (h *Server) sessionsRoute(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listSessions(w, r)
	case http.MethodPost:
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Name == "" {
			body.Name = "default"
		}
		s := h.Srv.Session(body.Name) // creates it lazily
		writeJSON(w, sessionInfo{Name: s.Name, Dir: s.Dir(), Jobs: len(s.Jobs())})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	// We expose the sessions we know about. (The daemon creates sessions lazily;
	// asking for "default" is always valid.)
	names := h.Srv.SessionNames()
	if len(names) == 0 {
		names = []string{"default"}
	}
	out := make([]sessionInfo, 0, len(names))
	for _, n := range names {
		s := h.Srv.Session(n)
		out = append(out, sessionInfo{Name: n, Dir: s.Dir(), Jobs: len(s.Jobs())})
	}
	writeJSON(w, out)
}

// sessionRoutes dispatches /api/sessions/{name}/...
func (h *Server) sessionRoutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	parts := strings.SplitN(rest, "/", 2)
	name := parts[0]
	if name == "" {
		http.NotFound(w, r)
		return
	}
	sess := h.Srv.Session(name)
	sub := ""
	if len(parts) > 1 {
		sub = parts[1]
	}
	switch {
	case sub == "run" && r.Method == http.MethodPost:
		h.run(w, r, sess)
	case sub == "history" && r.Method == http.MethodGet:
		writeJSON(w, map[string]any{"history": sess.History()})
	case sub == "history" && r.Method == http.MethodPost:
		var body struct {
			Command string `json:"command"`
		}
		if json.NewDecoder(r.Body).Decode(&body) == nil {
			sess.AddHistory(body.Command)
		}
		writeJSON(w, map[string]any{"ok": true})
	case sub == "ws":
		h.ws(w, r, sess)
	default:
		http.NotFound(w, r)
	}
}

// run starts a job and returns its id (fire-and-forget; watch via SSE/WS).
func (h *Server) run(w http.ResponseWriter, r *http.Request, sess *daemon.Session) {
	var body struct {
		Script   string `json:"script"`
		DetachMs int    `json:"detachMs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.DetachMs > 0 {
		sess.SetDetachAfter(time.Duration(body.DetachMs) * time.Millisecond)
	}
	j, err := sess.StartJob(h.base(), body.Script, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"jobId": j.ID})
}

// ── jobs ────────────────────────────────────────────────────────────────────

type jobJSON struct {
	ID        string `json:"id"`
	Command   string `json:"command"`
	State     string `json:"state"`
	ExitCode  int    `json:"exitCode"`
	StartedAt int64  `json:"startedAt"`
	EndedAt   int64  `json:"endedAt"`
	Detached  bool   `json:"detached"`
	Lines     int    `json:"lines"`
	Bytes     int    `json:"bytes"`
}

func (h *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	sess := h.Srv.Session(r.URL.Query().Get("session"))
	var out []jobJSON
	for _, j := range sess.Jobs() {
		id, cmd, state, code, st, en, det, lines, nb := j.Info()
		out = append(out, jobJSON{ID: id, Command: cmd, State: state, ExitCode: code,
			StartedAt: st, EndedAt: en, Detached: det, Lines: lines, Bytes: nb})
	}
	// Also include historical jobs from the store (running jobs are already
	// listed above; the store covers jobs from previous daemon runs).
	if h.Store != nil {
		seen := map[string]bool{}
		for _, j := range out {
			seen[j.ID] = true
		}
		if rows, err := h.Store.QueryJobs(200); err == nil {
			for _, row := range rows {
				if seen[row.ID] {
					continue
				}
				out = append(out, jobJSON{ID: row.ID, Command: row.Command, State: row.State,
					ExitCode: row.ExitCode, StartedAt: row.StartedAt, EndedAt: row.FinishedAt, Detached: row.Detached})
			}
		}
	}
	writeJSON(w, out)
}

// jobRoutes dispatches /api/jobs/{id}/...
func (h *Server) jobRoutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
	parts := strings.Split(rest, "/")
	id := parts[0]
	if id == "" {
		http.NotFound(w, r)
		return
	}
	sub := ""
	if len(parts) > 1 {
		sub = parts[1]
	}
	// Find the job in any session.
	job := h.Srv.FindJob(id)

	switch {
	case sub == "" && r.Method == http.MethodGet:
		h.jobDetail(w, job, id)
	case sub == "output" && r.Method == http.MethodGet:
		h.jobOutput(w, r, job, id)
	case sub == "graph" && r.Method == http.MethodGet:
		h.jobGraph(w, job, id)
	case sub == "watch" && r.Method == http.MethodGet:
		h.jobWatch(w, r, job, id)
	case sub == "stdin" && r.Method == http.MethodPost:
		h.jobStdin(w, r, job)
	case sub == "kill" && r.Method == http.MethodPost:
		if job != nil {
			job.Kill()
		}
		writeJSON(w, map[string]any{"ok": true})
	case sub == "flows" && len(parts) > 2 && r.Method == http.MethodGet:
		fid, _ := strconv.ParseInt(parts[2], 10, 64)
		// /flows/{fid} or /flows/{fid}/watch
		if len(parts) > 3 && parts[3] == "watch" {
			h.jobFlowWatch(w, r, job, id, fid)
		} else {
			h.jobFlowContent(w, r, id, fid)
		}
	default:
		http.NotFound(w, r)
	}
}

func (h *Server) jobDetail(w http.ResponseWriter, job *daemon.Job, id string) {
	if job == nil {
		if h.Store != nil {
			if rows, err := h.Store.QueryJobs(500); err == nil {
				for _, row := range rows {
					if row.ID == id {
						writeJSON(w, jobJSON{ID: row.ID, Command: row.Command, State: row.State,
							ExitCode: row.ExitCode, StartedAt: row.StartedAt, EndedAt: row.FinishedAt, Detached: row.Detached})
						return
					}
				}
			}
		}
		http.NotFound(w, nil)
		return
	}
	_, cmd, state, code, st, en, det, lines, nb := job.Info()
	writeJSON(w, jobJSON{ID: id, Command: cmd, State: state, ExitCode: code,
		StartedAt: st, EndedAt: en, Detached: det, Lines: lines, Bytes: nb})
}

func (h *Server) jobOutput(w http.ResponseWriter, r *http.Request, job *daemon.Job, id string) {
	tail := 0
	if t := r.URL.Query().Get("tail"); t != "" {
		tail, _ = strconv.Atoi(t)
	}
	if job != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(job.Output(tail))
		return
	}
	// Fall back to the store.
	if h.Store != nil {
		if b, err := h.Store.Flow.Content(id, 2); err == nil && len(b) > 0 {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write(b)
			return
		}
	}
	http.NotFound(w, r)
}

// flowJSON mirrors daemon.flowSnapshot with exported JSON names.
type flowJSON struct {
	FID      int64  `json:"fid"`
	Kind     string `json:"kind"`
	Fd       string `json:"fd,omitempty"`
	Owner    string `json:"owner,omitempty"`
	Producer string `json:"producer,omitempty"`
	Consumer string `json:"consumer,omitempty"`
	Path     string `json:"path,omitempty"`
	Bytes    int64  `json:"bytes"`
	Lines    int64  `json:"lines"`
	// Source span of the owning stage (1-based; end exclusive), for highlighting.
	Line    int `json:"line,omitempty"`
	Col     int `json:"col,omitempty"`
	EndLine int `json:"endLine,omitempty"`
	EndCol  int `json:"endCol,omitempty"`
	// Iter is the loop-iteration label ("#2") when the stage ran inside a loop.
	Iter string `json:"iter,omitempty"`
}

func (h *Server) jobGraph(w http.ResponseWriter, job *daemon.Job, id string) {
	var flows []flowJSON
	var edges [][2]int64
	if job != nil {
		fs, es := job.Graph()
		for _, f := range fs {
			flows = append(flows, flowJSON{FID: f.FID, Kind: f.Kind, Fd: f.Fd, Owner: f.Owner,
				Producer: f.Producer, Consumer: f.Consumer, Path: f.Path, Bytes: f.Bytes, Lines: f.Lines,
				Line: f.Line, Col: f.Col, EndLine: f.EndLine, EndCol: f.EndCol, Iter: f.Iter})
		}
		edges = es
	} else if h.Store != nil {
		if rows, err := h.Store.Flow.Flows(id); err == nil {
			for _, f := range rows {
				flows = append(flows, flowJSON{FID: f.FID, Kind: f.Kind, Fd: f.Fd, Owner: f.Owner,
					Producer: f.Producer, Consumer: f.Consumer, Path: f.Path, Bytes: f.Bytes, Lines: f.Lines,
					Line: f.Line, Col: f.Col, EndLine: f.EndLine, EndCol: f.EndCol, Iter: f.Iter})
			}
		}
		edges, _ = h.Store.Flow.Edges(id)
	}
	if flows == nil {
		flows = []flowJSON{}
	}
	if edges == nil {
		edges = [][2]int64{}
	}
	writeJSON(w, map[string]any{"flows": flows, "edges": edges})
}

func (h *Server) jobStdin(w http.ResponseWriter, r *http.Request, job *daemon.Job) {
	if job == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Data  string `json:"data"`
		Close bool   `json:"close"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := job.Stdin([]byte(body.Data), body.Close); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// jobFlowContent returns one flow's captured intermediate bytes.
func (h *Server) jobFlowContent(w http.ResponseWriter, r *http.Request, jobID string, fid int64) {
	if h.Store != nil {
		if b, err := h.Store.Flow.Content(jobID, fid); err == nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write(b)
			return
		}
	}
	http.NotFound(w, r)
}

// jobFlowWatch streams one flow's bytes live (SSE). For a running job it taps
// the flow; for a finished job it replays the stored chunks. Non-blocking: a
// slow watcher never slows the job.
func (h *Server) jobFlowWatch(w http.ResponseWriter, r *http.Request, job *daemon.Job, jobID string, fid int64) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	if job == nil {
		if h.Store != nil {
			if b, _ := h.Store.Flow.Content(jobID, fid); len(b) > 0 {
				sse(w, "output", string(b))
			}
		}
		sse(w, "done", `{}`)
		flusher.Flush()
		return
	}

	// Replay what we have so far, then stream live.
	if h.Store != nil {
		if b, _ := h.Store.Flow.Content(jobID, fid); len(b) > 0 {
			sse(w, "output", string(b))
			flusher.Flush()
		}
	}
	ch, cancel := job.SubscribeFlow(fid)
	defer cancel()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case b, ok := <-ch:
			if !ok {
				sse(w, "done", `{}`)
				flusher.Flush()
				return
			}
			sse(w, "output", string(b))
			flusher.Flush()
		}
	}
}

// ── SSE watch ───────────────────────────────────────────────────────────────

// jobWatch streams a job's output as Server-Sent Events until it finishes.
func (h *Server) jobWatch(w http.ResponseWriter, r *http.Request, job *daemon.Job, jobID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	if job == nil {
		// Finished job: send what the store has, then done.
		if h.Store != nil {
			if b, _ := h.Store.Flow.Content(jobID, 2); len(b) > 0 {
				sse(w, "output", string(b))
			}
		}
		sse(w, "done", `{"exitCode":0}`)
		flusher.Flush()
		return
	}

	ch := make(chan string, 256)
	done := make(chan struct{})
	ctx := r.Context()

	// Tap the job's live output by polling its buffer; simpler and robust than
	// plumbing a subscription through Job. (The buffer is the source of truth.)
	go func() {
		defer close(done)
		last := 0
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				buf := job.Output(0)
				if len(buf) > last {
					select {
					case ch <- string(buf[last:]):
						last = len(buf)
					default:
					}
				}
				if _, _, state, _, _, _, _, _, _ := job.Info(); state != "running" && state != "detached" {
					// flush the rest, then done
					if len(buf) > last {
						select {
						case ch <- string(buf[last:]):
						default:
						}
					}
					_, _, _, code, _, _, _, _, _ := job.Info()
					select {
					case ch <- "\x00DONE\x00" + strconv.Itoa(code):
					default:
					}
					return
				}
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			if strings.HasPrefix(msg, "\x00DONE\x00") {
				code, _ := strconv.Atoi(strings.TrimPrefix(msg, "\x00DONE\x00"))
				sse(w, "done", `{"exitCode":`+strconv.Itoa(code)+`}`)
				flusher.Flush()
				return
			}
			sse(w, "output", msg)
			flusher.Flush()
		}
	}
}

func sse(w io.Writer, event, data string) {
	// SSE data must not contain raw newlines; encode them per line.
	for _, line := range strings.Split(data, "\n") {
		io.WriteString(w, "event: "+event+"\ndata: "+line+"\n\n")
	}
}
