//go:build !windows

package mgnt

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/styd/magenta/internal/render"
	"github.com/styd/magenta/internal/tty"
)

// fakeFzf is a stand-in for fzf: it records how it was started and what it
// was fed, waits until the test creates "release", prints the prepared output
// and exits with the prepared status.
const fakeFzf = `#!/bin/sh
D="$FAKE_FZF_DIR"
if [ "$1" = "--version" ]; then
  echo x >> "$D/version_calls"
  printf '%s\n' "${FAKE_FZF_VERSION:-0.74.4 (fake)}"
  exit 0
fi
case " $* " in *" --filter="*)
  printf '%s\n' "$@" > "$D/filter_args"
  cat "$D/filter_out" 2>/dev/null
  [ -s "$D/filter_out" ] && exit 0 || exit 1;;
esac
echo $$ > "$D/pid"
printf '%s\n' "$@" > "$D/args"
env > "$D/env"
echo started >> "$D/started"
# dash gives a background job /dev/null for stdin before "<&0" is read, so keep the pipe on fd 3.
exec 3<&0
cat <&3 > "$D/stdin" 2>/dev/null &
CAT=$!
while [ ! -f "$D/release" ]; do sleep 0.02; done
# --print-query: when fzf leaves, the query comes first, then the selected lines
case " $* " in *" --print-query "*) cat "$D/query" 2>/dev/null || echo;; esac
[ -f "$D/out" ] && cat "$D/out"
kill $CAT 2>/dev/null
wait $CAT 2>/dev/null
rm -f "$D/release"
exit $(cat "$D/exit" 2>/dev/null || echo 0)
`

type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fakeTerm records what the stream view does to the terminal.
type fakeTerm struct {
	mu          sync.Mutex
	cbreak      bool
	reading     bool
	keys        chan byte
	events      []string
	cbreakCalls int
	panicAt     int // panic in the n-th Cbreak call (0: never)
	closed      bool
	rows, cols  int
	reply       string // the answer to Query ("" for none)
}

func newFakeTerm() *fakeTerm { return &fakeTerm{keys: make(chan byte, 64), rows: 24, cols: 80} }

func (f *fakeTerm) Size() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rows, f.cols
}

func (f *fakeTerm) log(e string) { f.events = append(f.events, e) }

func (f *fakeTerm) Cbreak() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cbreakCalls++
	if f.panicAt == f.cbreakCalls {
		f.cbreak = true // the terminal is left in cbreak mode by the failure
		panic("boom in Cbreak")
	}
	f.cbreak = true
	f.log("cbreak")
	return nil
}

func (f *fakeTerm) Restore() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cbreak {
		f.cbreak = false
		f.log("restore")
	}
	return nil
}

func (f *fakeTerm) StartKeys() <-chan byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reading = true
	f.log("startkeys")
	return f.keys
}

func (f *fakeTerm) StopKeys() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reading {
		f.reading = false
		f.log("stopkeys")
	}
}

// Query answers with the canned reply, if there is one.
func (f *fakeTerm) Query(request string, timeout time.Duration) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("query")
	return f.reply, f.reply != ""
}

func (f *fakeTerm) Close() error {
	f.Restore()
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func (f *fakeTerm) state() (cbreak, reading bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cbreak, f.reading
}

func (f *fakeTerm) press(s string) {
	for _, b := range []byte(s) {
		f.keys <- b
	}
}

var _ tty.Terminal = (*fakeTerm)(nil)

type harness struct {
	t        *testing.T
	dir      string // the fake fzf's scratch directory
	term     *fakeTerm
	stdout   *safeBuf
	stderr   *safeBuf
	in       *io.PipeWriter
	sigs     chan os.Signal
	done     chan int
	vars     map[string]string
	opened   int
	openErr  error
	noFzf    bool
	stdoutTT bool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "fzf"), []byte(fakeFzf), 0o755); err != nil {
		t.Fatal(err)
	}
	h := &harness{
		t: t, dir: t.TempDir(), term: newFakeTerm(), stdout: &safeBuf{}, stderr: &safeBuf{},
		sigs: make(chan os.Signal, 2), done: make(chan int, 1), stdoutTT: true,
		vars: map[string]string{"NO_COLOR": "1"},
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return h
}

