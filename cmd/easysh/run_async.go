package main

import (
	"fmt"
	"os"

	"github.com/easydev-org/easysh/pkg/proto"
)

// RunAsync starts a script and returns its job id immediately, streaming output
// in the background. It is how an interactive client drives a job: start, then
// feed stdin, then read output. (The daemon detaches it anyway if it outlives
// the threshold.)
func (c *Client) RunAsync(session, script string, detachMs int) (string, error) {
	if err := c.Send(proto.Request{Session: session, Op: "run", Script: script, DetachAfterMs: detachMs}); err != nil {
		return "", err
	}
	// Read events until "started" (the first reply is always "started"), and pump
	// the rest in a goroutine so the caller can send stdin and read output.
	for {
		r, err := c.Recv()
		if err != nil {
			return "", err
		}
		switch r.Event {
		case "started":
			go c.pumpOutput()
			return r.JobID, nil
		case "error":
			return "", fmt.Errorf("%s", r.Err)
		case "stdout", "stderr":
			os.Stdout.WriteString(r.Data)
		}
	}
}

// pumpOutput forwards a detached job's streamed output to stdout until "done".
func (c *Client) pumpOutput() {
	for {
		r, err := c.Recv()
		if err != nil {
			return
		}
		switch r.Event {
		case "stdout", "stderr":
			os.Stdout.WriteString(r.Data)
		case "done":
			return
		}
	}
}

// errStr renders a response error.
func errStr(r proto.Response, err error) string {
	if r.Err != "" {
		return r.Err
	}
	if err != nil {
		return err.Error()
	}
	return "unknown error"
}
