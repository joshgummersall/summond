package cli

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/joshgummersall/summond/internal/job"
)

func TestSanitizeLineKeepsOnlySGR(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text", "hello wörld", "hello wörld"},
		{"basic SGR", "\x1b[1;31mred\x1b[0m", "\x1b[1;31mred\x1b[0m"},
		{"empty SGR", "\x1b[mx", "\x1b[mx"},
		{"256 colour", "\x1b[38;5;208mx\x1b[48;5;17my", "\x1b[38;5;208mx\x1b[48;5;17my"},
		{"true colour", "\x1b[38;2;255;128;0mx", "\x1b[38;2;255;128;0mx"},
		{"true colour colon form", "\x1b[38:2::255:128:0mx", "\x1b[38:2::255:128:0mx"},
		{"underline style colon", "\x1b[4:3mx", "\x1b[4:3mx"},
		{"full reset", "a\x1bcb", "ab"},
		{"save cursor", "a\x1b7b", "ab"},
		{"restore cursor", "a\x1b8b", "ab"},
		{"charset select", "a\x1b(0b", "ab"},
		{"keypad mode", "a\x1b=b\x1b>c", "abc"},
		{"device attributes secondary", "a\x1b[>cb", "ab"},
		{"device attributes tertiary", "a\x1b[=cb", "ab"},
		{"private less-than", "a\x1b[<0;10;5Mb", "ab"},
		{"xterm modify keys ends in m", "a\x1b[>4;2mb", "ab"},
		{"private question mark ends in m", "a\x1b[?1mb", "ab"},
		{"SGR with intermediate", "a\x1b[1 mb", "ab"},
		{"private mode set", "a\x1b[?25lb", "ab"},
		{"8-bit CSI", "a\u009b31mb", "ab"},
		{"8-bit CSI query", "a\u009b>cb", "ab"},
		{"8-bit OSC", "a\u009d0;title\u009cb", "ab"},
		{"other C1 controls", "a\u0080\u0085\u0084\u008e\u009ab", "ab"},
		{"8-bit APC with 8-bit ST", "a\u009fdata\u009cb", "ab"},
		{"lone escape at end", "abc\x1b", "abc"},
		{"lone escape before newline", "abc\x1b\ndef", "abc\ndef"},
		{"double escape", "a\x1b\x1bcb", "ab"},
		{"OSC with BEL", "a\x1b]0;title\x07b", "ab"},
		{"OSC hyperlink with ST", "a\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\b", "alinkb"},
		{"DCS", "a\x1bP1$r0m\x1b\\b", "ab"},
		{"APC", "a\x1b_Gf=24;AAAA\x1b\\b", "ab"},
		{"PM and SOS", "a\x1b^pm\x1b\\\x1bXsos\x1b\\b", "ab"},
		{"unterminated OSC", "a\x1b]0;title", "a"},
		{"invalid CSI byte", "a\x1b[1\x01b", "ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeContent(tt.in); got != tt.want {
				t.Fatalf("sanitizeContent(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTUIJobListAndStatusLineStripEscapes(t *testing.T) {
	m := newTUIModel(nil)
	m.width, m.height = 120, 30
	m.jobs = []job.Spec{{Name: "evil\x1bc\x1b[>cname\u009b6n"}}
	m.statusMsg = "error: boom\x1b[>c\x1b7\u009b6n\x1b"
	view := m.View()
	for _, bad := range []string{"\x1bc", "\x1b[>c", "\x1b7", "\u009b", "[>c", "6n"} {
		if strings.Contains(view, bad) {
			t.Fatalf("view contains %q: %q", bad, view)
		}
	}
	if !strings.Contains(view, "evilname") || !strings.Contains(view, "error: boom") {
		t.Fatalf("view lost text: %q", view)
	}
}

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
