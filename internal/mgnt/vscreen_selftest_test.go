package mgnt

import (
	"strings"
	"testing"
)

func TestVScreen(t *testing.T) {
	s := screenOf("hello\r\nworld", 4, 10)
	if got := strings.Join(s.lines(), "|"); got != "hello|world||" {
		t.Errorf("%q", got)
	}
	// Wrapping at the last column is deferred, and scrolling pushes the top row off.
	s = screenOf("0123456789ABC\r\nx\r\ny\r\nz", 3, 10)
	if got := strings.Join(s.lines(), "|"); got != "x|y|z" {
		t.Errorf("scroll: %q", got)
	}
	s = screenOf("0123456789\r\nnext", 3, 10) // exactly full: no blank row before CRLF
	if got := strings.Join(s.lines(), "|"); got != "0123456789|next|" {
		t.Errorf("exact fit: %q", got)
	}
	// Alternate screen: content on it vanishes when it is left, and the normal one returns.
	s = screenOf("main\r\n\x1b[?1049h\x1b[H\x1b[Jalt text\x1b[2;1Hstatus", 4, 12)
	if got := strings.Join(s.lines(), "|"); got != "alt text|status||" || !s.alt {
		t.Errorf("alt: %q", got)
	}
	s.feed([]byte("\x1b[?1049l"))
	if got := strings.Join(s.lines(), "|"); got != "main|||" || s.alt {
		t.Errorf("back to main: %q", got)
	}
	// Erase below and erase line. Color codes take no room.
	s = screenOf("\x1b[31mred\x1b[0m line one\r\nline two\x1b[1;4H\x1b[K\x1b[2;1H\x1b[J", 3, 20)
	if got := strings.Join(s.lines(), "|"); got != "red||" {
		t.Errorf("erase: %q", got)
	}
}
