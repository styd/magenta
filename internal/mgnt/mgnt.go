// Package mgnt is the log highlighter behind the mgnt command.
package mgnt

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/styd/magenta/internal/background"
	"github.com/styd/magenta/internal/cli"
	"github.com/styd/magenta/internal/colorscheme"
	"github.com/styd/magenta/internal/config"
	"github.com/styd/magenta/internal/lines"
	"github.com/styd/magenta/internal/pipe"
	"github.com/styd/magenta/internal/render"
	"github.com/styd/magenta/internal/rules"
	"github.com/styd/magenta/internal/search"
	"github.com/styd/magenta/internal/tty"
)

// Env is everything mgnt takes from the outside world, so tests can replace it.
type Env struct {
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
	Getenv      func(string) string
	StdinIsTTY  bool
	StdoutIsTTY bool
	LookPath    func(string) (string, error)

	// For the interactive search. OpenTTY opens the controlling terminal
	// (nil: no terminal, so no search). Signals delivers SIGINT and SIGTERM.
	// Executable is the path of this binary, for the preview helper.
	OpenTTY    func() (tty.Terminal, error)
	Signals    func() (<-chan os.Signal, func())
	Executable func() (string, error)
	FzfVersion func(path string) (string, error)
	Environ    []string
}

// DefaultEnv describes the real process.
func DefaultEnv() Env {
	ignoreSIGPIPE()
	enableVT(os.Stdout)
	return Env{
		Stdin:       os.Stdin,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Getenv:      os.Getenv,
		StdinIsTTY:  term.IsTerminal(int(os.Stdin.Fd())),
		StdoutIsTTY: term.IsTerminal(int(os.Stdout.Fd())),
		LookPath:    exec.LookPath,
		OpenTTY:     tty.Open,
		Signals:     notifySignals,
		Executable:  os.Executable,
		FzfVersion: func(path string) (string, error) {
			out, err := exec.Command(path, "--version").Output()
			return string(out), err
		},
		Environ: os.Environ(),
	}
}

const usage = `Usage: mgnt [flags] [file...]

Color a log by its kind. Pipe a log into mgnt, or give it files:

  tail -f unicorn.log | mgnt
  git diff | mgnt
  mgnt app.log

On a terminal, mgnt pages long output the way less does: a command with a lot
of output ready (git log) opens a full-screen pager (j/k, space/b, g/G scroll
both ways, / searches with fzf if installed, q quits). Output that fits on the
screen is just printed, and a log that trickles in, like tail -f, streams into
your scrollback. Output that is not a terminal, or --no-interactive, is just
colored.

The log type is automatic. The colorscheme, the theme (dark or light), and the
other settings live in config.toml
(edit them with the magenta command). The options below change a setting for
one run only, and never write the file. --set takes any setting, such as
--set preview_window=right,40% or --set keys.accept=enter.

Flags:
`

