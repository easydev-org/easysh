package syntax

import (
	"testing"
	"time"
)

// TestForWithSemicolon guards a parser hang: `for i in a b; do ...` (a
// separator between the `in` list and `do`) used to spin forever. The parser
// must terminate and produce a valid ForCmd.
func TestForWithSemicolon(t *testing.T) {
	done := make(chan struct{})
	var sc *Script
	var err error
	go func() {
		defer close(done)
		sc, err = Parse("for i in 1 2 3; do echo x end")
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("parser hung on `for ... in ...; do`")
	}
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	fc, ok := sc.Stmts[0].Chain[0].Stages[0].Cmd.(*ForCmd)
	if !ok {
		t.Fatalf("not a for: %T", sc.Stmts[0].Chain[0].Stages[0].Cmd)
	}
	if fc.Name != "i" || len(fc.In) != 3 || len(fc.Body) == 0 {
		t.Fatalf("for = name=%q in=%d body=%d", fc.Name, len(fc.In), len(fc.Body))
	}
}

// TestNoHangOnParseError guards the other half: once the parser has set an
// error, skipNewlines must not spin (next() refuses to advance on error).
func TestNoHangOnParseError(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = Parse("for i in 1 2 3; do echo x") // missing `end`
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("parser hung after an error")
	}
}
