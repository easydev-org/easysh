// Command astdump prints a parsed AST, for debugging the parser.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/abcp-sdk/easysh/internal/syntax"
)

func main() {
	src := strings.Join(os.Args[1:], " ")
	sc, err := syntax.Parse(src)
	if err != nil {
		fmt.Println("ERR:", err)
		return
	}
	for i, st := range sc.Stmts {
		fmt.Printf("stmt[%d] chains=%d ops=%v\n", i, len(st.Chain), st.Ops)
		for j, pl := range st.Chain {
			for k, sg := range pl.Stages {
				fmt.Printf("  stage[%d.%d] bindings=%d cmd=%T redirs=%d\n",
					j, k, len(sg.Bindings), sg.Cmd, len(sg.Redirs))
				for _, b := range sg.Bindings {
					fmt.Printf("    bind mode=%s name=%q val=%q\n", b.Mode, b.Name, litOf(b.Value))
				}
				if sm, ok := sg.Cmd.(*syntax.Simple); ok {
					var args []string
					for _, a := range sm.Args {
						args = append(args, litOf(a))
					}
					fmt.Printf("    args=%q\n", args)
				}
			}
		}
	}
}

func litOf(w *syntax.Word) string {
	if w == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range w.Parts {
		switch v := p.(type) {
		case *syntax.Lit:
			b.WriteString(v.Value)
		case *syntax.Param:
			b.WriteString("$" + v.Name)
		case *syntax.CmdSub:
			b.WriteString("$()")
		case *syntax.Arith:
			b.WriteString("$((" + v.Raw + "))")
		}
	}
	return b.String()
}
