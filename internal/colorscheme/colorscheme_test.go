package colorscheme

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/styd/magenta"
	"github.com/styd/magenta/internal/render"
)

func TestResolveLongestPrefix(t *testing.T) {
	th, err := Parse("t.toml", []byte(`
name = "t"
[palette.dark]
red = "#ff0000"
[styles]
"string" = { fg = "green" }
"string.quoted" = { fg = "red", bold = true }
"level" = { dim = true }
`), Dark)
	if err != nil {
		t.Fatal(err)
	}
	green, _ := render.ParseColor("green")
	red, _ := render.ParseColor("#ff0000")
	tests := []struct {
		scope  string
		wantFG render.Color
		wantOK bool
	}{
		{"string", green, true},
		{"string.quoted", red, true},
		{"string.quoted.double", red, true},
		{"string.other", green, true},
		{"level.error", render.Color{}, true},
		{"strings", render.Color{}, false},
		{"number", render.Color{}, false},
	}
	for _, tt := range tests {
		st, ok := th.Resolve(tt.scope)
		if ok != tt.wantOK || st.FG != tt.wantFG {
			t.Errorf("Resolve(%q) = %+v, %v", tt.scope, st, ok)
		}
	}
}

func TestStarts(t *testing.T) {
	th, err := Parse("t.toml", []byte("name = \"t\"\n[styles]\n\"a\" = { fg = \"red\" }\n"), Dark)
	if err != nil {
		t.Fatal(err)
	}
	got := th.Starts(render.Basic16, []string{"a.b", "zzz", "a"})
	want := []string{"\x1b[31m", "", "\x1b[31m"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("starts[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name, src, want string
	}{
		{"syntax", "name = \"t\"\n[styles\nx = 1\n", "t.toml:2:"},
		{"no name", "[styles]\n", `t.toml: missing "name"`},
		{"bad color", "name = \"t\"\n[styles]\n\"a\" = { fg = \"nope\" }\n", `t.toml:3: style "a": unknown color "nope"`},
		{"bad palette", "name = \"t\"\n[palette.dark]\nx = \"#12\"\n", `t.toml:3: palette "x": invalid hex color "#12"`},
		{"unknown palette", "name = \"t\"\n[palette.blue]\nx = \"#123456\"\n", `t.toml:2: palette "blue": use palette.dark and palette.light`},
		{"unknown key", "name = \"t\"\n[styles]\n\"a\" = { fg = \"red\", bolld = true }\n", "t.toml:3: unknown key styles.a.bolld"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("t.toml", []byte(tt.src), Dark)
			var te *Error
			if !errors.As(err, &te) || !startsWith(err.Error(), tt.want) {
				t.Errorf("got %v, want prefix %q", err, tt.want)
			}
		})
	}
}

