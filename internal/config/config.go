// Package config locates the user's config directory and reads config.toml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Themes are the values of the theme setting. "auto" asks the terminal whether
// its background is dark or light. The colorscheme then shows its palette for
// that theme.
var Themes = []string{"auto", "dark", "light"}

// LegacyTheme reads a value of the "theme" setting from before colorschemes had
// their own setting, when "theme" named what is now a colorscheme: "mono",
// "magenta-dark" (the magenta colorscheme, dark theme), and so on. legacy is
// false for auto, dark, light, and for an empty value. theme is "" when the
// old value did not choose a theme.
func LegacyTheme(value string) (colorscheme, theme string, legacy bool) {
	switch {
	case value == "" || slices.Contains(Themes, value):
		return "", "", false
	case value == "magenta-dark":
		return "magenta", "dark", true
	case value == "magenta-light":
		return "magenta", "light", true
	}
	return value, "", true
}

// Keys are the fzf key bindings mgnt adds.
type Keys struct {
	PreviewToggle string `toml:"preview_toggle"`
	// Accept prints the selected lines. Enter opens the preview instead, so it
	// needs its own key, "enter" gives Enter back its usual meaning.
	Accept string `toml:"accept"`
}

// Config is the contents of config.toml.
type Config struct {
	Colorscheme   string `toml:"colorscheme"`
	Theme         string `toml:"theme"`
	Buffer        int    `toml:"buffer"`
	ANSI          string `toml:"ansi"`
	FzfOpts       string `toml:"fzf_opts"`
	Preview       bool   `toml:"preview"`
	PreviewWindow string `toml:"preview_window"`
	Keys          Keys   `toml:"keys"`

	// PreviewWindowSet and FzfOptsSet record that the user chose these, so
	// the fzf flags are not rewritten for old fzf versions.
	PreviewWindowSet bool `toml:"-"`
	FzfOptsSet       bool `toml:"-"`

	// Deprecated lists the settings in the file that still use an old spelling.
	// They work, and mgnt and magenta say what to write instead.
	Deprecated []string `toml:"-"`
}

// DefaultPreviewWindow is the fzf --preview-window used unless configured.
//
// The preview sits above the list at half the height, and starts hidden: Enter
// on a line opens it and Esc closes it again.
const DefaultPreviewWindow = "up,50%,wrap,border-bottom,hidden"

// Default returns the built-in configuration.
func Default() Config {
	return Config{
		Colorscheme:   "magenta",
		Theme:         "auto",
		Buffer:        100000,
		ANSI:          "strip",
		Preview:       true,
		PreviewWindow: DefaultPreviewWindow,
		Keys:          Keys{PreviewToggle: "ctrl-/", Accept: "ctrl-o"},
	}
}

// Dir returns $XDG_CONFIG_HOME/magenta, or ~/.config/magenta.
func Dir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "magenta")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "magenta")
	}
	return filepath.Join(home, ".config", "magenta")
}

// Path returns the path of config.toml.
func Path() string { return filepath.Join(Dir(), "config.toml") }

// Validate checks the values of c.
func (c Config) Validate() error {
	switch {
	case c.Colorscheme == "":
		return errors.New(`"colorscheme" must not be empty`)
	case !slices.Contains(Themes, c.Theme):
		return fmt.Errorf(`"theme" must be "auto", "dark", or "light", got %q`, c.Theme)
	case c.Buffer < 1:
		return fmt.Errorf(`"buffer" must be at least 1, got %d`, c.Buffer)
	case c.ANSI != "strip" && c.ANSI != "keep":
		return fmt.Errorf(`"ansi" must be "strip" or "keep", got %q`, c.ANSI)
	}
	return nil
}

// Load reads config.toml. A missing file gives the defaults.
func Load() (Config, error) { return LoadFile(Path()) }

// LoadFile reads the config file at path.
func LoadFile(path string) (Config, error) {
	c := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	md, err := toml.Decode(string(data), &c)
	if err != nil {
		if line := ParseErrorLine(err); line > 0 {
			return Default(), fmt.Errorf("%s:%d: %v", path, line, err)
		}
		return Default(), fmt.Errorf("%s: %w", path, err)
	}
	if un := md.Undecoded(); len(un) > 0 {
		return Default(), fmt.Errorf("%s: unknown setting %q", path, un[0].String())
	}
	c.PreviewWindowSet = md.IsDefined("preview_window")
	c.FzfOptsSet = md.IsDefined("fzf_opts")
	if scheme, theme, legacy := LegacyTheme(c.Theme); legacy {
		old := c.Theme
		if !md.IsDefined("colorscheme") {
			c.Colorscheme = scheme
		}
		c.Theme = Default().Theme
		use := fmt.Sprintf("colorscheme = %q", scheme)
		if theme != "" {
			c.Theme = theme
			use += fmt.Sprintf(" and theme = %q", theme)
		}
		c.Deprecated = append(c.Deprecated, fmt.Sprintf("%s: theme = %q is deprecated, use %s", path, old, use))
	}
	if err := c.Validate(); err != nil {
		return Default(), fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Apply sets one setting of c for this run only, from text such as a command
// line option gives. The values are checked like those in config.toml.
func (c *Config) Apply(key, value string) error {
	n := *c
	switch key {
	case "colorscheme":
		n.Colorscheme = value
	case "theme":
		n.Theme = value
	case "buffer":
		v, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf(`"buffer" must be a number, got %q`, value)
		}
		n.Buffer = v
	case "ansi":
		n.ANSI = value
	case "fzf_opts":
		n.FzfOpts, n.FzfOptsSet = value, true
	case "preview":
		v, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf(`"preview" must be true or false, got %q`, value)
		}
		n.Preview = v
	case "preview_window":
		n.PreviewWindow, n.PreviewWindowSet = value, true
	case "keys.preview_toggle":
		n.Keys.PreviewToggle = value
	case "keys.accept":
		n.Keys.Accept = value
	default:
		return unknownKey(key)
	}
	if err := n.Validate(); err != nil {
		return err
	}
	*c = n
	return nil
}

