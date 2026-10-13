// Package exec runs a flowsh AST and builds the flow graph as a side effect.
package exec

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/abcp-sdk/easysh/internal/flow"
	"github.com/abcp-sdk/easysh/internal/syntax"
)

// Options configures a run.
type Options struct {
	Dir       string   // working directory
	Env       []string // environment (KEY=VALUE)
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	Capture   bool      // capture intermediate flow content
	Sink      flow.Sink // where captured bytes go
	RingLines int
	// OnRegistry, when set, is called at the start of each run with that run's
	// flow registry, so a caller can attach OnAlloc/OnWrite to build the graph
	// and stream flows live (used by the daemon for the WebUI).
	OnRegistry func(*flow.Registry)
}

// SetRegistryHook sets (or clears, with nil) the registry hook applied to each
// run. It is how the daemon observes flows as they are created.
func (r *Runner) SetRegistryHook(fn func(*flow.Registry)) {
	r.opts.OnRegistry = fn
}

// Result is the outcome of a run plus the flow graph it produced.
type Result struct {
	ExitCode int
	Registry *flow.Registry
}

// Runner executes scripts and tracks all flows.
type Runner struct {
	opts       Options
	reg        *flow.Registry
	vars       map[string]string
	mut        map[string]bool
	args       []string // positional parameters ($1.., $@, $#)
	lastExit   int
	nodeSeq    int
	iterStack  []int  // active loop iteration depths (1-based); non-empty inside a loop
	loopCtl    string // "break" | "continue" sentinel from builtins
	returning  bool   // set by the `return` builtin
	retCode    int
	exiting    bool      // set by the `exit` builtin: unwind all statement lists
	curDir     string    // cwd, persisted across Run calls so `cd` sticks
	activeSink flow.Sink // the sink for the current run (per-job)

	funcs map[string]*syntax.FuncDef
}

// New creates a runner.
func New(opts Options) *Runner {
	if opts.RingLines <= 0 {
		opts.RingLines = 10000
	}
	if opts.Sink == nil {
		opts.Sink = flow.Discard{}
	}
	return &Runner{
		opts:   opts,
		reg:    flow.NewRegistry(),
		curDir: opts.Dir,
		vars:   map[string]string{},
		mut:    map[string]bool{},
		funcs:  map[string]*syntax.FuncDef{},
	}
}

// Run parses and executes src, returning the exit code and the graph.
func (r *Runner) Run(ctx context.Context, src string) (*Result, error) {
	return r.RunWithSink(ctx, src, r.opts.Sink)
}

// RunWithSink is Run with an explicit sink, so a caller (the daemon) can observe
// every flow's bytes — including the job's own stdout/stderr — as they are
// produced. Passing nil uses the sink from Options.
func (r *Runner) RunWithSink(ctx context.Context, src string, sink flow.Sink) (*Result, error) {
	return r.RunWithIO(ctx, src, sink, r.opts.Stdin)
}

// RunWithIO is RunWithSink with an explicit stdin, so a caller (the daemon) can
// feed a job's stdin at run time (interactive commands: cat, read, a REPL).
func (r *Runner) RunWithIO(ctx context.Context, src string, sink flow.Sink, stdin io.Reader) (*Result, error) {
	// Each run gets a fresh flow registry: a job's graph must contain only that
	// job's flows, not every flow the session has ever produced.
	r.reg = flow.NewRegistry()
	if r.opts.OnRegistry != nil {
		r.opts.OnRegistry(r.reg)
	}
	sc, err := syntax.Parse(src)
	if err != nil {
		return nil, err
	}
	if sink == nil {
		sink = r.opts.Sink
	}
	r.activeSink = sink
	// Terminal flows for the job boundary. The stdout/stderr writers are wrapped
	// in a tap so the daemon sees the job's own output in the same pipeline as
	// every intermediate flow.
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	tIn := r.reg.Terminal(flow.Stdin, nil, stdin)
	tOut := r.reg.TerminalTap(flow.Stdout, r.opts.Stdout, nil, sink, r.opts.RingLines)
	tErr := r.reg.TerminalTap(flow.Stderr, r.opts.Stderr, nil, sink, r.opts.RingLines)
	if sa, ok := sink.(flow.StdoutAware); ok {
		sa.SetStdoutFID(tOut.FID)
	}

	// First pass: collect function definitions.
	collectFuncs(sc.Stmts, r.funcs)

	st := &state{
		dir:    r.curDir,
		env:    append([]string{}, r.opts.Env...),
		stdin:  tIn,
		stdout: tOut,
		stderr: tErr,
	}
	code := r.runStmts(ctx, sc.Stmts, st)
	r.curDir = st.dir
	r.reg.Finalize()
	return &Result{ExitCode: code, Registry: r.reg}, nil
}