func (h *harness) config(content string) {
	h.t.Helper()
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "magenta")
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) env(stdin io.Reader) Env {
	fzfEnv := []string{"PATH=/usr/bin:/bin", "FAKE_FZF_DIR=" + h.dir, "FAKE_FZF_VERSION=" + h.vars["FAKE_FZF_VERSION"], "FZF_DEFAULT_OPTS=--color=dark"}
	return Env{
		Stdin: stdin, Stdout: h.stdout, Stderr: h.stderr,
		Getenv:      func(k string) string { return h.vars[k] },
		StdoutIsTTY: h.stdoutTT,
		LookPath: func(name string) (string, error) {
			if h.noFzf {
				return "", exec.ErrNotFound
			}
			return exec.LookPath(name)
		},
		OpenTTY: func() (tty.Terminal, error) {
			h.opened++
			if h.openErr != nil {
				return nil, h.openErr
			}
			return h.term, nil
		},
		Signals:    func() (<-chan os.Signal, func()) { return h.sigs, func() {} },
		Executable: func() (string, error) { return "/path/to/mgnt", nil },
		FzfVersion: func(path string) (string, error) {
			cmd := exec.Command(path, "--version")
			cmd.Env = fzfEnv
			out, err := cmd.Output()
			return string(out), err
		},
		Environ: fzfEnv,
	}
}

// start runs mgnt on a pipe that the test writes to.
func (h *harness) start(args ...string) {
	h.t.Helper()
	pr, pw := io.Pipe()
	h.in = pw
	h.t.Cleanup(func() { pw.Close() })
	go func() {
		defer func() {
			if r := recover(); r != nil {
				h.t.Errorf("mgnt panicked: %v", r)
				h.done <- -99
			}
		}()
		h.done <- Run(h.env(pr), args)
	}()
}

func (h *harness) send(lines ...string) {
	h.t.Helper()
	for _, l := range lines {
		if _, err := io.WriteString(h.in, l+"\n"); err != nil {
			h.t.Fatal(err)
		}
	}
}

func (h *harness) eventually(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s\nstdout: %q\nstderr: %q", what, h.stdout.String(), h.stderr.String())
}

func (h *harness) waitOut(substr string) {
	h.t.Helper()
	h.eventually("output "+strconv.Quote(substr), func() bool { return strings.Contains(h.stdout.String(), substr) })
}

// waitVisible waits for text in the output once color codes are removed.
func (h *harness) waitVisible(substr string) {
	h.t.Helper()
	h.eventually("visible output "+strconv.Quote(substr), func() bool { return strings.Contains(visible(h.stdout.String()), substr) })
}

func (h *harness) file(name string) string {
	data, _ := os.ReadFile(filepath.Join(h.dir, name))
	return string(data)
}

func (h *harness) set(name, content string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.dir, name), []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) release() { h.set("release", "") }

func (h *harness) waitFzf() {
	h.t.Helper()
	h.eventually("fzf to start", func() bool { return strings.Count(h.file("started"), "started") >= 1 })
}

// fzfStarts counts how many times the fake fzf has been started.
func (h *harness) fzfStarts() int { return strings.Count(h.file("started"), "started") }

func (h *harness) wait() int {
	h.t.Helper()
	select {
	case code := <-h.done:
		return code
	case <-time.After(5 * time.Second):
		h.t.Fatalf("mgnt did not exit\nstdout: %q\nstderr: %q", h.stdout.String(), h.stderr.String())
		return -1
	}
}

func (h *harness) restored() {
	h.t.Helper()
	if cb, reading := h.term.state(); cb || reading {
		h.t.Errorf("terminal not restored: cbreak=%v reading=%v events=%v", cb, reading, h.term.events)
	}
}

func lines5() []string {
	return []string{"alpha one", "bravo two", "charlie three", "delta four", "echo five"}
}