var settingKeys = []string{"colorscheme", "theme", "buffer", "ansi", "fzf_opts", "preview", "preview_window", "keys.preview_toggle", "keys.accept"}

// SettingKeys returns the names accepted by Get and Set.
func SettingKeys() []string { return append([]string(nil), settingKeys...) }

// Get returns one setting as text.
func (c Config) Get(key string) (string, error) {
	switch key {
	case "colorscheme":
		return c.Colorscheme, nil
	case "theme":
		return c.Theme, nil
	case "buffer":
		return strconv.Itoa(c.Buffer), nil
	case "ansi":
		return c.ANSI, nil
	case "fzf_opts":
		return c.FzfOpts, nil
	case "preview":
		return strconv.FormatBool(c.Preview), nil
	case "preview_window":
		return c.PreviewWindow, nil
	case "keys.preview_toggle":
		return c.Keys.PreviewToggle, nil
	case "keys.accept":
		return c.Keys.Accept, nil
	}
	return "", unknownKey(key)
}

func unknownKey(key string) error {
	return fmt.Errorf("unknown setting %q (known: %s)", key, strings.Join(settingKeys, ", "))
}

// Set changes one setting in config.toml, creating the file if needed. The
// rest of the file, comments included, is left as it is.
func Set(key, value string) error { return SetFile(Path(), key, value) }

// SetFile changes one setting in the config file at path.
func SetFile(path, key, value string) error {
	var lit string // the value as TOML
	switch key {
	case "buffer":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf(`"buffer" must be a number, got %q`, value)
		}
		lit = strconv.Itoa(n)
	case "theme":
		if !slices.Contains(Themes, value) {
			return fmt.Errorf(`"theme" must be "auto", "dark", or "light", got %q`, value)
		}
		lit = tomlString(value)
	case "preview":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf(`"preview" must be true or false, got %q`, value)
		}
		lit = strconv.FormatBool(b)
	case "colorscheme", "ansi", "fzf_opts", "preview_window", "keys.preview_toggle", "keys.accept":
		lit = tomlString(value)
	default:
		return unknownKey(key)
	}
	var src string
	if data, err := os.ReadFile(path); err == nil {
		src = string(data)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	table, name := "", key
	if t, n, ok := strings.Cut(key, "."); ok {
		table, name = t, n
	}
	out := setKey(src, table, name, lit)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o644); err != nil {
		return err
	}
	// Validate what is about to be installed, so a bad value never lands.
	if _, err := LoadFile(tmp); err != nil {
		os.Remove(tmp)
		return errors.New(strings.Replace(err.Error(), tmp, path, 1))
	}
	return os.Rename(tmp, path)
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\u%04x", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// setKey sets `name = lit` in the given table ("" for the top level) of a
// TOML source, replacing the key's line, or a commented-out `# name = ...`
// placeholder, and otherwise adding a line at the end of that table.
func setKey(src, table, name, lit string) string {
	lines := strings.Split(strings.TrimSuffix(src, "\n"), "\n")
	if src == "" {
		lines = nil
	}
	assign := name + " = " + lit
	isKey := func(l, prefix string) bool {
		t := strings.TrimSpace(l)
		if p, ok := strings.CutPrefix(t, "#"); ok && prefix != "" {
			t = strings.TrimSpace(p)
		} else if prefix != "" {
			return false
		}
		rest, ok := strings.CutPrefix(t, name)
		return ok && strings.HasPrefix(strings.TrimSpace(rest), "=")
	}
	cur := ""      // table we are in
	tableEnd := -1 // index after the last line of the wanted table
	tableFound := table == ""
	placeholder := -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") && !strings.HasPrefix(t, "[[") {
			cur = strings.Trim(strings.TrimSpace(strings.SplitN(t, "#", 2)[0]), "[] ")
			if cur == table {
				tableFound = true
			}
			continue
		}
		if cur != table {
			continue
		}
		if isKey(l, "") {
			lines[i] = assign
			return strings.Join(lines, "\n") + "\n"
		}
		if placeholder < 0 && isKey(l, "#") {
			placeholder = i
		}
		if t != "" && !strings.HasPrefix(t, "#") {
			tableEnd = i + 1
		}
	}
	if placeholder >= 0 {
		lines[placeholder] = assign
		return strings.Join(lines, "\n") + "\n"
	}
	switch {
	case table == "":
		// Top-level keys must come before the first table.
		first := len(lines)
		for i, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "[") {
				first = i
				break
			}
		}
		at := first
		for at > 0 && strings.TrimSpace(lines[at-1]) == "" {
			at--
		}
		if first == len(lines) {
			lines = append(lines, assign)
		} else {
			lines = append(lines[:at], append([]string{assign}, lines[at:]...)...)
		}
	case tableFound:
		if tableEnd < 0 {
			for i, l := range lines {
				if strings.Trim(strings.TrimSpace(l), "[] ") == table && strings.HasPrefix(strings.TrimSpace(l), "[") {
					tableEnd = i + 1
				}
			}
		}
		lines = append(lines[:tableEnd], append([]string{assign}, lines[tableEnd:]...)...)
	default:
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, "["+table+"]", assign)
	}
	return strings.Join(lines, "\n") + "\n"
}
