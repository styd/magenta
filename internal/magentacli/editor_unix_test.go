//go:build !windows

package magentacli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRunEditorUsesTheShell checks that an EDITOR with arguments and a file
// name with spaces both work, by using a shell command as the editor.
func TestRunEditorUsesTheShell(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a file.toml")
	if err := os.WriteFile(file, []byte("start\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runEditor(`printf 'edited\n' >>`, file); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(file); string(data) != "start\nedited\n" {
		t.Errorf("got %q", data)
	}
	if err := runEditor("exit 3 #", file); err == nil {
		t.Error("a failing editor must be reported")
	}
}
