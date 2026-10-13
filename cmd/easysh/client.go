// Package main implements the easysh client: it connects to the daemon socket
// and drives an interactive REPL (or runs a one-shot script). Job management
// lives here and in the daemon, NOT in the shell language.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"

	"github.com/easydev-org/easysh/pkg/proto"
)

// Client is a connection to the daemon.
type Client struct {
	conn net.Conn
	enc  *json.Encoder
	dec  *json.Decoder
}

// Dial connects to the daemon at addr (a unix socket path or host:port).
func Dial(network, addr string) (*Client, error) {
	conn, err := net.Dial(network, addr)
	if err != nil {
		return nil, err
	}
	return &Client{
		conn: conn,
		enc:  json.NewEncoder(conn),
		dec:  json.NewDecoder(bufio.NewReader(conn)),
	}, nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Send sends one request.
func (c *Client) Send(req proto.Request) error { return c.enc.Encode(&req) }

// Recv reads one response.
func (c *Client) Recv() (proto.Response, error) {
	var r proto.Response
	err := c.dec.Decode(&r)
	return r, err
}

// RunOne sends a script and streams its output/graph to stdout. It returns the
// exit code, or -1 if the job was detached (so the caller keeps the prompt).
func (c *Client) RunOne(session, script string, detachMs int, showGraph bool) (int, string) {
	if err := c.Send(proto.Request{Session: session, Op: "run", Script: script, DetachAfterMs: detachMs}); err != nil {
		fmt.Fprintln(os.Stderr, "send:", err)
		return 1, ""
	}
	jobID := ""
	for {
		r, err := c.Recv()
		if err != nil {
			fmt.Fprintln(os.Stderr, "recv:", err)
			return 1, jobID
		}
		switch r.Event {
		case "started":
			jobID = r.JobID
		case "stdout", "stderr":
			os.Stdout.WriteString(r.Data)
		case "detached":
			fmt.Fprintf(os.Stderr, "\n[job %s detached — still running in the background]\n", r.JobID)
			return -1, r.JobID
		case "done":
			if showGraph && r.JobID != "" {
				c.PrintGraph(session, r.JobID)
			}
			return r.Code, r.JobID
		case "error":
			fmt.Fprintln(os.Stderr, "error:", r.Err)
			return 1, jobID
		}
	}
}

// PrintGraph fetches and prints a job's dataflow graph.
func (c *Client) PrintGraph(session, jobID string) {
	if err := c.Send(proto.Request{Session: session, Op: "graph", JobID: jobID}); err != nil {
		return
	}
	r, err := c.Recv()
	if err != nil || r.Event != "graph" {
		return
	}
	var g struct {
		Flows []struct {
			FID      int64  `json:"FID"`
			Kind     string `json:"Kind"`
			Fd       string `json:"Fd"`
			Owner    string `json:"Owner"`
			Producer string `json:"Producer"`
			Consumer string `json:"Consumer"`
			Path     string `json:"Path"`
			Bytes    int64  `json:"Bytes"`
			Lines    int64  `json:"Lines"`
		} `json:"flows"`
		Edges [][2]int64 `json:"edges"`
	}
	if json.Unmarshal([]byte(r.Data), &g) != nil {
		return
	}
	fmt.Fprintf(os.Stderr, "\n=== flow graph (%s) ===\n", jobID)
	fmt.Fprintf(os.Stderr, "%-4s %-9s %-9s %-22s %-22s %8s\n", "fid", "kind", "fd", "producer/owner", "consumer", "bytes")
	for _, f := range g.Flows {
		fd := f.Fd
		label := f.Producer
		if f.Kind == "fd" {
			fd = f.Fd
			label = f.Owner
		}
		fmt.Fprintf(os.Stderr, "%-4d %-9s %-9s %-22s %-22s %8d\n", f.FID, f.Kind, fd, trunc(label), trunc(f.Consumer), f.Bytes)
	}
	for _, e := range g.Edges {
		fmt.Fprintf(os.Stderr, "f%d --> f%d\n", e[0], e[1])
	}
}

// Jobs lists the session's jobs.
func (c *Client) Jobs(session string) error {
	if err := c.Send(proto.Request{Session: session, Op: "jobs"}); err != nil {
		return err
	}
	r, err := c.Recv()
	if err != nil {
		return err
	}
	fmt.Printf("%-14s %-9s %-4s %8s  %s\n", "job", "state", "exit", "lines", "command")
	for _, j := range r.Jobs {
		fmt.Printf("%-14s %-9s %-4d %8d  %s\n", j.ID, j.State, j.ExitCode, j.Lines, j.Command)
	}
	return nil
}

// Output prints a job's output.
func (c *Client) Output(session, jobID string, tail int) error {
	if err := c.Send(proto.Request{Session: session, Op: "output", JobID: jobID, Tail: tail}); err != nil {
		return err
	}
	r, err := c.Recv()
	if err != nil {
		return err
	}
	if r.Event == "error" {
		return fmt.Errorf("%s", r.Err)
	}
	os.Stdout.WriteString(r.Data)
	return nil
}

// History fetches the session's persisted history.
func (c *Client) History(session string) []string {
	if err := c.Send(proto.Request{Session: session, Op: "history"}); err != nil {
		return nil
	}
	r, err := c.Recv()
	if err != nil {
		return nil
	}
	return r.History
}

// AddHistory records a typed command in the session's history.
func (c *Client) AddHistory(session, command string) {
	if err := c.Send(proto.Request{Session: session, Op: "history-add", Command: command}); err != nil {
		return
	}
	_, _ = c.Recv()
}
