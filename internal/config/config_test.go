package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x/cfg")
	if got := Dir(); got != "/x/cfg/magenta" {
		t.Errorf("got %q", got)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/u")
	if got := Dir(); got != "/home/u/.config/magenta" {
		t.Errorf("got %q", got)
	}
}

func TestLoadDefaultsWhenMissing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Colorscheme != "magenta" || c.Theme != "auto" || c.Buffer != 100000 || c.ANSI != "strip" || !c.Preview ||
		c.PreviewWindow != DefaultPreviewWindow || c.Keys.PreviewToggle != "ctrl-/" || c.Keys.Accept != "ctrl-o" || c.PreviewWindowSet || c.FzfOptsSet {
		t.Errorf("unexpected defaults: %+v", c)
	}
}

func TestLoadValues(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	write(t, p, "colorscheme = \"mono\"\nbuffer = 50\nansi = \"keep\"\nfzf_opts = \"--height 40%\"\npreview = false\npreview_window = \"right\"\n[keys]\npreview_toggle = \"alt-p\"\n")
	c, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Colorscheme != "mono" || c.Buffer != 50 || c.ANSI != "keep" || c.FzfOpts != "--height 40%" || c.Preview ||
		c.PreviewWindow != "right" || c.Keys.PreviewToggle != "alt-p" || !c.PreviewWindowSet || !c.FzfOptsSet {
		t.Errorf("got %+v", c)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"bad ansi", "ansi = \"x\"\n", `"ansi" must be "strip" or "keep"`},
		{"bad buffer", "buffer = 0\n", `"buffer" must be at least 1`},
		{"unknown", "color = 1\n", `unknown setting "color"`},
		{"syntax", "colorscheme = \"a\"\nbuffer = \n", "config.toml:2:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.toml")
			write(t, p, tt.src)
			_, err := LoadFile(p)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("got %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSetAndGet(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.toml")
	for _, kv := range [][2]string{{"colorscheme", "mono"}, {"buffer", "250"}, {"preview", "false"}, {"keys.preview_toggle", "alt-p"}, {"ansi", "keep"}} {
		if err := SetFile(p, kv[0], kv[1]); err != nil {
			t.Fatalf("set %s: %v", kv[0], err)
		}
	}
	c, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"colorscheme": "mono", "buffer": "250", "preview": "false", "keys.preview_toggle": "alt-p", "ansi": "keep"} {
		if got, err := c.Get(key); err != nil || got != want {
			t.Errorf("Get(%s) = %q, %v, want %q", key, got, err, want)
		}
	}
}

func TestSetRejectsBadValuesAndKeepsFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	write(t, p, "colorscheme = \"mono\"\n")
	for _, kv := range [][2]string{{"ansi", "bogus"}, {"buffer", "abc"}, {"buffer", "0"}, {"preview", "maybe"}, {"color", "1"}} {
		if err := SetFile(p, kv[0], kv[1]); err == nil {
			t.Errorf("set %s=%s: want error", kv[0], kv[1])
		}
	}
	data, _ := os.ReadFile(p)
	if strings.TrimSpace(string(data)) != `colorscheme = "mono"` {
		t.Errorf("file changed: %q", data)
	}
	if _, err := os.Stat(p + ".tmp"); err == nil {
		t.Error("temp file left behind")
	}
}

func TestFindAndListOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f, err := Find(Colorschemes, "mono")
	if err != nil || !f.Builtin || f.Override {
		t.Fatalf("builtin: %+v, %v", f, err)
	}
	// Same name under a different file name still overrides.
	write(t, filepath.Join(UserDir(Colorschemes), "my-mono.toml"), "name = \"mono\"\n# mine\n")
	write(t, filepath.Join(UserDir(Colorschemes), "extra.toml"), "name = \"extra\"\n")
	f, err = Find(Colorschemes, "mono")
	if err != nil || f.Builtin || !f.Override || !strings.HasSuffix(f.Path, "my-mono.toml") {
		t.Fatalf("override: %+v, %v", f, err)
	}
	if f, err = Find(Colorschemes, "extra"); err != nil || f.Override {
		t.Fatalf("new name: %+v, %v", f, err)
	}
	if _, err = Find(Colorschemes, "nope"); err == nil {
		t.Error("want error for unknown colorscheme")
	}
	list, err := List(Colorschemes)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range list {
		names = append(names, f.Name)
	}
	if got := strings.Join(names, ","); got != "extra,magenta,mono" {
		t.Errorf("got %s", got)
	}
}

