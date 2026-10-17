package render

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDetectDepth(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want Depth
	}{
		{"truecolor", map[string]string{"COLORTERM": "truecolor", "TERM": "xterm"}, TrueColor},
		{"24bit", map[string]string{"COLORTERM": "24bit"}, TrueColor},
		{"256", map[string]string{"TERM": "xterm-256color"}, Palette256},
		{"plain xterm", map[string]string{"TERM": "xterm"}, Basic16},
		{"empty env", map[string]string{}, Basic16},
		{"NO_COLOR wins", map[string]string{"NO_COLOR": "1", "COLORTERM": "truecolor"}, None},
		{"NO_COLOR empty is ignored", map[string]string{"NO_COLOR": "", "TERM": "xterm-256color"}, Palette256},
		{"dumb", map[string]string{"TERM": "dumb", "COLORTERM": "truecolor"}, None},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectDepth(env(tt.env)); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseColor(t *testing.T) {
	tests := []struct {
		in      string
		want    Color
		wantErr bool
	}{
		{"#c026d3", Color{Kind: KindRGB, R: 0xc0, G: 0x26, B: 0xd3}, false},
		{"#fff", Color{Kind: KindRGB, R: 255, G: 255, B: 255}, false},
		{"141", Color{Kind: KindIndex, Index: 141}, false},
		{"magenta", Color{Kind: KindANSI, Index: 5}, false},
		{"bright_black", Color{Kind: KindANSI, Index: 8}, false},
		{"256", Color{}, true},
		{"#12345", Color{}, true},
		{"#gggggg", Color{}, true},
		{"chartreuse", Color{}, true},
	}
	for _, tt := range tests {
		got, err := ParseColor(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("ParseColor(%q) = %+v, %v", tt.in, got, err)
		}
	}
}

func TestStartDowngrade(t *testing.T) {
	rgb := func(h string) Color {
		c, err := ParseColor(h)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	tests := []struct {
		name  string
		style Style
		depth Depth
		want  string
	}{
		{"truecolor", Style{FG: rgb("#c026d3")}, TrueColor, "\x1b[38;2;192;38;211m"},
		{"256 cube", Style{FG: rgb("#c026d3")}, Palette256, "\x1b[38;5;128m"},
		{"16 magenta", Style{FG: rgb("#c026d3")}, Basic16, "\x1b[35m"},
		{"16 bright magenta", Style{FG: rgb("#e879f9")}, Basic16, "\x1b[95m"},
		{"16 sky blue to blue", Style{FG: rgb("#38bdf8")}, Basic16, "\x1b[34m"},
		{"16 teal to cyan", Style{FG: rgb("#14b8a6")}, Basic16, "\x1b[36m"},
		{"16 cyan", Style{FG: rgb("#22d3ee")}, Basic16, "\x1b[36m"},
		{"16 green", Style{FG: rgb("#22c55e")}, Basic16, "\x1b[32m"},
		{"16 yellow", Style{FG: rgb("#eab308")}, Basic16, "\x1b[33m"},
		{"16 orange to yellow", Style{FG: rgb("#fb923c")}, Basic16, "\x1b[33m"},
		{"16 rose to red", Style{FG: rgb("#f43f5e")}, Basic16, "\x1b[31m"},
		// The purples of the magenta colorscheme stay purple (ANSI magenta) on a
		// basic terminal. The light ones of the dark theme are the bright variant.
		{"16 purple, dark theme", Style{FG: rgb("#c3a6ff")}, Basic16, "\x1b[95m"},
		{"16 purple, light theme", Style{FG: rgb("#6b3fd4")}, Basic16, "\x1b[35m"},
		// The rose of the dark theme is pinkish, which must still be red.
		{"16 rose, dark theme", Style{FG: rgb("#f28fb0")}, Basic16, "\x1b[91m"},
		{"16 slate to bright black", Style{FG: rgb("#94a3b8")}, Basic16, "\x1b[90m"},
		{"attributes", Style{FG: rgb("red"), Bold: true, Dim: true, Italic: true, Underline: true}, Basic16, "\x1b[1;2;3;4;31m"},
		{"index kept at 256", Style{FG: rgb("141")}, Palette256, "\x1b[38;5;141m"},
		{"index to 16", Style{FG: rgb("141")}, Basic16, "\x1b[95m"},
		{"ansi name at truecolor", Style{FG: rgb("bright_red")}, TrueColor, "\x1b[91m"},
		{"background", Style{BG: rgb("#c026d3")}, TrueColor, "\x1b[48;2;192;38;211m"},
		{"gray ramp", Style{FG: rgb("#808080")}, Palette256, "\x1b[38;5;244m"},
		{"mono bold only", Style{Bold: true}, Basic16, "\x1b[1m"},
		{"empty style", Style{}, TrueColor, ""},
		{"color off", Style{FG: rgb("red"), Bold: true}, None, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.style.Start(tt.depth); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRows(t *testing.T) {
	tests := []struct {
		name string
		in   string
		cols int
		want int
	}{
		{"empty line is one row", "", 80, 1},
		{"short", "hello", 80, 1},
		{"exactly the width", strings.Repeat("x", 80), 80, 1},
		{"one over", strings.Repeat("x", 81), 80, 2},
		{"three rows", strings.Repeat("x", 45), 20, 3},
		{"color codes take no room", "\x1b[1;38;2;1;2;3m" + strings.Repeat("x", 20) + "\x1b[0m", 20, 1},
		{"a tab goes to the next stop", "a\tb", 80, 1},
		{"tabs can wrap", strings.Repeat("\t", 3), 20, 2}, // columns 0-8, 8-16, then 16-24 does not fit in 20
		{"wide characters take two", strings.Repeat("日", 11), 20, 2},
		{"combining marks take none", "ééé", 3, 1},
		{"no width given", strings.Repeat("x", 100), 0, 2},
	}
	for _, tt := range tests {
		if got := Rows([]byte(tt.in), tt.cols); got != tt.want {
			t.Errorf("%s: %d rows, want %d", tt.name, got, tt.want)
		}
	}
}

func TestHex(t *testing.T) {
	for in, want := range map[string]string{"#2d2547": "#2d2547", "#FBF3C8": "#fbf3c8", "": ""} {
		c := Color{}
		if in != "" {
			var err error
			if c, err = ParseColor(in); err != nil {
				t.Fatal(err)
			}
		}
		if got := c.Hex(); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