// state is the per-scope execution state (fd slots + cwd + env + vars).
type state struct {
	dir    string
	env    []string
	stdin  flow.Stream
	stdout flow.Stream
	stderr flow.Stream
}

func collectFuncs(stmts []*syntax.Stmt, out map[string]*syntax.FuncDef) {
	for _, st := range stmts {
		for _, pl := range st.Chain {
			for _, sg := range pl.Stages {
				if fd, ok := sg.Cmd.(*syntax.FuncDef); ok {
					out[fd.Name] = fd
				}
			}
		}
	}
}

// runStmts executes a statement list, returning the last exit code.
func (r *Runner) runStmts(ctx context.Context, stmts []*syntax.Stmt, st *state) int {
	code := 0
	for _, s := range stmts {
		if r.exiting {
			return r.retCode
		}
		if ctx.Err() != nil {
			return 130
		}
		code = r.runStmt(ctx, s, st)
	}
	return code
}

// runStmt executes one statement (a &&/|| chain of pipelines).
func (r *Runner) runStmt(ctx context.Context, s *syntax.Stmt, st *state) int {
	code := r.runPipeline(ctx, s.Chain[0], st)
	for i, op := range s.Ops {
		switch op {
		case "&&":
			if code == 0 {
				code = r.runPipeline(ctx, s.Chain[i+1], st)
			}
		case "||":
			if code != 0 {
				code = r.runPipeline(ctx, s.Chain[i+1], st)
			}
		}
	}
	r.lastExit = code
	return code
}

// runPipeline wires all stages of a pipeline together via the registry, then
// runs them. This is where pipes become flow edges.
func (r *Runner) runPipeline(ctx context.Context, pl *syntax.Pipeline, st *state) int {
	n := len(pl.Stages)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return r.runStage(ctx, pl.Stages[0], st)
	}

	// Create one Pipe flow per adjacent pair.
	type wiring struct {
		in  flow.Stream
		out flow.Stream
	}
	links := make([]wiring, n)
	for i := 0; i < n-1; i++ {
		lref := cmdRef(r, pl.Stages[i])
		rref := cmdRef(r, pl.Stages[i+1])
		w, rd := r.reg.NewPipe(lref, rref, r.opts.Capture, r.activeSink, r.opts.RingLines)
		links[i].out = w
		links[i+1].in = rd
	}
	links[0].in = st.stdin
	links[n-1].out = st.stdout

	// Run stages concurrently; pipe ends carry backpressure.
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sub := *st
			sub.stdin = links[i].in
			sub.stdout = links[i].out
			sub.stderr = st.stderr
			codes[i] = r.runStage(ctx, pl.Stages[i], &sub)
			// Close our write end so the next stage sees EOF. The writer may be
			// an io.MultiWriter (pipe + tap), which is not itself a Closer, so
			// we look through it to the pipe writer.
			closeWrite(links[i].out)
		}(i)
	}
	wg.Wait()
	return codes[n-1]
}

// closeWrite closes the pipe end behind a stream's writer, if any.
func closeWrite(s flow.Stream) {
	if s.W == nil {
		return
	}
	if cw, ok := s.W.(io.Closer); ok {
		cw.Close()
		return
	}
	// io.MultiWriter stored in an unexported field; we instead keep the raw
	// pipe writer on the Stream for this purpose.
	if s.Closer != nil {
		s.Closer.Close()
	}
}

// cmdRef builds a CommandRef from a stage, carrying its source span.
func cmdRef(r *Runner, sg *syntax.Stage) *flow.CommandRef {
	p := sg.Pos()
	ref := &flow.CommandRef{Node: r.nextNode(), Argv: argvOf(sg),
		Line: p.Line, Col: p.Col, EndLine: p.EndLine, EndCol: p.EndCol}
	if len(r.iterStack) > 0 {
		ref.Iter = append([]int(nil), r.iterStack...)
	}
	return ref
}