func TestSetKeepsCommentsAndOtherSettings(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	write(t, p, `# my notes
colorscheme = "magenta"   # the look
buffer = 100000
# fzf_opts = ""
# preview = true

# [keys]
# preview_toggle = "ctrl-/"
`)
	for _, kv := range [][2]string{{"colorscheme", "mono"}, {"fzf_opts", "--height 40%"}, {"keys.preview_toggle", "alt-p"}, {"keys.accept", "enter"}, {"preview", "false"}, {"ansi", "keep"}} {
		if err := SetFile(p, kv[0], kv[1]); err != nil {
			t.Fatalf("set %s: %v", kv[0], err)
		}
	}
	data, _ := os.ReadFile(p)
	got := string(data)
	for _, want := range []string{"# my notes", "colorscheme = \"mono\"", "buffer = 100000", "fzf_opts = \"--height 40%\"", "preview = false", "ansi = \"keep\"", "[keys]", "preview_toggle = \"alt-p\""} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// Setting a key in a table that already exists must not create a second one.
	if n := strings.Count(got, "\n[keys]\n"); n != 1 {
		t.Errorf("%d [keys] tables:\n%s", n, got)
	}
	c, err := LoadFile(p)
	if err != nil || c.Colorscheme != "mono" || c.Keys.PreviewToggle != "alt-p" || c.Keys.Accept != "enter" || c.Preview || c.ANSI != "keep" || c.FzfOpts != "--height 40%" || c.Buffer != 100000 {
		t.Errorf("%+v, %v", c, err)
	}
}

func TestSetTopLevelKeyGoesBeforeTables(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	write(t, p, "[keys]\npreview_toggle = \"alt-p\"\n")
	if err := SetFile(p, "colorscheme", "mono"); err != nil {
		t.Fatal(err)
	}
	c, err := LoadFile(p)
	if err != nil || c.Colorscheme != "mono" || c.Keys.PreviewToggle != "alt-p" {
		t.Errorf("%+v, %v", c, err)
	}
}

func TestSetQuotesValues(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	val := `--prompt "x> " --color 'a\b'`
	if err := SetFile(p, "fzf_opts", val); err != nil {
		t.Fatal(err)
	}
	c, err := LoadFile(p)
	if err != nil || c.FzfOpts != val {
		t.Errorf("%q, %v", c.FzfOpts, err)
	}
}

func TestAcceptKeySetting(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if c, _ := LoadFile(p); c.Keys.Accept != "ctrl-o" {
		t.Errorf("default accept key %q", c.Keys.Accept)
	}
	if err := SetFile(p, "keys.accept", "enter"); err != nil {
		t.Fatal(err)
	}
	c, err := LoadFile(p)
	if err != nil || c.Keys.Accept != "enter" || c.Keys.PreviewToggle != "ctrl-/" {
		t.Fatalf("%+v %v", c, err)
	}
	if got, err := c.Get("keys.accept"); err != nil || got != "enter" {
		t.Errorf("Get: %q %v", got, err)
	}
	// Setting one key of [keys] keeps the other.
	if err := SetFile(p, "keys.preview_toggle", "alt-p"); err != nil {
		t.Fatal(err)
	}
	if c, _ := LoadFile(p); c.Keys.Accept != "enter" || c.Keys.PreviewToggle != "alt-p" {
		t.Errorf("%+v", c.Keys)
	}
}

