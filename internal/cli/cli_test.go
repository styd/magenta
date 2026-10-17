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
