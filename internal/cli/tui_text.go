package cli

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// ansiSequence matches CSI, OSC and two-byte escape sequences.
var ansiSequence = regexp.MustCompile("\x1b(?:\\[[0-9;:?]*[ -/]*[@-~]|\\][^\x07\x1b]*(?:\x07|\x1b\\\\)?|[@-Z\\\\-_])")

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

// keepSGROnly drops every escape sequence except SGR (color and style), which
// is the only one a pane can render without moving the cursor.
func keepSGROnly(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	return ansiSequence.ReplaceAllStringFunc(s, func(seq string) string {
		if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
			return seq
		}
		return ""
	})
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
