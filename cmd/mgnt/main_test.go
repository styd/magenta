package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestMain lets the test binary stand in for mgnt: with this variable set it
// runs main instead of the tests.
func TestMain(m *testing.M) {
	if os.Getenv("MGNT_TEST_RUN_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

func run(t *testing.T, stdin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "MGNT_TEST_RUN_MAIN=1", "XDG_CONFIG_HOME="+t.TempDir())
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
	if code != 0 || !strings.HasPrefix(out, "mgnt ") {
		t.Errorf("exit %d, output %q", code, out)
	}
}

func TestFiltersStdin(t *testing.T) {
	out, code := run(t, "hello\n", "--no-interactive")
	if code != 0 || !strings.Contains(out, "hello") {
		t.Errorf("exit %d, output %q", code, out)
	}
}

func TestMissingFileFails(t *testing.T) {
	if _, code := run(t, "", "/no/such/file.log"); code == 0 {
		t.Error("a missing file should give a non-zero exit code")
	}
}