func TestSearchFlow(t *testing.T) {
	h := newHarness(t)
	h.config("fzf_opts = \"--height 40% --prompt 'find> '\"\n")
	h.vars["MGNT_FZF_OPTS"] = "--no-multi"
	h.start()
	h.send(lines5()...)
	h.waitOut("echo five\n")
	if h.opened != 1 {
		t.Fatalf("terminal opened %d times", h.opened)
	}
	if cb, reading := h.term.state(); !cb || !reading {
		t.Fatalf("the stream view must be in cbreak mode, reading keys: %v %v", cb, reading)
	}
	if h.file("version_calls") != "" {
		t.Error("fzf --version ran before the first search")
	}

	h.term.press("x/") // other keys are ignored, "/" opens the search
	h.waitFzf()
	// By the time fzf runs, mgnt has given the terminal back.
	if cb, reading := h.term.state(); cb || reading {
		t.Errorf("terminal not released before fzf ran: cbreak=%v reading=%v", cb, reading)
	}
	args := strings.Split(strings.TrimSpace(h.file("args")), "\n")
	wantPrefix := []string{"--ansi", "--no-sort", "--multi", "--delimiter", `\t`, "--with-nth", "2.."}
	if len(args) < len(wantPrefix) || strings.Join(args[:len(wantPrefix)], " ") != strings.Join(wantPrefix, " ") {
		t.Fatalf("args: %q", args)
	}
	// A trickling log is read from its end: newest line nearest the prompt. The
	// prompt is at the bottom and long lines are wrapped, not cut. The preview is
	// on top at half the height and hidden until Enter. Esc closes it, ctrl-o
	// accepts and ctrl-/ toggles. The user's options come last.
	rest := args[len(wantPrefix):]
	pos := 0
	next := func(want ...string) bool { // the next arguments, in order
		if pos+len(want) > len(rest) {
			return false
		}
		ok := strings.Join(rest[pos:pos+len(want)], "\x00") == strings.Join(want, "\x00")
		pos += len(want)
		return ok
	}
	value := func() string { // the next argument, whatever it is
		if pos >= len(rest) {
			return ""
		}
		pos++
		return rest[pos-1]
	}
	okOrder := next("--tac") && next("--tail", "100000") && next("--layout", "default") && next("--wrap") && next("--preview")
	cmd := value()
	okOrder = okOrder && strings.HasPrefix(cmd, "'/path/to/mgnt' __preview --sock '") && strings.HasSuffix(cmd, "' {1}") &&
		next("--preview-window", "up,50%,wrap,border-bottom,hidden") && next("--header")
	header := value()
	okOrder = okOrder && strings.Contains(header, "enter: context")
	// The keys that run the helper (Esc and the movement keys), then Enter, the accept key, and the toggle.
	for _, key := range []string{"esc", "up", "down", "ctrl-p", "ctrl-n", "ctrl-k", "ctrl-j", "page-up", "page-down", "preview-scroll-up", "preview-scroll-down", "enter"} {
		okOrder = okOrder && next("--bind")
		b := value()
		okOrder = okOrder && strings.HasPrefix(b, key+":transform:'/path/to/mgnt' __preview --sock '") && strings.Contains(b, "' --key ")
	}
	okOrder = okOrder && next("--bind", "ctrl-o:accept") && next("--bind")
	toggle := value()
	okOrder = okOrder && strings.HasPrefix(toggle, "ctrl-/:transform:'/path/to/mgnt' __preview --sock '") && strings.HasSuffix(toggle, "--key toggle")
	if !okOrder {
		t.Fatalf("layout and preview options (stopped at %d): %q", pos, rest)
	}
	if got := rest[pos:]; strings.Join(got, "|") != "--height|40%|--prompt|find> |--no-multi|--print-query" {
		t.Errorf("user options: %q", got)
	}
	if !strings.Contains(h.file("env"), "FZF_DEFAULT_OPTS=--color=dark") {
		t.Error("the user's FZF_DEFAULT_OPTS did not reach fzf")
	}

	// The whole buffer comes first, with sequence numbers, then new lines keep flowing.
	h.eventually("buffer fed to fzf", func() bool { return strings.Contains(h.file("stdin"), "5\techo five\n") })
	if got := h.file("stdin"); !strings.HasPrefix(got, "1\talpha one\n2\tbravo two\n") {
		t.Errorf("stdin: %q", got)
	}
	h.send("foxtrot six", "golf seven")
	h.eventually("new lines fed to a running fzf", func() bool { return strings.Contains(h.file("stdin"), "7\tgolf seven\n") })
	if strings.Contains(h.stdout.String(), "foxtrot") {
		t.Error("lines that arrive while fzf is open must not be printed over its screen")
	}

	// fzf's answer: lines 2 and 4. They are printed, then the lines that were missed.
	h.set("out", "2\tbravo two\n4\tdelta four\n")
	h.release()
	h.waitOut("golf seven\n")
	out := h.stdout.String()
	want := "alpha one\nbravo two\ncharlie three\ndelta four\necho five\n" + "bravo two\ndelta four\n" + "foxtrot six\ngolf seven\n"
	if out != want {
		t.Errorf("stdout:\n%q\nwant:\n%q", out, want)
	}
	// Back in the stream view.
	h.eventually("stream view again", func() bool { cb, reading := h.term.state(); return cb && reading })
	h.send("hotel eight")
	h.waitOut("hotel eight\n")
	if h.file("version_calls") != "x\n" {
		t.Errorf("fzf --version calls: %q", h.file("version_calls"))
	}

	h.in.Close()
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d", code)
	}
	h.restored()
}

