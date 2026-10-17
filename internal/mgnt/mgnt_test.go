package mgnt

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/styd/magenta/internal/render"
)

type result struct {
	code           int
	stdout, stderr string
}

type opts struct {
	env      map[string]string
	stdinTTY bool
	stdin    io.Reader
}

func run(t *testing.T, o opts, args ...string) result {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	in := o.stdin
	if in == nil {
		in = strings.NewReader("")
	}
	env := Env{
		Stdin: in, Stdout: &stdout, Stderr: &stderr,
		Getenv:     func(k string) string { return o.env[k] },
		StdinIsTTY: o.stdinTTY,
		LookPath:   func(string) (string, error) { return "", exec.ErrNotFound },
	}
	code := Run(env, args)
	return result{code, stdout.String(), stderr.String()}
}

// failReader fails the test if anything reads from it.
type failReader struct{ t *testing.T }

func (f failReader) Read([]byte) (int, error) {
	f.t.Error("stdin was read")
	return 0, io.EOF
}

func TestUnknownFlag(t *testing.T) {
	r := run(t, opts{}, "--frobnicate")
	if r.code != 2 || !strings.Contains(r.stderr, "unknown flag: --frobnicate") || !strings.Contains(r.stderr, "mgnt --help") {
		t.Errorf("%+v", r)
	}
}

func TestTerminalStdinWithoutFileShowsUsage(t *testing.T) {
	r := run(t, opts{stdinTTY: true, stdin: failReader{t}})
	want := "usage: mgnt [file...]\npipe a log into mgnt, or give it a file (try \"mgnt --help\")\n"
	if r.code != 2 || r.stderr != want || r.stdout != "" {
		t.Errorf("%+v", r)
	}
}

func TestAdminWords(t *testing.T) {
	for _, w := range []string{"init", "rule", "colorscheme", "config", "doctor"} {
		r := run(t, opts{stdinTTY: true}, w)
		want := `mgnt: "` + w + `" is a magenta command. Try: magenta ` + w + " ...\n"
		if r.code != 2 || r.stderr != want {
			t.Errorf("%s: %+v", w, r)
		}
	}
	// An existing file of that name wins.
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("rule", []byte("hello 12\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := run(t, opts{}, "rule")
	if r.code != 0 || !strings.Contains(r.stdout, "hello") {
		t.Errorf("file called rule: %+v", r)
	}
	// Admin word with more arguments is a file name like any other.
	r = run(t, opts{}, "rule", "nope.log")
	if r.code != 1 || !strings.Contains(r.stderr, "nope.log") {
		t.Errorf("%+v", r)
	}
}

func TestHelpAndVersion(t *testing.T) {
	r := run(t, opts{stdinTTY: true}, "--help")
	for _, s := range []string{"Usage: mgnt", "--no-interactive", "--version", "pages long output", "scroll", "full-screen pager"} {
		if r.code != 0 || !strings.Contains(r.stdout, s) {
			t.Errorf("help lacks %q: %+v", s, r)
		}
	}
	r = run(t, opts{stdinTTY: true}, "--version")
	if r.code != 0 || !strings.HasPrefix(r.stdout, "mgnt ") {
		t.Errorf("%+v", r)
	}
}

const unicornLine = "I, [2026-10-06T10:00:00.123 #4242]  INFO -- : worker=0 ready\n"

func TestColorPolicy(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want render.Depth
	}{
		{"piped, truecolor", map[string]string{"COLORTERM": "truecolor"}, render.TrueColor},
		{"piped, 256", map[string]string{"TERM": "xterm-256color"}, render.Palette256},
		{"piped, 16", map[string]string{"TERM": "xterm"}, render.Basic16},
		{"piped, nothing set", nil, render.Basic16},
		{"NO_COLOR", map[string]string{"NO_COLOR": "1", "COLORTERM": "truecolor"}, render.None},
		{"dumb", map[string]string{"TERM": "dumb"}, render.None},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// run() gives mgnt a non-terminal stdout, as when piped.
			r := run(t, opts{env: tt.env, stdin: strings.NewReader(strings.Repeat(unicornLine, 3))})
			if r.code != 0 || r.stderr != "" {
				t.Fatalf("%+v", r)
			}
			if tt.want == render.None {
				if r.stdout != strings.Repeat(unicornLine, 3) {
					t.Errorf("color should be off: %q", r.stdout)
				}
				return
			}
			marker := map[render.Depth]string{render.TrueColor: "\x1b[38;2;", render.Palette256: "\x1b[38;5;", render.Basic16: "\x1b[9"}[tt.want]
			if !strings.Contains(r.stdout, marker) {
				t.Errorf("want %q in %q", marker, r.stdout)
			}
			if tt.want == render.Basic16 && (strings.Contains(r.stdout, ";2;") || strings.Contains(r.stdout, ";5;")) {
				t.Errorf("16 color output uses extended colors: %q", r.stdout)
			}
			if got := string(render.StripSGR(nil, []byte(r.stdout))); got != strings.Repeat(unicornLine, 3) {
				t.Errorf("text changed: %q", got)
			}
		})
	}
}

