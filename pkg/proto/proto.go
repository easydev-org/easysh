// Package proto defines the easysh daemon wire protocol: a line-delimited JSON
// request/response over a unix socket (local CLI) or TCP (worker). It is
// deliberately tiny and language-neutral.
package proto

// Request is one command sent to a session.
type Request struct {
	// Session names the session to run in ("" = default). A session keeps
	// variables, functions, cwd and its job list across requests.
	Session string `json:"session,omitempty"`

	// Op is the operation: "run", "complete", "highlight", "jobs", "output",
	// "wait", "detach", "list", "new", "kill", "shutdown".
	Op string `json:"op"`

	// Script is the source for Op=="run".
	Script string `json:"script,omitempty"`

	// Prefix is the partial word for Op=="complete"/"highlight".
	Prefix string `json:"prefix,omitempty"`
	Line   string `json:"line,omitempty"`

	// JobID targets a job for output/wait/kill.
	JobID string `json:"jobId,omitempty"`

	// DetachAfterMs, when >0, overrides the session default: a foreground job
	// still running after this long is detached to the background.
	DetachAfterMs int `json:"detachAfterMs,omitempty"`

	// Tail, for Op=="output", limits returned lines (0 = all).
	Tail int `json:"tail,omitempty"`

	// Data is stdin bytes for Op=="stdin".
	Data string `json:"data,omitempty"`

	// Close, for Op=="stdin", closes the job's stdin (EOF) after writing Data.
	Close bool `json:"close,omitempty"`

	// Command, for Op=="history-add", is the typed line to record.
	Command string `json:"command,omitempty"`
}

// Response is one reply, or one streamed event.
type Response struct {
	// Event is set for streamed output: "stdout", "stderr", "graph", "done",
	// "detached", "error".
	Event string `json:"event,omitempty"`

	// Data carries event payload: output bytes (as a string) for stdout/stderr,
	// a JSON FlowGraph for "graph", an exit code string for "done".
	Data string `json:"data,omitempty"`

	// JobID is set once a job is created.
	JobID string `json:"jobId,omitempty"`

	// Code is the exit code for "done"/terminal events.
	Code int `json:"code,omitempty"`

	// Jobs carries the job list for Op=="jobs"/"list".
	Jobs []JobInfo `json:"jobs,omitempty"`

	// Completions carries candidates for Op=="complete".
	Completions []Completion `json:"completions,omitempty"`

	// Spans carries highlight spans for Op=="highlight".
	Spans []Span `json:"spans,omitempty"`

	// History carries the session's typed commands for Op=="history".
	History []string `json:"history,omitempty"`

	// Err is set when the operation failed.
	Err string `json:"err,omitempty"`
}

// JobInfo describes one job (running or finished).
type JobInfo struct {
	ID        string `json:"id"`
	Command   string `json:"command"`
	State     string `json:"state"` // running | done | failed | detached | killed
	ExitCode  int    `json:"exitCode"`
	StartedAt int64  `json:"startedAt"` // unix millis
	EndedAt   int64  `json:"endedAt"`
	Detached  bool   `json:"detached"`
	Lines     int    `json:"lines"`
	Bytes     int    `json:"bytes"`
}

// Completion is one completion candidate.
type Completion struct {
	Text string `json:"text"`
	Kind string `json:"kind"` // command | variable | function | builtin | file | keyword
	Desc string `json:"desc,omitempty"`
}

// Span is one syntax-highlight span over [Start,End) of a line.
type Span struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Class string `json:"class"` // keyword | string | param | cmdsubst | arith | operator | command | comment
}