func TestExitCodes(t *testing.T) {
	tests := []struct {
		name   string
		code   string
		out    string
		want   string // after the ring lines
		failed bool
	}{
		{"selection", "0", "1\talpha one\n", "alpha one\n", false},
		{"no match", "1", "", "", false},
		{"canceled with Esc or Ctrl-C", "130", "", "", false},
		{"error", "2", "", "", true},
		{"other status", "3", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.start()
			h.send("alpha one", "bravo two")
			h.waitOut("bravo two\n")
			h.term.press("/")
			h.waitFzf()
			h.set("out", tt.out)
			h.set("exit", tt.code)
			h.send("late line") // arrives while fzf is open
			h.eventually("late line fed", func() bool { return strings.Contains(h.file("stdin"), "late line") })
			h.release()
			h.waitOut("late line\n")
			got := strings.TrimPrefix(h.stdout.String(), "alpha one\nbravo two\n")
			if tt.failed {
				if !strings.HasPrefix(got, "[mgnt] fzf failed: fzf exited with status "+tt.code+"\n") || !strings.HasSuffix(got, "late line\n") {
					t.Errorf("got %q", got)
				}
			} else if got != tt.want+"late line\n" {
				t.Errorf("got %q, want %q", got, tt.want+"late line\n")
			}
			// Always back in the stream view, which keeps working.
			h.eventually("stream view again", func() bool { cb, reading := h.term.state(); return cb && reading })
			h.send("after")
			h.waitOut("after\n")
			h.in.Close()
			if code := h.wait(); code != 0 {
				t.Errorf("exit %d", code)
			}
			h.restored()
		})
	}
}

func TestFzfThatCannotStart(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(filepath.Join(filepath.Dir(mustLook(t, "fzf")), "fzf"), []byte("not a program\n"), 0o755)
	h.start()
	h.send("alpha one")
	h.waitOut("alpha one\n")
	h.term.press("/")
	h.waitOut("[mgnt] fzf failed:")
	h.eventually("stream view again", func() bool { cb, reading := h.term.state(); return cb && reading })
	h.send("next")
	h.waitOut("next\n")
	h.in.Close()
	h.wait()
	h.restored()
}

func mustLook(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDimNoticesOnlyWhenColorIsOn(t *testing.T) {
	h := newHarness(t)
	delete(h.vars, "NO_COLOR")
	h.vars["COLORTERM"] = "truecolor"
	h.set("exit", "2")
	h.start()
	h.send("x")
	h.term.press("/")
	h.waitFzf()
	h.release()
	h.waitOut("fzf failed")
	if !strings.Contains(h.stdout.String(), "\x1b[2m[mgnt] fzf failed: fzf exited with status 2\x1b[0m\n") {
		t.Errorf("not dim: %q", h.stdout.String())
	}
	h.in.Close()
	h.wait()
}

func TestSelectionIsLookedUpInTheRing(t *testing.T) {
	smallRing(t)
	h := newHarness(t)
	delete(h.vars, "NO_COLOR")
	h.vars["COLORTERM"] = "truecolor"
	h.config("buffer = 3\n")
	h.start()
	// Six lines into a ring of three: lines 1-3 are evicted, 4-6 stay.
	var in []string
	for i := 1; i <= 6; i++ {
		in = append(in, "I, [2026-10-06T10:00:00.123 #4242]  INFO -- : worker="+strconv.Itoa(i)+" ready")
	}
	for i, l := range in { // one at a time: a batch bigger than this tiny ring would lose lines
		h.send(l)
		h.waitVisible("worker=" + strconv.Itoa(i+1) + " ready")
	}
	h.term.press("/")
	h.waitFzf()
	// Line 1 is gone from the ring: fzf's own text is used. Line 5 is still there: its colors come back.
	h.set("out", "1\tI, [2026] INFO worker=1 ready\n5\tignored text\n")
	h.release()
	h.eventually("both selections", func() bool { return strings.Count(visible(h.stdout.String()), "worker=") >= 6+2 })
	out := withoutPaint(h.stdout.String())
	if !strings.Contains(out, "\nI, [2026] INFO worker=1 ready\n") {
		t.Errorf("evicted line not printed from fzf's text: %q", out)
	}
	fifth := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(visible(l), "worker=5 ready") {
			fifth = l
		}
	}
	if !strings.Contains(fifth, "\x1b[") || visible(fifth) != "I, [2026-10-06T10:00:00.123 #4242]  INFO -- : worker=5 ready" {
		t.Errorf("line 5: %q", fifth)
	}
	if strings.Count(out, "ignored text") != 0 {
		t.Error("the text fzf returned must not be used when the ring still has the line")
	}
	h.in.Close()
	h.wait()
}

// smallRing lets the ring be as small as the buffer setting for one test.
func smallRing(t *testing.T) {
	t.Helper()
	old, oldLimit := pagerMinRing, limitReadAhead
	pagerMinRing, limitReadAhead = 0, false
	t.Cleanup(func() { pagerMinRing, limitReadAhead = old, oldLimit })
}

func visible(s string) string { return string(render.StripSGR(nil, []byte(s))) }

func TestLinesDroppedWhileSearching(t *testing.T) {
	smallRing(t)
	h := newHarness(t)
	h.config("buffer = 4\n")
	h.start()
	h.send("one", "two")
	h.waitOut("two\n")
	h.term.press("/")
	h.waitFzf()
	var more []string
	for i := 1; i <= 10; i++ {
		more = append(more, "m"+strconv.Itoa(i))
	}
	h.send(more...)
	h.eventually("lines fed", func() bool { return strings.Contains(h.file("stdin"), "12\tm10") })
	h.release()
	h.waitOut("m10\n")
	out := h.stdout.String()
	// Lines 3-12 arrived meanwhile. The ring holds the last four (9-12).
	if !strings.Contains(out, "[mgnt] 6 line(s) were dropped before they could be shown") || !strings.HasSuffix(out, "m7\nm8\nm9\nm10\n") {
		t.Errorf("%q", out)
	}
	h.in.Close()
	h.wait()
}

