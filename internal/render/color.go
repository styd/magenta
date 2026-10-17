// Package render turns styles into ANSI escape sequences and downgrades
// colors to what the terminal supports.
package render

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Depth is the number of colors the terminal can show.
type Depth int

const (
	None Depth = iota
	Basic16
	Palette256
	TrueColor
)

func (d Depth) String() string {
	switch d {
	case None:
		return "none"
	case Basic16:
		return "16"
	case Palette256:
		return "256"
	default:
		return "truecolor"
	}
}

// DetectDepth decides the color depth from the environment. Color does not
// depend on whether output is a terminal: it stays on when piped, with two
// exceptions, NO_COLOR set and non-empty, and TERM=dumb.
func DetectDepth(getenv func(string) string) Depth {
	term := getenv("TERM")
	if getenv("NO_COLOR") != "" || term == "dumb" {
		return None
	}
	ct := strings.ToLower(getenv("COLORTERM"))
	if ct == "truecolor" || ct == "24bit" || strings.Contains(term, "direct") {
		return TrueColor
	}
	if strings.Contains(term, "256color") {
		return Palette256
	}
	return Basic16
}

// Kind says how a Color is specified.
type Kind uint8

const (
	KindNone  Kind = iota // terminal default
	KindANSI              // Index 0..15
	KindIndex             // Index 0..255
	KindRGB
)

// Color is a foreground or background color.
type Color struct {
	Kind    Kind
	Index   uint8
	R, G, B uint8
}

var ansiNames = [...]string{"black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"}

// ParseColor accepts a hex color (#rrggbb or #rgb), a 256-color index
// ("0".."255"), or one of the 16 ANSI names (black, red, ... bright_white).
func ParseColor(s string) (Color, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "#") {
		h := s[1:]
		if len(h) == 3 {
			h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
		}
		if len(h) != 6 {
			return Color{}, fmt.Errorf("invalid hex color %q", s)
		}
		v, err := strconv.ParseUint(h, 16, 32)
		if err != nil {
			return Color{}, fmt.Errorf("invalid hex color %q", s)
		}
		return Color{Kind: KindRGB, R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v)}, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		if n < 0 || n > 255 {
			return Color{}, fmt.Errorf("color index %d out of range 0..255", n)
		}
		return Color{Kind: KindIndex, Index: uint8(n)}, nil
	}
	name := strings.ToLower(s)
	bright := 0
	if rest, ok := strings.CutPrefix(name, "bright_"); ok {
		name, bright = rest, 8
	}
	for i, n := range ansiNames {
		if n == name {
			return Color{Kind: KindANSI, Index: uint8(i + bright)}, nil
		}
	}
	return Color{}, fmt.Errorf("unknown color %q (use #rrggbb, 0-255, or an ANSI color name)", s)
}

