package cli

import (
	"strings"
	"testing"
)

func TestMgntOnlyFlag(t *testing.T) {
	for arg, want := range map[string]bool{"--no-interactive": true, "--no-interactive=true": true, "--colorscheme": true, "--theme": true, "--ansi": true, "--buffer": true, "--fzf-opts": true, "--no-preview": true, "--set": true, "--set=a=b": true, "--verbose": false, "no-interactive": false} {
		if got := MgntOnlyFlag(arg); got != want {
			t.Errorf("%q: %v", arg, got)
		}
	}
}

func TestVersionString(t *testing.T) {
	if s := VersionString("mgnt"); !strings.HasPrefix(s, "mgnt ") || !strings.Contains(s, "commit") {
		t.Error(s)
	}
}

func TestVersionStringUsesLdflags(t *testing.T) {
	oldV, oldC, oldD := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = oldV, oldC, oldD })
	Version, Commit, Date = "1.2.3", "abc1234", "2026-01-02"
	if got, want := VersionString("magenta"), "magenta 1.2.3 (commit abc1234, built 2026-01-02)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAdminWords(t *testing.T) {
	for _, w := range AdminWords {
		if !IsAdminWord(w) {
			t.Errorf("%q is not an admin word", w)
		}
	}
	if IsAdminWord("app.log") {
		t.Error("app.log is not an admin word")
	}
	if got := AdminCommand("theme"); got != "colorscheme" {
		t.Errorf("theme maps to %q", got)
	}
	if got := AdminCommand("doctor"); got != "doctor" {
		t.Errorf("doctor maps to %q", got)
	}
}
