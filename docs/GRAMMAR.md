# flowsh — language specification (v0)

A **non-POSIX**, flow-first shell. Its reason to exist is the dataflow graph:
every stream has an identity (`fid`), every pipe/redirect is a first-class edge,
and the whole decomposition of a command is observable (see `FLOW.md`).

It is **not** bash-compatible. That is deliberate: dropping compatibility
deletes ~13k lines of the 20k that `mvdan/sh` carries, and deletes whole
classes of grammar ambiguity.

---

## 1. Design decisions (and what they delete)

| Decision | Replaces | Deletes |
|----------|----------|---------|
| **Jobs live *outside* the shell** | `&`, job control, `coproc` | `jobs` `fg` `bg` `disown` `wait` `kill` `$!` `set -m`, SIGCHLD traps, tty process groups |
| **`end`-terminated blocks** | `fi` `done` `esac` | keyword-pair matching; parser is simpler |
| **Explicit `let` / `mut` bindings** | `VAR=x cmd`, bare assignment | the `name=value word word` ambiguity; `VAR=x cmd` prefix env |
| **No `eval`, no `alias`** | runtime re-parse | self-modifying parse; makes the AST the sole authority |
| **Static word model** | `${x#p}` `${x:o:l}` `${x//a/b}` `$'...'` backticks `{a,b}` | the entire parameter-modification grammar (~2k lines) |
| **Structured `if/for/case` only** | `[[ ]]`, `select`, `getopts` | bash test grammar, arg-parsing builtins |
| **Text streams, not structured values (v0)** | — | (structured records are a *later* layer; v0 stays byte-stream so `grep`/`wc` work) |

**Kept** (the irreducible core): pipelines, redirects, quoting, `$var`,
`$(...)`, arithmetic, globs, `if/for/while/case`, functions, `;` `&&` `||`, and
a small builtin set.

---

## 2. Lexical structure

### 2.1 Whitespace & comments

```
WS      := (' ' | '\t')+
NEWLINE := '\n'
COMMENT := '#' { any-char-not-newline }
```
A `#` starts a comment only at a **word boundary** (start of line / after
whitespace or an operator), so `a#b` is one word.

### 2.2 Line continuation

A `\` immediately before a newline joins the lines. Inside `( ) { }` and after a
trailing operator (`| && || ;`), newlines are **implicitly** continuations.

### 2.3 Words

A word is a maximal run of word characters and quoted/expanded segments:

```
word        := word-part+
word-part   := bare-char+
             | single-quoted
             | double-quoted
             | var-ref
             | cmd-subst
             | arith-subst
             | tilde
bare-char   := any char except  whitespace  newline  #  '  "  $  `  \
                 and except the operators  ; | & < > ( ) { }
