package magentacli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/styd/magenta"
	"github.com/styd/magenta/internal/colorscheme"
	"github.com/styd/magenta/internal/config"
	"github.com/styd/magenta/internal/lines"
	"github.com/styd/magenta/internal/pipe"
	"github.com/styd/magenta/internal/render"
	"github.com/styd/magenta/internal/rules"
)

type fx struct {
	t              *testing.T
	stdout, stderr bytes.Buffer
	env            Env
	vars           map[string]string
	editor         func(file string) error
	fzfPath        string
	fzfOut         string
}

// newFx gives a magenta with an empty config dir and a terminal on stdin.
func newFx(t *testing.T) *fx {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := &fx{t: t, vars: map[string]string{"COLORTERM": "truecolor"}}
	f.env = Env{
		Stdin: strings.NewReader(""), Stdout: &f.stdout, Stderr: &f.stderr,
		Getenv:     func(k string) string { return f.vars[k] },
		StdinIsTTY: true,
		RunEditor: func(editor, file string) error {
			if f.editor == nil {
				return errors.New("no editor in this test")
			}
			return f.editor(file)
		},
		LookPath: func(name string) (string, error) {
			if name == "fzf" && f.fzfPath != "" {
				return f.fzfPath, nil
			}
			return "", exec.ErrNotFound
		},
		FzfVersion: func(string) (string, error) { return f.fzfOut, nil },
	}
	return f
}

func (f *fx) run(args ...string) int {
	f.stdout.Reset()
	f.stderr.Reset()
	return Run(f.env, args)
}

func (f *fx) mustRun(args ...string) {
	f.t.Helper()
	if code := f.run(args...); code != 0 {
		f.t.Fatalf("magenta %v: exit %d\nstdout: %s\nstderr: %s", args, code, f.stdout.String(), f.stderr.String())
	}
}

func visible(s string) string { return string(render.StripSGR(nil, []byte(s))) }

// ---- wrong-binary guards ----------------------------------------------------

func TestPipedStdinIsRefusedWithoutReading(t *testing.T) {
	f := newFx(t)
	pr, pw := io.Pipe() // nothing is ever written, and it is never closed
	defer pw.Close()
	f.env.Stdin, f.env.StdinIsTTY = pr, false
	done := make(chan int, 1)
	go func() { done <- Run(f.env, nil) }()
	select {
	case code := <-done:
		want := "magenta: this command manages rules and colorschemes, it does not show logs.\n" +
			"To color a log, use mgnt instead:\n" +
			"  tail -f app.log | mgnt\n" +
			"Run \"magenta --help\" for what magenta can do.\n"
		if code != 2 || f.stderr.String() != want || f.stdout.Len() != 0 {
			t.Errorf("code %d\nstderr %q\nstdout %q", code, f.stderr.String(), f.stdout.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("magenta waited for its stdin")
	}
}

func TestNoArgumentsOnTerminalShowsHelp(t *testing.T) {
	f := newFx(t)
	if code := f.run(); code != 0 {
		t.Fatalf("code %d, stderr %q", code, f.stderr.String())
	}
	out := f.stdout.String()
	for _, want := range []string{"Usage:", "rule", "colorscheme", "detect", "doctor", "does not show logs", "mgnt"} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q:\n%s", want, out)
		}
	}
	if f.stderr.Len() != 0 {
		t.Errorf("stderr %q", f.stderr.String())
	}
}

func TestExistingFileIsPointedToMgnt(t *testing.T) {
	f := newFx(t)
	file := filepath.Join(t.TempDir(), "app.log")
	os.WriteFile(file, []byte("x\n"), 0o644)
	if code := f.run(file); code != 2 {
		t.Fatalf("code %d", code)
	}
	for _, want := range []string{"does not show logs", "  mgnt " + file + "\n", `Run "magenta --help"`} {
		if !strings.Contains(f.stderr.String(), want) {
			t.Errorf("stderr lacks %q: %q", want, f.stderr.String())
		}
	}
	if f.stdout.Len() != 0 {
		t.Errorf("stdout %q", f.stdout.String())
	}
	// A file named like a subcommand is still the subcommand.
	t.Chdir(t.TempDir())
	os.WriteFile("doctor", []byte("x\n"), 0o644)
	if code := f.run("doctor"); code != 0 {
		t.Errorf("magenta doctor with a file called doctor: %d", code)
	}
}

