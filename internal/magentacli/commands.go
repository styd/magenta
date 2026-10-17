package magentacli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/styd/magenta/internal/background"
	"github.com/styd/magenta/internal/cli"
	"github.com/styd/magenta/internal/colorscheme"
	"github.com/styd/magenta/internal/config"
	"github.com/styd/magenta/internal/detect"
	"github.com/styd/magenta/internal/engine"
	"github.com/styd/magenta/internal/mgnt"
	"github.com/styd/magenta/internal/render"
	"github.com/styd/magenta/internal/rules"
	"github.com/styd/magenta/internal/search"
)

const defaultConfig = `# magenta settings. Change them by editing this file, or with
# "magenta config set <key> <value>". mgnt reads this file when it starts.

# The colorscheme mgnt uses ("magenta colorscheme list" shows the choices).
colorscheme = "magenta"

# The theme: "dark" or "light" picks that palette of the colorscheme, and "auto"
# picks one by the terminal background.
theme = "auto"

# How many lines mgnt remembers for the / search.
buffer = 100000

# "strip" removes ANSI colors already in the input and colors the plain
# text. "keep" passes lines through untouched.
ansi = "strip"

# Extra options for fzf, added after the ones mgnt needs.
# fzf_opts = ""

# The preview pane of the / search.
# preview = true
# preview_window = "up,50%,wrap,border-bottom,hidden"

# [keys]
# preview_toggle = "ctrl-/"
# Enter opens the preview and Esc closes it. This key prints the selected lines
# instead. "enter" gives Enter that job back.
# accept = "ctrl-o"
`

func (a *app) initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Copy the built-in rules and colorschemes into your config directory for editing",
		Long: `Copies the built-in rules and colorschemes into the config directory so you can edit
them, and writes a config.toml with the defaults. Files that already exist are
not touched.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			out := a.env.Stdout
			put := func(path string, data []byte) error {
				if _, err := os.Stat(path); err == nil {
					fmt.Fprintf(out, "kept     %s\n", path)
					return nil
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(path, data, 0o644); err != nil {
					return err
				}
				fmt.Fprintf(out, "created  %s\n", path)
				return nil
			}
			if err := put(config.Path(), []byte(defaultConfig)); err != nil {
				return fail("%v", err)
			}
			for _, k := range []config.Kind{config.Rules, config.Colorschemes} {
				for _, name := range config.BuiltinNames(k) {
					f, err := config.Builtin(k, name)
					if err != nil {
						return fail("%v", err)
					}
					if err := put(filepath.Join(config.UserDir(k), name+".toml"), f.Data); err != nil {
						return fail("%v", err)
					}
				}
			}
			return nil
		},
	}
}

func (a *app) depth() render.Depth { return render.DetectDepth(a.env.Getenv) }

func (a *app) previewCmd() *cobra.Command {
	var themeFlag string
	c := &cobra.Command{
		Use:   "preview [rule] [colorscheme]",
		Short: "Show a rule's sample lines in a colorscheme",
		Long: `Prints the sample lines of a rule (of every rule when none is given) colored
with a colorscheme (the configured one when none is given), so you can see the
effect without a real log. --theme chooses the dark or the light palette
(the configured theme when not given).`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fail("%v", err)
			}
			schemeName, theme := cfg.Colorscheme, cfg.Theme
			if len(args) == 2 {
				schemeName = args[1]
			}
			if themeFlag != "" {
				if !slices.Contains(config.Themes, themeFlag) {
					return fail(`--theme must be "auto", "dark", or "light", got %q`, themeFlag)
				}
				theme = themeFlag
			}
			if theme == background.Auto {
				theme = background.Detect(nil, a.env.Getenv).Theme()
			}
			tf, err := config.Find(config.Colorschemes, schemeName)
			if err != nil {
				return fail("%v", err)
			}
			cs, err := colorscheme.Parse(tf.Path, tf.Data, theme)
			if err != nil {
				return fail("%v", err)
			}
			var names []string
			if len(args) >= 1 {
				names = []string{args[0]}
			} else {
				files, err := config.List(config.Rules)
				if err != nil {
					return fail("%v", err)
				}
				for _, f := range files {
					names = append(names, f.Name)
				}
			}
			depth := a.depth()
			var loader rules.ConfigLoader
			for _, name := range names {
				rf, err := loader.File(name)
				if err != nil {
					return fail("%v", err)
				}
				c, err := rf.Compile()
				if err != nil {
					return fail("%v", err)
				}
				fmt.Fprintf(a.env.Stdout, "== %s (colorscheme %s, %s theme)\n", name, cs.Name, cs.Theme)
				h := engine.New(c, cs.Starts(depth, c.Scopes), cfg.ANSI)
				var buf []byte
				for _, s := range rf.Sample {
					buf = h.Highlight(buf[:0], []byte(s))
					a.env.Stdout.Write(append(buf, '\n'))
				}
			}
			return nil
		},
	}
	c.Flags().StringVar(&themeFlag, "theme", "", "use this theme: auto, dark, or light")
	return c
}

func (a *app) detectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "detect <file>",
		Short: "Show which rule mgnt would pick for a file, and why",
		Long: `Reads the first lines of the file (never stdin) and prints how each rule scores
