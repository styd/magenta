package rules

import (
	"errors"
	"strings"
	"testing"

	"github.com/styd/magenta/internal/config"
)

func TestBuiltinRulesAreValid(t *testing.T) {
	for _, name := range config.BuiltinNames(config.Rules) {
		cf, err := config.Builtin(config.Rules, name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := Parse(cf.Path, cf.Data)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if f.Name != name {
			t.Errorf("%s: name field is %q", name, f.Name)
		}
		for _, e := range f.Validate() {
			t.Errorf("%v", e)
		}
		if len(f.Sample) == 0 {
			t.Errorf("%s: no sample lines", name)
		}
		if name != "generic" && len(f.Detect) == 0 {
			t.Errorf("%s: no detect patterns", name)
		}
	}
}

func TestExpectedBuiltins(t *testing.T) {
	want := "compiler,cron,generic,git-diff,git-log,go-test,jsonl,nginx-access,ping,rails,syslog,unicorn"
	if got := strings.Join(config.BuiltinNames(config.Rules), ","); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

const header = `name = "t"` + "\n"

func TestValidateErrors(t *testing.T) {
	tests := []struct {
		name, src string
		want      string // "line: message substring"
	}{
		{"bad regex", header + "[[context.default.rule]]\nregex = '(a'\nscope = \"x\"\n", "t.toml:3: invalid rule regex"},
		{"bad regex in second rule", header + "[[context.default.rule]]\nregex = 'a'\nscope = \"x\"\n\n[[context.default.rule]]\nregex = '[b'\nscope = \"x\"\n", "t.toml:7: invalid rule regex"},
		{"bad detect", "name = \"t\"\ndetect = [\n  'ok',\n  '(bad',\n]\n[[context.default.rule]]\nregex = 'a'\nscope = \"x\"\n", "t.toml:4: invalid detect regex"},
		{"no default", header + "[[context.other.rule]]\nregex = 'a'\nscope = \"x\"\n", `missing "default" context`},
		{"unknown context", header + "[[context.default.rule]]\nregex = 'a'\nscope = \"x\"\n[[context.default.enter]]\nregex = 'a'\nto = \"nowhere\"\n", `t.toml:7: enter in context "default" goes to unknown context "nowhere"`},
		{"unknown exit target", header + "[[context.default.rule]]\nregex = 'a'\nscope = \"x\"\n[[context.default.exit]]\nregex = 'a'\nto = \"nowhere\"\n", `unknown context "nowhere"`},
		{"captures mismatch", header + "[[context.default.rule]]\nregex = '(a)(b)'\ncaptures = [\"x\"]\n", "t.toml:4: regex has 2 capture groups but captures lists 1 scopes"},
		{"scope and captures", header + "[[context.default.rule]]\nregex = '(a)'\nscope = \"x\"\ncaptures = [\"x\"]\n", `either "scope" or "captures"`},
		{"no scope", header + "[[context.default.rule]]\nregex = 'a'\n", `needs "scope" or "captures"`},
		{"no regex", header + "[[context.default.rule]]\nscope = \"x\"\n", `needs "regex" or "prefix"`},
		{"bad scope", header + "[[context.default.rule]]\nregex = 'a'\nscope = \"Bad Scope\"\n", `t.toml:4: invalid scope "Bad Scope"`},
		{"exit both", header + "[[context.default.rule]]\nregex = 'a'\nscope = \"x\"\n[[context.default.exit]]\nregex = 'a'\nunless = 'b'\n", `either "regex" or "unless"`},
		{"self cycle", header + "[[context.default.rule]]\nregex = 'a'\nscope = \"x\"\n[[context.default.enter]]\nregex = 'a'\nto = \"default\"\n", "context cycle: default -> default"},
		{"cycle", header + "[[context.default.rule]]\nregex = 'a'\nscope = \"x\"\n[[context.default.enter]]\nregex = 'a'\nto = \"a\"\n[[context.a.enter]]\nregex = 'a'\nto = \"b\"\n[[context.b.enter]]\nregex = 'a'\nto = \"a\"\n", "context cycle: a -> b -> a"},
		{"unmatched sample", header + "sample = [\"zzz\"]\n[[context.default.rule]]\nregex = 'a'\nscope = \"x\"\n", `t.toml:2: sample line is not matched by any rule: "zzz"`},
		{"bad glob", "name = \"t\"\nfilenames = [\"[\"]\n[[context.default.rule]]\nregex = 'a'\nscope = \"x\"\n", "t.toml:2: invalid filenames glob"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := Parse("t.toml", []byte(tt.src))
			if err != nil {
				t.Fatal(err)
			}
			errs := f.Validate()
			for _, e := range errs {
				if strings.Contains(e.Error(), tt.want) {
					return
				}
			}
			t.Errorf("want %q in %v", tt.want, errs)
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"syntax", "name = \"t\"\n[context\nx = 1\n", "t.toml:2:"},
		{"no name", "detect = []\n", `t.toml: missing "name"`},
		{"unknown key", header + "color = 1\n", "t.toml:2: unknown key color"},
		{"unknown rule key", header + "[[context.default.rule]]\nregex = 'a'\nscop = \"x\"\n", "t.toml:4: unknown key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("t.toml", []byte(tt.src))
			var re *Error
			if !errors.As(err, &re) || !strings.HasPrefix(err.Error(), tt.want) {
				t.Errorf("got %v, want prefix %q", err, tt.want)
			}
		})
	}
}

func TestCompileScopes(t *testing.T) {
	f, err := Parse("t.toml", []byte(header+`
[[context.default.rule]]
regex = '(a)(b)'
captures = ["x.one", ""]
[[context.default.rule]]
prefix = "+"
scope = "x.two"
[[context.default.rule]]
regex = 'c'
scope = "x.one"
`))
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Scopes, ",") != "x.one,x.two" {
		t.Errorf("scopes %v", c.Scopes)
	}
	r := c.Contexts[0].Rules
	if r[0].Captures[0] != 0 || r[0].Captures[1] != -1 || r[1].Scope != 1 || r[2].Scope != 0 || r[1].Re != nil {
		t.Errorf("unexpected rules %+v", r)
	}
}

func TestDetectorOnlyCompilesDetect(t *testing.T) {
	// The rule regex is invalid, but a detector must not care.
	f, err := Parse("t.toml", []byte("name = \"t\"\ndetect = ['^x']\nfilenames = [\"*.log\"]\n[[context.default.rule]]\nregex = '(bad'\nscope = \"x\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := f.Detector()
	if err != nil {
		t.Fatal(err)
	}
	if g, ok := d.MatchFilename("/var/log/a.log"); !ok || g != "*.log" {
		t.Errorf("glob %q %v", g, ok)
	}
	if _, ok := d.MatchFilename("a.txt"); ok {
		t.Error("unexpected glob match")
	}
	if _, err := f.Compile(); err == nil {
		t.Error("Compile should fail")
	}
}
