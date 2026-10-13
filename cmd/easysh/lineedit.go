package main

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// lineEditor is a small raw-mode line editor: cursor movement, history, and
// rendering of syntax-highlight spans and a completion menu. It is deliberately
// dependency-free beyond golang.org/x/term — the whole point of easysh is that
// it owns its own parsing, so it can highlight and complete without a second
// lexer or a line-editing library.
type lineEditor struct {
	prompt   string
	buf      []rune
	pos      int
	hist     []string
	histPos  int // == len(hist) means "editing a new line"
	draft    string
	cols     int
	spans    func(string) []span
	complete func(string) []completion

	// span cache: highlighting runs on every keystroke, so we avoid a round trip
	// when the line has not changed (e.g. a pure cursor move).
	lastSpanLine string
	lastSpans    []span
	spansValid   bool
}

// span is a highlighted region of the current line.
type span struct {
	start, end int
	class      string
}

// completion is one completion candidate.
type completion struct {
	text, kind string
}

const (
	colReset  = "\x1b[0m"
	colBold   = "\x1b[1m"
	colRed    = "\x1b[31m"
	colGreen  = "\x1b[32m"
	colYellow = "\x1b[33m"
	colBlue   = "\x1b[34m"
	colMag    = "\x1b[35m"
	colCyan   = "\x1b[36m"
	colDim    = "\x1b[2m"
)

func classColor(class string) string {
	switch class {
	case "keyword":
		return colMag
	case "builtin":
		return colCyan
	case "command":
		return colGreen
	case "string":
		return colYellow
	case "param":
		return colBlue
	case "cmdsubst", "arith":
		return colCyan
	case "operator":
		return colRed
	case "comment":
		return colDim
	default:
		return ""
	}
}

// newLineEditor builds an editor with the given prompt and callbacks.
func newLineEditor(prompt string, spans func(string) []span, complete func(string) []completion) *lineEditor {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		w = 80
	}
	return &lineEditor{prompt: prompt, cols: w, spans: spans, complete: complete, histPos: 0}
}

// addHistory records an executed line (deduplicating consecutive repeats).
func (e *lineEditor) addHistory(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	if n := len(e.hist); n > 0 && e.hist[n-1] == line {
		return
	}
	e.hist = append(e.hist, line)
	e.histPos = len(e.hist)
}

// ReadLine reads one line in raw mode. ok is false on EOF/Ctrl-D.
func (e *lineEditor) ReadLine() (string, bool) {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		// Not a terminal: fall back to a plain read.
		return e.readPlain()
	}
	defer term.Restore(fd, old)

	e.buf = e.buf[:0]
	e.pos = 0
	e.histPos = len(e.hist)
	e.render()

	rd := os.Stdin
	var b [8]byte
	for {
		n, err := rd.Read(b[:])
		if err != nil {
			fmt.Print("\r\n")
			return "", false
		}
		i := 0
		for i < n {
			r, size := utf8.DecodeRune(b[i:n])
			i += size
			switch r {
			case '\r', '\n':
				fmt.Print("\r\n")
				return string(e.buf), true
			case 0x04: // Ctrl-D
				if len(e.buf) == 0 {
					fmt.Print("\r\n")
					return "", false
				}
				e.deleteAt()
			case 0x03: // Ctrl-C
				fmt.Print("^C\r\n")
				e.buf = e.buf[:0]
				e.pos = 0
				e.render()
			case 0x01: // Ctrl-A
				e.pos = 0
			case 0x05: // Ctrl-E
				e.pos = len(e.buf)
			case 0x0b: // Ctrl-K
				e.buf = e.buf[:e.pos]
			case 0x15: // Ctrl-U
				e.buf = e.buf[e.pos:]
				e.pos = 0
			case 0x17: // Ctrl-W (delete word)
				e.deleteWord()
			case 0x7f, 0x08: // Backspace
				e.backspace()
			case 0x09: // Tab -> complete
				e.doComplete()
			case 0x1b: // ESC sequence
				i += e.escape(b[i:n])
			default:
				if r >= 0x20 {
					e.insert(r)
				}
			}
			e.render()
		}
	}
}

// escape handles an ESC [ ... sequence; returns how many bytes were consumed.
func (e *lineEditor) escape(rest []byte) int {
	if len(rest) == 0 || rest[0] != '[' {
		return 0
	}
	if len(rest) < 2 {
		return 1
	}
	switch rest[1] {
	case 'A': // Up
		e.historyPrev()
	case 'B': // Down
		e.historyNext()
	case 'C': // Right
		if e.pos < len(e.buf) {
			e.pos++
		}
	case 'D': // Left
		if e.pos > 0 {
			e.pos--
		}
	case 'H': // Home
		e.pos = 0
	case 'F': // End
		e.pos = len(e.buf)
	case '3': // Delete (ESC [ 3 ~)
		e.deleteAt()
		return 3
	}
	return 2
}

func (e *lineEditor) insert(r rune) {
	e.buf = append(e.buf, 0)
	copy(e.buf[e.pos+1:], e.buf[e.pos:])
	e.buf[e.pos] = r
	e.pos++
}

func (e *lineEditor) backspace() {
	if e.pos == 0 {
		return
	}
	e.buf = append(e.buf[:e.pos-1], e.buf[e.pos:]...)
	e.pos--
}

