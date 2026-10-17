package magentacli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/styd/magenta/internal/colorscheme"
	"github.com/styd/magenta/internal/config"
	"github.com/styd/magenta/internal/rules"
)

// kindInfo describes one kind of user-editable file: rules or colorschemes.
type kindInfo struct {
	kind     config.Kind
	noun     string
	check    func(a *app, path string, data []byte) (errs, warns []string)
	template func(name string) []byte
}

var (
	ruleKind        = kindInfo{config.Rules, "rule", (*app).checkRule, ruleTemplate}
	colorschemeKind = kindInfo{config.Colorschemes, "colorscheme", (*app).checkColorscheme, colorschemeTemplate}
)

var nameOK = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func (a *app) kindCmd(k kindInfo) *cobra.Command {
	c := group(k.noun, fmt.Sprintf("List, show, create, edit, and check %ss", k.noun))
	c.AddCommand(
		&cobra.Command{
			Use: "list", Short: "List the " + k.noun + "s and where each comes from", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error { return a.list(k) },
		},
		&cobra.Command{
			Use: "show <name>", Short: "Print a " + k.noun + " file", Args: cobra.ExactArgs(1),
			RunE: func(_ *cobra.Command, args []string) error { return a.show(k, args[0]) },
		},
		&cobra.Command{
			Use: "new <name>", Short: "Create a new " + k.noun + " file from a template", Args: cobra.ExactArgs(1),
			RunE: func(_ *cobra.Command, args []string) error { return a.create(k, args[0]) },
		},
		&cobra.Command{
			Use: "edit <name>", Short: "Edit a " + k.noun + " in $EDITOR, then check it",
			Long: fmt.Sprintf("Opens your copy of the %[1]s in $VISUAL or $EDITOR, creating it from the built-in one first\nif needed, and runs `magenta %[1]s check` when the editor exits.", k.noun),
			Args: cobra.ExactArgs(1),
			RunE: func(_ *cobra.Command, args []string) error { return a.edit(k, args[0]) },
		},
		&cobra.Command{
			Use: "check [file]", Short: "Check a " + k.noun + " file, or every " + k.noun + " when none is given",
			Long: "Reports errors with file and line. For rules this also verifies that every\n`sample` line is matched by at least one rule. Exits 1 if anything is wrong.",
			Args: cobra.MaximumNArgs(1),
			RunE: func(_ *cobra.Command, args []string) error { return a.checkCmd(k, args) },
		},
	)
	if k.kind == config.Colorschemes {
		c.AddCommand(&cobra.Command{
			Use: "use <name>", Short: "Make a colorscheme the one mgnt uses", Args: cobra.ExactArgs(1),
			RunE: func(_ *cobra.Command, args []string) error { return a.useColorscheme(args[0]) },
		})
	}
	return c
}

func source(f config.File) string {
	switch {
	case f.Builtin:
		return "built-in"
	case f.Override:
		return "user (overrides built-in)"
	}
	return "user"
}

