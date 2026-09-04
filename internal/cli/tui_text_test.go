package cli

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestSanitizeContentCollapsesCarriageReturns(t *testing.T) {
	in := "Receiving objects:  20% (1/5)\rReceiving objects: 100% (5/5), done.\n"
	got := sanitizeContent(in)
	if strings.ContainsRune(got, '\r') {
		t.Fatalf("carriage return survived: %q", got)
	}
	if want := "Receiving objects: 100% (5/5), done.\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSanitizeContentOverwritesShorterSegment(t *testing.T) {
	got := sanitizeContent("Resolving deltas: 100% (2/2), done.\rResolving deltas:  50%")
	if want := "Resolving deltas:  50% (2/2), done."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSanitizeContentKeepsColorDropsCursorMoves(t *testing.T) {
	got := sanitizeContent("\x1b[31mred\x1b[0m\x1b[2J\x1b[Habc\x07\bdef")
	if want := "\x1b[31mred\x1b[0mabcdef"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandTabs(t *testing.T) {
	if got, want := expandTabs("a\tb"), "a       b"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFitWidthTruncatesEveryLine(t *testing.T) {
	got := fitWidth("aaaaaaaa\n\x1b[31mbbbbbbbb\x1b[0m", 3)
	for _, line := range strings.Split(got, "\n") {
		if w := ansi.StringWidth(line); w > 3 {
			t.Fatalf("line %q has width %d, want <= 3", line, w)
		}
	}
	if !strings.Contains(got, "\x1b[31m") {
		t.Fatalf("color lost: %q", got)
	}
}

func TestFitWidthIgnoresNonPositiveWidth(t *testing.T) {
	if got := fitWidth("abc", 0); got != "abc" {
		t.Fatalf("got %q, want %q", got, "abc")
	}
}

// Every sanitized line must be renderable inside a pane: no cursor motion, no
// line wider than the pane.
func TestSanitizedLogFitsPane(t *testing.T) {
	const width = 60
	raw := "\x1b[32m[blink.cmp]\x1b[0m fetch | remote: Counting objects:  11% (1/9)\r" +
		"remote: Counting objects: 100% (9/9), done." + strings.Repeat(" long tail", 300) + "\n" +
		"plain line\ttabbed\n"
	for _, line := range strings.Split(fitWidth(sanitizeContent(raw), width), "\n") {
		if strings.ContainsAny(line, "\r\b\x0c") {
			t.Fatalf("control char survived in %q", line)
		}
		if w := ansi.StringWidth(line); w > width {
			t.Fatalf("line width %d exceeds pane width %d: %q", w, width, line)
		}
	}
}
