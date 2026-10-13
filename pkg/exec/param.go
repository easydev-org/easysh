package exec

import (
	"strconv"
	"strings"
)

// shMatch reports whether the shell pattern pat matches s, where `*` matches
// ANY run of characters (including `/`) and `?` matches one. This differs from
// path.Match, whose `*` stops at `/` — shell parameter expansion uses the
// separator-agnostic form, so `${x##*/}` on `/a/b/c` must yield `c`.
func shMatch(pat, s string) bool {
	return shMatchAt(pat, s, 0, 0)
}

func shMatchAt(pat, s string, pi, si int) bool {
	for pi < len(pat) {
		switch pat[pi] {
		case '*':
			// Collapse consecutive '*'.
			for pi < len(pat) && pat[pi] == '*' {
				pi++
			}
			if pi == len(pat) {
				return true
			}
			for k := si; k <= len(s); k++ {
				if shMatchAt(pat, s, pi, k) {
					return true
				}
			}
			return false
		case '?':
			if si >= len(s) {
				return false
			}
			pi++
			si++
		case '[':
			// Character class: [abc], [a-z], [!abc] / [^abc].
			end := pi + 1
			if end < len(pat) && (pat[end] == '!' || pat[end] == '^') {
				end++
			}
			if end < len(pat) && pat[end] == ']' {
				end++
			}
			for end < len(pat) && pat[end] != ']' {
				end++
			}
			if end >= len(pat) {
				// Unterminated class: treat '[' literally.
				if si < len(s) && s[si] == '[' {
					pi++
					si++
					continue
				}
				return false
			}
			if si >= len(s) {
				return false
			}
			if !classMatch(pat[pi+1:end], s[si]) {
				return false
			}
			pi = end + 1
			si++
		default:
			if si >= len(s) || s[si] != pat[pi] {
				return false
			}
			pi++
			si++
		}
	}
	return si == len(s)
}

func classMatch(cls string, c byte) bool {
	neg := false
	if cls != "" && (cls[0] == '!' || cls[0] == '^') {
		neg = true
		cls = cls[1:]
	}
	hit := false
	for i := 0; i < len(cls); i++ {
		if i+2 < len(cls) && cls[i+1] == '-' {
			if cls[i] <= c && c <= cls[i+2] {
				hit = true
			}
			i += 2
			continue
		}
		if cls[i] == c {
			hit = true
		}
	}
	if neg {
		return !hit
	}
	return hit
}

// These implement the POSIX ${x#p} / ${x%p} / ${x/a/b} / ${x:o:l} family. They
// use shell globbing semantics (path.Match), not regex.

// matchAt reports whether pattern pat matches a prefix of s, returning the
// number of bytes matched (0 = no match). Shell globs are unanchored at the
// match point, so we try every prefix length.
func prefixMatchLen(s, pat string) int {
	best := -1
	for i := 0; i <= len(s); i++ {
		if shMatch(pat, s[:i]) {
			if i > best {
				best = i
			}
		}
	}
	return best
}

func suffixMatchLen(s, pat string) int {
	best := -1
	for i := 0; i <= len(s); i++ {
		if shMatch(pat, s[i:]) {
			if i > best {
				best = i
			}
		}
	}
	return best
}

// trimPrefixShortest removes the shortest matching prefix (${x#p}).
func trimPrefixShortest(s, pat string) string {
	best := -1
	for i := 0; i <= len(s); i++ {
		if shMatch(pat, s[:i]) {
			best = i
			break
		}
	}
	if best < 0 {
		return s
	}
	return s[best:]
}

// trimPrefixLongest removes the longest matching prefix (${x##p}).
func trimPrefixLongest(s, pat string) string {
	best := -1
	for i := 0; i <= len(s); i++ {
		if shMatch(pat, s[:i]) {
			best = i
		}
	}
	if best < 0 {
		return s
	}
	return s[best:]
}

// trimSuffixShortest removes the shortest matching suffix (${x%p}).
func trimSuffixShortest(s, pat string) string {
	best := -1
	for i := len(s); i >= 0; i-- {
		if shMatch(pat, s[i:]) {
			best = i
			break
		}
	}
	if best < 0 {
		return s
	}
	return s[:best]
}

// trimSuffixLongest removes the longest matching suffix (${x%%p}).
func trimSuffixLongest(s, pat string) string {
	best := -1
	for i := len(s); i >= 0; i-- {
		if shMatch(pat, s[i:]) {
			best = i
		}
	}
	if best < 0 {
		return s
	}
	return s[:best]
}

// globToPrefix is used by the unanchored ${x#p}: prefix removal already scans
// all prefixes, so the pattern is used as-is.
func globToPrefix(pat string) string { return pat }

// substPattern implements ${x/pat/repl} (first) and ${x//pat/repl} (all). The
// operand word is "pat/repl" (a '/' separates them).
func substPattern(s, operand string, all bool) string {
	pat, repl := operand, ""
	if i := strings.IndexByte(operand, '/'); i >= 0 {
		pat, repl = operand[:i], operand[i+1:]
	}
	if pat == "" {
		return s
	}
	var out strings.Builder
	for i := 0; i < len(s); {
		// Longest match at i (shell uses longest match for substitution).
		best := -1
		for j := len(s); j >= i; j-- {
			if shMatch(pat, s[i:j]) {
				best = j
				break
			}
		}
		if best >= 0 && best > i {
			out.WriteString(repl)
			i = best
			if !all {
				out.WriteString(s[i:])
				break
			}
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

// substrExp implements ${x:off} / ${x:off:len}. Negative offsets count from the
// end. The operand is "off" or "off:len".
func substrExp(s, operand string) string {
	offStr, lenStr := operand, ""
	if i := strings.IndexByte(operand, ':'); i >= 0 {
		offStr, lenStr = operand[:i], operand[i+1:]
	}
	off, err := strconv.Atoi(strings.TrimSpace(offStr))
	if err != nil {
		return s
	}
	n := len(s)
	if off < 0 {
		off = n + off
	}
	if off < 0 {
		off = 0
	}
	if off > n {
		return ""
	}
	if lenStr == "" {
		return s[off:]
	}
	l, err := strconv.Atoi(strings.TrimSpace(lenStr))
	if err != nil {
		return s[off:]
	}
	if l < 0 {
		end := n + l
		if end < off {
			return ""
		}
		return s[off:end]
	}
	if off+l > n {
		l = n - off
	}
	return s[off : off+l]
}
