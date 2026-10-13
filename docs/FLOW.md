# flowsh — dataflow model & executor design

This is the whole point of flowsh. A normal shell treats streams as ephemeral
`io.Reader`/`io.Writer` values that vanish when the command ends. flowsh treats
**every stream as an identified, observable object** — a `Flow` — with:

* a **fid** (stable id),
* a **producer** (what wrote it) and **consumers** (what read it),
* its **content** (captured incrementally), and
* its position in a **directed graph** of flows.

Because **flowsh itself creates every pipe and every redirect**, the graph is
known at construction time. There is **no** `fstat`, no inode comparison, no
`ptrace`, no AST inference, and no per-OS syscall differences. (This is exactly
what we could not do when hooking `mvdan/sh`: there, `Pipe` creation lives
inside the interpreter and macOS gives pipe ends *different* inodes.)

---

## 1. The `Flow` object

```go
// FlowKind classifies where a stream's bytes come from / go.
type FlowKind uint8

const (
    FlowPipe   FlowKind = iota // a | b          (kernel pipe)
    FlowFile                   // > f  < f  >> f (a file)
    FlowHere                   // <<EOF  <<<      (a here-doc/string)
    FlowDup                    // 2>&1            (an alias of another flow)
    FlowNull                   // >&-  </dev/null (discard / null)
    FlowCapture                // $( ... )        (in-memory capture)
    FlowTerminal               // fd0/1/2 at the job boundary (in/out of flowsh)
)

// Flow is one identified stream.
type Flow struct {
    ID       FID        // dense, monotonic within a job
    Kind     FlowKind

    // Graph edges.
    Inputs   []FID      // flows this one reads from (0..n)
    Outputs  []FID      // flows this one feeds (back-filled as consumers attach)
    DupOf    FID        // when Kind == FlowDup, the flow this aliases

    // Who.
    Producer *CommandRef // the stage that writes this flow (nil for a pure input)
    Consumer *CommandRef // the stage that reads it (nil for an output/sink)

    // Where (for FlowFile / FlowHere).
    Path     string      // file path, or the here-doc tag

    // Content capture.
    Data     *TapBuffer  // bounded ring + spill (see §4); nil if not recorded

    // Bookkeeping.
    Started  time.Time
    Closed   time.Time
}

// CommandRef identifies a stage for the graph.
type CommandRef struct {
    Node   int      // index into the job's stage list (parse order)
    Argv   []string // the resolved argv (for real commands), else the builtin name
    Line   int      // source line (from the AST) for "jump to source"
}
```

### 1.1 Why an `FID` and not a pointer

An `FID` is a small integer, so:
* it is cheap to log, store, and put in SQLite;
* edges are integers, not object references (serializable);
* the graph survives the process (a job's flow table is persisted).

```go
type FID uint32   // 0 = "none"
```

---

## 2. The executor's fd slots

Every running stage has three fd slots. Instead of bare `io.Writer`s, a slot
holds a **`Stream`** — a writer/reader bound to a fid:

```go
// Stream is an fd target. It carries the fid so the executor always knows which
// flow a write belongs to.
type Stream struct {
    fid  FID
    w    io.Writer // when the stage writes here (stdout/stderr)
    r    io.Reader // when the stage reads here (stdin)
    kind End       // write-end / read-end / dup
}
```

The runner keeps, per stage:

```
stdin  Stream
stdout Stream
stderr Stream
```

**The rule**: a stream is *always* created by `flowRegistry.New*`. There is no
other way to obtain a write/read target. That single chokepoint is what makes
the graph complete.

```go
type flowRegistry struct {
    job   JobID
    next  FID
    flows map[FID]*Flow
    sink  FlowSink     // persistence + live fanout (see WORKER.md)
}

func (fr *flowRegistry) NewPipe(l, r *CommandRef) (writeEnd Stream, readEnd Stream)
func (fr *flowRegistry) NewFile(path string, flags int, prod *CommandRef) Stream
func (fr *flowRegistry) NewHere(body []byte, prod *CommandRef) Stream
func (fr *flowRegistry) NewNull(write bool) Stream
func (fr *flowRegistry) NewCapture(prod *CommandRef) (Stream, *CaptureBuf)
func (fr *flowRegistry) Dup(dst Slot, src Stream, cmd *CommandRef) Stream
func (fr *flowRegistry) Terminal(slot Slot) Stream
```

---

## 3. Graph construction — where each edge comes from

The executor walks the AST. At each **construction point**, the registry is
called, and the fid + edges are created **atomically with the stream**:

### 3.1 Pipeline `a | b | c`

```
pipeline stages = [a, b, c]

for i in 0..len-2:
    wEnd, rEnd = registry.NewPipe(producer=a_i, consumer=a_{i+1})
    a_i.stdout  = wEnd        // a_i writes flow f
    a_{i+1}.stdin = rEnd      // a_{i+1} reads flow f

// edges:   a.stdout --f--> b.stdin
//          b.stdout --f'--> c.stdin
```

One `Flow{Pipe}` per `|`. `Inputs`/`Outputs` are filled because **both ends are
created together** — `a`'s write end and `b`'s read end are the same fid.

`|&` is the same, except `b`'s stderr is also bound to the read end (a second
`Outputs` entry on the pipe flow).