func TestMgntOnlyFlags(t *testing.T) {
	for _, flag := range []string{"--no-interactive"} {
		f := newFx(t)
		if code := f.run(flag); code != 2 {
			t.Errorf("%s: code %d", flag, code)
		}
		if !strings.Contains(f.stderr.String(), "these options belong to mgnt") || !strings.Contains(f.stderr.String(), flag) || !strings.Contains(f.stderr.String(), "use mgnt") || f.stdout.Len() != 0 {
			t.Errorf("%s: %q", flag, f.stderr.String())
		}
	}
	f := newFx(t)
	if code := f.run("rule", "list", "--no-interactive"); code != 2 || !strings.Contains(f.stderr.String(), "belong to mgnt") {
		t.Errorf("flag after a subcommand: %d %q", code, f.stderr.String())
	}
}

func TestUnknownSubcommand(t *testing.T) {
	f := newFx(t)
	if code := f.run("rulle"); code != 2 {
		t.Fatalf("code %d", code)
	}
	e := f.stderr.String()
	if !strings.Contains(e, `unknown command "rulle" for "magenta"`) || !strings.Contains(e, "Did you mean this?\n\trule") || !strings.HasSuffix(e, "To view logs, use mgnt.\n") || f.stdout.Len() != 0 {
		t.Errorf("stderr %q", e)
	}
	if code := f.run("rule", "lst"); code != 2 || !strings.Contains(f.stderr.String(), `unknown command "lst" for "magenta rule"`) || !strings.Contains(f.stderr.String(), "\tlist") || !strings.Contains(f.stderr.String(), "To view logs, use mgnt.") {
		t.Errorf("subcommand typo: %q", f.stderr.String())
	}
}

func TestUnknownFlagAndArgCount(t *testing.T) {
	f := newFx(t)
	if code := f.run("--frobnicate"); code != 2 || !strings.Contains(f.stderr.String(), "unknown flag: --frobnicate") || !strings.Contains(f.stderr.String(), `Run "magenta --help"`) {
		t.Errorf("%d %q", code, f.stderr.String())
	}
	if code := f.run("config", "set", "theme"); code != 2 || !strings.Contains(f.stderr.String(), "accepts 2 arg(s)") {
		t.Errorf("%d %q", code, f.stderr.String())
	}
}

func TestVersion(t *testing.T) {
	f := newFx(t)
	f.mustRun("--version")
	if !strings.HasPrefix(f.stdout.String(), "magenta ") || !strings.Contains(f.stdout.String(), "commit") {
		t.Errorf("%q", f.stdout.String())
	}
}

// ---- init, rule, colorscheme ------------------------------------------------------

func TestInitCopiesBuiltinsAndKeepsExistingFiles(t *testing.T) {
	f := newFx(t)
	f.mustRun("init")
	for _, p := range []string{"config.toml", "rules/unicorn.toml", "rules/generic.toml", "colorschemes/magenta.toml", "colorschemes/mono.toml"} {
		if _, err := os.Stat(filepath.Join(config.Dir(), p)); err != nil {
			t.Errorf("missing %s", p)
		}
	}
	if !strings.Contains(f.stdout.String(), "created") {
		t.Error(f.stdout.String())
	}
	if _, err := config.Load(); err != nil {
		t.Errorf("the written config.toml does not load: %v", err)
	}
	mine := filepath.Join(config.Dir(), "rules", "cron.toml")
	if err := os.WriteFile(mine, []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.mustRun("init")
	data, _ := os.ReadFile(mine)
	if string(data) != "# mine\n" || strings.Contains(f.stdout.String(), "created") || !strings.Contains(f.stdout.String(), "kept") {
		t.Errorf("second init changed things: %q / %q", data, f.stdout.String())
	}
	// After init the user copies are the effective files, and they still check out.
	f.mustRun("colorscheme", "check")
}

func TestListShowAndSources(t *testing.T) {
	f := newFx(t)
	f.mustRun("rule", "list")
	if !regexp.MustCompile(`unicorn\s+built-in`).MatchString(f.stdout.String()) {
		t.Errorf("%s", f.stdout.String())
	}
	f.mustRun("rule", "show", "unicorn")
	want, _ := magenta.FS.ReadFile("rules/unicorn.toml")
	if f.stdout.String() != string(want) {
		t.Error("show does not print the file")
	}
	if code := f.run("rule", "show", "nope"); code != 1 || !strings.Contains(f.stderr.String(), `no rule named "nope"`) {
		t.Errorf("%d %q", code, f.stderr.String())
	}
	// A user file with the same name overrides. A new name is added.
	dir := filepath.Join(config.Dir(), "colorschemes")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "mono.toml"), []byte("name = \"mono\"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "extra.toml"), []byte("name = \"extra\"\n"), 0o644)
	f.mustRun("colorscheme", "list")
	out := f.stdout.String()
	for _, re := range []string{`extra\s+user\s`, `mono\s+user \(overrides built-in\)`, `magenta\s+built-in`} {
		if !regexp.MustCompile(re).MatchString(out) {
			t.Errorf("no match for %q in\n%s", re, out)
		}
	}
}