func TestApply(t *testing.T) {
	c := Default()
	for key, value := range map[string]string{
		"colorscheme": "mono", "theme": "light", "buffer": "5000", "ansi": "keep", "fzf_opts": "--exact", "preview": "false",
		"preview_window": "right,40%", "keys.preview_toggle": "alt-p", "keys.accept": "enter",
	} {
		if err := c.Apply(key, value); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if got, _ := c.Get(key); got != value {
			t.Errorf("%s: got %q, want %q", key, got, value)
		}
	}
	if !c.PreviewWindowSet || !c.FzfOptsSet {
		t.Error("a chosen preview_window or fzf_opts must be marked as the user's choice")
	}

	// A bad value changes nothing.
	before := c
	for key, value := range map[string]string{"buffer": "x", "buffer ": "5", "ansi": "no", "preview": "maybe", "colorscheme": "", "theme": "blue"} {
		if err := c.Apply(key, value); err == nil {
			t.Errorf("%s=%q should be refused", key, value)
		}
	}
	if !reflect.DeepEqual(c, before) {
		t.Errorf("a refused value changed the config: %+v", c)
	}
}

func TestSetTheme(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	for _, theme := range []string{"auto", "dark", "light"} {
		if err := SetFile(p, "theme", theme); err != nil {
			t.Errorf("%s: %v", theme, err)
		}
	}
	if err := SetFile(p, "theme", "magenta-dark"); err == nil || !strings.Contains(err.Error(), `"auto", "dark", or "light"`) {
		t.Errorf("a colorscheme name is not a theme: %v", err)
	}
	if c, err := LoadFile(p); err != nil || c.Theme != "light" {
		t.Errorf("theme %q, %v", c.Theme, err)
	}
}

func TestLegacyThemeValues(t *testing.T) {
	tests := []struct {
		value, scheme, theme string
		legacy               bool
	}{
		{"magenta-dark", "magenta", "dark", true},
		{"magenta-light", "magenta", "light", true},
		{"magenta", "magenta", "", true},
		{"mono", "mono", "", true},
		{"mine", "mine", "", true},
		{"auto", "", "", false},
		{"dark", "", "", false},
		{"light", "", "", false},
		{"", "", "", false},
	}
	for _, tt := range tests {
		scheme, theme, legacy := LegacyTheme(tt.value)
		if scheme != tt.scheme || theme != tt.theme || legacy != tt.legacy {
			t.Errorf("%q: %q %q %v", tt.value, scheme, theme, legacy)
		}
	}
}

// A config.toml from before colorschemes had their own setting still works.
func TestOldThemeSettingStillWorks(t *testing.T) {
	tests := []struct {
		src, scheme, theme string
	}{
		{`theme = "mono"`, "mono", "auto"},
		{`theme = "magenta"`, "magenta", "auto"},
		{`theme = "magenta-dark"`, "magenta", "dark"},
		{`theme = "magenta-light"`, "magenta", "light"},
		{"colorscheme = \"mono\"\ntheme = \"magenta-light\"", "mono", "light"},
		{`theme = "light"`, "magenta", "light"},
	}
	for _, tt := range tests {
		p := filepath.Join(t.TempDir(), "config.toml")
		write(t, p, tt.src+"\n")
		c, err := LoadFile(p)
		if err != nil || c.Colorscheme != tt.scheme || c.Theme != tt.theme {
			t.Errorf("%q: %q %q %v", tt.src, c.Colorscheme, c.Theme, err)
		}
		if got := len(c.Deprecated) == 1; got != strings.Contains(tt.src, `theme = "m`) {
			t.Errorf("%q: deprecated notices %q", tt.src, c.Deprecated)
		}
	}
	p := filepath.Join(t.TempDir(), "config.toml")
	write(t, p, "theme = \"magenta-dark\"\n")
	c, _ := LoadFile(p)
	if len(c.Deprecated) != 1 || !strings.Contains(c.Deprecated[0], `theme = "magenta-dark" is deprecated, use colorscheme = "magenta" and theme = "dark"`) {
		t.Errorf("%q", c.Deprecated)
	}
}
