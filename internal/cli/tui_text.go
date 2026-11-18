package cli

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// sgrSequence matches a well-formed SGR sequence: CSI, then only digits, ';'
// and ':', then 'm'. Private parameter bytes (<=>?) and intermediates are not
// SGR even when the sequence ends in 'm'.
var sgrSequence = regexp.MustCompile(`^\x1b\[[0-9;:]*m$`)

// controlChars matches C0 controls and DEL, minus the ones handled elsewhere
// (newline, carriage return, tab, escape).
var controlChars = regexp.MustCompile("[\x00-\x08\x0b\x0c\x0e-\x1a\x1c-\x1f\x7f]")

const tabWidth = 8

// sanitizeContent makes arbitrary program output safe to draw inside a pane.
// Job logs contain progress output (git fetch, curl) that moves the cursor with
// carriage returns and escape sequences; drawn verbatim these escape the pane
// and paint over the rest of the screen.
func sanitizeContent(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, line := range lines {
		lines[i] = sanitizeLine(line)
	}
	return strings.Join(lines, "\n")
}

func sanitizeLine(line string) string {
	line = keepSGROnly(line)
	line = applyCarriageReturns(line)
	line = expandTabs(line)
	return controlChars.ReplaceAllString(line, "")
}

// keepSGROnly drops every escape sequence and C1 control except SGR (color and
// style), which is the only one a pane can render without moving the cursor.
// Job output can come from other machines, so anything else (resets, queries
// the terminal would answer into our input, cursor moves) must not get through.
//
// The ansi decoder splits the line into whole sequences (CSI, OSC, DCS, APC,
// PM, SOS, ESC x) and text, so a dropped sequence takes its parameters with it.
// An unterminated sequence runs to the end of the line and is dropped whole.
func keepSGROnly(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r == 0x1b || isC1(r) }) {
		return s
	}
	s = c1ToBytes(s)
	var b strings.Builder
	var state byte
	for len(s) > 0 {
		seq, _, n, newState := ansi.DecodeSequence(s, state, nil)
		if n == 0 {
			n, seq = 1, s[:1]
		}
		state = newState
		s = s[n:]
		if !isIntroducer(seq[0]) || sgrSequence.MatchString(seq) {
			b.WriteString(seq)
		}
	}
	return b.String()
}

func isC1(r rune) bool { return r >= 0x80 && r <= 0x9f }

// isIntroducer reports whether a decoded piece starting with byte c is an
// escape sequence or C1 control rather than text: ESC, or a raw byte
// 0x80-0x9F (a C1 control as rewritten by c1ToBytes, or stray invalid UTF-8).
func isIntroducer(c byte) bool {
	return c == 0x1b || (c >= 0x80 && c <= 0x9f)
}

// c1ToBytes rewrites each C1 control (U+0080-U+009F) as its raw 8-bit byte,
// which is the form the ansi decoder parses as a sequence introducer (for
// example 0x9B as CSI), so keepSGROnly drops it with its parameters. Every
// other byte, valid UTF-8 or not, is copied unchanged.
func c1ToBytes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if isC1(r) {
			b.WriteByte(byte(r))
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// applyCarriageReturns collapses a line to what a terminal would show after the
// overwrites: each segment is drawn from column 0 over what is already there.
func applyCarriageReturns(line string) string {
	if !strings.ContainsRune(line, '\r') {
		return line
	}
	var out string
	for _, seg := range strings.Split(line, "\r") {
		if seg == "" {
			continue
		}
		segWidth, outWidth := ansi.StringWidth(seg), ansi.StringWidth(out)
		if segWidth >= outWidth {
			out = seg
			continue
		}
		out = seg + ansi.Cut(out, segWidth, outWidth)
	}
	return out
}

func expandTabs(s string) string {
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			pad := tabWidth - col%tabWidth
			b.WriteString(strings.Repeat(" ", pad))
			col += pad
			continue
		}
		b.WriteRune(r)
		col++
	}
	return b.String()
}

// fitWidth truncates every line so nothing can spill past the pane border.
func fitWidth(s string, width int) string {
	if width < 1 || s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "")
	}
	return strings.Join(lines, "\n")
}