func TestFilesAndMissingFile(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "unicorn.log")
	b := filepath.Join(dir, "b.txt")
	os.WriteFile(a, []byte(unicornLine), 0o644)
	os.WriteFile(b, []byte("second 5\n"), 0o644)
	r := run(t, opts{env: map[string]string{"NO_COLOR": "1"}}, a, filepath.Join(dir, "missing.log"), b)
	if r.code != 1 || r.stdout != unicornLine+"second 5\n" || !strings.Contains(r.stderr, "missing.log") {
		t.Errorf("%+v", r)
	}
	// The file name alone selects the rule (no unicorn line inside).
	os.WriteFile(a, []byte("plain text 5\n"), 0o644)
	r = run(t, opts{env: map[string]string{"COLORTERM": "truecolor"}}, a)
	if !strings.Contains(r.stdout, "\x1b[") || string(render.StripSGR(nil, []byte(r.stdout))) != "plain text 5\n" {
		t.Errorf("%+v", r)
	}
}

type epipeWriter struct{ n int }

func (w *epipeWriter) Write(p []byte) (int, error) {
	w.n++
	return 0, &os.PathError{Op: "write", Path: "|1", Err: syscall.EPIPE}
}

func TestClosedPipeExitsQuietly(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stderr bytes.Buffer
	w := &epipeWriter{}
	env := Env{Stdin: strings.NewReader(strings.Repeat(unicornLine, 50000)), Stdout: w, Stderr: &stderr,
		Getenv: func(string) string { return "" }, LookPath: func(string) (string, error) { return "", exec.ErrNotFound }}
	if code := Run(env, nil); code != 0 || stderr.Len() != 0 {
		t.Errorf("code %d stderr %q", code, stderr.String())
	}
	if w.n == 0 {
		t.Error("never wrote")
	}
}

func TestWriteErrorIsReported(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stderr bytes.Buffer
	env := Env{Stdin: strings.NewReader("x\n"), Stdout: errWriter{errors.New("disk full")}, Stderr: &stderr,
		Getenv: func(string) string { return "" }, LookPath: func(string) (string, error) { return "", exec.ErrNotFound }}
	if code := Run(env, nil); code != 1 || !strings.Contains(stderr.String(), "disk full") {
		t.Errorf("code %d stderr %q", code, stderr.String())
	}
}

type errWriter struct{ err error }

func (w errWriter) Write([]byte) (int, error) { return 0, w.err }

// chanWriter reports each write on a channel.
type chanWriter struct{ c chan string }

func (w chanWriter) Write(p []byte) (int, error) { w.c <- string(p); return len(p), nil }