func TestEndOfInput(t *testing.T) {
	t.Run("exits at once, like cat", func(t *testing.T) {
		h := newHarness(t)
		h.start()
		h.send(lines5()...)
		h.in.Close()
		if code := h.wait(); code != 0 {
			t.Errorf("exit %d", code)
		}
		if h.stdout.String() != strings.Join(lines5(), "\n")+"\n" {
			t.Errorf("%q", h.stdout.String())
		}
		h.restored()
		if h.fzfStarts() != 0 {
			t.Error("fzf started")
		}
	})
	t.Run("a closed input is not an interactive session", func(t *testing.T) {
		h := newHarness(t)
		h.start("--no-interactive")
		h.send("a")
		h.in.Close()
		h.wait()
		if h.opened != 0 {
			t.Error("--no-interactive opened the terminal")
		}
	})
	t.Run("with fzf open, fzf's input is closed and the user can finish", func(t *testing.T) {
		h := newHarness(t)
		h.start()
		h.send("alpha", "bravo")
		h.waitOut("bravo\n")
		h.term.press("/")
		h.waitFzf()
		h.send("charlie")
		h.in.Close() // EOF while fzf is open
		h.eventually("fzf's stdin closed after the last line", func() bool { return strings.HasSuffix(h.file("stdin"), "3\tcharlie\n") })
		select {
		case code := <-h.done:
			t.Fatalf("mgnt exited (%d) while fzf was still open", code)
		case <-time.After(200 * time.Millisecond):
		}
		h.set("out", "3\tcharlie\n")
		h.release()
		if code := h.wait(); code != 0 {
			t.Errorf("exit %d", code)
		}
		// charlie is printed as the selection, and again as a line that arrived
		// while fzf was open.
		if got := h.stdout.String(); got != "alpha\nbravo\ncharlie\ncharlie\n" {
			t.Errorf("%q", got)
		}
		h.restored()
	})
}

func TestPlainModeNeverTouchesTheTerminal(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *harness)
		args  []string
	}{
		{"stdout is not a terminal", func(h *harness) { h.stdoutTT = false }, nil},
		{"--no-interactive", func(h *harness) {}, []string{"--no-interactive"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			tt.setup(h)
			pr, pw := io.Pipe()
			env := h.env(pr)
			env.OpenTTY = func() (tty.Terminal, error) {
				t.Error("the terminal was opened in plain mode")
				return nil, errors.New("no")
			}
			env.Signals = func() (<-chan os.Signal, func()) {
				t.Error("signal handling was set up in plain mode")
				return nil, func() {}
			}
			go func() {
				io.WriteString(pw, strings.Join(lines5(), "\n")+"\n")
				pw.Close()
			}()
			if code := Run(env, tt.args); code != 0 {
				t.Fatalf("exit %d", code)
			}
			if h.stdout.String() != strings.Join(lines5(), "\n")+"\n" {
				t.Errorf("%q", h.stdout.String())
			}
			if h.fzfStarts() != 0 {
				t.Error("fzf started")
			}
		})
	}
}

func TestTerminalThatCannotBeOpened(t *testing.T) {
	h := newHarness(t)
	h.openErr = errors.New("no controlling terminal")
	h.start()
	h.send("alpha")
	h.waitOut("alpha\n")
	h.in.Close()
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d", code)
	}
	if !strings.Contains(h.stderr.String(), "cannot open the terminal for paging and search (no controlling terminal), so mgnt only colors the output") {
		t.Errorf("stderr %q", h.stderr.String())
	}
}

func TestSignals(t *testing.T) {
	t.Run("SIGINT in the stream view", func(t *testing.T) {
		h := newHarness(t)
		h.start()
		h.send("a")
		h.waitOut("a\n")
		h.sigs <- syscall.SIGINT
		if code := h.wait(); code != 130 {
			t.Errorf("exit %d", code)
		}
		h.restored()
	})
	t.Run("SIGTERM kills the fzf child", func(t *testing.T) {
		h := newHarness(t)
		h.start()
		h.send("a")
		h.waitOut("a\n")
		h.term.press("/")
		h.waitFzf()
		pid, err := strconv.Atoi(strings.TrimSpace(h.file("pid")))
		if err != nil {
			t.Fatalf("no pid: %v", err)
		}
		h.sigs <- syscall.SIGTERM
		if code := h.wait(); code != 143 {
			t.Errorf("exit %d", code)
		}
		h.restored()
		h.eventually("fzf to be gone", func() bool { return syscall.Kill(pid, 0) != nil })
	})
}