func TestRuleNewCreatesAValidRule(t *testing.T) {
	f := newFx(t)
	f.mustRun("rule", "new", "myapp")
	p := filepath.Join(config.Dir(), "rules", "myapp.toml")
	if _, err := os.Stat(p); err != nil || !strings.Contains(f.stdout.String(), "ok: "+p) {
		t.Fatalf("%v %q", err, f.stdout.String())
	}
	if f.stderr.Len() != 0 {
		t.Errorf("template produced warnings: %q", f.stderr.String())
	}
	f.mustRun("rule", "check", "myapp")
	f.mustRun("rule", "check", p)
	// The new rule is used for detection straight away.
	dets, errs := rules.ConfigLoader{}.Detectors()
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	var found bool
	for _, d := range dets {
		found = found || d.Name == "myapp"
	}
	if !found {
		t.Error("new rule not picked up")
	}
	// Errors.
	if code := f.run("rule", "new", "myapp"); code != 1 || !strings.Contains(f.stderr.String(), "already exists") {
		t.Errorf("%d %q", code, f.stderr.String())
	}
	if code := f.run("rule", "new", "unicorn"); code != 1 || !strings.Contains(f.stderr.String(), "built-in rule called \"unicorn\"") || !strings.Contains(f.stderr.String(), "magenta rule edit unicorn") {
		t.Errorf("%d %q", code, f.stderr.String())
	}
	if code := f.run("rule", "new", "Bad Name"); code != 1 || !strings.Contains(f.stderr.String(), "invalid rule name") {
		t.Errorf("%d %q", code, f.stderr.String())
	}
}

func TestColorschemeNewAndUse(t *testing.T) {
	f := newFx(t)
	f.mustRun("colorscheme", "new", "mine")
	f.mustRun("colorscheme", "use", "mine")
	cfg, err := config.Load()
	if err != nil || cfg.Colorscheme != "mine" {
		t.Fatalf("%+v %v", cfg, err)
	}
	f.mustRun("config", "get", "colorscheme")
	if strings.TrimSpace(f.stdout.String()) != "mine" {
		t.Errorf("%q", f.stdout.String())
	}
	if code := f.run("colorscheme", "use", "nope"); code != 1 || !strings.Contains(f.stderr.String(), `no colorscheme named "nope"`) {
		t.Errorf("%d %q", code, f.stderr.String())
	}
	if cfg, _ := config.Load(); cfg.Colorscheme != "mine" {
		t.Error("a failed `colorscheme use` changed the config")
	}
	// A colorscheme with errors cannot be selected.
	dir := filepath.Join(config.Dir(), "colorschemes")
	os.WriteFile(filepath.Join(dir, "broken.toml"), []byte("name = \"broken\"\n[styles]\n\"a\" = { fg = \"nope\" }\n"), 0o644)
	if code := f.run("colorscheme", "use", "broken"); code != 1 || !strings.Contains(f.stderr.String(), "broken.toml:3") {
		t.Errorf("%d %q", code, f.stderr.String())
	}
	// The theme is not part of the colorscheme, and the new file has both palettes.
	data, err := os.ReadFile(filepath.Join(dir, "mine.toml"))
	if err != nil || !strings.Contains(string(data), "[palette.dark]") || !strings.Contains(string(data), "[palette.light]") {
		t.Errorf("template %q %v", data, err)
	}
	f.mustRun("colorscheme", "use", "mono")
}

