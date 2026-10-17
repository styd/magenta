// Package colorscheme parses colorscheme files and resolves scopes to styles.
// A colorscheme can carry a dark and a light palette. The theme (dark or light)
// says which one is used. With one palette only, it is used for both themes.
package colorscheme

import (
	"bytes"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/styd/magenta/internal/config"
	"github.com/styd/magenta/internal/render"
)

type styleDef struct {
	FG        string `toml:"fg"`
	BG        string `toml:"bg"`
	Bold      bool   `toml:"bold"`
	Dim       bool   `toml:"dim"`
	Italic    bool   `toml:"italic"`
	Underline bool   `toml:"underline"`
}

type file struct {
	Name    string                       `toml:"name"`
	Palette map[string]map[string]string `toml:"palette"`
	Styles  map[string]styleDef          `toml:"styles"`
}

// The two themes. A colorscheme has a palette for each, for one of them (used
// for both), or none at all.
const (
	Dark  = "dark"
	Light = "light"
)

// Themes lists the palettes a colorscheme file may have.
var Themes = []string{Dark, Light}

// Colorscheme maps scopes to styles, for one theme.
type Colorscheme struct {
	Name    string
	Theme   string
	Palette map[string]render.Color
	styles  map[string]render.Style
}

// Error is a problem in a colorscheme file, reported as path:line: message.
type Error struct {
	Path string
	Line int
	Msg  string
}

func (e *Error) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", e.Path, e.Line, e.Msg)
	}
	return fmt.Sprintf("%s: %s", e.Path, e.Msg)
}

// Parse reads a colorscheme file for the given theme (dark or light). path is
// only used in error messages.
func Parse(path string, data []byte, theme string) (*Colorscheme, error) {
	var f file
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return nil, &Error{path, config.ParseErrorLine(err), err.Error()}
	}
	if un := md.Undecoded(); len(un) > 0 {
		return nil, &Error{path, findLine(data, un[0].String()), "unknown key " + un[0].String()}
	}
	if f.Name == "" {
		return nil, &Error{path, 0, `missing "name"`}
	}
	for name := range f.Palette {
		if !slices.Contains(Themes, name) {
			return nil, &Error{path, findHeader(data, "palette."+name), fmt.Sprintf("palette %q: use palette.dark and palette.light", name)}
		}
	}
	// A colorscheme with the palette of one theme only uses it for both.
	palette, ok := f.Palette[theme]
	if !ok {
		for _, other := range Themes {
			if p, found := f.Palette[other]; found {
				palette = p
			}
		}
	}
	t := &Colorscheme{Name: f.Name, Theme: theme, Palette: map[string]render.Color{}, styles: map[string]render.Style{}}
	for k, v := range palette {
		c, err := render.ParseColor(v)
		if err != nil {
			return nil, &Error{path, findLine(data, k), fmt.Sprintf("palette %q: %v", k, err)}
		}
		t.Palette[k] = c
	}
	resolve := func(scope, s string) (render.Color, error) {
		if s == "" {
			return render.Color{}, nil
		}
		if c, ok := t.Palette[s]; ok {
			return c, nil
		}
		c, err := render.ParseColor(s)
		if err != nil {
			return c, fmt.Errorf("style %q: %w", scope, err)
		}
		return c, nil
	}
	for scope, d := range f.Styles {
		fg, err := resolve(scope, d.FG)
		if err != nil {
			return nil, &Error{path, findLine(data, scope), err.Error()}
		}
		bg, err := resolve(scope, d.BG)
		if err != nil {
			return nil, &Error{path, findLine(data, scope), err.Error()}
		}
		t.styles[scope] = render.Style{FG: fg, BG: bg, Bold: d.Bold, Dim: d.Dim, Italic: d.Italic, Underline: d.Underline}
	}
	return t, nil
}

// findHeader returns the 1-based line of the table header [name], or 0.
func findHeader(data []byte, name string) int {
	for i, l := range bytes.Split(data, []byte("\n")) {
		if bytes.HasPrefix(bytes.TrimSpace(l), []byte("["+name+"]")) {
			return i + 1
		}
	}
	return 0
}

// Check parses the file for every theme, so a mistake in either palette is found.
func Check(path string, data []byte) error {
	for _, theme := range Themes {
		if _, err := Parse(path, data, theme); err != nil {
			return err
		}
	}
	return nil
}

// findLine returns the 1-based line that mentions key, or 0.
func findLine(data []byte, key string) int {
	key = key[strings.LastIndex(key, ".")+1:]
	for i, l := range bytes.Split(data, []byte("\n")) {
		t := bytes.TrimSpace(l)
		if bytes.HasPrefix(t, []byte(key)) || bytes.HasPrefix(t, []byte(`"`+key+`"`)) {
			return i + 1
		}
	}
	// A key inside an inline table, such as { fg = "red", bolld = true }.
	for i, l := range bytes.Split(data, []byte("\n")) {
		if bytes.Contains(l, []byte(key+" =")) {
			return i + 1
		}
	}
	return 0
}

// Resolve finds the style for scope by longest-prefix match on dotted names:
// a colorscheme that defines "string" also styles "string.quoted".
func (t *Colorscheme) Resolve(scope string) (render.Style, bool) {
	for {
		if s, ok := t.styles[scope]; ok {
			return s, true
		}
		i := strings.LastIndex(scope, ".")
		if i < 0 {
			return render.Style{}, false
		}
		scope = scope[:i]
	}
}

// Scopes lists the scopes the colorscheme defines, sorted.
func (t *Colorscheme) Scopes() []string {
	out := make([]string, 0, len(t.styles))
	for k := range t.styles {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Starts precomputes the ANSI start sequence of every scope, indexed like
// scopes, so the hot loop needs no map lookups or formatting. Scopes the
// colorscheme does not style get "".
func (t *Colorscheme) Starts(d render.Depth, scopes []string) []string {
	out := make([]string, len(scopes))
	for i, sc := range scopes {
		if st, ok := t.Resolve(sc); ok {
			out[i] = st.Start(d)
		}
	}
	return out
}