func (a *app) list(k kindInfo) error {
	files, err := config.List(k.kind)
	if err != nil {
		return fail("%v", err)
	}
	w := tabwriter.NewWriter(a.env.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSOURCE\tFILE")
	for _, f := range files {
		p := f.Path
		if f.Builtin {
			p = ""
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", f.Name, source(f), p)
	}
	return w.Flush()
}

func (a *app) show(k kindInfo, name string) error {
	f, err := config.Find(k.kind, name)
	if err != nil {
		return fail("%v", err)
	}
	_, err = a.env.Stdout.Write(f.Data)
	return err
}

func (a *app) writeUser(k kindInfo, name string, data []byte) (string, error) {
	dir := config.UserDir(k.kind)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, name+".toml")
	return p, os.WriteFile(p, data, 0o644)
}

func (a *app) create(k kindInfo, name string) error {
	if !nameOK.MatchString(name) {
		return fail("invalid %s name %q (use lowercase letters, digits, -, and _)", k.noun, name)
	}
	if f, err := config.Find(k.kind, name); err == nil {
		if f.Builtin {
			return fail("there is already a built-in %s called %q. Override it with `magenta %s edit %s`", k.noun, name, k.noun, name)
		}
		return fail("%s already exists: %s", k.noun, f.Path)
	}
	p, err := a.writeUser(k, name, k.template(name))
	if err != nil {
		return fail("%v", err)
	}
	fmt.Fprintf(a.env.Stdout, "created %s\nedit it with: magenta %s edit %s\n", p, k.noun, name)
	return a.report(k, p, k.template(name))
}

func (a *app) editor() string {
	for _, v := range []string{"VISUAL", "EDITOR"} {
		if e := a.env.Getenv(v); e != "" {
			return e
		}
	}
	return "vi"
}

func (a *app) edit(k kindInfo, name string) error {
	f, err := config.Find(k.kind, name)
	if err != nil {
		return fail("%v. Create one with `magenta %s new %s`", err, k.noun, name)
	}
	path := f.Path
	if f.Builtin {
		if path, err = a.writeUser(k, name, f.Data); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(a.env.Stderr, "magenta: copied the built-in %s to %s\n", k.noun, path)
	}
	if err := a.env.RunEditor(a.editor(), path); err != nil {
		return fail("editor %q failed: %v", a.editor(), err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fail("%v", err)
	}
	return a.report(k, path, data)
}

// report checks one file and prints the outcome. It fails if there are errors.
func (a *app) report(k kindInfo, path string, data []byte) error {
	errs, warns := k.check(a, path, data)
	for _, w := range warns {
		fmt.Fprintf(a.env.Stderr, "%s: warning: %s\n", path, w)
	}
	for _, e := range errs {
		fmt.Fprintln(a.env.Stderr, e)
	}
	if len(errs) > 0 {
		return &exitError{1, fmt.Sprintf("%d problem(s) in %s", len(errs), path)}
	}
	fmt.Fprintf(a.env.Stdout, "ok: %s\n", path)
	return nil
}

func (a *app) checkCmd(k kindInfo, args []string) error {
	if len(args) == 1 {
		path, data, err := a.resolve(k, args[0])
		if err != nil {
			return fail("%v", err)
		}
		return a.report(k, path, data)
	}
	files, err := config.List(k.kind)
	if err != nil {
		return fail("%v", err)
	}
	bad := 0
	for _, f := range files {
		if a.report(k, f.Path, f.Data) != nil {
			bad++
		}
	}
	if bad > 0 {
		return &exitError{1, ""}
	}
	return nil
}

// resolve turns the argument of check into a file: a path if one exists,
// otherwise a rule or colorscheme name.
func (a *app) resolve(k kindInfo, arg string) (string, []byte, error) {
	if st, err := os.Stat(arg); err == nil && !st.IsDir() {
		data, err := os.ReadFile(arg)
		return arg, data, err
	}
	f, err := config.Find(k.kind, arg)
	if err != nil {
		return "", nil, fmt.Errorf("%q is neither a file nor a %s name", arg, k.noun)
	}
	return f.Path, f.Data, nil
}

func (a *app) useColorscheme(name string) error {
	if a.oldTheme {
		return a.useOldTheme(name)
	}
	f, err := config.Find(config.Colorschemes, name)
	if err != nil {
		return fail("%v", err)
	}
	if err := colorscheme.Check(f.Path, f.Data); err != nil {
		return fail("%v", err)
	}
	if err := config.Set("colorscheme", name); err != nil {
		return fail("%v", err)
	}
	fmt.Fprintf(a.env.Stdout, "colorscheme set to %s (%s)\n", name, config.Path())
	return nil
}

// useOldTheme is `magenta theme use <name>` as it was typed before colorschemes
// had their own command, when the name was a colorscheme, magenta-dark,
// magenta-light, or auto. It sets what the name means now.
func (a *app) useOldTheme(name string) error {
	if slices.Contains(config.Themes, name) {
		if err := config.Set("theme", name); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(a.env.Stderr, "magenta: `magenta theme use %[1]s` is deprecated, use `magenta config set theme %[1]s`\n", name)
		fmt.Fprintf(a.env.Stdout, "theme set to %s (%s)\n", name, config.Path())
		return nil
	}
	scheme, theme, _ := config.LegacyTheme(name)
	if theme != "" {
		if err := config.Set("theme", theme); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(a.env.Stderr, "magenta: `magenta theme use %s` is deprecated, use `magenta colorscheme use %s` and `magenta config set theme %s`\n", name, scheme, theme)
	}
	a.oldTheme = false
	return a.useColorscheme(scheme)
}

// colorschemesForVocabulary returns the colorschemes a scope may be styled by:
// the default one and the configured one, both in the dark theme.
func (a *app) colorschemesForVocabulary() []*colorscheme.Colorscheme {
	var out []*colorscheme.Colorscheme
	names := []string{defaultColorscheme}
	if cfg, err := config.Load(); err == nil && cfg.Colorscheme != defaultColorscheme {
		names = append(names, cfg.Colorscheme)
	}
	for _, n := range names {
		if f, err := config.Find(config.Colorschemes, n); err == nil {
			if cs, err := colorscheme.Parse(f.Path, f.Data, colorscheme.Dark); err == nil {
				out = append(out, cs)
			}
		}
	}
	return out
}

// defaultColorscheme is the built-in colorscheme that scopes are checked against.
const defaultColorscheme = "magenta"

func (a *app) checkRule(path string, data []byte) (errs, warns []string) {
	f, err := rules.Parse(path, data)
	if err != nil {
		return []string{err.Error()}, nil
	}
	for _, e := range f.Validate() {
		errs = append(errs, e.Error())
	}
	if len(errs) > 0 {
		return errs, nil
	}
	c, err := f.Compile()
	if err != nil {
		return []string{err.Error()}, nil
	}
	schemes := a.colorschemesForVocabulary()
	for _, sc := range c.Scopes {
		styled := false
		for _, cs := range schemes {
			if _, ok := cs.Resolve(sc); ok {
				styled = true
			}
		}
		if !styled {
			warns = append(warns, fmt.Sprintf("scope %q has no style in the magenta colorscheme or yours, so it will not be colored", sc))
		}
	}
	return nil, warns
}

func (a *app) checkColorscheme(path string, data []byte) (errs, warns []string) {
	if err := colorscheme.Check(path, data); err != nil {
		return []string{err.Error()}, nil
	}
	return nil, nil
}

func ruleTemplate(name string) []byte {
	q := regexp.QuoteMeta(name)
	return []byte(fmt.Sprintf(`# Rule file for %[1]s logs. Edit it, then run: magenta rule check %[1]s
# See what it looks like with: magenta preview %[1]s
name = "%[1]s"

# mgnt picks the rule whose detect patterns match the most of the first lines
# of a log. "magenta detect <file>" shows how a file scores.
detect = ['^%[2]s ']

# Optional file name globs. A match is a strong hint for detection.
filenames = ["%[1]s*.log"]

# Example lines. "magenta rule check" verifies that a rule matches each one.
sample = ['%[1]s started worker=1 in 35ms, see https://example.com/x']

# Earlier rules win where matches overlap. A rule has a "scope" for the whole
# match, or "captures" with one scope per capture group.
[[context.default.rule]]
regex = '^(%[2]s)\b'
captures = ["key"]

[[context.default.rule]]
regex = '\b([a-z_][a-z0-9_]*)='
captures = ["key"]

[[context.default.rule]]
regex = '\b\d+(?:\.\d+)?(?:ms|s)?\b'
scope = "number"

[[context.default.rule]]
regex = '\b[a-z][a-z0-9+.-]*://[^\s"<>)\]]+'
scope = "url"
`, name, q))
}

func colorschemeTemplate(name string) []byte {
	f, err := config.Builtin(config.Colorschemes, defaultColorscheme)
	if err != nil {
		return []byte("name = \"" + name + "\"\n")
	}
	body := strings.Replace(string(f.Data), `name = "`+defaultColorscheme+`"`, `name = "`+name+`"`, 1)
	// Drop the built-in's own header comment.
	for strings.HasPrefix(body, "#") {
		_, body, _ = strings.Cut(body, "\n")
	}
	return []byte("# Colorscheme " + name + ", started from the magenta colorscheme.\n" +
		"# It has a palette for the dark theme and one for the light theme.\n" +
		"# Colors are hex (#c3a6ff), 0-255 indexes, ANSI names (bright_red) or palette names.\n" +
		"# A scope also styles its sub-scopes: \"string\" covers \"string.quoted\".\n" +
		"# Use it with: magenta colorscheme use " + name + "\n" + body)
}