// Run runs mgnt and returns the exit status.
func Run(env Env, args []string) int {
	// fzf starts this hidden subcommand to draw its preview pane.
	if len(args) > 0 && args[0] == search.PreviewSubcommand {
		return search.PreviewHelper(args[1:], env.Getenv, env.Stdout)
	}
	fs := pflag.NewFlagSet("mgnt", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	noInteractive := fs.Bool("no-interactive", false, "never open the fzf search, just color the log")
	colorschemeName := fs.String("colorscheme", "", "use this colorscheme for this run (a name from `magenta colorscheme list`)")
	themeName := fs.String("theme", "", "use this theme for this run: auto, dark, or light")
	ansiMode := fs.String("ansi", "", "strip or keep: what to do with colors already in the input")
	buffer := fs.String("buffer", "", "number of lines kept for the / search")
	fzfOpts := fs.String("fzf-opts", "", "extra fzf options for this run")
	noPreview := fs.Bool("no-preview", false, "no preview pane in the / search")
	sets := fs.StringArray("set", nil, "change any setting of config.toml for this run, as `key=value` (repeatable)")
	help := fs.BoolP("help", "h", false, "show this help")
	version := fs.Bool("version", false, "show the version")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(env.Stderr, "mgnt: %v\nTry \"mgnt --help\".\n", err)
		return 2
	}
	switch {
	case *help:
		fmt.Fprint(env.Stdout, usage+fs.FlagUsages())
		return 0
	case *version:
		fmt.Fprintln(env.Stdout, cli.VersionString("mgnt"))
		return 0
	}
	files := fs.Args()
	if len(files) == 1 && cli.IsAdminWord(files[0]) {
		if _, err := os.Stat(files[0]); err != nil {
			fmt.Fprintf(env.Stderr, "mgnt: %q is a magenta command. Try: magenta %s ...\n", files[0], cli.AdminCommand(files[0]))
			return 2
		}
	}
	if len(files) == 0 && env.StdinIsTTY {
		fmt.Fprintln(env.Stderr, "usage: mgnt [file...]")
		fmt.Fprintln(env.Stderr, "pipe a log into mgnt, or give it a file (try \"mgnt --help\")")
		return 2
	}

	var over []override
	for _, o := range []struct{ flag, key, val string }{
		{"colorscheme", "colorscheme", *colorschemeName}, {"theme", "theme", *themeName}, {"ansi", "ansi", *ansiMode}, {"buffer", "buffer", *buffer}, {"fzf-opts", "fzf_opts", *fzfOpts},
	} {
		if fs.Changed(o.flag) {
			over = append(over, override{"--" + o.flag, o.key, o.val})
		}
	}
	if *noPreview {
		over = append(over, override{"--no-preview", "preview", "false"})
	}
	for _, kv := range *sets {
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			fmt.Fprintf(env.Stderr, "mgnt: --set %s: expected key=value, for example --set buffer=5000\n", kv)
			return 2
		}
		over = append(over, override{"--set " + kv, key, val})
	}
	s, err := setup(env, over)
	if err != nil {
		fmt.Fprintf(env.Stderr, "mgnt: %v\n", err)
		return 2
	}
	if s.interactiveAvailable(*noInteractive) {
		fzf, _ := s.env.LookPath("fzf") // "" if fzf is not installed: paging without search
		return s.interactive(files, fzf)
	}
	return s.plain(files)
}

// session holds what is loaded at start-up.
type session struct {
	env    Env
	cfg    config.Config
	scheme *colorscheme.Colorscheme
	// paintOn sets the colors of the terminal for the pager and the search, or
	// is "" (see paintSequence).
	paintOn string
	depth   render.Depth
	loader  rules.Loader
}

func warnf(env Env, format string, args ...any) {
	fmt.Fprintf(env.Stderr, "mgnt: warning: "+format+"\n", args...)
}

// setup loads the config and colorscheme. Problems are reported as warnings and the
// defaults used, so a typo in a file never stops a log from being shown.
func setup(env Env, over []override) (*session, error) {
	s := &session{env: env, loader: rules.ConfigLoader{}}
	cfg, err := config.Load()
	if err != nil {
		warnf(env, "%v (using defaults)", err)
		cfg = config.Default()
	}
	for _, d := range cfg.Deprecated {
		warnf(env, "%s", d)
	}
	// Options of this run come last, so they win over the file.
	for _, o := range over {
		for _, n := range deprecatedTheme(env, o) {
			if err := cfg.Apply(n.key, n.val); err != nil {
				return nil, fmt.Errorf("%s: %v", n.arg, err)
			}
		}
	}
	s.cfg = cfg
	s.depth = render.DetectDepth(env.Getenv)
	s.scheme = loadColorscheme(env, cfg.Colorscheme, cfg.Theme, nil)
	return s, nil
}

// override is one setting changed by an option, for this run only.
type override struct{ arg, key, val string }

// deprecatedTheme turns the old use of the theme option, which named a
// colorscheme (--theme mono, --set theme=magenta-dark), into the options that
// mean it now, and says so. Every other override comes back as it is.
func deprecatedTheme(env Env, o override) []override {
	scheme, theme, legacy := config.LegacyTheme(o.val)
	if o.key != "theme" || !legacy {
		return []override{o}
	}
	use := "--colorscheme " + scheme
	out := []override{{o.arg, "colorscheme", scheme}}
	if theme != "" {
		use += " --theme " + theme
		out = append(out, override{o.arg, "theme", theme})
	}
	if strings.HasPrefix(o.arg, "--set") {
		use = "--set colorscheme=" + scheme
		if theme != "" {
			use += " --set theme=" + theme
		}
	}
	warnf(env, "%s is deprecated (a theme is dark or light now), use %s", o.arg, use)
	return out
}