func TestEditCreatesUserCopyRunsEditorThenChecks(t *testing.T) {
	f := newFx(t)
	f.vars["EDITOR"] = "myeditor"
	var gotFile string
	f.editor = func(file string) error {
		gotFile = file
		data, _ := os.ReadFile(file)
		if !bytes.Contains(data, []byte(`name = "cron"`)) {
			t.Errorf("editor was not given a copy of the built-in rule")
		}
		return os.WriteFile(file, append(data, []byte("\n[[context.default.rule]]\nregex = '(oops'\nscope = \"x\"\n")...), 0o644)
	}
	code := f.run("rule", "edit", "cron")
	user := filepath.Join(config.Dir(), "rules", "cron.toml")
	if gotFile != user {
		t.Errorf("edited %q, want %q", gotFile, user)
	}
	// The check runs when the editor exits and reports file and line.
	if code != 1 || !regexp.MustCompile(regexp.QuoteMeta(user)+`:\d+: invalid rule regex`).MatchString(f.stderr.String()) {
		t.Errorf("code %d stderr %q", code, f.stderr.String())
	}
	// Fix it, edit again: the user copy is used and now passes.
	f.editor = func(file string) error {
		data, _ := os.ReadFile(file)
		return os.WriteFile(file, bytes.Replace(data, []byte("(oops"), []byte("(oops)"), 1), 0o644)
	}
	f.mustRun("rule", "edit", "cron")
	if !strings.Contains(f.stdout.String(), "ok: "+user) {
		t.Errorf("%q", f.stdout.String())
	}
	if code := f.run("rule", "edit", "nonesuch"); code != 1 || !strings.Contains(f.stderr.String(), "magenta rule new nonesuch") {
		t.Errorf("%d %q", code, f.stderr.String())
	}
	f.editor = func(string) error { return errors.New("boom") }
	if code := f.run("colorscheme", "edit", "mono"); code != 1 || !strings.Contains(f.stderr.String(), "boom") {
		t.Errorf("%d %q", code, f.stderr.String())
	}
}

func TestEditorSelection(t *testing.T) {
	f := newFx(t)
	var used string
	f.env.RunEditor = func(editor, file string) error { used = editor; return nil }
	f.mustRun("colorscheme", "edit", "mono")
	if used != "vi" {
		t.Errorf("default editor %q", used)
	}
	f.vars["EDITOR"] = "nano"
	f.mustRun("colorscheme", "edit", "mono")
	if used != "nano" {
		t.Errorf("EDITOR: %q", used)
	}
	f.vars["VISUAL"] = "code -w"
	f.mustRun("colorscheme", "edit", "mono")
	if used != "code -w" {
		t.Errorf("VISUAL: %q", used)
	}
}

func TestRuleCheck(t *testing.T) {
	f := newFx(t)
	dir := t.TempDir()
	write := func(name, src string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(src), 0o644)
		return p
	}
	bad := write("bad.toml", `name = "bad"
sample = ["zzz", "aaa"]
[[context.default.rule]]
regex = 'a+'
scope = "number"
[[context.default.enter]]
regex = 'a'
to = "nowhere"
`)
	if code := f.run("rule", "check", bad); code != 1 {
		t.Fatalf("code %d", code)
	}
	for _, want := range []string{bad + ":8: enter in context \"default\" goes to unknown context \"nowhere\""} {
		if !strings.Contains(f.stderr.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, f.stderr.String())
		}
	}
	// Samples are checked once the structure is sound.
	nosample := write("nosample.toml", "name = \"ns\"\nsample = [\"zzz\", \"aaa\"]\n[[context.default.rule]]\nregex = 'a+'\nscope = \"number\"\n")
	if code := f.run("rule", "check", nosample); code != 1 || !strings.Contains(f.stderr.String(), nosample+":2: sample line is not matched by any rule: \"zzz\"") {
		t.Errorf("%d %q", code, f.stderr.String())
	}
	// An unstyled scope is a warning, not an error.
	warn := write("warn.toml", "name = \"w\"\nsample = [\"aaa\"]\n[[context.default.rule]]\nregex = 'a+'\nscope = \"totally.unknown\"\n")
	if code := f.run("rule", "check", warn); code != 0 || !strings.Contains(f.stderr.String(), `scope "totally.unknown" has no style`) || !strings.Contains(f.stdout.String(), "ok: "+warn) {
		t.Errorf("%d out %q err %q", code, f.stdout.String(), f.stderr.String())
	}
	if code := f.run("rule", "check", filepath.Join(dir, "missing.toml")); code != 1 || !strings.Contains(f.stderr.String(), "neither a file nor a rule name") {
		t.Errorf("%d %q", code, f.stderr.String())
	}
	// Without an argument everything is checked, and the built-ins pass.
	f.mustRun("rule", "check")
	if strings.Count(f.stdout.String(), "ok: ") != 12 {
		t.Errorf("%s", f.stdout.String())
	}
	f.mustRun("colorscheme", "check")
	if strings.Count(f.stdout.String(), "ok: ") != 2 {
		t.Errorf("%s", f.stdout.String())
	}
	// A broken user file is reported among the rest.
	os.MkdirAll(filepath.Join(config.Dir(), "rules"), 0o755)
	os.WriteFile(filepath.Join(config.Dir(), "rules", "broken.toml"), []byte("name = \"broken\"\ndetect = ['(']\n"), 0o644)
	if code := f.run("rule", "check"); code != 1 || !strings.Contains(f.stderr.String(), "broken.toml:2: invalid detect regex") {
		t.Errorf("%d %q", code, f.stderr.String())
	}
}

