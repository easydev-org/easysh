// Command easysh is the client: it talks to a running easysh daemon. Subcommands
// mirror the daemon's job API (which is deliberately NOT shell syntax):
//
//	easysh                 interactive REPL
//	easysh -c 'a | b'      run one script
//	easysh jobs            list jobs
//	easysh output <id>     show a job's output
//	easysh graph <id>      show a job's dataflow graph
//	easysh wait <id>       wait for a job
//	easysh kill <id>       kill a job
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/abcp-sdk/easysh/internal/proto"
)

func main() {
	fs := flag.NewFlagSet("easysh", flag.ContinueOnError)
	sock := fs.String("socket", defaultSock(), "daemon unix socket path")
	tcp := fs.String("tcp", "", "connect to daemon over TCP instead (host:port)")
	session := fs.String("session", "default", "session name")
	detachMs := fs.Int("detach-ms", 0, "detach a foreground job after this many ms (0 = daemon default)")
	script := fs.String("c", "", "run one script and exit")
	graph := fs.Bool("graph", true, "print the flow graph after a one-shot script")
	async := fs.Bool("async", false, "start the script and return its job id immediately (do not wait)")
	_ = fs.Parse(os.Args[1:])

	network, addr := "unix", *sock
	if *tcp != "" {
		network, addr = "tcp", *tcp
	}
	c, err := Dial(network, addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "easysh: cannot reach daemon at %s: %v\n", addr, err)
		fmt.Fprintln(os.Stderr, "start it with `easyshd` (see easyshd -h)")
		os.Exit(2)
	}
	defer c.Close()

	args := fs.Args()
	switch {
	case *script != "":
		if *async {
			id, err := c.RunAsync(*session, *script, *detachMs)
			if err != nil {
				die(err.Error())
			}
			fmt.Println(id)
			os.Exit(0)
		}
		code, _ := c.RunOne(*session, *script, *detachMs, *graph)
		if code < 0 {
			code = 0
		}
		os.Exit(code)
	case len(args) > 0:
		sub := args[0]
		rest := args[1:]
		switch sub {
		case "jobs", "list":
			must(c.Jobs(*session))
		case "output", "out":
			if len(rest) == 0 {
				die("usage: easysh output <jobID> [tail]")
			}
			tail := 0
			if len(rest) > 1 {
				fmt.Sscanf(rest[1], "%d", &tail)
			}
			must(c.Output(*session, rest[0], tail))
		case "graph":
			if len(rest) == 0 {
				die("usage: easysh graph <jobID>")
			}
			c.PrintGraph(*session, rest[0])
		case "kill":
			if len(rest) == 0 {
				die("usage: easysh kill <jobID>")
			}
			must(c.Send(proto.Request{Session: *session, Op: "kill", JobID: rest[0]}))
		case "wait":
			if len(rest) == 0 {
				die("usage: easysh wait <jobID>")
			}
			must(c.Send(proto.Request{Session: *session, Op: "wait", JobID: rest[0]}))
			r, _ := c.Recv()
			fmt.Printf("exit %d\n", r.Code)
		case "stdin":
			if len(rest) == 0 {
				die("usage: easysh stdin <jobID> [data] [--close]")
			}
			jobID := rest[0]
			var data string
			doClose := false
			for _, a := range rest[1:] {
				if a == "--close" || a == "-c" {
					doClose = true
					continue
				}
				data += a
			}
			// If no data was given, read it from our stdin.
			if data == "" && !doClose {
				if b, err := io.ReadAll(os.Stdin); err == nil {
					data = string(b)
				}
			}
			must(c.Send(proto.Request{Session: *session, Op: "stdin", JobID: jobID, Data: data, Close: doClose}))
			r, err := c.Recv()
			if err != nil || r.Event == "error" {
				die("stdin: " + errStr(r, err))
			}
		case "help", "-h", "--help":
			usage()
		default:
			// treat the whole argv as a script
			code, _ := c.RunOne(*session, strings.Join(args, " "), *detachMs, *graph)
			os.Exit(code)
		}
	default:
		repl(c, *session, *detachMs)
	}
}

func must(err error) {
	if err != nil {
		die(err.Error())
	}
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "easysh:", msg)
	os.Exit(1)
}

func usage() {
	fmt.Print(`easysh — flow-first shell client
  easysh                    interactive REPL
  easysh -c 'a | b'         run one script
  easysh jobs               list jobs
  easysh output <id> [tail] show a job's output
  easysh graph <id>         show a job's dataflow graph
  easysh wait <id>          wait for a job
  easysh kill <id>          kill a job
flags: -socket, -tcp, -session, -detach-ms, -c, -graph
`)
}

func defaultSock() string {
	if p := os.Getenv("EASYSH_SOCK"); p != "" {
		return p
	}
	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
		return x + "/easysh.sock"
	}
	return "/tmp/easysh.sock"
}