// loadColorscheme loads the named colorscheme with the palette of the theme.
// The theme "auto" picks dark or light by the terminal background, asking the
// terminal when asker is not nil (see package background).
func loadColorscheme(env Env, name, theme string, asker background.Asker) *colorscheme.Colorscheme {
	if theme == background.Auto {
		theme = background.Detect(asker, env.Getenv).Theme()
	}
	f, err := config.Find(config.Colorschemes, name)
	if err == nil {
		var cs *colorscheme.Colorscheme
		if cs, err = colorscheme.Parse(f.Path, f.Data, theme); err == nil {
			return cs
		}
	}
	warnf(env, "%v (using the %s colorscheme)", err, defaultColorscheme)
	f, _ = config.Builtin(config.Colorschemes, defaultColorscheme)
	cs, _ := colorscheme.Parse(f.Path, f.Data, theme)
	return cs
}

// defaultColorscheme is the built-in colorscheme used when the chosen one cannot be loaded.
const defaultColorscheme = "magenta"

// interactiveAvailable says whether mgnt can be a pager: output is a terminal,
// the user did not opt out, and a terminal can be opened
// for keys. Otherwise mgnt is a plain filter that does not touch the terminal
// at all. fzf is optional: without it the pager has no "/" search.
func (s *session) interactiveAvailable(noInteractive bool) bool {
	return !noInteractive && s.env.StdoutIsTTY && s.env.OpenTTY != nil
}

// plain colors the inputs and writes them out, touching nothing else.
func (s *session) plain(files []string) int {
	w := bufio.NewWriterSize(s.env.Stdout, 64<<10)
	status := 0
	if len(files) == 0 {
		files = []string{"-"}
	}
	for _, name := range files {
		var in io.Reader = s.env.Stdin
		display := ""
		if name != "-" {
			f, err := os.Open(name)
			if err != nil {
				fmt.Fprintf(s.env.Stderr, "mgnt: %v\n", err)
				status = 1
				continue
			}
			defer f.Close()
			in, display = f, name
		}
		switch code := s.filter(w, in, display); {
		case code == 0:
		default:
			w.Flush()
			if code == epipe {
				return 0
			}
			status = code
		}
	}
	if err := w.Flush(); err != nil && !isEPIPE(err) {
		fmt.Fprintf(s.env.Stderr, "mgnt: %v\n", err)
		return 1
	}
	return status
}

const epipe = -1

func (s *session) filter(w *bufio.Writer, in io.Reader, filename string) int {
	p := pipe.New(pipe.Config{
		ANSI:        s.cfg.ANSI,
		Colorscheme: s.scheme,
		Depth:       s.depth,
		Loader:      s.loader,
		Filename:    filename,
		Warn:        func(m string) { warnf(s.env, "%s", m) },
	})
	var werr error
	emit := func(line []byte, nl bool) {
		if werr != nil {
			return
		}
		if _, werr = w.Write(line); werr == nil && nl {
			werr = w.WriteByte('\n')
		}
	}
	idle := func() bool {
		if werr == nil {
			werr = w.Flush()
		}
		return werr != nil
	}
	done := make(chan struct{})
	defer close(done)
	err := pipe.Drive(lines.Read(in, done), p, emit, idle)
	switch {
	case werr != nil && isEPIPE(werr):
		return epipe
	case werr != nil:
		fmt.Fprintf(s.env.Stderr, "mgnt: %v\n", werr)
		return 1
	case err != nil:
		fmt.Fprintf(s.env.Stderr, "mgnt: %v\n", err)
		return 1
	}
	return 0
}

// Startup does what mgnt does before the first line is shown: it loads the
// config and the colorscheme, compiles the detect regexes of every rule file, and
// fully compiles the named one. `magenta doctor` times it, and the start-up
// budget test and benchmark run it.
func Startup(ruleName string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cs := loadColorscheme(Env{Stderr: io.Discard, Getenv: os.Getenv}, cfg.Colorscheme, cfg.Theme, nil)
	var l rules.ConfigLoader
	if _, errs := l.Detectors(); len(errs) > 0 {
		return errs[0]
	}
	c, err := l.Load(ruleName)
	if err != nil {
		return err
	}
	_ = cs.Starts(render.TrueColor, c.Scopes)
	return nil
}