func argvOf(sg *syntax.Stage) []string {
	if sm, ok := sg.Cmd.(*syntax.Simple); ok {
		out := make([]string, 0, len(sm.Args))
		for _, a := range sm.Args {
			out = append(out, literal(a))
		}
		return out
	}
	return nil
}

// literal renders a word's static text (for labeling; expansion happens at run).
func literal(w *syntax.Word) string {
	var b strings.Builder
	for _, p := range w.Parts {
		switch v := p.(type) {
		case *syntax.Lit:
			b.WriteString(v.Value)
		case *syntax.Param:
			b.WriteString("$" + v.Name)
		case *syntax.CmdSub:
			b.WriteString("$(...)")
		case *syntax.Arith:
			b.WriteString("$((" + v.Raw + "))")
		}
	}
	// Labels are rendered on one line, so collapse whitespace runs (newlines,
	// tabs) to single spaces. This is display-only; the executed argv keeps its
	// exact bytes.
	return strings.Join(strings.Fields(b.String()), " ")
}

func (r *Runner) nextNode() int { r.nodeSeq++; return r.nodeSeq }

// runStage executes one stage: bindings, redirects, then the command.
func (r *Runner) runStage(ctx context.Context, sg *syntax.Stage, st *state) int {
	// Every stage has three fd endpoints, so its own stdin/stdout/stderr are
	// distinguishable objects in the graph — even when several stages share the
	// job's terminal (e.g. two commands both writing to stderr). The endpoints
	// carry no bytes; they name where each fd points and are wired to the
	// data-carrying flows by edges.
	owner := cmdRef(r, sg)
	fdIn := r.reg.NewFd(flow.Stdin, owner)
	fdOut := r.reg.NewFd(flow.Stdout, owner)
	fdErr := r.reg.NewFd(flow.Stderr, owner)

	// Bindings.
	for _, b := range sg.Bindings {
		val := r.expandOne(b.Value, st)
		if b.Mode == "set" {
			// A bare assignment mutates an existing `mut` variable.
			if _, ok := r.vars[b.Name]; !ok {
				fmt.Fprintf(writerOf(st.stderr, r), "%s: not declared (use `let`/`mut`)\n", b.Name)
				return 1
			}
			r.vars[b.Name] = val
			continue
		}
		r.vars[b.Name] = val
		if b.Mode == "mut" {
			r.mut[b.Name] = true
		}
		if b.Mode == "export" {
			st.env = append(st.env, b.Name+"="+val)
		}
	}

	// Redirects (in order; each mutates a slot).
	for _, rd := range sg.Redirs {
		if code := r.applyRedirect(rd, sg, st); code != 0 {
			return code
		}
	}

	// Wire the fd endpoints AFTER redirects, so they point where the fd really
	// ends up (a file, a pipe end, the terminal).
	if up := r.reg.Get(st.stdin.FID); up != nil {
		r.reg.Link(up, fdIn)
	}
	if d := r.reg.Get(st.stdout.FID); d != nil {
		r.reg.Link(fdOut, d)
	}
	if d := r.reg.Get(st.stderr.FID); d != nil {
		r.reg.Link(fdErr, d)
	}

	if sg.Cmd == nil {
		return 0
	}
	code := 0
	switch c := sg.Cmd.(type) {
	case *syntax.Simple:
		code = r.runSimple(ctx, c, st)
	case *syntax.IfCmd:
		code = r.runIf(ctx, c, st)
	case *syntax.ForCmd:
		code = r.runFor(ctx, c, st)
	case *syntax.WhileCmd:
		code = r.runWhile(ctx, c, st)
	case *syntax.CaseCmd:
		code = r.runCase(ctx, c, st)
	case *syntax.Block:
		code = r.runStmts(ctx, c.Body, st)
	case *syntax.FuncDef:
		r.funcs[c.Name] = c
		code = 0
	}
	return code
}