### 3.2 Redirects

Processed **left to right** (§4 of GRAMMAR), each mutating a slot:

| Redirect | Registry call | Result |
|----------|---------------|--------|
| `> f` | `NewFile(f, O_TRUNC, cmd)` | `cmd.stdout` = File flow |
| `>> f` | `NewFile(f, O_APPEND, cmd)` | `cmd.stdout` = File flow |
| `2> f` | `NewFile(f, ..., cmd)` | `cmd.stderr` = File flow |
| `< f` | `NewFile(f, O_RDONLY, cmd)` | `cmd.stdin` = File flow (read) |
| `&> f` | `NewFile(f, ...)` **once** | both slots = the **same** File flow |
| `2>&1` | `Dup(2, currentStdout)` | `cmd.stderr` = `Flow{Dup, DupOf: stdout.fid}` |
| `>&-` | `NewNull(false)` | slot closed |

Because order matters, the executor applies redirects sequentially against the
*current* slot values — so `2>&1 > f` correctly dups the old stdout first.

### 3.3 Command substitution `$( ... )`

```
f, buf = registry.NewCapture(prod=outer)
run inner script with stdout = f
when inner finishes: replace the word with buf.String()
```

The capture is a **flow too** — so `$(...)` appears in the graph as a node whose
content is the captured text.

### 3.4 Here-doc / here-string

`<<EOF` and `<<<` create a `Flow{Here}` whose `Data` is pre-filled with the body;
the stage's stdin is its read end.

### 3.5 Job boundary (stdin/stdout/stderr of the whole job)

The job's own three fds are flows too (`Kind=FlowTerminal`), created once at job
start from the worker-supplied pipes. So the graph is **closed**: every stage
input traces back to either a terminal fd, a file, a here-doc, or a capture.

---

## 4. Content capture — `TapBuffer`

Every `Flow` that should be recorded gets a `TapBuffer`. It is the same design
proven in the Go worker's `LineBuffer`, generalized per-flow:

```go
// TapBuffer is an io.Writer that keeps a bounded line-ring in memory and spools
// the full byte stream to the sink. It never blocks the pipeline.
type TapBuffer struct {
    fid    FID
    ring   *lineRing     // last N lines, for live view / tail
    sink   FlowSink      // persistent + streaming (bounded, non-blocking)
}

func (t *TapBuffer) Write(p []byte) (int, error) {
    t.ring.Write(p)          // O(1)-amortized, drops oldest
    t.sink.Enqueue(t.fid, p) // non-blocking; drops + counts on backpressure
    return len(p), nil
}
```

**Where a tap is inserted** — this is the elegant part:

> A flow has a **write end** and **read end**. The tap wraps the **write end**:
>
> ```
> producer ──► TapBuffer(fid) ──► real pipe/file ──► consumer
>                    └──► ring + sink
> ```
>
> The producer still talks to a normal `io.Writer`; the tap tees to the recorder.
> **No `tee(2)`, no extra process, no fd games** — it is an `io.MultiWriter`.

For **file** flows we don't need a tap at all: the file *is* the content; we
record `Path` + (optionally) a post-hoc diff/hash.

For **capture** flows, the buffer *is* the content.

### 4.1 Backpressure policy

Matches the worker's existing design:
* sink channel is **bounded** (e.g. 64k records); when full → **drop + count** (never block the pipeline);
* ring is bounded by lines (e.g. 10k) + byte cap;
* full stream optionally **spills to a temp file** if it exceeds the byte cap.

---

## 5. Worked example — the graph the executor builds