single-quoted := "'" { any-char-except-quote } "'"
double-quoted := '"' { dq-part } '"'
dq-part     := any-char-except-[" $ \]
             | '\' any
             | var-ref | cmd-subst | arith-subst
tilde       := '~' [ '/' ]                      -- only as the whole first segment
```

| Escapes (outside quotes) | Meaning |
|---|---|
| `\n` `\t` `\r` `\\` `\'` `\"` `\$` | the obvious char |
| `\<other>` | literal `<other>` |

### 2.4 Identifiers, numbers, operators

```
IDENT   := [A-Za-z_][A-Za-z0-9_]*
INT     := [0-9]+
```

```
PIPE      := "|"
PIPE_ERR  := "|&"
AND       := "&&"
OR        := "||"
SEMI      := ";"
LPAREN RPAREN := "(" ")"
LBRACE RBRACE := "{" "}"
ASSIGN    := "="
```

Redirect operators (see section 4):
```
GT  := ">"     APPEND := ">>"
LT  := "<"
ERR := "2>"    ERR_APPEND := "2>>"
BOTH := "&>"   BOTH_APPEND := "&>>"
DUP_OUT := ">&"   DUP_IN := "<&"
```

| Keywords | |
|---|---|
| `if` `then` `elif` `else` `end` | conditionals |
| `for` `in` `do` | loops |
| `while` `case` | loops / choice |
| `fn` `return` | functions |
| `let` `mut` `export` `unset` | bindings |
| `break` `continue` | loop control |
| `true` `false` | literals (also builtins) |

Keywords are recognised only in **command position** (start of a pipeline
stage), so a file named `if` is still usable as `./if`.

---

## 3. Grammar (EBNF)

```
script      := stmt-list EOF
stmt-list   := { stmt sep }
sep         := ";" | NEWLINE

stmt        := logical
logical     := pipeline { ( "&&" | "||" ) pipeline }

pipeline    := stage { ( "|" | "|&" ) stage }

stage       := { binding } ( compound | simple )

compound    := if-cmd | for-cmd | while-cmd | case-cmd | block | func-def

if-cmd      := "if" stmt-list "then" stmt-list
               { "elif" stmt-list "then" stmt-list }
               [ "else" stmt-list ]
               "end"
for-cmd     := "for" IDENT "in" word-list "do" stmt-list "end"
while-cmd   := "while" stmt-list "do" stmt-list "end"
case-cmd    := "case" word "in" case-arm { case-arm } "end"
case-arm    := pattern { "|" pattern } "=>" stmt-list
block       := "(" stmt-list ")"          -- subshell: fresh var scope
             | "{" stmt-list "}"          -- current shell
func-def    := "fn" IDENT "(" [ param { "," param } ] ")" "{" stmt-list "}"
param       := IDENT

word-list   := { word }

simple      := target redirect*
target      := word | word word*          -- command + args
redirect    := ( GT | APPEND | LT
               | ERR | ERR_APPEND
               | BOTH | BOTH_APPEND
               | DUP_OUT | DUP_IN ) word

binding     := ( "let" | "mut" ) IDENT "=" word
             | "export" IDENT "=" word

pattern     := word { "*" | "?" | "[" ... "]" }   -- glob, unanchored
```

### 3.1 Operator precedence

Lowest to highest:

```
;                     (statement separator)
&&  ||                (short-circuit, left-assoc)
|  |&                 (pipeline, left-assoc)
redirects             (bind to the nearest simple, not the pipeline)
```

`a && b | c` parses as `a && (b | c)` (pipeline binds tighter than `&&`).

### 3.2 Where redirects attach

A redirect on a **simple stage** redirects that command. A redirect may also
follow a compound (`{ ... } > f`, `if ... end > f`), applying to the whole block.

```
a | b > out          # redirects b only
{ a | b } > out      # redirects the group
```

---

## 4. Redirects and the fd model

Only three fds exist: **0 stdin, 1 stdout, 2 stderr**. No `n<>`, no `{fd}>`, no
arbitrary fd numbers — that keeps the flow graph finite and legible.

| Syntax | fd | Effect | Flow node |
|--------|----|--------|-----------|
| `> f` / `>> f` | 1 | stdout to file | **File** flow (truncate / append) |
| `2> f` / `2>> f` | 2 | stderr to file | **File** flow |
| `< f` | 0 | stdin from file | **File** flow (read) |
| `&> f` / `&>> f` | 1,2 | stdout+stderr to file | **one** File flow, two fds write it |
| `2>&1` | 2 to 1 | stderr to wherever stdout goes | **Dup**: fd2 aliases fd1's flow |
| `1>&2` | 1 to 2 | stdout to wherever stderr goes | **Dup** |
| `>&-` / `<&-` | — | close that fd | no flow (fd = null) |

**Positional order matters**: `> f 2>&1` sends both to `f`; `2>&1 > f` sends
stderr to the *original* stdout and stdout to `f` (POSIX-identical semantics,
kept because it is the one redirect gotcha users expect).

---

## 5. Expansions

Applied to a word, in this order:

1. **Tilde** `~` to `$HOME` (only as the leading segment).
2. **Parameter** `$name` `${name}` to value. Unset becomes **empty** (`-u` makes it an error).
3. **Command substitution** `$( stmt-list )` to captured stdout, **trailing newlines stripped**.
4. **Arithmetic** `$(( expr ))` to decimal integer.
5. **Field splitting** on expanded results — **only** for unquoted expansions, **only** on the fixed separators `space/tab/newline` (there is no `IFS`).
6. **Glob** `* ? [...]` — only if the word contains an unquoted glob char and the result is a single field; no match falls back to the literal word (like bash without `nullglob`).

`$@` expands to the function's arguments, one field each.

**Special variables**: `$?` `$#` `$@` `$0` `$1..$9` (positional). `$$`, `$!`,
`$-`, `$_`, `$*` **do not exist**.

**No** `${x:-d}` `${x#p}` `${x/a/b}` `${x:1:2}` `${!x}` `${x@Q}`, no `$'...'`,
no backticks, no brace expansion.

---

## 6. Statements & control flow

### 6.1 Bindings

```
let x = 1              # immutable
mut y = 0              # mutable
y = $((y + 1))         # assignment to a `mut`
export PATH = "/usr/bin:$PATH"
unset y
```

`let`/`mut` bodies are **words** (so `let n = $(wc -l < f)` works). Re-`let` of
an existing name is an error. No arrays in v0.

### 6.2 Conditionals

The condition is a **statement list**; its exit status decides. There is no
`[[ ]]`; use `test`/`[` (a builtin) or a command.

```
if test -f "$path" then
  cat "$path"
elif test -d "$path" then
  ls "$path"
else
  echo "missing"
end
```

### 6.3 Loops

```
for f in *.txt do
  wc -l "$f"
end

mut n = 3
while test "$n" -gt 0 do
  echo "$n"
  n = $((n - 1))
end
```

`break` / `continue` act on the innermost loop. No `until`, no C-style `for`.

### 6.4 Case

```
case "$cmd" in
  start | run   => echo starting
  stop          => echo stopping
  *             => echo unknown
end
```

### 6.5 Functions

```
fn greet(name) {
  echo "hello $name"
}
greet world
```

Functions are called like commands; params are positional (`$1`, `$@` also set,
and the named params are bound as locals). `return` sets `$?`. Other variables
are dynamically scoped, as in every shell, but `let` inside a function is local.

---

## 7. Exit status

- `0` success, non-zero failure.
- `$?` holds the last pipeline's status; with `set -o pipefail` it is the last
  non-zero stage's status, else the **last** stage's.
- Command not found gives `127`; not executable `126`; killed by signal `128+sig`.
- `set -e` exits on the first failure (except in `if`/`while` conditions and
  after `!`).

---

## 8. Options

A tiny `set` (no `set -x` tracing in v0):

```
set -e            # errexit
set -u            # nounset
set -o pipefail
```

---

## 9. The cuts, in one table (review checklist)

| Removed | Why |
|---------|-----|
| `&`, `jobs`, `fg`, `bg`, `disown`, `wait`, `kill`, `$!`, `coproc` | **jobs are the worker's, not the shell's** |
| `trap`, `set -m`, tty process groups | no job control |
| `eval`, `alias`, `unalias`, `source`/`.` | no runtime re-parse; AST is the only authority |
| `${x#p}` and friends, `$'...'`, backticks, `{a,b}` | parameter-modification grammar |
| `[[ ]]`, `select`, `getopts`, `read -a` | bash-only grammar/builtins |
| arrays, associative arrays, `IFS` | value-model complexity |
| process substitution `<()` `>()` | needs FIFOs + background, conflicts with the job model |
| `n<>`, `{fd}>`, arbitrary fds | keep the flow graph finite |
| `history`, `bind`, `shopt`, `dirs`/`pushd`/`popd`, `caller`, `times`, `enable`, `let`(bash), `mapfile` | interactive/compat baggage |

---

## 10. Worked example (what the flow graph will see)

```
curl -s "$url" | grep -i error | wc -l > errcount.txt
```

Stages and flows:

```
flow f1  Pipe      producer=curl.stdout        consumer=grep.stdin
flow f2  Pipe      producer=grep.stdout        consumer=wc.stdin
flow f3  File("errcount.txt")  producer=wc.stdout
edge f1 -> f2 -> f3
```

`FLOW.md` defines exactly how the executor produces this.
