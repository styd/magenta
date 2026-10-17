package pipe

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/styd/magenta"
	"github.com/styd/magenta/internal/colorscheme"
	"github.com/styd/magenta/internal/detect"
	"github.com/styd/magenta/internal/lines"
	"github.com/styd/magenta/internal/render"
	"github.com/styd/magenta/internal/rules"
)

func testConfig(t testing.TB, depth render.Depth, ansi, filename string) Config {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	data, err := magenta.FS.ReadFile("colorschemes/magenta.toml")
	if err != nil {
		t.Fatal(err)
	}
	cs, err := colorscheme.Parse("magenta", data, colorscheme.Dark)
	if err != nil {
		t.Fatal(err)
	}
	return Config{ANSI: ansi, Colorscheme: cs, Depth: depth, Loader: rules.ConfigLoader{}, Filename: filename,
		Warn: func(m string) { t.Errorf("unexpected warning: %s", m) }}
}

type out struct{ buf bytes.Buffer }

func (o *out) emit(line []byte, nl bool) {
	o.buf.Write(line)
	if nl {
		o.buf.WriteByte('\n')
	}
}

func visible(s string) string { return string(render.StripSGR(nil, []byte(s))) }

func runString(t testing.TB, cfg Config, in string) (string, *Pipeline) {
	t.Helper()
	p := New(cfg)
	var o out
	done := make(chan struct{})
	defer close(done)
	if err := Drive(lines.Read(strings.NewReader(in), done), p, o.emit, func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	return o.buf.String(), p
}

func TestDetectsAndPreservesText(t *testing.T) {
	for name, rule := range map[string]string{"unicorn": "unicorn", "git-diff": "git-diff", "nginx-access": "nginx-access", "jsonl": "jsonl", "generic": "generic"} {
		data, err := os.ReadFile("../../testdata/" + name + "/in.log")
		if err != nil {
			t.Fatal(err)
		}
		got, p := runString(t, testConfig(t, render.TrueColor, "strip", ""), string(data))
		if p.Rule() != rule {
			t.Errorf("%s: rule %q", name, p.Rule())
		}
		if visible(got) != string(data) {
			t.Errorf("%s: text changed", name)
		}
		if got == string(data) {
			t.Errorf("%s: no color", name)
		}
	}
}

func TestMissingFinalNewlineAndCRLF(t *testing.T) {
	for _, in := range []string{"no newline at end", "a\r\nb\r\n", "\n\n", "x\n\ny", ""} {
		got, _ := runString(t, testConfig(t, render.TrueColor, "strip", ""), in)
		if visible(got) != in {
			t.Errorf("%q became %q", in, got)
		}
	}
}

func TestLongLinePassesThrough(t *testing.T) {
	in := strings.Repeat("12345 ", 20000) + "\nshort 1\n" // 120 KB first line
	got, _ := runString(t, testConfig(t, render.TrueColor, "strip", ""), in)
	if visible(got) != in {
		t.Fatal("long line altered")
	}
	if !strings.HasPrefix(got, "12345 12345") {
		t.Error("long line should be unhighlighted")
	}
}

func TestHugeLineIsChunkedButExact(t *testing.T) {
	in := strings.Repeat("x", lines.MaxChunk*2+5) + "\nnext\n"
	got, _ := runString(t, testConfig(t, render.TrueColor, "strip", ""), in)
	if visible(got) != in {
		t.Fatal("huge line altered")
	}
}

func TestColorOffAndKeep(t *testing.T) {
	in := "E, [2026-10-06T10:00:00.123 #1]  ERROR -- : worker=0 ready\n"
	got, _ := runString(t, testConfig(t, render.None, "strip", ""), in)
	if got != in {
		t.Errorf("color off changed the text: %q", got)
	}
	got, _ = runString(t, testConfig(t, render.None, "strip", ""), "\x1b[31mred\x1b[0m\n")
	if got != "red\n" {
		t.Errorf("strip with color off: %q", got)
	}
	raw := "\x1b[31mred 12\x1b[0m\n"
	got, p := runString(t, testConfig(t, render.TrueColor, "keep", ""), raw)
	if got != raw || p.Rule() != "" {
		t.Errorf("keep mode: %q rule %q", got, p.Rule())
	}
}

func TestFilenameChoosesRule(t *testing.T) {
	// Nothing in the text says "unicorn", but the file name does.
	_, p := runString(t, testConfig(t, render.TrueColor, "strip", "/var/log/unicorn.stderr.log"), "hello\nworld\n")
	if p.Rule() != "unicorn" {
		t.Errorf("rule %q", p.Rule())
	}
	_, p = runString(t, testConfig(t, render.TrueColor, "strip", "notes.txt"), "hello\nworld\n")
	if p.Rule() != "generic" {
		t.Errorf("rule %q", p.Rule())
	}
}

func TestDecidesAfterSampleLinesOrTimeout(t *testing.T) {
	cfg := testConfig(t, render.TrueColor, "strip", "")
	unicorn := "I, [2026-10-06T10:00:00.123 #4242]  INFO -- : worker=0 ready\n"

	// The sample size decides at once, the rest flow through live.
	p := New(cfg)
	var o out
	for i := 0; i < detect.SampleLines-1; i++ {
		p.Line([]byte(strings.TrimSuffix(unicorn, "\n")), true, o.emit)
	}
	if o.buf.Len() != 0 || p.Decided() {
		t.Fatal("decided too early")
	}
	p.Line([]byte(strings.TrimSuffix(unicorn, "\n")), true, o.emit)
	if !p.Decided() || p.Rule() != "unicorn" || strings.Count(o.buf.String(), "\n") != detect.SampleLines {
		t.Fatalf("not decided after %d lines: rule %q, %d lines", detect.SampleLines, p.Rule(), strings.Count(o.buf.String(), "\n"))
	}

	// A slow stream is decided by the timer, with fewer lines.
	r, w := io.Pipe()
	defer w.Close()
	done := make(chan struct{})
	defer close(done)
	p = New(cfg)
	o = out{}
	got := make(chan string, 1)
	var mu sync.Mutex
	go func() {
		_ = Drive(lines.Read(r, done), p, func(l []byte, nl bool) {
			mu.Lock()
			defer mu.Unlock()
			o.emit(l, nl)
		}, func() bool {
			mu.Lock()
			s := o.buf.String()
			mu.Unlock()
			if strings.Contains(s, "worker") {
				select {
				case got <- s:
				default:
				}
			}
			return false
		})
	}()
	start := time.Now()
	io.WriteString(w, unicorn)
	select {
	case s := <-got:
		if time.Since(start) > 2*time.Second {
			t.Errorf("took %v", time.Since(start))
		}
		if visible(s) != unicorn || s == unicorn {
			t.Errorf("unexpected output %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a single line was never emitted: the timer did not decide")
	}
	if p.Rule() != "unicorn" {
		t.Errorf("rule %q", p.Rule())
	}
}

func TestRedetectsAfterManyUnmatchedLines(t *testing.T) {
	cfg := testConfig(t, render.TrueColor, "strip", "")
	var in strings.Builder
	for i := 0; i < 25; i++ {
		fmt.Fprintf(&in, "I, [2026-10-06T10:00:00.123 #4242]  INFO -- : worker=%d ready\n", i)
	}
	for i := 0; i < 150; i++ {
		fmt.Fprintf(&in, `{"level":"info","msg":"m%d","n":%d}`+"\n", i, i)
	}
	got, p := runString(t, cfg, in.String())
	if p.Rule() != "jsonl" {
		t.Errorf("did not switch: rule %q", p.Rule())
	}
	if visible(got) != in.String() {
		t.Error("text changed while switching")
	}
}

func TestKeepsRuleThroughLongQuietStretch(t *testing.T) {
	cfg := testConfig(t, render.TrueColor, "strip", "")
	in := "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1,300 +1,300 @@\n" + strings.Repeat(" same line\n", 300)
	_, p := runString(t, cfg, in)
	if p.Rule() != "git-diff" {
		t.Errorf("rule %q", p.Rule())
	}
}

func TestBrokenUserRuleIsReportedNotFatal(t *testing.T) {
	cfg := testConfig(t, render.TrueColor, "strip", "")
	dir := os.Getenv("XDG_CONFIG_HOME") + "/magenta/rules"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(dir+"/bad.toml", []byte("name = \"bad\"\ndetect = ['(oops']\n"), 0o644)
	var warned []string
	cfg.Warn = func(m string) { warned = append(warned, m) }
	got, _ := runString(t, cfg, "hello 12\n")
	if visible(got) != "hello 12\n" || len(warned) != 1 || !strings.Contains(warned[0], "bad.toml:2: invalid detect regex") {
		t.Errorf("got %q, warnings %q", got, warned)
	}
}