func startsWith(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

func luminance(c render.Color) float64 {
	r, g, b := c.RGB()
	lin := func(v uint8) float64 {
		f := float64(v) / 255
		if f <= 0.04045 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

func contrast(a, b float64) float64 {
	if a < b {
		a, b = b, a
	}
	return (a + 0.05) / (b + 0.05)
}

// roles are the colors every theme of the magenta colorscheme names.
var roles = []string{
	"background", "mantle", "surface0", "surface2", "text", "comment", "keyword", "operator",
	"symbol", "function", "string", "number", "type", "param", "error", "magenta",
}

func magentaTheme(t testing.TB, theme string) *Colorscheme {
	t.Helper()
	data, err := magenta.FS.ReadFile("colorschemes/magenta.toml")
	if err != nil {
		t.Fatal(err)
	}
	cs, err := Parse("magenta", data, theme)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func TestMagentaNamesEveryRoleInBothThemes(t *testing.T) {
	for _, theme := range Themes {
		cs := magentaTheme(t, theme)
		if len(cs.Palette) != len(roles) {
			t.Errorf("%s: %d colors, want %d", theme, len(cs.Palette), len(roles))
		}
		for _, r := range roles {
			if _, ok := cs.Palette[r]; !ok {
				t.Errorf("%s: no %s in the palette", theme, r)
			}
		}
	}
	if magentaTheme(t, Dark).Palette["background"] == magentaTheme(t, Light).Palette["background"] {
		t.Error("the dark and the light palette are the same")
	}
}

// TestContrast checks the text colors of the magenta colorscheme against the
// background of their theme: 4.5:1, and 4:1 for comments. It checks the roles,
// and the style of every scope, so a style cannot use a color that fails. The
// search bar is checked too, its text on surface0, and the text on surface2.
func TestContrast(t *testing.T) {
	ratio := func(fg, bg render.Color) float64 { return contrast(luminance(fg), luminance(bg)) }
	min := func(role string) float64 {
		if role == "comment" {
			return 4
		}
		return 4.5
	}
	for _, theme := range Themes {
		cs := magentaTheme(t, theme)
		bg := cs.Palette["background"]
		for _, r := range roles {
			switch r {
			case "background", "mantle", "surface0", "surface2":
				continue
			}
			if got := ratio(cs.Palette[r], bg); got < min(r) {
				t.Errorf("%s: %s on the background: %.2f < %.1f", theme, r, got, min(r))
			}
		}
		for _, sc := range cs.Scopes() {
			st, _ := cs.Resolve(sc)
			// A style with a background is a surface, and ui.border is a line,
			// which is meant to be quiet. Neither is text.
			if st.BG != (render.Color{}) || sc == "ui.border" {
				continue
			}
			want := 4.5
			if st.FG == cs.Palette["comment"] {
				want = 4
			}
			if got := ratio(st.FG, bg); got < want {
				t.Errorf("%s: scope %s: %.2f < %.1f", theme, sc, got, want)
			}
		}
		// The pager and the search sit on the background, in the text color.
		if st, ok := cs.Resolve("ui.background"); !ok || st.BG != cs.Palette["background"] || st.FG != cs.Palette["text"] {
			t.Errorf("%s: ui.background is %+v, want text on the background", theme, st)
		}
		// The marker and the hints of fzf are the magenta, and the line under the
		// cursor is the stronger surface.
		for _, sc := range []string{"ui.marker", "ui.hint"} {
			if st, ok := cs.Resolve(sc); !ok || st.FG != cs.Palette["magenta"] {
				t.Errorf("%s: %s is %+v, want the magenta", theme, sc, st)
			}
		}
		if st, ok := cs.Resolve("ui.current"); !ok || st.BG != cs.Palette["surface2"] || st.FG != cs.Palette["text"] {
			t.Errorf("%s: ui.current is %+v, want text on surface2", theme, st)
		}
		// The search bar, and the surface of a selection, hold their text.
		st, ok := cs.Resolve("ui.search")
		if !ok || st.BG != cs.Palette["surface0"] {
			t.Errorf("%s: ui.search is %+v, want a surface0 background", theme, st)
		}
		for _, surface := range []string{"surface0", "surface2"} {
			if got := ratio(cs.Palette["text"], cs.Palette[surface]); got < 4.5 {
				t.Errorf("%s: text on %s: %.2f < 4.5", theme, surface, got)
			}
		}
	}
}

// Every color of the magenta colorscheme has a nearest color on 256 and on 16
// color terminals. The conversion itself is tested in package render.
func TestMagentaFallsBackToNearestColors(t *testing.T) {
	for _, theme := range Themes {
		cs := magentaTheme(t, theme)
		for _, r := range roles {
			c := cs.Palette[r]
			if got := (render.Style{FG: c}).Start(render.Palette256); !strings.HasPrefix(got, "\x1b[38;5;") {
				t.Errorf("%s %s on 256 colors: %q", theme, r, got)
			}
			var n int
			fmt.Sscanf((render.Style{FG: c}).Start(render.Basic16), "\x1b[%dm", &n)
			if !(n >= 30 && n <= 37 || n >= 90 && n <= 97) {
				t.Errorf("%s %s on 16 colors: code %d", theme, r, n)
			}
		}
	}
}

func TestBuiltinColorschemesParse(t *testing.T) {
	for _, n := range []string{"magenta", "mono"} {
		data, err := magenta.FS.ReadFile("colorschemes/" + n + ".toml")
		if err != nil {
			t.Fatal(err)
		}
		if err := Check(n, data); err != nil {
			t.Fatal(err)
		}
		for _, theme := range Themes {
			cs, err := Parse(n, data, theme)
			if err != nil {
				t.Fatal(err)
			}
			if cs.Name != n || cs.Theme != theme {
				t.Errorf("%s: name %q, theme %q", n, cs.Name, cs.Theme)
			}
			// Every colorscheme must style the headline scopes.
			for _, sc := range []string{"key", "level.error", "diff.added", "timestamp", "stack.frame"} {
				if _, ok := cs.Resolve(sc); !ok && n != "mono" {
					t.Errorf("%s %s: no style for %s", n, theme, sc)
				}
			}
		}
	}
}

func TestThemesStyleTheSameScopes(t *testing.T) {
	if !slices.Equal(magentaTheme(t, Dark).Scopes(), magentaTheme(t, Light).Scopes()) {
		t.Error("the two themes style different scopes")
	}
}

// A colorscheme with the palette of one theme only uses it for both themes.
func TestOnePaletteServesBothThemes(t *testing.T) {
	src := []byte("name = \"t\"\n[palette.light]\nred = \"#c0305f\"\n[styles]\n\"a\" = { fg = \"red\" }\n")
	for _, theme := range Themes {
		cs, err := Parse("t.toml", src, theme)
		if err != nil {
			t.Fatalf("%s: %v", theme, err)
		}
		if st, ok := cs.Resolve("a"); !ok || st.FG != cs.Palette["red"] || cs.Theme != theme {
			t.Errorf("%s: %+v %v", theme, st, ok)
		}
	}
	if err := Check("t.toml", src); err != nil {
		t.Error(err)
	}
}