func TestColorschemeCheckReportsLine(t *testing.T) {
	f := newFx(t)
	p := filepath.Join(t.TempDir(), "t.toml")
	os.WriteFile(p, []byte("name = \"t\"\n[styles]\n\"a\" = { fg = \"red\" }\n\"b\" = { fg = \"#12\" }\n"), 0o644)
	if code := f.run("colorscheme", "check", p); code != 1 || !strings.Contains(f.stderr.String(), p+":4: style \"b\": invalid hex color") {
		t.Errorf("%d %q", code, f.stderr.String())
	}
}

// ---- preview ------------------------------------------------------------------

func TestPreviewTheme(t *testing.T) {
	f := newFx(t)
	f.mustRun("preview", "unicorn", "--theme", "dark")
	dark := f.stdout.String()
	f.mustRun("preview", "unicorn", "--theme", "light")
	light := f.stdout.String()
	if !strings.HasPrefix(light, "== unicorn (colorscheme magenta, light theme)\n") || light == dark {
		t.Errorf("light preview: %q", light)
	}
	if code := f.run("preview", "unicorn", "--theme", "magenta-dark"); code != 1 || !strings.Contains(f.stderr.String(), `--theme must be "auto", "dark", or "light"`) {
		t.Errorf("%d %q", code, f.stderr.String())
	}
}

func TestPreview(t *testing.T) {
	f := newFx(t)
	f.mustRun("preview", "unicorn", "magenta", "--theme", "dark")
	out := f.stdout.String()
	if !strings.HasPrefix(out, "== unicorn (colorscheme magenta, dark theme)\n") || !strings.Contains(out, "\x1b[38;2;") {
		t.Errorf("%q", out)
	}
	want, _ := magenta.FS.ReadFile("rules/unicorn.toml")
	rf, _ := rules.Parse("u", want)
	for _, s := range rf.Sample {
		if !strings.Contains(visible(out), s) {
			t.Errorf("sample missing: %q", s)
		}
	}
	// The colorscheme argument matters: mono has no hues.
	f.mustRun("preview", "unicorn", "mono")
	if strings.Contains(f.stdout.String(), ";2;") || strings.Contains(f.stdout.String(), "\x1b[3") {
		t.Errorf("mono preview has color: %q", f.stdout.String())
	}
	// With no arguments every rule is shown with the configured colorscheme.
	f.mustRun("preview")
	for _, name := range config.BuiltinNames(config.Rules) {
		if !strings.Contains(f.stdout.String(), "== "+name+" (colorscheme magenta, dark theme)") {
			t.Errorf("rule %s missing", name)
		}
	}
	f.mustRun("colorscheme", "use", "mono")
	f.mustRun("preview", "cron")
	if !strings.Contains(f.stdout.String(), "(colorscheme mono, dark theme)") {
		t.Errorf("configured colorscheme not used: %q", f.stdout.String()[:40])
	}
	f.vars["NO_COLOR"] = "1"
	f.mustRun("preview", "unicorn", "magenta")
	if strings.Contains(f.stdout.String(), "\x1b") {
		t.Error("NO_COLOR ignored")
	}
	if code := f.run("preview", "nope"); code != 1 || !strings.Contains(f.stderr.String(), `no rule named "nope"`) {
		t.Errorf("%d %q", code, f.stderr.String())
	}
	if code := f.run("preview", "unicorn", "nope"); code != 1 || !strings.Contains(f.stderr.String(), `no colorscheme named "nope"`) {
		t.Errorf("%d %q", code, f.stderr.String())
	}
}

// ---- detect -------------------------------------------------------------------

