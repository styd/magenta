package magentacli

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/styd/magenta/internal/cli"
)

var subcommands = []string{"init", "rule", "colorscheme", "theme", "preview", "detect", "config", "doctor", "completion", "help"}

// wrongBinary explains that magenta does not show logs. It goes to stderr.
func wrongBinary(env Env, first, example string) {
	if first != "" {
		fmt.Fprintln(env.Stderr, first)
	}
	fmt.Fprintln(env.Stderr, "magenta: this command manages rules and colorschemes, it does not show logs.")
	fmt.Fprintln(env.Stderr, "To color a log, use mgnt instead:")
	fmt.Fprintln(env.Stderr, "  "+example)
	fmt.Fprintln(env.Stderr, `Run "magenta --help" for what magenta can do.`)
}

// flagIs reports whether arg is the option name, with or without a =value.
func flagIs(arg, name string) bool {
	return arg == name || strings.HasPrefix(arg, name+"=")
}

// guard catches the ways people use magenta as if it were mgnt, before cobra
// sees the arguments. It never reads stdin. handled is false when the
// arguments are an ordinary magenta command line.
func guard(env Env, args []string) (code int, handled bool) {
	positional := ""
	// magenta preview has a --theme option of its own.
	previewing := slices.Contains(args, "preview")
	for _, a := range args {
		if a == "--" {
			break
		}
		if cli.MgntOnlyFlag(a) && !(previewing && flagIs(a, "--theme")) {
			name, _, _ := strings.Cut(a, "=")
			wrongBinary(env, fmt.Sprintf("magenta: %s: these options belong to mgnt.", name), "tail -f app.log | mgnt "+name)
			return 2, true
		}
		if positional == "" && !strings.HasPrefix(a, "-") {
			positional = a
		}
	}
	switch {
	case len(args) == 0 && !env.StdinIsTTY:
		// Something is piped in, and it is a log: do not wait for it.
		wrongBinary(env, "", "tail -f app.log | mgnt")
		return 2, true
	case positional != "" && !slices.Contains(subcommands, positional):
		if st, err := os.Stat(positional); err == nil && !st.IsDir() {
			wrongBinary(env, "", "mgnt "+positional)
			return 2, true
		}
	}
	return 0, false
}
