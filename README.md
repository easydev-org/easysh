# flowsh

(The language module is named `flowsh` internally; the shipped binaries are
`easysh` and `easyshd`.)

A **non-POSIX, flow-first, async-by-default shell**. Every stream is an identified
object (`Flow`) with a producer, consumers, its content, and a place in a directed
graph — so the **decomposition of any command is observable**: which command
produced which stream, how streams were piped, and what each intermediate result
contained.

## Architecture: a daemon, because the features require one

Two requirements force a long-lived daemon:

1. **fish-class interactive experience** — highlighting, completion and history
   need a persistent process that holds the session.
2. **Async by default** — a foreground job still running after a threshold
   (default **10s**, configurable) is **detached** to the background, and the
   client gets its prompt back immediately. The job keeps running; its output
   keeps being captured.

Both are the same shape — a resident process reading input — so they share one
kernel:

```
┌─ easyshd (resident) ──────────────────────────────────┐
│  Sessions: variables/functions/cwd persist per session │
│  Job scheduler: foreground > threshold → detach        │
│  flow graph + intermediate content, per job            │
│  IPC: unix socket (CLI) / TCP (worker)                 │
│  Store: SQLite (jobs / job_lines / flow / flow_edge /  │
│         flow_chunk) — same shape as the worker's jobs.db│
└───────────────────────────────────────────────────────┘
      ▲ unix socket              ▲ tcp
   easysh CLI                the worker
```

**Job management is NOT shell syntax.** The language has no `&`, `jobs`, `fg`,
`kill` or `wait`; those live in the daemon API and the client subcommands (and in
the worker's Jobs view). The shell language stays pure.

## Why a new shell

`abc-agent/worker` executes commands through the `mvdan/sh` interpreter (20k
lines, non-test). To record *dataflow* on top of it we tried everything short of
a rewrite:

* **`fstat` the pipe ends** — works on Linux/Windows (same inode), but **fails on
  macOS** (a pipe's two ends have *different* inodes). Verified on all three.
* **hook `mvdan`'s `ExecHandlers`/`OpenHandler`** — the pipe creation lives inside
  the interpreter, so the graph can only be inferred, not observed.

So flowsh **creates every stream itself**. `flow.Registry.NewPipe/NewFile/
NewCapture/Dup` is the only way to obtain a read/write target; the fid and the
graph edges exist the instant a stream does. **No syscalls, no per-OS code.**

## What it does NOT do (by design)

| Removed | Because |
|---------|---------|
| `&`, `jobs`, `fg`, `bg`, `wait`, `kill`, `coproc`, `$!` | **jobs are the worker's, not the shell's** |
| `eval`, `alias`, `source` | the AST is the only authority |
| `${x#p}` & friends, `$'...'`, backticks, `{a,b}` | parameter-modification grammar |
| `[[ ]]`, `select`, `getopts`, arrays, `IFS` | bash-only complexity |
| process substitution `<() >()` | needs FIFOs + background, conflicts with the job model |

## What it keeps

pipelines, redirects, quoting, `$var`/`${var}`, `$(...)`, arithmetic, globs,
`if/elif/else`, `for`, `while`, `case`, functions, `;` `&&` `||`, and a small
builtin set (`echo test read cd pwd export unset exit return break continue
true false set`). Blocks end with `end` (no `fi`/`done`/`esac`).

## Example

```
$ flowsh 'echo "a b c d e" | wc -w > count.txt'

=== flow graph (exit=0) ===
fid  kind    producer           consumer    bytes  lines
4    pipe    echo a b c d e     wc -w       10     1
5    file    wc -w                          2      1

=== edges ===
f1 --> f4
f4 --> f5

=== intermediate results ===
f4 (pipe): 10 bytes
    | a b c d e
```

## Layout

```
docs/GRAMMAR.md   language spec (cuts + EBNF)
docs/FLOW.md      the dataflow model & executor design
internal/syntax/  lexer + parser + AST
internal/flow/    Flow, Registry, TapBuffer (content capture)
internal/exec/    executor (builds the graph as a side effect)
cmd/flowsh/       demo CLI that prints the flow graph
cmd/astdump/      parser debug dumper
```

## Build & test

```
go build ./...
go test ./...

# start the daemon
./easyshd -socket /tmp/easysh.sock -detach 10s

# and drive it
EASYSH_SOCK=/tmp/easysh.sock ./easysh -c 'echo a b c | wc -w'   # one-shot + graph
EASYSH_SOCK=/tmp/easysh.sock ./easysh                            # interactive REPL
EASYSH_SOCK=/tmp/easysh.sock ./easysh jobs                       # job list
EASYSH_SOCK=/tmp/easysh.sock ./easysh output <jobID>             # a job's output
EASYSH_SOCK=/tmp/easysh.sock ./easysh graph <jobID>              # a job's graph
```

## Size

- **language + executor**: ~3.3k lines (vs `mvdan/sh`'s 20k non-test) — the difference is the POSIX
compatibility tax this shell deliberately does not pay.
- **daemon + client + store**: another ~1.5k.