func TestTerminalRestoredAfterPanic(t *testing.T) {
	h := newHarness(t)
	h.term.panicAt = 2 // the Cbreak call after the search
	pr, pw := io.Pipe()
	defer pw.Close()
	var recovered any
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer func() { recovered = recover() }()
		Run(h.env(pr), nil)
	}()
	io.WriteString(pw, "a\n")
	h.waitOut("a\n")
	h.term.press("/")
	h.waitFzf()
	h.release()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("no panic")
	}
	if recovered == nil {
		t.Fatal("expected the panic to propagate")
	}
	if cb, reading := h.term.state(); cb || reading {
		t.Errorf("terminal left in cbreak mode after a panic: %v %v", cb, reading)
	}
}

func TestFzfVersionRunsOnceAndNoticesOnce(t *testing.T) {
	h := newHarness(t)
	h.vars["FAKE_FZF_VERSION"] = "0.20.0 (fake)"
	h.start()
	h.send("alpha")
	h.waitOut("alpha\n")
	if h.file("version_calls") != "" {
		t.Fatal("version checked at start-up")
	}
	for i := 1; i <= 2; i++ {
		h.term.press("/")
		h.eventually("fzf run "+strconv.Itoa(i), func() bool { return h.fzfStarts() == i })
		h.release()
		h.eventually("stream view again", func() bool { cb, reading := h.term.state(); return cb && reading })
	}
	h.in.Close()
	h.wait()
	if got := h.file("version_calls"); got != "x\n" {
		t.Errorf("fzf --version ran %d times", strings.Count(got, "x"))
	}
	out := h.stdout.String()
	for _, notice := range []string{
		"[mgnt] fzf 0.20.0 is too old for the preview border (needs >= 0.27.0), showing the preview pane without it",
		"[mgnt] fzf 0.20.0 cannot open the preview with Enter and close it with Esc (needs >= 0.49.0), so the preview is always shown",
	} {
		if strings.Count(out, notice) != 1 {
			t.Errorf("notice count %d for %q in %q", strings.Count(out, notice), notice, out)
		}
	}
	// The search itself still worked, with the border left out.
	if !strings.Contains(h.file("args"), "--ansi\n") {
		t.Errorf("args %q", h.file("args"))
	}
}

