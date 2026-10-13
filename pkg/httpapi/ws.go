package httpapi

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/easydev-org/easysh/pkg/daemon"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	// Same-origin WebUI; allow any origin for a local dev tool.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// wsMsg is the browser<->daemon frame.
type wsMsg struct {
	// Client -> server
	Op     string `json:"op,omitempty"`       // run | stdin | kill | complete | highlight | ping
	Script string `json:"script,omitempty"`   // run
	JobID  string `json:"jobId,omitempty"`    // stdin | kill
	Data   string `json:"data,omitempty"`     // stdin
	Close  bool   `json:"close,omitempty"`    // stdin
	Prefix string `json:"prefix,omitempty"`   // complete
	Line   string `json:"line,omitempty"`     // highlight
	Detach int    `json:"detachMs,omitempty"` // run

	// Server -> client
	Event string          `json:"event,omitempty"` // started|output|done|detached|completions|spans|error|pong
	ID    string          `json:"id,omitempty"`
	Code  int             `json:"code,omitempty"`
	Spans []spanOut       `json:"spans,omitempty"`
	Comps []completionOut `json:"completions,omitempty"`
	Err   string          `json:"err,omitempty"`
}

type spanOut struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Class string `json:"class"`
}

type completionOut struct {
	Text string `json:"text"`
	Kind string `json:"kind"`
}

// ws upgrades the connection and serves the interactive protocol: the browser
// sends run/stdin, the daemon streams output/detach/done — plus complete and
// highlight for the in-browser editor (which reuses the daemon's lexer).
func (h *Server) ws(w http.ResponseWriter, r *http.Request, sess *daemon.Session) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	var writeMu sync.Mutex
	send := func(m wsMsg) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.WriteJSON(m)
	}

	for {
		var m wsMsg
		if err := conn.ReadJSON(&m); err != nil {
			return
		}
		switch m.Op {
		case "ping":
			send(wsMsg{Event: "pong"})

		case "run":
			if m.Detach > 0 {
				sess.SetDetachAfter(msToDur(m.Detach))
			}
			sess.AddHistory(m.Script)
			j, err := sess.StartJob(r.Context(), m.Script, func(e daemon.Event) {
				switch e.Kind {
				case "stdout", "stderr":
					send(wsMsg{Event: "output", ID: e.JobID, Data: string(e.Data)})
				case "detached":
					send(wsMsg{Event: "detached", ID: e.JobID})
				case "done":
					send(wsMsg{Event: "done", ID: e.JobID, Code: e.Code})
				}
			})
			if err != nil {
				send(wsMsg{Event: "error", Err: err.Error()})
				continue
			}
			send(wsMsg{Event: "started", ID: j.ID})

		case "stdin":
			if j := sess.Job(m.JobID); j != nil {
				if err := j.Stdin([]byte(m.Data), m.Close); err != nil {
					send(wsMsg{Event: "error", ID: m.JobID, Err: err.Error()})
				}
			}

		case "kill":
			if j := sess.Job(m.JobID); j != nil {
				j.Kill()
			}

		case "complete":
			var out []completionOut
			for _, c := range sess.Complete(m.Prefix) {
				out = append(out, completionOut{Text: c.Text, Kind: c.Kind})
			}
			send(wsMsg{Event: "completions", Comps: out})

		case "highlight":
			var out []spanOut
			for _, sp := range daemon.Highlight(m.Line) {
				out = append(out, spanOut{Start: sp.Start, End: sp.End, Class: sp.Class})
			}
			send(wsMsg{Event: "spans", Spans: out})
		}
	}
}

// jsonUnused keeps encoding/json imported for future binary frames.
var _ = json.Marshal

func msToDur(ms int) time.Duration { return time.Duration(ms) * time.Millisecond }
