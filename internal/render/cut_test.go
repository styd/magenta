package render

import (
	"math/rand"
	"strings"
	"testing"
)

func strs(rows [][]byte) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = string(r)
	}
	return out
}

func TestWrap(t *testing.T) {
	red, off := "\x1b[31m", "\x1b[0m"
	tests := []struct {
		name  string
		in    string
		width int
		want  []string
	}{
		{"empty", "", 10, []string{""}},
		{"fits", "hello", 10, []string{"hello"}},
		{"exact", "0123456789", 10, []string{"0123456789"}},
		{"one over", "0123456789A", 10, []string{"0123456789", "A"}},
		{"three rows", strings.Repeat("x", 25), 10, []string{"xxxxxxxxxx", "xxxxxxxxxx", "xxxxx"}},
		{"color stays inside one row", red + "hi" + off, 10, []string{red + "hi" + off}},
		// A color that spans the break is switched off at the end of the row and on again at the next.
		{"color across a break", red + "abcdef" + off, 4, []string{red + "abcd" + off, red + "ef" + off}},
		{"color ended before the break", red + "ab" + off + "cdef", 4, []string{red + "ab" + off + "cd", "ef"}},
		{"wide characters do not split", "日本語", 5, []string{"日本", "語"}},
		{"a wide character in a one column row is kept", "日", 1, []string{"日"}},
		{"tabs become spaces", "a\tb", 20, []string{"a       b"}},
		{"tabs can wrap", "\t\t\t", 10, []string{"          ", "              "}[:0]},
		{"combining marks take no room", "ééé", 3, []string{"ééé"}},
	}
	for _, tt := range tests {
		if tt.name == "tabs can wrap" {
			if got := strs(Wrap([]byte(tt.in), tt.width)); len(got) != 3 || got[0] != strings.Repeat(" ", 10) || got[2] != strings.Repeat(" ", 4) {
				t.Errorf("%s: %q", tt.name, got)
			}
			continue
		}
		if got := strs(Wrap([]byte(tt.in), tt.width)); strings.Join(got, "|") != strings.Join(tt.want, "|") {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestCut(t *testing.T) {
	red, off := "\x1b[31m", "\x1b[0m"
	tests := []struct {
		name        string
		in          string
		from, width int
		want        string
	}{
		{"from the start", "hello world", 0, 5, "hello"},
		{"from the middle", "hello world", 6, 5, "world"},
		{"past the end", "hello", 10, 5, ""},
		{"shorter than the window", "hi", 0, 10, "hi"},
		{"color before the cut is switched on again", red + "hello world" + off, 6, 5, red + "world" + off},
		{"color inside the window", "ab" + red + "cd" + off + "ef", 1, 4, "b" + red + "cd" + off + "e"},
		{"color ended before the cut is not repeated", red + "ab" + off + "cdef", 2, 3, "cde"},
		{"a window that ends inside a colored stretch is reset", red + "abcdef" + off, 0, 3, red + "abc" + off},
		{"a wide character cut in half at the left becomes a space", "日本", 1, 3, " 本"},
		{"a wide character that does not fit at the right becomes a space", "ab日", 0, 3, "ab "},
		{"tabs are expanded before cutting", "a\tb", 7, 2, " b"},
	}
	for _, tt := range tests {
		if got := string(Cut([]byte(tt.in), tt.from, tt.width)); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
}

// Whatever is cut or wrapped, the visible text is the line's, in order, and
// each piece is no wider than asked.
func TestWrapAndCutProperties(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	alphabet := []string{"a", "b", " ", "\t", "日", "é", "\x1b[31m", "\x1b[1;38;5;100m", "\x1b[0m", "x", "y"}
	for i := 0; i < 3000; i++ {
		var sb strings.Builder
		for n := rng.Intn(40); n > 0; n-- {
			sb.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		text := []byte(sb.String())
		width := 1 + rng.Intn(12)

		rows := Wrap(text, width)
		if len(rows) != Count(text, width) {
			t.Fatalf("Count %d != rows %d for %q width %d", Count(text, width), len(rows), text, width)
		}
		var joined []byte
		for _, r := range rows {
			if w := Width(r); w > width && !(w <= 2 && width == 1) {
				t.Fatalf("row %q is %d wide for width %d (line %q)", r, w, width, text)
			}
			joined = append(joined, StripSGR(nil, r)...)
		}
		want := StripSGR(nil, []byte(strings.ReplaceAll(string(text), "\t", "")))
		got := strings.ReplaceAll(string(joined), " ", "")
		wantNoSpace := strings.ReplaceAll(string(want), " ", "")
		if got != wantNoSpace {
			t.Fatalf("wrapping changed the text: %q -> %q (width %d)", text, joined, width)
		}

		from := rng.Intn(30)
		cut := Cut(text, from, width)
		if w := Width(cut); w > width {
			t.Fatalf("cut %q is %d wide for width %d", cut, w, width)
		}
		// A cut never leaves a color switched on at its end.
		if strings.Contains(string(cut), "\x1b[") {
			tail := string(cut)
			last := strings.LastIndex(tail, "\x1b[")
			if end := tail[last:]; !strings.HasPrefix(end, "\x1b[0m") && !strings.HasPrefix(end, "\x1b[m") {
				// The last sequence is a color: it must have been closed by a reset after it.
				t.Fatalf("cut %q ends with a color still on (line %q from %d width %d)", cut, text, from, width)
			}
		}
	}
}