func TestOutputIsExactWhenColorIsOn(t *testing.T) {
	h := newHarness(t)
	delete(h.vars, "NO_COLOR")
	h.vars["COLORTERM"] = "truecolor"
	h.start()
	in := "I, [2026-10-06T10:00:00.123 #4242]  INFO -- : worker=0 ready\nsecond 12 line"
	io.WriteString(h.in, in) // no final newline
	h.in.Close()
	if code := h.wait(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	out := h.stdout.String()
	if visible(out) != in || !strings.Contains(out, "\x1b[") {
		t.Errorf("%q", out)
	}
}

func TestPreviewSubcommandIsQuietWhenTheSocketIsGone(t *testing.T) {
	for _, vars := range []map[string]string{{}, {"NO_COLOR": "1"}} {
		var stdout, stderr safeBuf
		env := Env{Stdout: &stdout, Stderr: &stderr, Getenv: func(k string) string { return vars[k] }, StdinIsTTY: true}
		if code := Run(env, []string{"__preview", "--sock", "/nonexistent/sock", "7"}); code != 0 {
			t.Errorf("exit %d", code)
		}
		want := "\x1b[2m[mgnt] preview unavailable\x1b[0m\n"
		if vars["NO_COLOR"] != "" {
			want = "[mgnt] preview unavailable\n"
		}
		if stdout.String() != want || stderr.String() != "" {
			t.Errorf("stdout %q stderr %q", stdout.String(), stderr.String())
		}
	}
}

// fzf prints a multi-selection newest first (the list is --tac). The lines
// must reach the scrollback oldest first.
func TestSelectionsArePrintedOldestFirst(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.send("alpha", "bravo", "charlie", "delta")
	h.waitOut("delta\n")
	h.term.press("/")
	h.waitFzf()
	h.set("out", "4\tdelta\n2\tbravo\n3\tcharlie\n")
	h.release()
	h.eventually("selection printed", func() bool { return strings.HasSuffix(h.stdout.String(), "bravo\ncharlie\ndelta\n") })
	h.in.Close()
	h.wait()
}

// autoOutput runs the pager with the given theme setting and answer to the
// terminal's background question, and returns what it printed.
func autoOutput(t *testing.T, theme, reply string, env map[string]string) (string, *harness) {
	t.Helper()
	h := newHarness(t)
	h.vars = map[string]string{"COLORTERM": "truecolor"}
	for k, v := range env {
		h.vars[k] = v
	}
	h.term.reply = reply
	h.config("theme = \"" + theme + "\"\n")
	h.start("--no-interactive=false")
	h.send("ERROR 2026-01-01 10:00:00 failed 404 https://example.com/x")
	h.waitVisible("failed 404")
	h.in.Close()
	h.wait()
	return h.stdout.String(), h
}

func TestAutoThemeFollowsTheTerminalBackground(t *testing.T) {
	const light, dark = "\x1b]11;rgb:ffff/ffff/ffff\x07", "\x1b]11;rgb:0000/0000/0000\x07"
	pinnedDark, _ := autoOutput(t, "dark", "", nil)
	pinnedLight, _ := autoOutput(t, "light", "", nil)
	if pinnedDark == pinnedLight {
		t.Fatal("the two themes should look different")
	}
	if got, h := autoOutput(t, "auto", light, nil); got != pinnedLight || !slices.Contains(h.term.events, "query") {
		t.Errorf("light terminal: got %q (events %v)", got, h.term.events)
	}
	if got, _ := autoOutput(t, "auto", dark, nil); got != pinnedDark {
		t.Errorf("dark terminal: got %q", got)
	}
	// The terminal says nothing: COLORFGBG, then dark.
	if got, _ := autoOutput(t, "auto", "", map[string]string{"COLORFGBG": "0;15"}); got != pinnedLight {
		t.Errorf("COLORFGBG light: got %q", got)
	}
	if got, _ := autoOutput(t, "auto", "", nil); got != pinnedDark {
		t.Errorf("nothing known: got %q", got)
	}
	// MGNT_BACKGROUND beats the terminal.
	if got, _ := autoOutput(t, "auto", dark, map[string]string{"MGNT_BACKGROUND": "light"}); got != pinnedLight {
		t.Errorf("override: got %q", got)
	}
}

func TestTerminalIsNotAskedWhenItDoesNotMatter(t *testing.T) {
	// A pinned theme needs no answer, and without color there is nothing to pick.
	if _, h := autoOutput(t, "dark", "", nil); slices.Contains(h.term.events, "query") {
		t.Error("asked although the theme is pinned")
	}
	if _, h := autoOutput(t, "auto", "", map[string]string{"NO_COLOR": "1"}); slices.Contains(h.term.events, "query") {
		t.Error("asked although color is off")
	}
}

// The options of one run reach fzf, and are not written to the config.
func TestOverridesReachFzf(t *testing.T) {
	h := newHarness(t)
	h.start("--no-preview", "--buffer", "70000", "--fzf-opts", "--exact", "--set", "keys.accept=enter")
	h.send("alpha", "bravo")
	h.waitOut("bravo\n")
	h.term.press("/")
	h.waitFzf()
	args := h.file("args")
	if strings.Contains(args, "--preview") || strings.Contains(args, "--bind") || !strings.Contains(args, "--exact") {
		t.Errorf("--no-preview and --fzf-opts: %q", args)
	}
	// The ring is never below its floor, so 70000 changes nothing here, and a
	// buffer above the floor is the capacity.
	if !strings.Contains(args, "--tail\n"+strconv.Itoa(max(70000, pagerMinRing))+"\n") {
		t.Errorf("--tail follows the buffer: %q", args)
	}
	h.release()
	h.in.Close()
	h.wait()
}

// fzf gets the colors of the colorscheme, in the theme in use, on top of its own
// base scheme for that theme.
func TestFzfGetsTheColorsOfTheColorscheme(t *testing.T) {
	for _, tt := range []struct {
		name string
		vars map[string]string
		want []string // the base scheme first, then the colors
	}{
		{"dark", map[string]string{"COLORTERM": "truecolor", "MGNT_BACKGROUND": "dark"},
			[]string{"dark", "input-fg:#e4dcf7", "input-bg:#2d2547", "fg+:#e4dcf7", "bg+:#4e437a", "pointer:#e66bc0", "marker:#e66bc0",
				"header:#e66bc0", "info:#9a8cc6", "separator:#4e437a", "scrollbar:#4e437a", "border:#4e437a", "gutter:#1f1933"}},
		{"light", map[string]string{"COLORTERM": "truecolor", "MGNT_BACKGROUND": "light"},
			[]string{"light", "input-fg:#2a2040", "input-bg:#ded4f2", "fg+:#2a2040", "bg+:#bfaedd", "pointer:#ae1e81", "marker:#ae1e81",
				"header:#ae1e81", "info:#6a5a99", "separator:#bfaedd", "scrollbar:#bfaedd", "border:#bfaedd", "gutter:#f7f3ff"}},
		{"no color", map[string]string{"NO_COLOR": "1"}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.vars = tt.vars
			h.start("--no-interactive=false")
			h.send("alpha", "bravo")
			h.waitOut("bravo\n")
			h.term.press("/")
			h.waitFzf()
			args := strings.Split(strings.TrimSpace(h.file("args")), "\n")
			value := ""
			for i, a := range args {
				if a == "--color" && i+1 < len(args) {
					value = args[i+1]
				}
			}
			if tt.want == nil {
				if value != "" {
					t.Errorf("colors without color: %q", value)
				}
			} else if got := strings.Split(value, ","); !slices.Equal(got, tt.want) {
				t.Errorf("--color\n got %q\nwant %q", got, tt.want)
			}
			h.release()
			h.in.Close()
			h.wait()
		})
	}
}