// mgntPick is the rule mgnt itself locks on for a file.
func mgntPick(t *testing.T, path string) string {
	t.Helper()
	data, _ := magenta.FS.ReadFile("colorschemes/magenta.toml")
	cs, err := colorscheme.Parse("magenta", data, colorscheme.Dark)
	if err != nil {
		t.Fatal(err)
	}
	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	p := pipe.New(pipe.Config{ANSI: "strip", Colorscheme: cs, Depth: render.TrueColor, Loader: rules.ConfigLoader{}, Filename: path})
	done := make(chan struct{})
	defer close(done)
	if err := pipe.Drive(lines.Read(fh, done), p, func([]byte, bool) {}, func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	return p.Rule()
}

var pickRe = regexp.MustCompile(`(?m)^mgnt would pick: (\S+)`)

func TestDetectAgreesWithMgnt(t *testing.T) {
	f := newFx(t)
	tmp := t.TempDir()
	cases := map[string]string{} // path -> expected rule
	for _, name := range config.BuiltinNames(config.Rules) {
		cases[filepath.Join("..", "..", "testdata", name, "in.log")] = name
	}
	// Same content under file names that carry a hint.
	copyAs := func(from, to string) string {
		data, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(tmp, to)
		os.WriteFile(p, data, 0o644)
		return p
	}
	cases[copyAs("../../testdata/generic/in.log", "unicorn.stderr.log")] = "unicorn"
	cases[copyAs("../../testdata/generic/in.log", "change.patch")] = "git-diff"
	cases[copyAs("../../testdata/generic/in.log", "plain.txt")] = "generic"
	cases[copyAs("../../testdata/unicorn/in.log", "renamed.txt")] = "unicorn"
	for path, want := range cases {
		f.mustRun("detect", path)
		m := pickRe.FindStringSubmatch(f.stdout.String())
		if m == nil {
			t.Fatalf("%s: no verdict in\n%s", path, f.stdout.String())
		}
		if m[1] != want {
			t.Errorf("%s: detect says %s, want %s\n%s", path, m[1], want, f.stdout.String())
		}
		if got := mgntPick(t, path); got != m[1] {
			t.Errorf("%s: magenta detect says %s but mgnt picks %s", path, m[1], got)
		}
	}
}

func TestDetectExplainsTheScore(t *testing.T) {
	f := newFx(t)
	p := filepath.Join(t.TempDir(), "unicorn.log")
	os.WriteFile(p, []byte("I, [2026-10-06T10:00:00.123 #4242]  INFO -- : worker=0 ready\nsecond\n"), 0o644)
	f.mustRun("detect", p)
	out := f.stdout.String()
	for _, want := range []string{"sampled  2 line(s)", "RULE", "detect /^[IWEFDA]", `filenames "unicorn*.log"`, "mgnt would pick: unicorn"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	// A log nothing recognizes.
	q := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(q, []byte("hello\nworld\nfoo\nbar\n"), 0o644)
	f.mustRun("detect", q)
	if !strings.Contains(f.stdout.String(), "mgnt would pick: generic (no rule scored high enough)") {
		t.Errorf("%s", f.stdout.String())
	}
	// Existing ANSI in the file does not confuse detection.
	r := filepath.Join(t.TempDir(), "x.log")
	os.WriteFile(r, []byte("\x1b[31mI, [2026-10-06T10:00:00.123 #4242]  INFO -- : worker=0 ready\x1b[0m\n"), 0o644)
	f.mustRun("detect", r)
	if !strings.Contains(f.stdout.String(), "mgnt would pick: unicorn") {
		t.Errorf("%s", f.stdout.String())
	}
}

func TestDetectNeverReadsStdin(t *testing.T) {
	f := newFx(t)
	f.env.Stdin = readFailer{t}
	if code := f.run("detect", "-"); code != 1 || !strings.Contains(f.stderr.String(), "never stdin") {
		t.Errorf("%d %q", code, f.stderr.String())
	}
	if code := f.run("detect", filepath.Join(t.TempDir(), "missing.log")); code != 1 || f.stderr.Len() == 0 {
		t.Errorf("%d %q", code, f.stderr.String())
	}
	if code := f.run("detect", t.TempDir()); code != 1 || !strings.Contains(f.stderr.String(), "directory") {
		t.Errorf("%d %q", code, f.stderr.String())
	}
	if code := f.run("detect"); code != 2 {
		t.Errorf("no file: %d", code)
	}
}

type readFailer struct{ t *testing.T }

func (r readFailer) Read([]byte) (int, error) {
	r.t.Error("stdin was read")
	return 0, io.EOF
}

// ---- config, doctor ---------------------------------------------------------

func TestConfigCommands(t *testing.T) {
	f := newFx(t)
	f.mustRun("config", "path")
	if strings.TrimSpace(f.stdout.String()) != config.Path() {
		t.Errorf("%q", f.stdout.String())
	}
	f.mustRun("config", "get")
	for _, want := range []string{"colorscheme = magenta", "theme = auto", "buffer = 100000", "ansi = strip", "preview = true", "preview_window = up,50%,wrap,border-bottom,hidden", "keys.preview_toggle = ctrl-/", "keys.accept = ctrl-o"} {
		if !strings.Contains(f.stdout.String(), want) {
			t.Errorf("lacks %q:\n%s", want, f.stdout.String())
		}
	}
	f.mustRun("config", "set", "buffer", "500")
	f.mustRun("config", "set", "ansi", "keep")
	f.mustRun("config", "get", "buffer")
	if strings.TrimSpace(f.stdout.String()) != "500" {
		t.Errorf("%q", f.stdout.String())
	}
	for _, bad := range [][]string{{"set", "ansi", "x"}, {"set", "buffer", "many"}, {"set", "color", "1"}, {"get", "color"}} {
		if code := f.run(append([]string{"config"}, bad...)...); code != 1 || f.stderr.Len() == 0 {
			t.Errorf("%v: %d %q", bad, code, f.stderr.String())
		}
	}
	cfg, _ := config.Load()
	if cfg.Buffer != 500 || cfg.ANSI != "keep" {
		t.Errorf("%+v", cfg)
	}
}

func TestDoctor(t *testing.T) {
	f := newFx(t)
	f.fzfPath, f.fzfOut = "/usr/local/bin/fzf", "0.20.0 (brew)\n"
	os.MkdirAll(filepath.Join(config.Dir(), "rules"), 0o755)
	os.WriteFile(filepath.Join(config.Dir(), "rules", "unicorn.toml"), mustRead(t, "rules/unicorn.toml"), 0o644)
	f.mustRun("doctor")
	out := f.stdout.String()
	for _, want := range []string{
		"magenta dev", config.Dir(), "theme        auto, using dark", "colorscheme  magenta (built-in)",
		"12 rule file(s)", "unicorn        user (overrides built-in)", "cron           built-in",
		`truecolor (COLORTERM="truecolor"`,
		"/usr/local/bin/fzf 0.20.0 (brew)", "the / search is available",
		"preview pane (>= 0.18.0): yes", "preview border (>= 0.27.0): no", "preview on Enter, closed with Esc (>= 0.49.0): no",
		"fallback: fzf 0.20.0 is too old for the preview border",
		"start-up", "ms",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor lacks %q:\n%s", want, out)
		}
	}
	// An fzf too old for any preview.
	f.fzfOut = "0.9.4\n"
	f.mustRun("doctor")
	if !strings.Contains(f.stdout.String(), "searching without it") || !strings.Contains(f.stdout.String(), "preview pane (>= 0.18.0): no") {
		t.Errorf("%s", f.stdout.String())
	}
	// No fzf at all.
	f.fzfPath = ""
	f.vars["NO_COLOR"] = "1"
	f.mustRun("doctor")
	if !strings.Contains(f.stdout.String(), "not found on PATH: the / search is not available") || !strings.Contains(f.stdout.String(), "color        none") {
		t.Errorf("%s", f.stdout.String())
	}
	// Problems are reported, not fatal.
	os.WriteFile(filepath.Join(config.Dir(), "config.toml"), []byte("ansi = \"x\"\n"), 0o644)
	os.WriteFile(filepath.Join(config.Dir(), "rules", "bad.toml"), []byte("name = \"bad\"\ndetect = ['(']\n"), 0o644)
	f.mustRun("doctor")
	if !strings.Contains(f.stdout.String(), "PROBLEM") || !strings.Contains(f.stdout.String(), "bad.toml:2") {
		t.Errorf("%s", f.stdout.String())
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	data, err := magenta.FS.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// fzf options start with dashes. They must work as the value of `config set`.
func TestConfigSetAcceptsAValueThatLooksLikeAFlag(t *testing.T) {
	f := newFx(t)
	f.mustRun("config", "set", "fzf_opts", "--exact")
	f.mustRun("config", "get", "fzf_opts")
	if strings.TrimSpace(f.stdout.String()) != "--exact" {
		t.Errorf("%q", f.stdout.String())
	}
	f.mustRun("config", "set", "fzf_opts", "--exact --height 40%")
	if c, _ := config.Load(); c.FzfOpts != "--exact --height 40%" {
		t.Errorf("%q", c.FzfOpts)
	}
	// Still an error for a value that is missing, or a real extra argument.
	if code := f.run("config", "set", "fzf_opts"); code != 2 {
		t.Errorf("missing value: %d", code)
	}
}

// ---- auto theme ---------------------------------------------------------------

func TestAutoTheme(t *testing.T) {
	f := newFx(t)
	f.mustRun("config", "get", "theme")
	if strings.TrimSpace(f.stdout.String()) != "auto" {
		t.Errorf("default theme %q", f.stdout.String())
	}

	// It follows the environment: dark when nothing is known, light by COLORFGBG,
	// and MGNT_BACKGROUND beats both.
	themeOf := func() string {
		f.mustRun("preview", "unicorn")
		return strings.SplitN(f.stdout.String(), "\n", 2)[0]
	}
	if got := themeOf(); got != "== unicorn (colorscheme magenta, dark theme)" {
		t.Errorf("nothing known: %q", got)
	}
	f.vars["COLORFGBG"] = "0;15"
	if got := themeOf(); got != "== unicorn (colorscheme magenta, light theme)" {
		t.Errorf("COLORFGBG: %q", got)
	}
	f.vars["MGNT_BACKGROUND"] = "dark"
	if got := themeOf(); got != "== unicorn (colorscheme magenta, dark theme)" {
		t.Errorf("override: %q", got)
	}

	f.mustRun("doctor")
	if !strings.Contains(f.stdout.String(), "auto, using dark (from MGNT_BACKGROUND)") {
		t.Errorf("doctor:\n%s", f.stdout.String())
	}

	// A pinned theme ignores the environment.
	f.mustRun("config", "set", "theme", "light")
	if got := themeOf(); got != "== unicorn (colorscheme magenta, light theme)" {
		t.Errorf("pinned light: %q", got)
	}
	// The old meaning of theme, a colorscheme, still works and says what to use.
	f.mustRun("config", "set", "theme", "magenta-dark")
	if !strings.Contains(f.stderr.String(), "deprecated, use `magenta config set colorscheme magenta`") {
		t.Errorf("stderr %q", f.stderr.String())
	}
	if cfg, _ := config.Load(); cfg.Colorscheme != "magenta" || cfg.Theme != "dark" {
		t.Errorf("%+v", cfg)
	}
	if code := f.run("config", "set", "theme", ""); code != 1 || !strings.Contains(f.stderr.String(), `"theme" must be "auto", "dark", or "light"`) {
		t.Errorf("%d %q", code, f.stderr.String())
	}
}

// magenta theme is the old name of magenta colorscheme.
func TestOldThemeCommandStillWorks(t *testing.T) {
	f := newFx(t)
	f.mustRun("theme", "list")
	if !strings.Contains(f.stderr.String(), "`magenta theme` is deprecated, use `magenta colorscheme`") || !strings.Contains(f.stdout.String(), "magenta") {
		t.Errorf("stdout %q stderr %q", f.stdout.String(), f.stderr.String())
	}
	f.mustRun("theme", "use", "mono")
	if cfg, _ := config.Load(); cfg.Colorscheme != "mono" || cfg.Theme != "auto" {
		t.Errorf("use mono: %+v", cfg)
	}
	f.mustRun("theme", "use", "magenta-light")
	if cfg, _ := config.Load(); cfg.Colorscheme != "magenta" || cfg.Theme != "light" {
		t.Errorf("use magenta-light: %+v", cfg)
	}
	f.mustRun("theme", "use", "auto")
	if cfg, _ := config.Load(); cfg.Colorscheme != "magenta" || cfg.Theme != "auto" || !strings.Contains(f.stderr.String(), "`magenta config set theme auto`") {
		t.Errorf("use auto: %+v %q", cfg, f.stderr.String())
	}
	// An old config.toml is reported by doctor.
	os.MkdirAll(config.Dir(), 0o755)
	os.WriteFile(config.Path(), []byte("theme = \"mono\"\n"), 0o644)
	os.MkdirAll(filepath.Join(config.Dir(), "themes"), 0o755)
	f.mustRun("doctor")
	for _, want := range []string{`theme = "mono" is deprecated, use colorscheme = "mono"`, "themes is not read any more"} {
		if !strings.Contains(f.stdout.String(), want) {
			t.Errorf("doctor lacks %q:\n%s", want, f.stdout.String())
		}
	}
}

// The overrides belong to mgnt, and magenta says so.
func TestMgntOverridesAreNotMagentaOptions(t *testing.T) {
	for _, flag := range []string{"--colorscheme", "--theme", "--ansi", "--buffer", "--fzf-opts", "--no-preview", "--set"} {
		f := newFx(t)
		if code := f.run("rule", "list", flag, "x"); code != 2 || !strings.Contains(f.stderr.String(), "magenta: "+flag+": these options belong to mgnt.") || f.stdout.Len() != 0 {
			t.Errorf("%s: code %d stderr %q", flag, code, f.stderr.String())
		}
	}
}
