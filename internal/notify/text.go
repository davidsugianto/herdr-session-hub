package notify

import (
	"regexp"
	"strings"
	"unicode"
)

// ansiRE matches CSI sequences, OSC sequences (BEL or ST terminated) and the
// remaining two-byte escapes.
var ansiRE = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-_]`)

// clean makes s a single notification line: ANSI stripped, control characters
// and whitespace runs collapsed to one space, capped at maxBodyRunes.
func clean(s string) string {
	s = ansiRE.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxBodyRunes {
		s = strings.TrimRight(string(r[:maxBodyRunes-1]), " ") + "…"
	}
	return s
}

// lastLine returns the last line of out that is not empty after cleaning.
func lastLine(out string) string {
	out = ansiRE.ReplaceAllString(out, "")
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := clean(lines[i]); l != "" {
			return l
		}
	}
	return ""
}