// withoutPaint removes the sequences that set and reset the terminal's colors
// (OSC 10, 11, 110, and 111), for tests that look at the text around them.
func withoutPaint(s string) string {
	return regexp.MustCompile(`\x1b\]1(?:0|1|10|11)(;[^\x1b]*)?\x1b\\`).ReplaceAllString(s, "")
}

// While the pager or the search is open, the terminal has the text and the
// background colors of the colorscheme, in the theme in use. Afterward it has its
// own again.
func TestThePagerAndTheSearchPaintTheTerminal(t *testing.T) {
	for _, tt := range []struct {
		theme, set string
	}{
		{"dark", "\x1b]10;#e4dcf7\x1b\\\x1b]11;#1f1933\x1b\\"},
		{"light", "\x1b]10;#2a2040\x1b\\\x1b]11;#f7f3ff\x1b\\"},
	} {
		t.Run(tt.theme, func(t *testing.T) {
			h := newHarness(t)
			delete(h.vars, "NO_COLOR")
			h.vars["COLORTERM"], h.vars["MGNT_BACKGROUND"] = "truecolor", tt.theme
			h.term.rows, h.term.cols = 11, 60
			h.start()
			h.bulk(numbered(30))
			h.waitPager()
			if !strings.HasPrefix(h.stdout.String(), altOn+tt.set) {
				t.Errorf("the pager should set the colors right after it opens: %q", h.stdout.String()[:60])
			}
			// A search keeps the colors, and does not set them a second time.
			h.term.press("/")
			h.waitFzf()
			h.set("out", "\n")
			h.release()
			h.eventually("the pager again", func() bool { return h.inAlt() })
			h.term.press("q")
			if code := h.wait(); code != 0 {
				t.Errorf("exit %d", code)
			}
			out := h.stdout.String()
			if strings.Count(out, tt.set) != 1 || strings.Count(out, paintOff) != 1 || !strings.HasSuffix(out, altOff+paintOff) {
				t.Errorf("set %d times, reset %d times, ends %q", strings.Count(out, tt.set), strings.Count(out, paintOff), out[max(0, len(out)-40):])
			}
			h.restored()
		})
	}
}

// A search that starts from a followed log has the colors only while fzf runs.
func TestTheSearchOfAFollowedLogPaintsOnlyWhileItRuns(t *testing.T) {
	h := newHarness(t)
	delete(h.vars, "NO_COLOR")
	h.vars["COLORTERM"], h.vars["MGNT_BACKGROUND"] = "truecolor", "dark"
	h.start()
	h.send("alpha")
	h.waitOut("alpha\n")
	if strings.Contains(h.stdout.String(), "\x1b]11;") {
		t.Errorf("a followed log is not painted: %q", h.stdout.String())
	}
	h.term.press("/")
	h.waitFzf()
	h.set("out", "\n")
	h.release()
	h.eventually("the colors given back", func() bool { return strings.Contains(h.stdout.String(), paintOff) })
	out := h.stdout.String()
	if strings.Index(out, "\x1b]11;#1f1933") > strings.Index(out, paintOff) || strings.Count(out, paintOff) != 1 {
		t.Errorf("%q", out)
	}
	h.in.Close()
	h.wait()
}

// No color, or a colorscheme without ui.background (like mono), leaves the
// colors of the terminal alone.
func TestNothingIsPaintedWithoutColorOrABackground(t *testing.T) {
	for _, tt := range []struct {
		name   string
		config string
		vars   map[string]string
	}{
		{"NO_COLOR", "", map[string]string{"NO_COLOR": "1", "COLORTERM": "truecolor"}},
		{"mono", "colorscheme = \"mono\"\n", map[string]string{"COLORTERM": "truecolor"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			delete(h.vars, "NO_COLOR")
			for k, v := range tt.vars {
				h.vars[k] = v
			}
			if tt.config != "" {
				h.config(tt.config)
			}
			h.term.rows, h.term.cols = 11, 60
			h.start()
			h.bulk(numbered(30))
			h.waitPager()
			h.term.press("q")
			h.wait()
			if out := h.stdout.String(); strings.Contains(out, "\x1b]1") {
				t.Errorf("painted: %q", out)
			}
		})
	}
}