against them: which detect patterns and filenames globs matched, and which rule
mgnt would pick. Use it to tune the detect patterns of a rule.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			path := args[0]
			if path == "-" {
				return fail("detect reads a file, never stdin")
			}
			ls, err := readSample(path)
			if err != nil {
				return fail("%v", err)
			}
			dets, errs := rules.ConfigLoader{}.Detectors()
			for _, e := range errs {
				fmt.Fprintf(a.env.Stderr, "magenta: warning: %v\n", e)
			}
			res := detect.Choose(dets, ls, path)
			w := a.env.Stdout
			fmt.Fprintf(w, "file     %s\nsampled  %d line(s), a rule needs a score of at least %d\n\n", path, res.Sampled, res.Threshold)
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "RULE\tLINES\tBONUS\tTOTAL\tMATCHED BY")
			for _, s := range res.Scores {
				var by []string
				for _, p := range s.Patterns {
					by = append(by, fmt.Sprintf("detect /%s/ (%d)", p.Src, p.Lines))
				}
				if s.Glob != "" {
					by = append(by, fmt.Sprintf("filenames %q", s.Glob))
				}
				fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%s\n", s.Name, s.Lines, s.Bonus, s.Total, strings.Join(by, " and "))
			}
			tw.Flush()
			if res.Picked == detect.Fallback {
				fmt.Fprintf(w, "\nmgnt would pick: %s (no rule scored high enough)\n", res.Picked)
			} else {
				fmt.Fprintf(w, "\nmgnt would pick: %s\n", res.Picked)
			}
			if cfg, err := config.Load(); err == nil && cfg.ANSI == "keep" {
				fmt.Fprintln(w, `note: config.toml has ansi = "keep", so mgnt passes lines through without coloring them`)
			}
			if a.depth() == render.None {
				fmt.Fprintln(w, "note: color is off (NO_COLOR or TERM=dumb), so mgnt passes lines through without coloring them")
			}
			return nil
		},
	}
}

// readSample reads the first detect.SampleLines lines of a file, without ANSI
// escapes, the same way mgnt looks at them.
func readSample(path string) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.IsDir() {
		return nil, fmt.Errorf("%s is a directory", path)
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var ls [][]byte
	for len(ls) < detect.SampleLines && sc.Scan() {
		ls = append(ls, render.StripSGR(nil, sc.Bytes()))
	}
	return ls, nil
}

func (a *app) configCmd() *cobra.Command {
	c := group("config", "Show or change settings in config.toml")
	c.AddCommand(
		&cobra.Command{
			Use: "path", Short: "Print the path of config.toml", Args: cobra.NoArgs,
			Run: func(*cobra.Command, []string) { fmt.Fprintln(a.env.Stdout, config.Path()) },
		},
		&cobra.Command{
			Use: "get [key]", Short: "Print one setting, or all of them", Args: cobra.MaximumNArgs(1),
			RunE: func(_ *cobra.Command, args []string) error {
				cfg, err := config.Load()
				if err != nil {
					return fail("%v", err)
				}
				keys := config.SettingKeys()
				if len(args) == 1 {
					v, err := cfg.Get(args[0])
					if err != nil {
						return fail("%v", err)
					}
					fmt.Fprintln(a.env.Stdout, v)
					return nil
				}
				for _, k := range keys {
					v, _ := cfg.Get(k)
					fmt.Fprintf(a.env.Stdout, "%s = %s\n", k, v)
				}
				return nil
			},
		},
		setCmd(a),
	)
	return c
}

