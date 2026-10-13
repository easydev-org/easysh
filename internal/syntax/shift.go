package syntax

// posAt returns the absolute source position of byte offset off within text
// whose first byte is at base. Newlines advance the line and reset the column.
func posAt(raw string, off int, base Pos) Pos {
	line, col := base.Line, base.Col
	for i := 0; i < off && i < len(raw); i++ {
		if raw[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return Pos{Line: line, Col: col}
}

// rebase shifts a position that is relative to a substring whose first byte sits
// at start. A node at relative (1, C) is at (start.Line, start.Col+C-1); a node
// on a later relative line keeps its column (newlines reset to column 1 in the
// absolute text too) and is pushed down by start.Line-1. End positions are
// shifted the same way.
func rebase(p, start Pos) Pos {
	if p.Line == 0 {
		return p
	}
	one := func(line, col int) (int, int) {
		if line == 1 {
			return start.Line, start.Col + col - 1
		}
		return start.Line + line - 1, col
	}
	nl, nc := one(p.Line, p.Col)
	p.Line, p.Col = nl, nc
	if p.EndLine != 0 {
		el, ec := one(p.EndLine, p.EndCol)
		p.EndLine, p.EndCol = el, ec
	}
	return p
}

// rebaseStmts shifts every position in a parsed $() body so its spans become
// absolute within the enclosing word.
func rebaseStmts(stmts []*Stmt, start Pos) {
	for _, st := range stmts {
		rebaseStmt(st, start)
	}
}

func rebaseStmt(st *Stmt, s Pos) {
	if st == nil {
		return
	}
	st.At = rebase(st.At, s)
	for _, pl := range st.Chain {
		pl.At = rebase(pl.At, s)
		for _, sg := range pl.Stages {
			rebaseStage(sg, s)
		}
	}
}

func rebaseStage(sg *Stage, s Pos) {
	if sg == nil {
		return
	}
	sg.At = rebase(sg.At, s)
	for _, b := range sg.Bindings {
		b.At = rebase(b.At, s)
		rebaseWord(b.Value, s)
	}
	for _, rd := range sg.Redirs {
		rd.At = rebase(rd.At, s)
		rebaseWord(rd.Target, s)
	}
	rebaseCmd(sg.Cmd, s)
}

func rebaseCmd(c Node, s Pos) {
	switch v := c.(type) {
	case nil:
	case *Simple:
		v.At = rebase(v.At, s)
		for _, w := range v.Args {
			rebaseWord(w, s)
		}
	case *IfCmd:
		v.At = rebase(v.At, s)
		rebaseStmts(v.Cond, s)
		rebaseStmts(v.Then, s)
		for _, e := range v.Elifs {
			rebaseStmts(e.Cond, s)
			rebaseStmts(e.Then, s)
		}
		rebaseStmts(v.Else, s)
	case *ForCmd:
		v.At = rebase(v.At, s)
		for _, w := range v.In {
			rebaseWord(w, s)
		}
		rebaseStmts(v.Body, s)
	case *WhileCmd:
		v.At = rebase(v.At, s)
		rebaseStmts(v.Cond, s)
		rebaseStmts(v.Body, s)
	case *CaseCmd:
		v.At = rebase(v.At, s)
		rebaseWord(v.Word, s)
		for _, arm := range v.Arms {
			for _, w := range arm.Pats {
				rebaseWord(w, s)
			}
			rebaseStmts(arm.Body, s)
		}
	case *Block:
		v.At = rebase(v.At, s)
		rebaseStmts(v.Body, s)
	case *FuncDef:
		v.At = rebase(v.At, s)
		rebaseStmts(v.Body, s)
	}
}

func rebaseWord(w *Word, s Pos) {
	if w == nil {
		return
	}
	w.At = rebase(w.At, s)
	for _, p := range w.Parts {
		switch v := p.(type) {
		case *Lit:
			v.At = rebase(v.At, s)
		case *Param:
			v.At = rebase(v.At, s)
		case *Arith:
			v.At = rebase(v.At, s)
		case *CmdSub:
			v.At = rebase(v.At, s)
			// Nested $() bodies were parsed relative to their own substring;
			// they are already rebased when built, so leave them.
		}
	}
}