// applyRedirect mutates the appropriate fd slot through the registry.
func (r *Runner) applyRedirect(rd *syntax.Redirect, sg *syntax.Stage, st *state) int {
	ref := &flow.CommandRef{Node: r.nextNode(), Argv: argvOf(sg), Line: rd.Pos().Line}
	target := r.expandOne(rd.Target, st)

	switch rd.Op {
	case ">", ">>":
		flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		if rd.Op == ">>" {
			flags = os.O_WRONLY | os.O_CREATE | os.O_APPEND
		}
		s, err := r.reg.NewFile(target, flags, 0o644, ref, true)
		if err != nil {
			fmt.Fprintln(stderrOf(r, st), err)
			return 1
		}
		st.stdout = s
	case "2>", "2>>":
		flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		if rd.Op == "2>>" {
			flags = os.O_WRONLY | os.O_CREATE | os.O_APPEND
		}
		s, err := r.reg.NewFile(target, flags, 0o644, ref, true)
		if err != nil {
			fmt.Fprintln(stderrOf(r, st), err)
			return 1
		}
		st.stderr = s
	case "<":
		s, err := r.reg.NewFile(target, os.O_RDONLY, 0, ref, false)
		if err != nil {
			fmt.Fprintln(stderrOf(r, st), err)
			return 1
		}
		st.stdin = s
	case "&>", "&>>":
		flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		if rd.Op == "&>>" {
			flags = os.O_WRONLY | os.O_CREATE | os.O_APPEND
		}
		s, err := r.reg.NewFile(target, flags, 0o644, ref, true)
		if err != nil {
			fmt.Fprintln(stderrOf(r, st), err)
			return 1
		}
		st.stdout = s
		st.stderr = s
	case ">&":
		src := st.stdout
		if target == "1" {
			src = st.stdout
		} else if target == "2" {
			src = st.stderr
		}
		st.stderr = r.reg.Dup(src, ref)
	case "<&":
		st.stdin = r.reg.Dup(st.stdin, ref)
	case ">&-", "<&-":
		null := r.reg.NewNull(rd.Op == ">&-")
		if rd.Op == ">&-" {
			st.stdout = null
		} else {
			st.stdin = null
		}
	case "<<", "<<<":
		body := []byte(target)
		if rd.Op == "<<<" {
			body = []byte(target + "\n")
		}
		st.stdin = r.reg.NewHere(rd.Op, body, ref, r.activeSink, r.opts.RingLines)
	}
	return 0
}

// runSimple executes a simple command (builtin, function, or external).
func (r *Runner) runSimple(ctx context.Context, s *syntax.Simple, st *state) int {
	var argv []string
	for _, w := range s.Args {
		argv = append(argv, r.expandFields(w, st)...)
	}
	if len(argv) == 0 {
		return 0
	}
	name := argv[0]

	// builtins
	if code, ok := r.builtin(ctx, name, argv[1:], st); ok {
		return code
	}
	// functions
	if fn, ok := r.funcs[name]; ok {
		return r.callFunc(ctx, fn, argv[1:], st)
	}
	// external
	return r.runExternal(ctx, argv, st)
}

// runExternal runs a program with the stage's streams wired to fds.
func (r *Runner) runExternal(ctx context.Context, argv []string, st *state) int {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = st.dir
	cmd.Env = st.env
	cmd.Stdin = readerOf(st.stdin, r)
	cmd.Stdout = writerOf(st.stdout, r)
	cmd.Stderr = writerOf(st.stderr, r)
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		fmt.Fprintf(writerOf(st.stderr, r), "%s: %v\n", argv[0], err)
		return 127
	}
	return 0
}

// callFunc runs a function body with positional params set.
func (r *Runner) callFunc(ctx context.Context, fn *syntax.FuncDef, args []string, st *state) int {
	saved := map[string]string{}
	for i, p := range fn.Params {
		saved[p] = r.vars[p]
		if i < len(args) {
			r.vars[p] = args[i]
		} else {
			r.vars[p] = ""
		}
	}
	savedArgs := r.args
	r.args = args
	code := r.runStmts(ctx, fn.Body, st)
	for k, v := range saved {
		r.vars[k] = v
	}
	r.args = savedArgs
	return code
}

func readerOf(s flow.Stream, r *Runner) io.Reader {
	if s.R != nil {
		return s.R
	}
	return strings.NewReader("")
}

func writerOf(s flow.Stream, r *Runner) io.Writer {
	if s.W != nil {
		return s.W
	}
	return io.Discard
}

func stderrOf(r *Runner, st *state) io.Writer { return writerOf(st.stderr, r) }
