package syntax

import "fmt"

// Kind is the lexical category of a token.
type Kind uint8

const (
	EOF Kind = iota
	Newline

	// Literals / words.
	WordTok   // a bare, unquoted run of characters
	SglQuoted // '...'
	DblQuoted // "..."
	ParamExp  // $x or ${x}
	CmdSubst  // $( ... )
	ArithExp  // $(( ... ))

	// Separators & operators.
	Semi        // ;
	And         // &&
	Or          // ||
	Pipe        // |
	PipeErr     // |&
	Dsemi       // ;;
	Bang        // !
	LParen      // (
	RParen      // )
	LBrace      // {
	RBrace      // }
	Assign      // =
	RedirectTok // > >> < 2> 2>> &> &>> >& <&  >&-  <&-

	// Keywords (only meaningful in command position; the lexer emits Word and
	// the parser promotes, so `echo if` still works).
)

// Token is one lexeme with its source position.
type Token struct {
	Kind Kind
	Lit  string // raw text (for Word/ParamExp/CmdSubst it is the inner text)
	Line int    // 1-based start
	Col  int    // 1-based start
	// EndLine/EndCol are the exclusive end (the position just past the token),
	// so a token's source span is [Line:Col, EndLine:EndCol).
	EndLine int
	EndCol  int
}

func (t Token) String() string {
	return fmt.Sprintf("%s(%q)@%d:%d", t.Kind, t.Lit, t.Line, t.Col)
}

// String renders a Kind for diagnostics.
func (k Kind) String() string {
	switch k {
	case EOF:
		return "EOF"
	case Newline:
		return "Newline"
	case WordTok:
		return "Word"
	case SglQuoted:
		return "SglQuoted"
	case DblQuoted:
		return "DblQuoted"
	case ParamExp:
		return "ParamExp"
	case CmdSubst:
		return "CmdSubst"
	case ArithExp:
		return "ArithExp"
	case Semi:
		return "Semi"
	case And:
		return "And"
	case Or:
		return "Or"
	case Pipe:
		return "Pipe"
	case PipeErr:
		return "PipeErr"
	case Dsemi:
		return "Dsemi"
	case Bang:
		return "Bang"
	case LParen:
		return "LParen"
	case RParen:
		return "RParen"
	case LBrace:
		return "LBrace"
	case RBrace:
		return "RBrace"
	case Assign:
		return "Assign"
	case RedirectTok:
		return "Redirect"
	default:
		return "Unknown"
	}
}