func (a *app) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Report the configuration, what mgnt detects, and how fast it starts",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			w := a.env.Stdout
			row := func(k, v string, more ...string) {
				fmt.Fprintf(w, "%-12s %s\n", k, v)
				for _, m := range more {
					fmt.Fprintf(w, "%-12s %s\n", "", m)
				}
			}
			row("version", cli.VersionString("magenta"))
			dir := config.Dir()
			state := "exists"
			if _, err := os.Stat(dir); err != nil {
				state = "does not exist yet, `magenta init` creates it"
			}
			row("config dir", dir+" ("+state+")")
			cfg, err := config.Load()
			if err != nil {
				row("config", "PROBLEM: "+err.Error())
				cfg = config.Default()
			} else if _, serr := os.Stat(config.Path()); serr != nil {
				row("config", config.Path()+" (not present: defaults in use)")
			} else {
				row("config", config.Path())
			}
			for _, d := range cfg.Deprecated {
				row("deprecated", d)
			}
			if _, err := os.Stat(filepath.Join(config.Dir(), "themes")); err == nil {
				row("deprecated", filepath.Join(config.Dir(), "themes")+" is not read any more, move its files to the colorschemes directory")
			}
			shown, theme := cfg.Theme, cfg.Theme
			if theme == background.Auto {
				r := background.Detect(nil, a.env.Getenv)
				theme = r.Theme()
				shown = "auto, using " + r.String() + " here, and mgnt also asks the terminal"
			}
			row("theme", shown)
			if tf, err := config.Find(config.Colorschemes, cfg.Colorscheme); err != nil {
				row("colorscheme", cfg.Colorscheme+": PROBLEM: "+err.Error())
			} else if _, err := colorscheme.Parse(tf.Path, tf.Data, theme); err != nil {
				row("colorscheme", cfg.Colorscheme+": PROBLEM: "+err.Error())
			} else {
				row("colorscheme", cfg.Colorscheme+" ("+source(tf)+")")
			}
			files, err := config.List(config.Rules)
			if err != nil {
				return fail("%v", err)
			}
			var lines []string
			for _, f := range files {
				s := source(f)
				if !f.Builtin {
					s += ": " + f.Path
				}
				lines = append(lines, fmt.Sprintf("%-14s %s", f.Name, s))
			}
			row("rules", fmt.Sprintf("%d rule file(s)", len(files)), lines...)
			_, perr := rules.ConfigLoader{}.Detectors()
			for _, e := range perr {
				row("", "PROBLEM: "+e.Error())
			}
			d := a.depth()
			row("color", fmt.Sprintf("%s (COLORTERM=%q TERM=%q NO_COLOR=%q)", d, a.env.Getenv("COLORTERM"), a.env.Getenv("TERM"), a.env.Getenv("NO_COLOR")))
			a.doctorFzf(row, cfg)

			t0 := time.Now()
			serr := mgnt.Startup("generic")
			el := time.Since(t0)
			if serr != nil {
				row("start-up", "PROBLEM: "+serr.Error())
			} else {
				row("start-up", fmt.Sprintf("%.1f ms (config, colorscheme, rule detection, and one rule, budget about 10 ms)", float64(el.Microseconds())/1000))
			}
			return nil
		},
	}
}

func (a *app) doctorFzf(row func(k, v string, more ...string), cfg config.Config) {
	path, err := a.env.LookPath("fzf")
	if err != nil {
		row("fzf", "not found on PATH: the / search is not available (mgnt only colors)")
		return
	}
	out, err := a.env.FzfVersion(path)
	if err != nil {
		row("fzf", path+" (could not run `fzf --version`: "+err.Error()+")")
		return
	}
	text := strings.TrimSpace(out)
	v, known := search.ParseVersion(text)
	more := search.Describe(v, known, cfg)
	row("fzf", path+" "+text, "the / search is available")
	for _, m := range more {
		row("", m)
	}
}

// setCmd is `config set <key> <value>`. Flag parsing stops after the key, so a
// value such as --exact (an fzf option) is a value and not an unknown flag.
func setCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Change a setting",
		Long:  "Changes a setting in config.toml. Keys: " + strings.Join(config.SettingKeys(), ", ") + ".",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			key, value := args[0], args[1]
			if scheme, theme, legacy := config.LegacyTheme(value); key == "theme" && legacy {
				// The old meaning of theme: a colorscheme.
				fmt.Fprintf(a.env.Stderr, "magenta: theme %q is deprecated, use `magenta config set colorscheme %s`\n", value, scheme)
				if theme != "" {
					if err := config.Set("theme", theme); err != nil {
						return fail("%v", err)
					}
					fmt.Fprintf(a.env.Stdout, "theme = %s\n", theme)
				}
				key, value = "colorscheme", scheme
			}
			if err := config.Set(key, value); err != nil {
				return fail("%v", err)
			}
			fmt.Fprintf(a.env.Stdout, "%s = %s\n", key, value)
			return nil
		},
	}
	c.Flags().SetInterspersed(false)
	return c
}
