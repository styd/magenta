// Package cli holds what mgnt and magenta share: telling which options and
// words belong to which command, and the version string.
package cli

import (
	"fmt"
	"runtime/debug"
	"slices"
	"strings"
)

// Set with -ldflags "-X github.com/styd/magenta/internal/cli.Version=..." etc.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// VersionString describes the build. Binaries made by `go install` carry no
// ldflags, so the module version and VCS data are used when present.
func VersionString(prog string) string {
	v, c, d := Version, Commit, Date
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			v = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch {
			case s.Key == "vcs.revision" && c == "unknown":
				c = s.Value
				if len(c) > 12 {
					c = c[:12]
				}
			case s.Key == "vcs.time" && d == "unknown":
				d = s.Value
			}
		}
	}
	return fmt.Sprintf("%s %s (commit %s, built %s)", prog, v, c, d)
}

// flagName returns the name in "--name" or "--name=value", or "".
func flagName(arg string) string {
	name, ok := strings.CutPrefix(arg, "--")
	if !ok || name == "" {
		return ""
	}
	name, _, _ = strings.Cut(name, "=")
	return name
}

// AdminWords are the magenta subcommands mgnt recognizes by name.
//
// "theme" is the old name of "colorscheme", and it still works.
var AdminWords = []string{"init", "rule", "colorscheme", "theme", "config", "doctor"}

// AdminCommand is the magenta command to suggest for the admin word w.
func AdminCommand(w string) string {
	if w == "theme" {
		return "colorscheme"
	}
	return w
}

// IsAdminWord reports whether w is a magenta subcommand name.
func IsAdminWord(w string) bool { return slices.Contains(AdminWords, w) }

// MgntOnlyFlag reports whether arg is an option that belongs to mgnt.
func MgntOnlyFlag(arg string) bool {
	switch flagName(arg) {
	case "no-interactive", "set", "colorscheme", "theme", "ansi", "buffer", "fzf-opts", "no-preview":
		return true
	}
	return false
}
