package syntax

// Node is any AST node.
type Node interface {
	Pos() Pos
}

// Pos is a source position (1-based line/col) and, when known, the exclusive
// end of the node's span. EndLine==0 means "end not recorded".
type Pos struct {
	Line    int
	Col     int
	EndLine int
	EndCol  int
}

// ── top level ───────────────────────────────────────────────────────────────

// Script is a whole program: a sequence of statements.
type Script struct {
	At    Pos
	Stmts []*Stmt
}

// Stmt is one statement: a pipeline, possibly in a &&/|| chain.
type Stmt struct {
	At    Pos
	Chain []*Pipeline // logical chain: pipe0 (&&|pipe1) (|||pipe2) ...
	Ops   []string    // operators between pipelines: "&&", "||" (len = len(Chain)-1)
}

// Pipeline is one or more stages joined by | / |&.
type Pipeline struct {
	At     Pos
	Stages []*Stage
	Errs   []bool // Errs[i] true when the pipe BEFORE stage i+1 was |&
}

// Stage is a single command with optional bindings and redirects.
type Stage struct {
	At       Pos
	Bindings []*Binding
	Cmd      Node // Simple | *IfCmd | *ForCmd | *WhileCmd | *CaseCmd | *Block | *FuncDef
	Redirs   []*Redirect
}

// ── commands ────────────────────────────────────────────────────────────────

// Simple is `name arg1 arg2 ...`.
type Simple struct {
	At   Pos
	Args []*Word
}

// Binding is `let x = word` / `mut x = word` / `export x = word`.
type Binding struct {
	At    Pos
	Mode  string // "let" | "mut" | "export"
	Name  string
	Value *Word
}

// Redirect is one redirection operator + target word.
type Redirect struct {
	At     Pos
	Op     string // ">", ">>", "<", "2>", "2>>", "&>", "&>>", ">&", "<&"
	Fd     int    // 0, 1, or 2 (for the primary fd; &> touches 1 and 2)
	Target *Word  // file path, or a here-doc body for Op "<<" / "<<<"
	Here   bool   // true when Op is "<<" or "<<<"
}

// ── control flow ────────────────────────────────────────────────────────────

// IfCmd is `if cond then ... [elif ...] [else ...] end`.
type IfCmd struct {
	At    Pos
	Cond  []*Stmt
	Then  []*Stmt
	Elifs []IfBranch
	Else  []*Stmt
}

// IfBranch is one `elif cond then body`.
type IfBranch struct {
	Cond []*Stmt
	Then []*Stmt
}

// ForCmd is `for NAME in words do ... end`.
type ForCmd struct {
	At   Pos
	Name string
	In   []*Word
	Body []*Stmt
}

// WhileCmd is `while cond do ... end`.
type WhileCmd struct {
	At   Pos
	Cond []*Stmt
	Body []*Stmt
}

// CaseCmd is `case word in pat => body ... end`.
type CaseCmd struct {
	At   Pos
	Word *Word
	Arms []CaseArm
}

// CaseArm is one `pat | pat => body`.
type CaseArm struct {
	Pats []*Word
	Body []*Stmt
}

// Block is `( ... )` (subshell) or `{ ... }` (current shell).
type Block struct {
	At       Pos
	Subshell bool
	Body     []*Stmt
}

// FuncDef is `fn name(p1, p2) { ... }`.
type FuncDef struct {
	At     Pos
	Name   string
	Params []string
	Body   []*Stmt
}

// ── words ───────────────────────────────────────────────────────────────────

// Word is a sequence of parts that expand to one or more fields.
type Word struct {
	At    Pos
	Parts []WordPart
}

// WordPart is one segment of a word.
type WordPart interface {
	wordPart()
	Pos() Pos
}

// Lit is literal text (from a bare word, or the body of a quoted string).
type Lit struct {
	At     Pos
	Value  string
	Quoted bool // true when it came from '...' or "..."
}

// Param is `$x` or `${x}`.
type Param struct {
	At   Pos
	Name string
}

// CmdSub is `$( ... )`.
type CmdSub struct {
	At   Pos
	Body []*Stmt
}

// Arith is `$(( ... ))`; the raw expression text is kept for v0.
type Arith struct {
	At  Pos
	Raw string
}

func (*Lit) wordPart()    {}
func (*Param) wordPart()  {}
func (*CmdSub) wordPart() {}
func (*Arith) wordPart()  {}

func (n *Script) Pos() Pos   { return n.At }
func (n *Stmt) Pos() Pos     { return n.At }
func (n *Pipeline) Pos() Pos { return n.At }
func (n *Stage) Pos() Pos    { return n.At }
func (n *Simple) Pos() Pos   { return n.At }
func (n *Binding) Pos() Pos  { return n.At }
func (n *Redirect) Pos() Pos { return n.At }
func (n *IfCmd) Pos() Pos    { return n.At }
func (n *ForCmd) Pos() Pos   { return n.At }
func (n *WhileCmd) Pos() Pos { return n.At }
func (n *CaseCmd) Pos() Pos  { return n.At }
func (n *Block) Pos() Pos    { return n.At }
func (n *FuncDef) Pos() Pos  { return n.At }
func (n *Word) Pos() Pos     { return n.At }
func (n *Lit) Pos() Pos      { return n.At }
func (n *Param) Pos() Pos    { return n.At }
func (n *CmdSub) Pos() Pos   { return n.At }
func (n *Arith) Pos() Pos    { return n.At }