```
curl -s "$url" | grep -i error | wc -l > errcount.txt
```

AST: `Pipeline[ Simple(curl,-s,$url), Simple(grep,-i,error), Simple(wc,-l) ]`
with a redirect on the last stage.

Execution trace:

```
1. NewTerminal(0) -> t0
2. NewTerminal(1) -> t1
3. NewTerminal(2) -> t2

4. NewPipe(curl, grep):
      flow f1 { Kind=Pipe, Producer=curl, Consumer=grep }
      curl.stdout = f1.write ; grep.stdin = f1.read

5. NewPipe(grep, wc):
      flow f2 { Kind=Pipe, Producer=grep, Consumer=wc }
      grep.stdout = f2.write ; wc.stdin = f2.read

6. redirect "> errcount.txt" on wc:
      flow f3 { Kind=File, Path="errcount.txt", Producer=wc }
      wc.stdout = f3.write

7. curl.stdin = t0.read ; wc.stderr = t2.write   (defaults)

Result graph:
   t0 --> curl --> f1 --> grep --> f2 --> wc --> f3(errcount.txt)
                                                 wc.stderr --> t2
```

The **observable decomposition**:

```
fid  kind    producer          consumer   bytes   lines
f1   pipe    curl -s $url      grep       18342   412
f2   pipe    grep -i error     wc         902     37
f3   file    wc -l             (file)     12      1
```

And you can fetch **the content of `f1`** (curl's raw output) *and* `f2`
(grep's output) — every intermediate result, plus the relationship.

---

## 6. Why this cannot be done by hooking mvdan (recap)

| Requirement | Hooking mvdan | flowsh |
|-------------|---------------|--------|
| know pipe ends are the same flow | `fstat` — **fails on macOS** (different inodes); needs `pipe_peerhandle` per-OS | **trivial**: we call `NewPipe` |
| see `$()` / dynamic redirs | hard (AST ≠ runtime) | **trivial**: runtime creates the flow |
| intermediate content | must re-tee every fd | **built-in** at the write end |
| cross-OS | per-OS syscalls | **none** — one code path |
| nested shells (`bash -c 'a\|b'`) | invisible | **out of scope by design** (we do not spawn a foreign shell) |

The last row matters: flowsh **never delegates to an external shell**, so the
whole execution is ours and the graph is complete *within the job*.

---

## 7. Storage schema (worker side)

```sql
CREATE TABLE flow (
    job_id     TEXT NOT NULL,
    fid        INTEGER NOT NULL,
    kind       TEXT NOT NULL,      -- pipe|file|here|dup|capture|terminal
    producer   TEXT,               -- argv joined, or builtin name
    consumer   TEXT,
    path       TEXT,               -- file path / here-doc tag
    dup_of     INTEGER,            -- fid
    line       INTEGER,            -- source line
    bytes      INTEGER NOT NULL DEFAULT 0,
    lines      INTEGER NOT NULL DEFAULT 0,
    sha256     TEXT,
    started_at INTEGER NOT NULL,
    closed_at  INTEGER,
    PRIMARY KEY (job_id, fid)
);

CREATE TABLE flow_edge (
    job_id   TEXT NOT NULL,
    from_fid INTEGER NOT NULL,     -- upstream
    to_fid   INTEGER NOT NULL,     -- downstream
    PRIMARY KEY (job_id, from_fid, to_fid)
);

CREATE TABLE flow_chunk (          -- the intermediate results
    job_id  TEXT NOT NULL,
    fid     INTEGER NOT NULL,
    seq     INTEGER NOT NULL,
    data    BLOB NOT NULL,
    PRIMARY KEY (job_id, fid, seq)
);
```

`flow_edge` is derivable from `flow.inputs`, but storing it explicitly makes
"what fed what" a single indexed query.

---

## 8. What the executor tracks per stage (the runtime view)

```go
type stageRun struct {
    ref    CommandRef
    stdin  Stream
    stdout Stream
    stderr Stream
    pid    int          // for external commands
    status int
}
```

When the executor launches a stage it:
1. resolves argv,
2. opens redirect targets through the registry,
3. wires pipeline ends through the registry,
4. starts the process with `cmd.Stdin/Stdout/Stderr` = the streams' real ends,
5. on exit, closes write-end taps, finalizes `bytes/lines/sha256`, marks `Closed`.

The graph is thus **built as a side effect of normal execution**, not as a
second pass. That is the design goal: *observability with zero extra planning*.