func (e *lineEditor) deleteAt() {
	if e.pos >= len(e.buf) {
		return
	}
	e.buf = append(e.buf[:e.pos], e.buf[e.pos+1:]...)
}

func (e *lineEditor) deleteWord() {
	for e.pos > 0 && e.buf[e.pos-1] == ' ' {
		e.backspace()
	}
	for e.pos > 0 && e.buf[e.pos-1] != ' ' {
		e.backspace()
	}
}

func (e *lineEditor) historyPrev() {
	if e.histPos == 0 {
		return
	}
	if e.histPos == len(e.hist) {
		e.draft = string(e.buf)
	}
	e.histPos--
	e.setBuf(e.hist[e.histPos])
}

func (e *lineEditor) historyNext() {
	if e.histPos >= len(e.hist) {
		return
	}
	e.histPos++
	if e.histPos == len(e.hist) {
		e.setBuf(e.draft)
	} else {
		e.setBuf(e.hist[e.histPos])
	}
}

func (e *lineEditor) setBuf(s string) {
	e.buf = []rune(s)
	e.pos = len(e.buf)
}

// doComplete inserts the longest common prefix of the candidates, or lists them.
func (e *lineEditor) doComplete() {
	if e.complete == nil {
		return
	}
	word := e.currentWord()
	cands := e.complete(word)
	if len(cands) == 0 {
		return
	}
	if len(cands) == 1 {
		e.replaceWord(cands[0].text)
		return
	}
	common := commonPrefix(cands)
	if len(common) > len(word) {
		e.replaceWord(common)
		return
	}
	e.listCompletions(cands)
}

func (e *lineEditor) currentWord() string {
	start := e.pos
	for start > 0 && !isWordBreak(e.buf[start-1]) {
		start--
	}
	return string(e.buf[start:e.pos])
}

func (e *lineEditor) replaceWord(text string) {
	start := e.pos
	for start > 0 && !isWordBreak(e.buf[start-1]) {
		start--
	}
	e.buf = append(e.buf[:start], append([]rune(text), e.buf[e.pos:]...)...)
	e.pos = start + len([]rune(text))
}

func (e *lineEditor) listCompletions(cands []completion) {
	fmt.Print("\r\n")
	for _, c := range cands {
		col := ""
		switch c.kind {
		case "variable":
			col = colBlue
		case "function":
			col = colGreen
		case "builtin":
			col = colCyan
		case "keyword":
			col = colMag
		case "file":
			col = colDim
		}
		fmt.Printf("  %s%-20s%s %s\n", col, c.text, colReset, colDim+c.kind+colReset)
	}
}

// render redraws the prompt + highlighted buffer + cursor.
func (e *lineEditor) render() {
	line := string(e.buf)
	var sb strings.Builder
	sb.WriteString("\r\x1b[K") // CR + clear to end of line
	sb.WriteString(colBold + e.prompt + colReset)
	sb.WriteString(e.highlight(line))
	// Move the cursor to e.pos.
	tail := len([]rune(line)) - e.pos
	if tail > 0 {
		fmt.Fprintf(&sb, "\x1b[%dD", tail)
	}
	fmt.Print(sb.String())
}

// highlight renders the line with color spans; falls back to plain text.
func (e *lineEditor) highlight(line string) string {
	if e.spans == nil {
		return line
	}
	spans := e.cachedSpans(line)
	if len(spans) == 0 {
		return line
	}
	runes := []rune(line)
	var sb strings.Builder
	last := 0
	for _, sp := range spans {
		if sp.start > last {
			sb.WriteString(string(runes[last:sp.start]))
		}
		col := classColor(sp.class)
		end := sp.end
		if end > len(runes) {
			end = len(runes)
		}
		if col != "" && sp.start < end {
			sb.WriteString(col + string(runes[sp.start:end]) + colReset)
		} else if sp.start < end {
			sb.WriteString(string(runes[sp.start:end]))
		}
		last = end
	}
	if last < len(runes) {
		sb.WriteString(string(runes[last:]))
	}
	return sb.String()
}

// cachedSpans memoizes highlight results by line text.
func (e *lineEditor) cachedSpans(line string) []span {
	if e.spansValid && e.lastSpanLine == line {
		return e.lastSpans
	}
	e.lastSpans = e.spans(line)
	e.lastSpanLine = line
	e.spansValid = true
	return e.lastSpans
}

// readPlain is the non-terminal fallback.
func (e *lineEditor) readPlain() (string, bool) {
	fmt.Print(e.prompt)
	var line []byte
	var b [1]byte
	for {
		n, err := os.Stdin.Read(b[:])
		if n > 0 {
			if b[0] == '\n' {
				return string(line), true
			}
			line = append(line, b[0])
		}
		if err != nil {
			return string(line), len(line) > 0
		}
	}
}

func isWordBreak(r rune) bool {
	switch r {
	case ' ', '\t', '|', ';', '&', '<', '>', '(', ')', '{', '}':
		return true
	}
	return false
}

func commonPrefix(cs []completion) string {
	if len(cs) == 0 {
		return ""
	}
	p := []rune(cs[0].text)
	for _, c := range cs[1:] {
		t := []rune(c.text)
		n := len(p)
		if len(t) < n {
			n = len(t)
		}
		i := 0
		for i < n && p[i] == t[i] {
			i++
		}
		p = p[:i]
	}
	return string(p)
}