// RGB returns the color's approximate RGB value.
// Hex is the color as #rrggbb, or "" for the terminal default.
func (c Color) Hex() string {
	if c.Kind == KindNone {
		return ""
	}
	r, g, b := c.RGB()
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

func (c Color) RGB() (r, g, b uint8) {
	switch c.Kind {
	case KindRGB:
		return c.R, c.G, c.B
	case KindANSI, KindIndex:
		return indexRGB(c.Index)
	}
	return 0, 0, 0
}

var ansi16RGB = [16][3]uint8{
	{0, 0, 0}, {205, 49, 49}, {13, 188, 121}, {229, 229, 16}, {36, 114, 200}, {188, 63, 188}, {17, 168, 205}, {229, 229, 229},
	{102, 102, 102}, {241, 76, 76}, {35, 209, 139}, {245, 245, 67}, {59, 142, 234}, {214, 112, 214}, {41, 184, 219}, {255, 255, 255},
}

func indexRGB(i uint8) (r, g, b uint8) {
	switch {
	case i < 16:
		c := ansi16RGB[i]
		return c[0], c[1], c[2]
	case i < 232:
		n := int(i) - 16
		lv := func(v int) uint8 {
			if v == 0 {
				return 0
			}
			return uint8(55 + 40*v)
		}
		return lv(n / 36), lv(n / 6 % 6), lv(n % 6)
	}
	v := uint8(8 + 10*(int(i)-232))
	return v, v, v
}

// to256 maps RGB onto the nearer of the xterm color cube and gray ramp.
func to256(r, g, b uint8) uint8 {
	levels := [6]int{0, 95, 135, 175, 215, 255}
	near := func(v uint8) int {
		best := 0
		for i, l := range levels {
			if abs(int(v)-l) < abs(int(v)-levels[best]) {
				best = i
			}
		}
		return best
	}
	ri, gi, bi := near(r), near(g), near(b)
	cube := uint8(16 + 36*ri + 6*gi + bi)
	gray := (int(r) + int(g) + int(b)) / 3
	gi2 := (gray - 8 + 5) / 10
	if gi2 < 0 {
		gi2 = 0
	} else if gi2 > 23 {
		gi2 = 23
	}
	gv := 8 + 10*gi2
	dist := func(r2, g2, b2 int) int {
		return sq(int(r)-r2) + sq(int(g)-g2) + sq(int(b)-b2)
	}
	if dist(gv, gv, gv) < dist(levels[ri], levels[gi], levels[bi]) {
		return uint8(232 + gi2)
	}
	return cube
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func sq(v int) int { return v * v }

// to16 maps RGB to the ANSI color a human would pick for the hue: magenta to
// magenta, sky blue to blue, teal and cyan to cyan, orange and yellow to
// yellow, low-saturation mid tones (slate) to bright black. Nearest-RGB
// matching is not used because it picks surprising colors for muted tones.
func to16(r, g, b uint8) uint8 {
	h, s, l := hsl(r, g, b)
	if s < 0.22 {
		switch {
		case l < 0.15:
			return 0
		case l < 0.75:
			return 8
		case l < 0.92:
			return 7
		}
		return 15
	}
	var base uint8
	switch {
	case h < 15 || h >= 335: // a pinkish red, such as 343 degrees, is still red
		base = 1
	case h < 70:
		base = 3
	case h < 165:
		base = 2
	case h < 195:
		base = 6
	case h < 255:
		base = 4
	default:
		base = 5
	}
	if l > 0.7 {
		return base + 8
	}
	return base
}

func hsl(r8, g8, b8 uint8) (h, s, l float64) {
	r, g, b := float64(r8)/255, float64(g8)/255, float64(b8)/255
	mx, mn := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	l = (mx + mn) / 2
	if mx == mn {
		return 0, 0, l
	}
	d := mx - mn
	if l > 0.5 {
		s = d / (2 - mx - mn)
	} else {
		s = d / (mx + mn)
	}
	switch mx {
	case r:
		h = (g - b) / d
		if g < b {
			h += 6
		}
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	return h * 60, s, l
}

// sgr returns the SGR parameter for c as a foreground (bg false) or background.
func (c Color) sgr(d Depth, bg bool) string {
	base := 30
	ext := "38"
	if bg {
		base, ext = 40, "48"
	}
	ansi := func(i uint8) string {
		if i < 8 {
			return strconv.Itoa(base + int(i))
		}
		return strconv.Itoa(base + 60 + int(i) - 8)
	}
	switch c.Kind {
	case KindANSI:
		return ansi(c.Index)
	case KindIndex:
		switch {
		case d == Palette256 || d == TrueColor:
			return ext + ";5;" + strconv.Itoa(int(c.Index))
		case c.Index < 16:
			return ansi(c.Index)
		}
		r, g, b := indexRGB(c.Index)
		return ansi(to16(r, g, b))
	case KindRGB:
		switch d {
		case TrueColor:
			return fmt.Sprintf("%s;2;%d;%d;%d", ext, c.R, c.G, c.B)
		case Palette256:
			return ext + ";5;" + strconv.Itoa(int(to256(c.R, c.G, c.B)))
		}
		return ansi(to16(c.R, c.G, c.B))
	}
	return ""
}

// Style is a color plus attributes.
type Style struct {
	FG, BG                       Color
	Bold, Dim, Italic, Underline bool
}

// Reset ends any style.
const Reset = "\x1b[0m"

// Start returns the escape sequence that begins the style at depth d, or ""
// when the style is empty or color is off.
func (s Style) Start(d Depth) string {
	if d == None {
		return ""
	}
	var parts []string
	if s.Bold {
		parts = append(parts, "1")
	}
	if s.Dim {
		parts = append(parts, "2")
	}
	if s.Italic {
		parts = append(parts, "3")
	}
	if s.Underline {
		parts = append(parts, "4")
	}
	if p := s.FG.sgr(d, false); p != "" {
		parts = append(parts, p)
	}
	if p := s.BG.sgr(d, true); p != "" {
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(parts, ";") + "m"
}
