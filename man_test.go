package magenta_test

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/styd/magenta/internal/magentacli"
	"github.com/styd/magenta/internal/mgnt"
)

// roff writes a hyphen as \- in a man page.
func roff(s string) string { return strings.ReplaceAll(s, "-", `\-`) }

func manPage(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("man/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestMgntManPageDocumentsEveryFlag(t *testing.T) {
	var out bytes.Buffer
	env := mgnt.Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out, Getenv: func(string) string { return "" }}
	if code := mgnt.Run(env, []string{"--help"}); code != 0 {
		t.Fatalf("mgnt --help exited with %d", code)
	}
	page := manPage(t, "mgnt.1")
	flags := regexp.MustCompile(`(?m)^\s+(?:-\w, )?--([a-z-]+)`).FindAllStringSubmatch(out.String(), -1)
	if len(flags) == 0 {
		t.Fatal("no flags found in the help text")
	}
	for _, m := range flags {
		if !strings.Contains(page, roff("--"+m[1])) {
			t.Errorf("mgnt.1 does not document --%s", m[1])
		}
	}
}

func TestMagentaManPageDocumentsEveryCommand(t *testing.T) {
	page := manPage(t, "magenta.1")
	var walk func(path []string)
	walk = func(path []string) {
		var out bytes.Buffer
		env := magentacli.Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out, Getenv: func(string) string { return "" }}
		if code := magentacli.Run(env, append(append([]string{}, path...), "--help")); code != 0 {
			t.Fatalf("magenta %s --help exited with %d", strings.Join(path, " "), code)
		}
		_, list, ok := strings.Cut(out.String(), "Available Commands:\n")
		if !ok {
			return
		}
		list, _, _ = strings.Cut(list, "\n\n")
		for _, line := range strings.Split(list, "\n") {
			name := strings.Fields(line)[0]
			if name == "help" {
				continue
			}
			full := append(append([]string{}, path...), name)
			if strings.Join(full, " ") == "completion" {
				continue // documented as one entry for every shell
			}
			if !strings.Contains(page, "magenta "+strings.Join(full, " ")) && !strings.Contains(page, "magenta "+strings.Join(path, " ")+" "+name) {
				t.Errorf("magenta.1 does not document `magenta %s`", strings.Join(full, " "))
			}
			walk(full)
		}
	}
	walk(nil)
}
