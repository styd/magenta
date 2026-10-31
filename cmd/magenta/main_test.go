package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestMain lets the test binary stand in for magenta: with this variable set it
// runs main instead of the tests.
func TestMain(m *testing.M) {
	if os.Getenv("MAGENTA_TEST_RUN_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

func run(t *testing.T, stdin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "MAGENTA_TEST_RUN_MAIN=1", "XDG_CONFIG_HOME="+t.TempDir())
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	}
	t.Fatal(err)
	return "", -1
}

func TestVersion(t *testing.T) {
	out, code := run(t, "", "--version")
	if code != 0 || !strings.HasPrefix(out, "magenta ") {
		t.Errorf("exit %d, output %q", code, out)
	}
}

func TestListsColorschemes(t *testing.T) {
	out, code := run(t, "", "colorscheme", "list")
	if code != 0 || !strings.Contains(out, "mono") {
		t.Errorf("exit %d, output %q", code, out)
	}
}

func TestUnknownCommandFails(t *testing.T) {
	if _, code := run(t, "", "no-such-command"); code == 0 {
		t.Error("an unknown command should give a non-zero exit code")
	}
}