// TestFlushesWhenInputDrains: a line must reach stdout while stdin is still
// open and idle, not only at EOF.
func TestFlushesWhenInputDrains(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	pr, pw := io.Pipe()
	out := chanWriter{make(chan string, 16)}
	env := Env{Stdin: pr, Stdout: out, Stderr: io.Discard, Getenv: func(k string) string { return map[string]string{"NO_COLOR": "1"}[k] },
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound }}
	done := make(chan int, 1)
	go func() { done <- Run(env, nil) }()

	io.WriteString(pw, "first line\n")
	select {
	case got := <-out.c:
		if got != "first line\n" {
			t.Errorf("got %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first line not flushed while stdin stays open")
	}
	io.WriteString(pw, "second line\n")
	select {
	case got := <-out.c:
		if got != "second line\n" {
			t.Errorf("got %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second line not flushed")
	}
	pw.Close()
	if code := <-done; code != 0 {
		t.Errorf("code %d", code)
	}
}

func TestBrokenConfigAndColorschemeFallBack(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	os.MkdirAll(filepath.Join(cfgDir, "magenta"), 0o755)
	os.WriteFile(filepath.Join(cfgDir, "magenta", "config.toml"), []byte("ansi = \"nope\"\n"), 0o644)
	var stdout, stderr bytes.Buffer
	env := Env{Stdin: strings.NewReader("hello 5\n"), Stdout: &stdout, Stderr: &stderr, Getenv: func(string) string { return "" },
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound }}
	if code := Run(env, nil); code != 0 || stdout.String() != "hello 5\n" && !strings.Contains(stdout.String(), "hello") {
		t.Fatalf("code %d out %q", code, stdout.String())
	}
	if !strings.Contains(stderr.String(), `warning:`) || !strings.Contains(stderr.String(), `"ansi" must be`) {
		t.Errorf("stderr %q", stderr.String())
	}

	os.WriteFile(filepath.Join(cfgDir, "magenta", "config.toml"), []byte("colorscheme = \"does-not-exist\"\n"), 0o644)
	stdout.Reset()
	stderr.Reset()
	env.Stdin = strings.NewReader("hello 5\n")
	if code := Run(env, nil); code != 0 || !strings.Contains(stderr.String(), "does-not-exist") || !strings.Contains(stdout.String(), "hello") {
		t.Errorf("code %d stderr %q", code, stderr.String())
	}
}

func TestUserRuleOverridesBuiltin(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	rulesDir := filepath.Join(cfgDir, "magenta", "rules")
	os.MkdirAll(rulesDir, 0o755)
	os.WriteFile(filepath.Join(rulesDir, "mine.toml"), []byte(`name = "mine"
detect = ['^MINE ']
[[context.default.rule]]
regex = 'MINE'
scope = "level.error"
`), 0o644)
	var stdout bytes.Buffer
	env := Env{Stdin: strings.NewReader("MINE one\nMINE two\n"), Stdout: &stdout, Stderr: io.Discard,
		Getenv:   func(k string) string { return map[string]string{"COLORTERM": "truecolor"}[k] },
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound }}
	if code := Run(env, nil); code != 0 || !strings.HasPrefix(stdout.String(), "\x1b[1;38;2;") {
		t.Errorf("code %d out %q", code, stdout.String())
	}
}

