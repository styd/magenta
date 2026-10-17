// Package magentacli is the magenta command: it manages mgnt's rule files,
// colorschemes and settings. It never shows logs. That is what mgnt is for.
package magentacli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/styd/magenta/internal/cli"
)

// Env is everything magenta takes from the outside world, so tests can replace it.
type Env struct {
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	Getenv     func(string) string
	StdinIsTTY bool
	// RunEditor opens file in the user's editor and waits for it to exit.
	RunEditor func(editor, file string) error
	LookPath  func(string) (string, error)
	// FzfVersion returns the output of `fzf --version`.
	FzfVersion func(path string) (string, error)
}

// DefaultEnv describes the real process.
func DefaultEnv() Env {
	return Env{
		Stdin:      os.Stdin,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		Getenv:     os.Getenv,
		StdinIsTTY: term.IsTerminal(int(os.Stdin.Fd())),
		RunEditor:  runEditor,
		LookPath:   exec.LookPath,
		FzfVersion: func(path string) (string, error) {
			out, err := exec.Command(path, "--version").Output()
			return string(out), err
		},
	}
}

func runEditor(editor, file string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", editor+` "`+file+`"`)
	} else {
		// Through the shell, so that EDITOR="code -w" works.
		cmd = exec.Command("sh", "-c", editor+` "$1"`, "sh", file)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// exitError ends a command with a status and, if msg is not empty, a message.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func fail(format string, args ...any) error {
	return &exitError{1, fmt.Sprintf(format, args...)}
}

type app struct {
	env Env
	// oldTheme says the command was typed "magenta theme ...", the old name of
	// "magenta colorscheme ...", so "use" also takes the old values.
	oldTheme bool
}

// Run runs magenta and returns the exit status.
func Run(env Env, args []string) int {
	if code, handled := guard(env, args); handled {
		return code
	}
	a := &app{env: env}
	if len(args) > 0 && args[0] == "theme" {
		fmt.Fprintln(env.Stderr, "magenta: `magenta theme` is deprecated, use `magenta colorscheme`")
		args = append([]string{"colorscheme"}, args[1:]...)
		a.oldTheme = true
	}
	root := a.root()
	root.SetArgs(args)
	root.SetOut(env.Stdout)
	root.SetErr(env.Stderr)
	err := root.Execute()
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.msg != "" {
			fmt.Fprintf(env.Stderr, "magenta: %s\n", ee.msg)
		}
		return ee.code
	}
	// A usage error from cobra: unknown command, flag, or argument count.
	msg := err.Error()
	fmt.Fprintf(env.Stderr, "magenta: %s\n", msg)
	if strings.HasPrefix(msg, "unknown command") {
		fmt.Fprintln(env.Stderr, "To view logs, use mgnt.")
	} else {
		fmt.Fprintln(env.Stderr, `Run "magenta --help" for usage.`)
	}
	return 2
}

func (a *app) root() *cobra.Command {
	root := &cobra.Command{
		Use:   "magenta",
		Short: "Manage mgnt's rules, colorschemes, and settings",
		Long: `magenta manages the rule files, colorschemes, and settings that mgnt uses.
It does not show logs: to color a log, use mgnt (for example: tail -f app.log | mgnt).

Files live in the config directory (see "magenta doctor"). A user file with the
same name as a built-in one overrides it. New names are added.`,
		Version:       cli.VersionString("magenta"),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate("{{.Version}}\n")
	root.CompletionOptions.HiddenDefaultCmd = true
	root.AddCommand(
		a.initCmd(),
		a.kindCmd(ruleKind),
		a.kindCmd(colorschemeKind),
		a.previewCmd(),
		a.detectCmd(),
		a.configCmd(),
		a.doctorCmd(),
	)
	return root
}

// group makes a command that only groups subcommands, with "did you mean"
// for typos.
func group(use, short string) *cobra.Command {
	c := &cobra.Command{Use: use, Short: short, SuggestionsMinimumDistance: 2}
	c.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return nil
		}
		msg := fmt.Sprintf("unknown command %q for %q", args[0], cmd.CommandPath())
		if s := cmd.SuggestionsFor(args[0]); len(s) > 0 {
			msg += "\n\nDid you mean this?\n\t" + strings.Join(s, "\n\t")
		}
		return errors.New(msg)
	}
	c.RunE = func(cmd *cobra.Command, args []string) error { return cmd.Help() }
	return c
}
