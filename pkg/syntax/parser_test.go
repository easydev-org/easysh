package syntax

import "testing"

func mustParse(t *testing.T, src string) *Script {
	t.Helper()
	sc, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	return sc
}

func TestPipeline(t *testing.T) {
	sc := mustParse(t, `a | b | c`)
	if len(sc.Stmts) != 1 || len(sc.Stmts[0].Chain) != 1 {
		t.Fatalf("want 1 stmt/1 pipeline")
	}
	pl := sc.Stmts[0].Chain[0]
	if len(pl.Stages) != 3 {
		t.Fatalf("want 3 stages, got %d", len(pl.Stages))
	}
}

func TestLogicalChain(t *testing.T) {
	sc := mustParse(t, `a && b || c`)
	st := sc.Stmts[0]
	if len(st.Chain) != 3 {
		t.Fatalf("want 3 pipelines, got %d", len(st.Chain))
	}
	if len(st.Ops) != 2 || st.Ops[0] != "&&" || st.Ops[1] != "||" {
		t.Fatalf("ops = %v", st.Ops)
	}
}

func TestRedirects(t *testing.T) {
	sc := mustParse(t, `echo hi > out.txt 2>&1`)
	sg := sc.Stmts[0].Chain[0].Stages[0]
	if len(sg.Redirs) != 2 {
		t.Fatalf("want 2 redirects, got %d", len(sg.Redirs))
	}
	if sg.Redirs[0].Op != ">" || sg.Redirs[1].Op != ">&" {
		t.Fatalf("ops = %q,%q", sg.Redirs[0].Op, sg.Redirs[1].Op)
	}
}

func TestBinding(t *testing.T) {
	sc := mustParse(t, `let x = 1`)
	sg := sc.Stmts[0].Chain[0].Stages[0]
	if len(sg.Bindings) != 1 || sg.Bindings[0].Mode != "let" || sg.Bindings[0].Name != "x" {
		t.Fatalf("binding = %+v", sg.Bindings)
	}
	if sg.Cmd != nil {
		t.Fatalf("a bare binding should have no command")
	}
}

func TestBareAssign(t *testing.T) {
	sc := mustParse(t, `n = 5`)
	sg := sc.Stmts[0].Chain[0].Stages[0]
	if len(sg.Bindings) != 1 || sg.Bindings[0].Mode != "set" || sg.Bindings[0].Name != "n" {
		t.Fatalf("binding = %+v", sg.Bindings)
	}
}

func TestIf(t *testing.T) {
	sc := mustParse(t, "if a then b elif c then d else e end")
	ic, ok := sc.Stmts[0].Chain[0].Stages[0].Cmd.(*IfCmd)
	if !ok {
		t.Fatalf("not an IfCmd: %T", sc.Stmts[0].Chain[0].Stages[0].Cmd)
	}
	if len(ic.Cond) != 1 || len(ic.Then) != 1 || len(ic.Elifs) != 1 || len(ic.Else) != 1 {
		t.Fatalf("if shape wrong: %+v", ic)
	}
}

func TestFor(t *testing.T) {
	sc := mustParse(t, "for f in a b c do echo $f end")
	fc, ok := sc.Stmts[0].Chain[0].Stages[0].Cmd.(*ForCmd)
	if !ok {
		t.Fatalf("not a ForCmd")
	}
	if fc.Name != "f" || len(fc.In) != 3 || len(fc.Body) != 1 {
		t.Fatalf("for shape wrong: %+v", fc)
	}
}

func TestCase(t *testing.T) {
	sc := mustParse(t, "case $x in\n a => echo A\n b | c => echo BC\n * => echo other\nend")
	cc, ok := sc.Stmts[0].Chain[0].Stages[0].Cmd.(*CaseCmd)
	if !ok {
		t.Fatalf("not a CaseCmd")
	}
	if len(cc.Arms) != 3 {
		t.Fatalf("want 3 arms, got %d", len(cc.Arms))
	}
	if len(cc.Arms[1].Pats) != 2 {
		t.Fatalf("arm 1 should have 2 patterns")
	}
}

func TestFunc(t *testing.T) {
	sc := mustParse(t, `fn add(a, b) { echo "$a + $b" }`)
	fd, ok := sc.Stmts[0].Chain[0].Stages[0].Cmd.(*FuncDef)
	if !ok {
		t.Fatalf("not a FuncDef")
	}
	if fd.Name != "add" || len(fd.Params) != 2 || fd.Params[0] != "a" || fd.Params[1] != "b" {
		t.Fatalf("func shape wrong: %+v", fd)
	}
}

func TestWordPartsOrder(t *testing.T) {
	// "n=$n" must expand to Lit("n=") then Param("n"), in that order.
	sc := mustParse(t, `echo "n=$n"`)
	sm := sc.Stmts[0].Chain[0].Stages[0].Cmd.(*Simple)
	w := sm.Args[1]
	if len(w.Parts) != 2 {
		t.Fatalf("want 2 parts, got %d: %#v", len(w.Parts), w.Parts)
	}
	if l, ok := w.Parts[0].(*Lit); !ok || l.Value != "n=" {
		t.Fatalf("part0 = %#v", w.Parts[0])
	}
	if p, ok := w.Parts[1].(*Param); !ok || p.Name != "n" {
		t.Fatalf("part1 = %#v", w.Parts[1])
	}
}

func TestCmdSubst(t *testing.T) {
	sc := mustParse(t, `let n = $(echo 42)`)
	sg := sc.Stmts[0].Chain[0].Stages[0]
	w := sg.Bindings[0].Value
	if len(w.Parts) != 1 {
		t.Fatalf("want 1 part")
	}
	if _, ok := w.Parts[0].(*CmdSub); !ok {
		t.Fatalf("part = %#v", w.Parts[0])
	}
}

func TestArith(t *testing.T) {
	sc := mustParse(t, `echo $((1 + 2 * 3))`)
	sm := sc.Stmts[0].Chain[0].Stages[0].Cmd.(*Simple)
	if _, ok := sm.Args[1].Parts[0].(*Arith); !ok {
		t.Fatalf("part = %#v", sm.Args[1].Parts[0])
	}
}

func TestComment(t *testing.T) {
	sc := mustParse(t, "echo hi # this is a comment\n")
	sm := sc.Stmts[0].Chain[0].Stages[0].Cmd.(*Simple)
	if len(sm.Args) != 2 {
		t.Fatalf("comment should not be an arg: %d args", len(sm.Args))
	}
}