func TestAutoThemeInAPlainFilter(t *testing.T) {
	in := strings.Repeat(unicornLine, 3)
	pinned := func(theme string) string {
		cfg := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", cfg)
		os.MkdirAll(filepath.Join(cfg, "magenta"), 0o755)
		os.WriteFile(filepath.Join(cfg, "magenta", "config.toml"), []byte("theme = \""+theme+"\"\n"), 0o644)
		return runKeepingConfig(t, map[string]string{"COLORTERM": "truecolor"}, in)
	}
	dark, light := pinned("dark"), pinned("light")
	if dark == light {
		t.Fatal("the two themes should look different")
	}
	// The default is auto. A plain filter never asks the terminal, so it goes by
	// the environment and otherwise assumes dark.
	for _, tt := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"nothing known", nil, dark},
		{"COLORFGBG light", map[string]string{"COLORFGBG": "0;15"}, light},
		{"COLORFGBG dark", map[string]string{"COLORFGBG": "15;0"}, dark},
		{"override", map[string]string{"MGNT_BACKGROUND": "light", "COLORFGBG": "15;0"}, light},
	} {
		env := map[string]string{"COLORTERM": "truecolor"}
		for k, v := range tt.env {
			env[k] = v
		}
		if got := run(t, opts{env: env, stdin: strings.NewReader(in)}); got.stdout != tt.want || got.stderr != "" {
			t.Errorf("%s: got %q", tt.name, got.stdout)
		}
	}
}

// runKeepingConfig is run without replacing XDG_CONFIG_HOME.
func runKeepingConfig(t *testing.T, env map[string]string, in string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	e := Env{
		Stdin: strings.NewReader(in), Stdout: &stdout, Stderr: &stderr,
		Getenv:   func(k string) string { return env[k] },
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
	}
	if code := Run(e, nil); code != 0 || stderr.Len() != 0 {
		t.Fatalf("code %d stderr %q", code, stderr.String())
	}
	return stdout.String()
}

func TestOptionsOverrideTheConfigForOneRun(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	os.MkdirAll(filepath.Join(cfg, "magenta"), 0o755)
	file := filepath.Join(cfg, "magenta", "config.toml")
	body := "theme = \"dark\"\nansi = \"strip\"\n"
	os.WriteFile(file, []byte(body), 0o644)
	in := strings.Repeat(unicornLine, 3)
	color := map[string]string{"COLORTERM": "truecolor"}
	do := func(stdin string, args ...string) result {
		var stdout, stderr bytes.Buffer
		env := Env{
			Stdin: strings.NewReader(stdin), Stdout: &stdout, Stderr: &stderr,
			Getenv:   func(k string) string { return color[k] },
			LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
		}
		return result{Run(env, args), stdout.String(), stderr.String()}
	}

	dark := do(in)
	light := do(in, "--theme", "light")
	viaSet := do(in, "--set", "theme=light")
	if dark.stdout == light.stdout || light.stdout != viaSet.stdout || light.code != 0 || light.stderr != "" {
		t.Errorf("--theme and --set theme=... should agree and differ from the file's theme:\n%q\n%q\n%q", dark.stdout, light.stdout, viaSet.stdout)
	}
	// --set wins over the dedicated option, and the file is never written.
	if got := do(in, "--theme", "dark", "--set", "theme=light"); got.stdout != light.stdout {
		t.Errorf("--set should come last: %q", got.stdout)
	}
	// --ansi keep passes colors already in the input through.
	colored := "\x1b[31mred\x1b[0m plain\n"
	if got := do(colored, "--ansi", "keep"); !strings.Contains(got.stdout, "\x1b[31mred") {
		t.Errorf("--ansi keep: %q", got.stdout)
	}
	if got := do(colored); strings.Contains(got.stdout, "\x1b[31mred") {
		t.Errorf("the file says strip: %q", got.stdout)
	}
	// Options that only matter on a terminal are still accepted and checked.
	for _, args := range [][]string{{"--no-preview"}, {"--buffer", "50"}, {"--fzf-opts", "--exact"}, {"--set", "keys.accept=enter"}, {"--set", "preview_window=right,40%"}} {
		if got := do(in, args...); got.code != 0 || got.stderr != "" {
			t.Errorf("%v: %+v", args, got)
		}
	}
	if data, _ := os.ReadFile(file); string(data) != body {
		t.Errorf("config.toml was changed: %q", data)
	}
}

