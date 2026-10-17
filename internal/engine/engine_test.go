package engine

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/styd/magenta"
	"github.com/styd/magenta/internal/colorscheme"
	"github.com/styd/magenta/internal/config"
	"github.com/styd/magenta/internal/render"
	"github.com/styd/magenta/internal/rules"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func compileSource(t testing.TB, src string) *rules.Compiled {
	t.Helper()
	f, err := rules.Parse("test.toml", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.Compile()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func builtinRule(t testing.TB, name string) (*rules.File, *rules.Compiled) {
	t.Helper()
	cf, err := config.Builtin(config.Rules, name)
	if err != nil {
		t.Fatal(err)
	}
	f, err := rules.Parse(cf.Path, cf.Data)
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.Compile()
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

func builtinColorscheme(t testing.TB, name, theme string) *colorscheme.Colorscheme {
	t.Helper()
	data, err := magenta.FS.ReadFile("colorschemes/" + name + ".toml")
	if err != nil {
		t.Fatal(err)
	}
	cs, err := colorscheme.Parse(name, data, theme)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func highlighter(t testing.TB, rule, scheme, theme string, depth render.Depth) *Highlighter {
	t.Helper()
	_, c := builtinRule(t, rule)
	return New(c, builtinColorscheme(t, scheme, theme).Starts(depth, c.Scopes), "strip")
}

// run highlights every line of in (split on \n) and returns the output.
func run(h *Highlighter, in []byte) []byte {
	var out []byte
	lines := bytes.Split(in, []byte("\n"))
	for i, l := range lines {
		out = h.Highlight(out, l)
		if i < len(lines)-1 {
			out = append(out, '\n')
		}
	}
	return out
}

func visible(b []byte) string { return string(render.StripSGR(nil, b)) }

// TestGolden colors the log of every built-in rule with the magenta colorscheme
// in both themes, and compares it with testdata/<rule>/out.<theme>.ansi.
func TestGolden(t *testing.T) {
	for _, name := range config.BuiltinNames(config.Rules) {
		for _, theme := range colorscheme.Themes {
			t.Run(name+"/"+theme, func(t *testing.T) {
				dir := filepath.Join("..", "..", "testdata", name)
				in, err := os.ReadFile(filepath.Join(dir, "in.log"))
				if err != nil {
					t.Fatal(err)
				}
				got := run(highlighter(t, name, "magenta", theme, render.TrueColor), in)
				if visible(got) != string(in) {
					t.Fatal("output without escapes differs from the input")
				}
				golden := filepath.Join(dir, "out."+theme+".ansi")
				if *update {
					if err := os.WriteFile(golden, got, 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatalf("%v (run with -update to create it)", err)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("output differs from %s (run with -update after checking the change)\n got: %q\nwant: %q", golden, got, want)
				}
			})
		}
	}
}

// TestSamplesAreColored checks that the byte-preservation invariant holds on
// every sample line of every rule file, and that each sample gets some color.
func TestSamplesAreColored(t *testing.T) {
	for _, name := range config.BuiltinNames(config.Rules) {
		f, _ := builtinRule(t, name)
		h := highlighter(t, name, "magenta", colorscheme.Dark, render.TrueColor)
		for _, s := range f.Sample {
			out := h.Highlight(nil, []byte(s))
			if visible(out) != s {
				t.Errorf("%s: sample changed: %q", name, s)
			}
			if bytes.Equal(out, []byte(s)) {
				t.Errorf("%s: sample got no color: %q", name, s)
			}
		}
	}
}

func TestInvariantAcrossColorschemesAndDepths(t *testing.T) {
	in, err := os.ReadFile("../../testdata/rails/in.log")
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range [][2]string{{"magenta", colorscheme.Dark}, {"magenta", colorscheme.Light}, {"mono", colorscheme.Dark}} {
		for _, d := range []render.Depth{render.None, render.Basic16, render.Palette256, render.TrueColor} {
			h := highlighter(t, "rails", th[0], th[1], d)
			if got := visible(run(h, in)); got != string(in) {
				t.Errorf("%s %s/%v: invariant broken", th[0], th[1], d)
			}
		}
	}
}

func TestNoColorMeansNoEscapes(t *testing.T) {
	in, _ := os.ReadFile("../../testdata/generic/in.log")
	out := run(highlighter(t, "generic", "magenta", colorscheme.Dark, render.None), in)
	if !bytes.Equal(out, in) {
		t.Error("color off must pass the text through unchanged")
	}
}

func TestEarlierRuleWinsOverlap(t *testing.T) {
	c := compileSource(t, `name = "t"
[[context.default.rule]]
regex = 'bc'
scope = "first"
[[context.default.rule]]
regex = 'abcd'
scope = "second"
`)
	h := New(c, []string{"<1>", "<2>"}, "strip")
	got := string(h.Highlight(nil, []byte("xabcdx")))
	want := "x" + "<2>a" + render.Reset + "<1>bc" + render.Reset + "<2>d" + render.Reset + "x"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStopAndPrefix(t *testing.T) {
	c := compileSource(t, `name = "t"
[[context.default.rule]]
prefix = "++"
scope = "hdr"
stop = true
[[context.default.rule]]
prefix = "+"
scope = "add"
[[context.default.rule]]
regex = '\d+'
scope = "num"
`)
	h := New(c, []string{"<h>", "<a>", "<n>"}, "strip")
	for in, want := range map[string]string{
		"++ 12": "<h>++ 12" + render.Reset, // stop: the number rule does not run
		"+ 12":  "<a>+ 12" + render.Reset,  // whole-line scope already holds every byte
		"x 12":  "x <n>12" + render.Reset,
		"":      "",
	} {
		if got := string(h.Highlight(nil, []byte(in))); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestStateMachineReprocessesExitLine(t *testing.T) {
	c := compileSource(t, `name = "t"
[[context.default.rule]]
regex = '^Trace.*'
scope = "head"
[[context.default.enter]]
regex = '^Trace'
to = "stack"
[[context.stack.rule]]
regex = '^\s+at .*'
scope = "frame"
[[context.stack.exit]]
unless = '^\s+at '
`)
	h := New(c, []string{"<h>", "<f>"}, "strip")
	lines := []struct{ in, want string }{
		{"plain", "plain"},
		{"Traceback", "<h>Traceback" + render.Reset},
		{"  at a.rb:1", "<f>  at a.rb:1" + render.Reset},
		{"  at b.rb:2", "<f>  at b.rb:2" + render.Reset},
		// leaves "stack", is processed again in "default", and enters again
		{"Trace again", "<h>Trace again" + render.Reset},
		{"  at c.rb:3", "<f>  at c.rb:3" + render.Reset},
		{"done", "done"},
		{"  at d.rb:4", "  at d.rb:4"}, // back in default: not a frame
	}
	for _, l := range lines {
		if got := string(h.Highlight(nil, []byte(l.in))); got != l.want {
			t.Errorf("%q: got %q, want %q", l.in, got, l.want)
		}
	}
	h.Reset()
	if got := string(h.Highlight(nil, []byte("  at x"))); got != "  at x" {
		t.Errorf("after Reset: %q", got)
	}
}

func TestGitDiffHunkContext(t *testing.T) {
	h := highlighter(t, "git-diff", "magenta", colorscheme.Dark, render.TrueColor)
	out := visible(run(h, []byte("--- a/f\n+++ b/f\n@@ -1 +1 @@\n--- removed dashes\n+++ added pluses\ndiff --git a/g b/g\n--- a/g")))
	if !strings.Contains(out, "--- removed dashes") {
		t.Fatal(out)
	}
	// Inside a hunk, "--- x" is a removed line and uses the removed style. The
	// file header after the next "diff --git" is a header again.
	start := builtinColorscheme(t, "magenta", colorscheme.Dark)
	rm, _ := start.Resolve("diff.removed")
	hd, _ := start.Resolve("diff.header")
	h.Reset()
	lines := bytes.Split([]byte("@@ -1 +1 @@\n--- x\ndiff --git a/g b/g\n--- a/g"), []byte("\n"))
	var got [][]byte
	for _, l := range lines {
		got = append(got, h.Highlight(nil, l))
	}
	if !bytes.HasPrefix(got[1], []byte(rm.Start(render.TrueColor))) {
		t.Errorf("hunk line not styled as removed: %q", got[1])
	}
	if !bytes.HasPrefix(got[3], []byte(hd.Start(render.TrueColor))) {
		t.Errorf("header after hunk not styled as header: %q", got[3])
	}
}

func TestPassthroughCases(t *testing.T) {
	h := highlighter(t, "generic", "magenta", colorscheme.Dark, render.TrueColor)
	// Longer than MaxLine: untouched, even though it holds a number.
	long := append(bytes.Repeat([]byte("12 "), MaxLine/3+10), 'x')
	if out := h.Highlight(nil, long); !bytes.Equal(out, long) {
		t.Error("long line was altered")
	}
	// ansi = keep: untouched.
	_, c := builtinRule(t, "generic")
	keep := New(c, builtinColorscheme(t, "magenta", colorscheme.Dark).Starts(render.TrueColor, c.Scopes), "keep")
	in := []byte("\x1b[31mERROR 12\x1b[0m")
	if out := keep.Highlight(nil, in); !bytes.Equal(out, in) {
		t.Errorf("keep mode altered the line: %q", out)
	}
	// ansi = strip: the old color is replaced by ours. Text is preserved.
	out := h.Highlight(nil, in)
	if bytes.Contains(out, []byte("\x1b[31m")) || visible(out) != visible(in) {
		t.Errorf("strip mode: %q", out)
	}
	// CRLF keeps the carriage return after the colors.
	out = h.Highlight(nil, []byte("ERROR 12\r"))
	if !bytes.HasSuffix(out, []byte(render.Reset+"\r")) && !bytes.HasSuffix(out, []byte("12"+render.Reset+"\r")) {
		t.Errorf("CR lost: %q", out)
	}
	if visible(out) != "ERROR 12\r" {
		t.Errorf("CR line changed: %q", out)
	}
}

func TestNoAllocationsOnPlainLine(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector allocates")
	}
	h := highlighter(t, "git-diff", "magenta", colorscheme.Dark, render.TrueColor)
	line := []byte("nothing to see here")
	dst := make([]byte, 0, 256)
	h.Highlight(dst, line) // grow internal buffers
	if n := testing.AllocsPerRun(100, func() { h.Highlight(dst[:0], line) }); n != 0 {
		t.Errorf("%v allocations for a line with no matches", n)
	}
}

func FuzzHighlight(f *testing.F) {
	names := config.BuiltinNames(config.Rules)
	for _, n := range names {
		cf, _ := config.Builtin(config.Rules, n)
		rf, _ := rules.Parse(cf.Path, cf.Data)
		for _, s := range rf.Sample {
			f.Add(s, uint8(len(n)))
		}
	}
	f.Add("\xff\xfe\x00 invalid \x1b[31m utf8 \x1b[", uint8(0))
	f.Add(strings.Repeat("a1 ", 5000), uint8(1))
	hs := make([]*Highlighter, len(names))
	for i, n := range names {
		hs[i] = highlighter(f, n, "magenta", colorscheme.Dark, render.TrueColor)
	}
	f.Fuzz(func(t *testing.T, line string, which uint8) {
		h := hs[int(which)%len(hs)]
		if strings.ContainsRune(line, '\n') {
			return
		}
		out := h.Highlight(nil, []byte(line))
		if got, want := visible(out), visible([]byte(line)); got != want {
			t.Fatalf("invariant broken\n in: %q\nout: %q", line, out)
		}
	})
}

func generatedLog(n int) []byte {
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		switch i % 4 {
		case 0:
			b.WriteString("2026-10-06 10:00:01,123 INFO [main] request id=42 from 10.0.0.12 took 35ms path=/api/v1/users\n")
		case 1:
			b.WriteString("2026-10-06 10:00:02,456 ERROR [pool-1] failed https://example.com/x?y=1 uuid 3f2a8c1e-0b1d-4c8e-9a53-1f2e3d4c5b6a\n")
		case 2:
			b.WriteString("a line without anything special that goes on for a while to be realistic\n")
		default:
			b.WriteString("{\"level\":\"warn\",\"msg\":\"slow\",\"ms\":420}\n")
		}
	}
	return b.Bytes()
}

func BenchmarkHighlight(b *testing.B) {
	for _, name := range []string{"generic", "git-diff", "jsonl"} {
		b.Run(name, func(b *testing.B) {
			h := highlighter(b, name, "magenta", colorscheme.Dark, render.TrueColor)
			lines := bytes.Split(generatedLog(10000), []byte("\n"))
			var dst []byte
			var total int
			for _, l := range lines {
				total += len(l) + 1
			}
			b.SetBytes(int64(total))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for _, l := range lines {
					dst = h.Highlight(dst[:0], l)
				}
			}
		})
	}
}
