package syntax

import "testing"

// TestStageSpans checks each pipeline stage records its exact source span, so a
// UI can highlight which segment of the command it is.
func TestStageSpans(t *testing.T) {
	sc := mustParse(t, `ls -l | grep x | wc -l`)
	stages := sc.Stmts[0].Chain[0].Stages
	if len(stages) != 3 {
		t.Fatalf("want 3 stages, got %d", len(stages))
	}
	want := []struct{ col, end int }{{1, 6}, {9, 15}, {18, 23}}
	for i, st := range stages {
		p := st.Pos()
		if p.Line != 1 || p.Col != want[i].col || p.EndLine != 1 || p.EndCol != want[i].end {
			t.Fatalf("stage %d span = %+v, want col %d..%d", i, p, want[i].col, want[i].end)
		}
	}
}

// TestSpanAcrossLines checks a multi-line pipeline records per-line spans.
func TestSpanAcrossLines(t *testing.T) {
	sc := mustParse(t, "cat f |\n  grep x")
	stages := sc.Stmts[0].Chain[0].Stages
	if stages[0].Pos().Line != 1 || stages[1].Pos().Line != 2 {
		t.Fatalf("line spans wrong: %+v %+v", stages[0].Pos(), stages[1].Pos())
	}
}

// substStages returns the stages inside the first $(...) of a command.
func substStages(t *testing.T, src string) []*Stage {
	t.Helper()
	sc := mustParse(t, src)
	sm := sc.Stmts[0].Chain[0].Stages[0].Cmd.(*Simple)
	for _, p := range sm.Args {
		for _, part := range p.Parts {
			if cs, ok := part.(*CmdSub); ok {
				var out []*Stage
				for _, st := range cs.Body {
					for _, pl := range st.Chain {
						out = append(out, pl.Stages...)
					}
				}
				return out
			}
		}
	}
	t.Fatal("no command substitution found")
	return nil
}

// TestSubstitutionSpan checks a $() body's stages are reported at ABSOLUTE
// source columns (offset by where $( appears), not relative to the substring.
func TestSubstitutionSpan(t *testing.T) {
	// echo $(ls /etc | wc -l)
	//       ^$(                ls at col 8, wc at col 18
	st := substStages(t, `echo $(ls /etc | wc -l)`)
	if len(st) != 2 {
		t.Fatalf("want 2 subst stages, got %d", len(st))
	}
	if p := st[0].Pos(); p.Line != 1 || p.Col != 8 {
		t.Fatalf("ls span = %+v, want col 8", p)
	}
	if p := st[1].Pos(); p.Line != 1 || p.Col != 18 {
		t.Fatalf("wc span = %+v, want col 18", p)
	}
}

// TestNestedSubstitutionSpan checks nesting offsets accumulate.
func TestNestedSubstitutionSpan(t *testing.T) {
	// echo $(cat $(ls))
	//            outer $( at col 6, inner ls at col 12
	st := substStages(t, `echo $(cat $(ls))`)
	if len(st) != 1 || st[0].Pos().Col != 8 {
		t.Fatalf("outer cat span = %+v", st[0].Pos())
	}
}