func TestBadOverridesAreRefusedBeforeReadingInput(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"--buffer", "lots"}, `--buffer: "buffer" must be a number, got "lots"`},
		{[]string{"--buffer", "0"}, `--buffer: "buffer" must be at least 1`},
		{[]string{"--ansi", "maybe"}, `--ansi: "ansi" must be "strip" or "keep"`},
		{[]string{"--set", "buffer"}, `--set buffer: expected key=value`},
		{[]string{"--set", "color=red"}, `--set color=red: unknown setting "color"`},
		{[]string{"--set", "preview=maybe"}, `--set preview=maybe: "preview" must be true or false`},
		{[]string{"--theme", ""}, `--theme: "theme" must be "auto", "dark", or "light"`},
		{[]string{"--colorscheme", ""}, `--colorscheme: "colorscheme" must not be empty`},
	}
	for _, tt := range tests {
		r := run(t, opts{stdin: failReader{t}}, tt.args...)
		if r.code != 2 || !strings.HasPrefix(r.stderr, "mgnt: ") || !strings.Contains(r.stderr, tt.want) || r.stdout != "" {
			t.Errorf("%v: code %d stdout %q stderr %q", tt.args, r.code, r.stdout, r.stderr)
		}
	}
}

func TestHelpListsTheOverrides(t *testing.T) {
	r := run(t, opts{stdinTTY: true}, "--help")
	for _, s := range []string{"--colorscheme", "--theme", "--ansi", "--buffer", "--fzf-opts", "--no-preview", "--set", "one run only"} {
		if !strings.Contains(r.stdout, s) {
			t.Errorf("help lacks %q", s)
		}
	}
}

// Before colorschemes had a setting of their own, theme named one. The old
// options still work, and say what to use now.
func TestOldThemeOptionsStillWork(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	in := strings.Repeat(unicornLine, 3)
	color := map[string]string{"COLORTERM": "truecolor"}
	do := func(args ...string) result {
		var stdout, stderr bytes.Buffer
		env := Env{
			Stdin: strings.NewReader(in), Stdout: &stdout, Stderr: &stderr,
			Getenv:   func(k string) string { return color[k] },
			LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
		}
		return result{Run(env, args), stdout.String(), stderr.String()}
	}
	dark, light := do("--theme", "dark"), do("--theme", "light")
	for _, tt := range []struct {
		args []string
		want result
		note string
	}{
		{[]string{"--theme", "magenta-dark"}, dark, "--colorscheme magenta --theme dark"},
		{[]string{"--theme", "magenta-light"}, light, "--colorscheme magenta --theme light"},
		{[]string{"--set", "theme=magenta-light"}, light, "--set colorscheme=magenta --set theme=light"},
		{[]string{"--theme", "mono"}, do("--colorscheme", "mono"), "--colorscheme mono"},
		{[]string{"--theme", "magenta"}, do("--colorscheme", "magenta"), "--colorscheme magenta"},
	} {
		got := do(tt.args...)
		if got.code != 0 || got.stdout != tt.want.stdout {
			t.Errorf("%v: code %d, output differs from %v", tt.args, got.code, tt.want.stdout == got.stdout)
		}
		if !strings.Contains(got.stderr, "is deprecated") || !strings.Contains(got.stderr, "use "+tt.note) {
			t.Errorf("%v: stderr %q", tt.args, got.stderr)
		}
	}
	if dark.stderr != "" || light.stderr != "" || dark.stdout == light.stdout {
		t.Errorf("the new options say nothing and differ: %q %q", dark.stderr, light.stderr)
	}

	// The same old words in config.toml.
	os.MkdirAll(filepath.Join(cfg, "magenta"), 0o755)
	os.WriteFile(filepath.Join(cfg, "magenta", "config.toml"), []byte("theme = \"magenta-light\"\n"), 0o644)
	got := do()
	if got.stdout != light.stdout || !strings.Contains(got.stderr, `theme = "magenta-light" is deprecated, use colorscheme = "magenta" and theme = "light"`) {
		t.Errorf("config.toml: %q", got.stderr)
	}
}
