package search

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/styd/magenta/internal/buffer"
	"github.com/styd/magenta/internal/config"
)

func TestSplitArgs(t *testing.T) {
	tests := []struct {
		in   string
		want []string
		err  bool
	}{
		{"", nil, false},
		{"   ", nil, false},
		{"--height 40%", []string{"--height", "40%"}, false},
		{`--prompt 'find> ' --exact`, []string{"--prompt", "find> ", "--exact"}, false},
		{`--prompt "a b" x`, []string{"--prompt", "a b", "x"}, false},
		{`--bind "ctrl-r:execute(echo \"hi\")"`, []string{"--bind", `ctrl-r:execute(echo "hi")`}, false},
		{`a\ b c`, []string{"a b", "c"}, false},
		{`'a\b'`, []string{`a\b`}, false},
		{`''`, []string{""}, false},
		{"a\tb\nc", []string{"a", "b", "c"}, false},
		{`--prompt 'oops`, nil, true},
		{`"oops`, nil, true},
	}
	for _, tt := range tests {
		got, err := SplitArgs(tt.in)
		if (err != nil) != tt.err || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("SplitArgs(%q) = %q, %v, want %q (err %v)", tt.in, got, err, tt.want, tt.err)
		}
	}
}

func TestBuildArgs(t *testing.T) {
	cfg := config.Default()
	full := PlanFor(Version{0, 74, 4}, true, cfg)
	base := []string{"--ansi", "--no-sort", "--multi", "--delimiter", `\t`, "--with-nth", "2.."}
	withTac := func(rest ...string) []string {
		return append(append(append([]string(nil), base...), "--tac"), rest...)
	}

	args, err := BuildArgs(full, "'/bin/mgnt' __preview {1}", cfg, "", NewestLast)
	if err != nil {
		t.Fatal(err)
	}
	want := withTac("--layout", "default", "--wrap",
		"--preview", "'/bin/mgnt' __preview {1}", "--preview-window", "up,50%,wrap,border-bottom,hidden",
		"--header", "enter: context   esc: back   tab: mark   ctrl-o: print selection")
	for _, b := range full.Binds {
		want = append(want, "--bind", strings.ReplaceAll(b, "{HELPER}", "'/bin/mgnt' __preview"))
	}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("got  %q\nwant %q", args, want)
	}

	// A log that was already there is read from its first line: it is nearest the
	// prompt, so there is no --tac. The layout is the same one, so that a few
	// matches sit above the prompt and not at the top of the screen.
	top, _ := BuildArgs(full, "cmd {1}", cfg, "", FromTop)
	if !reflect.DeepEqual(top[:9], append(append([]string(nil), base...), "--layout", "default")) || slices.Contains(top, "--tac") || slices.Contains(top, "reverse-list") {
		t.Errorf("from the top: %q", top)
	}
	// The same preview flags follow either way. The header says what the accept key does here:
	// the pager goes to the line, a followed log prints it.
	if !slices.Contains(top, "--preview-window") || !slices.Contains(top, "enter: context   esc: back   tab: mark   ctrl-o: go to line") {
		t.Errorf("from the top lost the preview flags or has the wrong header: %q", top)
	}
	if slices.Contains(top, "enter: context   esc: back   tab: mark   ctrl-o: print selection") {
		t.Errorf("the pager must not claim to print: %q", top)
	}

	// No preview: the prompt is still at the bottom, but none of the preview flags.
	args, _ = BuildArgs(Plan{Layout: "default"}, "", cfg, "", NewestLast)
	if !reflect.DeepEqual(args, withTac("--layout", "default")) {
		t.Errorf("without preview: %q", args)
	}
	// A plan with a preview but no command (the helper could not be set up) adds nothing either.
	args, _ = BuildArgs(full, "", cfg, "", NewestLast)
	if !reflect.DeepEqual(args, withTac("--layout", "default", "--wrap")) {
		t.Errorf("without a command: %q", args)
	}
	// A very old fzf gets no --layout, whichever way the list runs (it would refuse the flag).
	old := PlanFor(Version{0, 17, 3}, true, cfg)
	if args, _ = BuildArgs(old, "", cfg, "", NewestLast); !reflect.DeepEqual(args, withTac("--no-hscroll")) {
		t.Errorf("old fzf: %q", args)
	}
	if args, _ = BuildArgs(old, "", cfg, "", FromTop); !reflect.DeepEqual(args, append(append([]string(nil), base...), "--no-hscroll")) {
		t.Errorf("old fzf, from the top: %q", args)
	}

	// The user's options come last, config.toml before MGNT_FZF_OPTS, so they can override ours.
	cfg.FzfOpts = "--reverse --prompt 'q> '"
	args, err = BuildArgs(Plan{Layout: "default"}, "", cfg, "--no-multi", NewestLast)
	if err != nil || !reflect.DeepEqual(args[len(withTac("--layout", "default")):], []string{"--reverse", "--prompt", "q> ", "--no-multi"}) {
		t.Errorf("%q %v", args, err)
	}
	cfg.FzfOpts = "'broken"
	if _, err := BuildArgs(Plan{}, "", cfg, "", NewestLast); err == nil {
		t.Error("an unterminated quote in fzf_opts must be reported")
	}
}

func TestParseSelections(t *testing.T) {
	got := parseSelections([]byte("12\tfirst line\n7\tsecond\twith tab\nno prefix here\n\n9x\toops\n"))
	want := []Selection{
		{12, "first line"},
		{7, "second\twith tab"},
		{0, "no prefix here"},
		{0, "9x\toops"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
	if got := parseSelections(nil); got != nil {
		t.Errorf("%v", got)
	}
}

type sink struct {
	buf    bytes.Buffer
	closed chan struct{}
	failAt int // fail writes after this many bytes (0: never)
}

func (s *sink) Write(p []byte) (int, error) {
	if s.failAt > 0 && s.buf.Len() >= s.failAt {
		return 0, io.ErrClosedPipe
	}
	return s.buf.Write(p)
}
func (s *sink) Close() error { close(s.closed); return nil }

func TestFeedSendsBufferThenLiveLinesThenCloses(t *testing.T) {
	ring := buffer.New(10)
	ring.Append([]byte("one"))
	ring.Append([]byte("two"))
	w := &sink{closed: make(chan struct{})}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { feed(w, ring, stop, nil); close(done) }()

	time.Sleep(50 * time.Millisecond)
	ring.Append([]byte("three"))
	ring.Append([]byte("four\twith tab"))
	ring.Close()
	select {
	case <-w.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("the feed did not close fzf's input after the ring closed")
	}
	<-done
	if got := w.buf.String(); got != "1\tone\n2\ttwo\n3\tthree\n4\tfour\twith tab\n" {
		t.Errorf("fed %q", got)
	}
}

func TestFeedStopsWhenAskedOrWhenFzfStopsReading(t *testing.T) {
	ring := buffer.New(10)
	ring.Append([]byte("one"))
	stop := make(chan struct{})
	done := make(chan struct{})
	w := &sink{closed: make(chan struct{})}
	go func() { feed(w, ring, stop, nil); close(done) }()
	time.Sleep(30 * time.Millisecond)
	close(stop)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("feed ignored stop")
	}
	select {
	case <-w.closed:
		t.Error("feed closed the input although the ring is still open")
	default:
	}

	// A closed pipe (fzf exited) ends the feed too.
	ring = buffer.New(10)
	ring.Append([]byte("one"))
	bad := &sink{closed: make(chan struct{}), failAt: 1}
	done = make(chan struct{})
	go func() { feed(bad, ring, make(chan struct{}), nil); close(done) }()
	time.Sleep(20 * time.Millisecond)
	for i := 0; i < 100; i++ {
		ring.Append([]byte(strings.Repeat("x", 1000)))
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("feed did not stop after a write error")
	}
}

func TestFeedStartsAtTheOldestLineThatIsStillThere(t *testing.T) {
	ring := buffer.New(3)
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		ring.Append([]byte(s))
	}
	ring.Close()
	w := &sink{closed: make(chan struct{})}
	feed(w, ring, make(chan struct{}), nil)
	if got := w.buf.String(); got != "3\tc\n4\td\n5\te\n" {
		t.Errorf("%q", got)
	}
}

// TestRealFzf checks the assumptions about fzf itself against the installed
// fzf, skipping when there is none: which line is listed first, that the
// sequence number is hidden from matching, and what fzf prints back.
func TestRealFzf(t *testing.T) {
	fzf, err := exec.LookPath("fzf")
	if err != nil {
		t.Skip("fzf is not installed")
	}
	ring := buffer.New(10)
	for _, s := range []string{"error: disk full", "ok: all good", "error: timeout"} {
		ring.Append([]byte(s))
	}
	ring.Close()
	run := func(query string, order Order) []string {
		args, err := BuildArgs(Plan{}, "", config.Default(), "", order)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(fzf, append(args, "--filter", query)...)
		var in bytes.Buffer
		w := &sink{closed: make(chan struct{})}
		feed(w, ring, make(chan struct{}), nil)
		in.Write(w.buf.Bytes())
		cmd.Stdin = &in
		out, err := cmd.Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
				return nil
			}
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(string(out)), "\n")
	}
	// --filter prints in list order, which is top to bottom of the list, and the
	// first entry is the one beside the prompt: the newest line.
	if got := run("", NewestLast); !reflect.DeepEqual(got, []string{"3\terror: timeout", "2\tok: all good", "1\terror: disk full"}) {
		t.Errorf("order: %q", got)
	}
	// Read from the top, the first line leads the list.
	if got := run("", FromTop); !reflect.DeepEqual(got, []string{"1\terror: disk full", "2\tok: all good", "3\terror: timeout"}) {
		t.Errorf("from the top: %q", got)
	}
	if got := run("error", NewestLast); !reflect.DeepEqual(got, []string{"3\terror: timeout", "1\terror: disk full"}) {
		t.Errorf("filtered: %q", got)
	}
	// The sequence number is not part of what is matched.
	if got := run("1", NewestLast); got != nil {
		t.Errorf("the sequence number was matched: %q", got)
	}
	if got := parseSelections([]byte(strings.Join(run("timeout", NewestLast), "\n"))); !reflect.DeepEqual(got, []Selection{{3, "error: timeout"}}) {
		t.Errorf("%v", got)
	}
}

// While the preview is open the list must hold still: nothing new is written
// to fzf, and what arrived meanwhile is written when the preview closes.
func TestFeedHoldsStillWhileTheGateIsPaused(t *testing.T) {
	ring := buffer.New(100)
	ring.Append([]byte("one"))
	ring.Append([]byte("two"))
	w := &syncSink{closed: make(chan struct{})}
	g := newGate()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { feed(w, ring, stop, g); close(done) }()
	eventuallySink(t, w, "1\tone\n2\ttwo\n")

	g.set(true) // the preview opens
	ring.Append([]byte("three"))
	ring.Append([]byte("four"))
	time.Sleep(150 * time.Millisecond)
	if got := w.String(); got != "1\tone\n2\ttwo\n" {
		t.Fatalf("lines were written while paused: %q", got)
	}
	// The ring closing does not end the feed early either: the backlog has to go first.
	ring.Close()
	time.Sleep(100 * time.Millisecond)
	select {
	case <-w.closed:
		t.Fatal("fzf's input was closed while paused")
	default:
	}

	g.set(false) // the preview closes
	eventuallySink(t, w, "1\tone\n2\ttwo\n3\tthree\n4\tfour\n")
	select {
	case <-w.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("the input was not closed after the backlog")
	}
	<-done
}

func TestPausedFeedStopsWhenAsked(t *testing.T) {
	ring := buffer.New(10)
	ring.Append([]byte("a"))
	w := &syncSink{closed: make(chan struct{})}
	g := newGate()
	g.set(true)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { feed(w, ring, stop, g); close(done) }()
	time.Sleep(50 * time.Millisecond)
	close(stop)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a paused feed ignored stop")
	}
	if w.String() != "" {
		t.Errorf("%q", w.String())
	}
}

func TestGate(t *testing.T) {
	var nilGate *gate
	nilGate.set(true) // harmless
	if !nilGate.wait(make(chan struct{})) {
		t.Error("a nil gate never pauses")
	}
	g := newGate()
	if !g.wait(make(chan struct{})) {
		t.Error("a new gate is open")
	}
	g.set(true)
	g.set(true) // twice is the same
	released := make(chan bool, 1)
	go func() { released <- g.wait(make(chan struct{})) }()
	time.Sleep(50 * time.Millisecond)
	select {
	case <-released:
		t.Fatal("wait returned while paused")
	default:
	}
	g.set(false)
	if ok := <-released; !ok {
		t.Error("wait should report the gate opened")
	}
	g.set(false) // opening an open gate is fine
	// It can pause and resume again and again.
	for i := 0; i < 3; i++ {
		g.set(true)
		go func() { released <- g.wait(make(chan struct{})) }()
		time.Sleep(10 * time.Millisecond)
		g.set(false)
		if !<-released {
			t.Error("round", i)
		}
	}
}

// syncSink is a sink that can be read while the feed writes to it.
type syncSink struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	closed chan struct{}
	once   sync.Once
}

func (s *syncSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}
func (s *syncSink) Close() error { s.once.Do(func() { close(s.closed) }); return nil }
func (s *syncSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func eventuallySink(t *testing.T, s *syncSink, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.String() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("fed %q, want %q", s.String(), want)
}

func TestParseOutputTakesTheQueryFirst(t *testing.T) {
	tests := []struct {
		name  string
		out   string
		query string
		sel   []Selection
	}{
		{"query and a selection", "fix\n12\tfirst\n", "fix", []Selection{{12, "first"}}},
		{"query and several", "'exact term\n3\tc\n1\ta\n", "'exact term", []Selection{{3, "c"}, {1, "a"}}},
		{"nothing matched: just the query", "zzz\n", "zzz", nil},
		{"an empty query", "\n5\tfive\n", "", []Selection{{5, "five"}}},
		{"no output", "", "", nil},
		{"a query with a tab in it is still one line", "a\tb\n7\tseven\n", "a\tb", []Selection{{7, "seven"}}},
	}
	for _, tt := range tests {
		q, sel := parseOutput([]byte(tt.out))
		if q != tt.query || !reflect.DeepEqual(sel, tt.sel) {
			t.Errorf("%s: %q %v, want %q %v", tt.name, q, sel, tt.query, tt.sel)
		}
	}
}

// fakeFzfPrints stands in for fzf: it prints $FAKE_STDOUT and exits with
// $FAKE_EXIT, and, when asked to --filter, prints $FAKE_FILTER_OUT.
const fakeFzfPrints = `#!/bin/sh
case " $* " in *" --filter="*)
  printf '%s\n' "$@" > "$FAKE_ARGS"
  printf '%s' "$FAKE_FILTER_OUT"
  [ -n "$FAKE_FILTER_OUT" ] && exit 0 || exit 1;;
esac
printf '%s\n' "$@" > "$FAKE_ARGS"
printf '%s' "$FAKE_STDOUT"
exit ${FAKE_EXIT:-0}
`

func fakeFzf(t *testing.T) (path, args string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "fzf")
	if err := os.WriteFile(path, []byte(fakeFzfPrints), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, filepath.Join(dir, "args")
}

func TestRunReturnsTheQuery(t *testing.T) {
	fzf, args := fakeFzf(t)
	tests := []struct {
		name   string
		exit   string
		stdout string
		code   int
		query  string
		sel    []Selection
	}{
		{"accepted", "0", "fix bug\n3\tthree\n9\tnine\n", 0, "fix bug", []Selection{{3, "three"}, {9, "nine"}}},
		{"nothing matched, but Enter was pressed", "1", "zzz\n", 1, "zzz", nil},
		{"canceled: whatever was printed is ignored", "130", "half typed\n3\tthree\n", 130, "", nil},
		{"an error", "2", "oops\n", 2, "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(Options{
				FzfPath: fzf, Config: config.Default(), Ring: ringOf(3, 3),
				Environ: []string{"FAKE_ARGS=" + args, "FAKE_EXIT=" + tt.exit, "FAKE_STDOUT=" + tt.stdout},
				Version: func(string) (string, error) { return "0.74.4", nil },
			})
			res := s.Run("", NewestLast)
			if res.Code != tt.code || res.Query != tt.query || !reflect.DeepEqual(res.Selected, tt.sel) {
				t.Errorf("%+v", res)
			}
			// --print-query is asked for last, after the user's options.
			data, _ := os.ReadFile(args)
			if !strings.HasSuffix(string(data), "--print-query\n") {
				t.Errorf("args: %q", data)
			}
		})
	}
}

func TestMatchesWithAFakeFzf(t *testing.T) {
	fzf, args := fakeFzf(t)
	cfg := config.Default()
	cfg.FzfOpts = "--exact --no-mouse"
	mk := func(filterOut string) *Searcher {
		return New(Options{
			FzfPath: fzf, Config: cfg, Ring: ringOf(10, 10),
			Environ: []string{"FAKE_ARGS=" + args, "FAKE_FILTER_OUT=" + filterOut},
		})
	}
	// Whatever order fzf prints them in, the numbers come back sorted.
	got, err := mk("7\tseven\n2\ttwo\n9\tnine\n").Matches("line", "--smart-case")
	if err != nil || !reflect.DeepEqual(got, []uint64{2, 7, 9}) {
		t.Fatalf("%v %v", got, err)
	}
	data, _ := os.ReadFile(args)
	want := []string{"--ansi", "--no-sort", "--multi", "--delimiter", `\t`, "--with-nth", "2..", "--exact", "--no-mouse", "--smart-case", "--filter=line"}
	if strings.Join(strings.Fields(strings.ReplaceAll(string(data), "\n", " ")), " ") != strings.Join(want, " ") {
		t.Errorf("args:\n%s", data)
	}
	// No line matched: not an error.
	if got, err := mk("").Matches("line", ""); err != nil || len(got) != 0 {
		t.Errorf("%v %v", got, err)
	}
	// An empty query asks nothing and matches nothing. A bad option is reported.
	if got, err := mk("1\tx\n").Matches("", ""); got != nil || err != nil {
		t.Errorf("%v %v", got, err)
	}
	if _, err := mk("").Matches("q", "'broken"); err == nil {
		t.Error("an unterminated quote in the options must be an error")
	}
	// A fzf that cannot run.
	bad := New(Options{FzfPath: filepath.Join(t.TempDir(), "missing"), Config: config.Default(), Ring: ringOf(1, 1)})
	if _, err := bad.Matches("q", ""); err == nil {
		t.Error("a missing fzf must be an error")
	}
}

// TestMatchesWithRealFzf checks the matching itself, including fzf's own query
// syntax, against the installed fzf.
func TestMatchesWithRealFzf(t *testing.T) {
	fzf, err := exec.LookPath("fzf")
	if err != nil {
		t.Skip("fzf is not installed")
	}
	ring := buffer.New(20)
	for _, s := range []string{"request failed", "ok", "request ok", "r e q u e s t scattered", "failed again", "Request FAILED"} {
		ring.Append([]byte(s))
	}
	mk := func(opts string) *Searcher {
		cfg := config.Default()
		cfg.FzfOpts = opts
		return New(Options{FzfPath: fzf, Config: cfg, Ring: ring, Environ: os.Environ()})
	}
	check := func(query, opts string, want ...uint64) {
		t.Helper()
		got, err := mk(opts).Matches(query, "")
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q (%q): %v %v, want %v", query, opts, got, err, want)
		}
	}
	check("'failed", "", 1, 5, 6)                  // an exact term. A lower case query ignores case (smart case)
	check("rqst", "", 1, 3, 4, 6)                  // fuzzy by default: the scattered "r e q u e s t" too
	check("rqst", "--exact")                       // --exact: only the literal text, which is nowhere
	check("^request", "", 1, 3, 6)                 // a prefix
	check("failed$ !again", "", 1, 6)              // a suffix, and not "again"
	check("'FAILED", "--no-ignore-case", 6)        // fzf_opts change the matching, and so the matches
	check("nomatchatall", "")                      // nothing
	check("failed | ok", "--exact", 1, 2, 3, 5, 6) // either
}

// fzf is told to keep as many lines as the ring does, but only if it knows --tail.
func TestTailFollowsTheRing(t *testing.T) {
	fzf, args := fakeFzf(t)
	tests := []struct {
		version string
		want    bool
	}{
		{"0.52.1 (brew)", false},
		{"0.53.0", true},
		{"0.74.4", true},
		{"not a version", true}, // unreadable: assumed recent, like everywhere else
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			s := New(Options{
				FzfPath: fzf, Config: config.Default(), Ring: ringOf(3, 1234),
				Environ: []string{"FAKE_ARGS=" + args, "FAKE_EXIT=0", "FAKE_STDOUT=\n"},
				Version: func(string) (string, error) { return tt.version, nil },
			})
			s.Run("", NewestLast)
			data, _ := os.ReadFile(args)
			got := strings.Contains(string(data), "--tail\n1234\n")
			if got != tt.want {
				t.Errorf("--tail 1234 in the arguments: %v, want %v\n%s", got, tt.want, data)
			}
		})
	}
}

// Which colors an fzf version gets: a color name that the version does not know
// is left out, because fzf refuses a color it has never heard of.
func TestColorArgLeavesOutWhatFzfDoesNotKnow(t *testing.T) {
	colors := []Color{
		{"pointer", "#e66bc0"}, {"gutter", "#1f1933"}, {"separator", "#4e437a"},
		{"scrollbar", "#4e437a"}, {"input-bg", "#2d2547"}, {"input-fg", "#e4dcf7"},
	}
	tests := []struct {
		name    string
		version Version
		known   bool
		want    string
	}{
		{"0.34.0 has no separator", Version{0, 34, 0}, true, "dark,pointer:#e66bc0,gutter:#1f1933"},
		{"0.35.0 has the separator", Version{0, 35, 0}, true, "dark,pointer:#e66bc0,gutter:#1f1933,separator:#4e437a"},
		{"0.36.0 has the scrollbar", Version{0, 36, 0}, true, "dark,pointer:#e66bc0,gutter:#1f1933,separator:#4e437a,scrollbar:#4e437a"},
		{"0.57.0 has no input colors", Version{0, 57, 0}, true, "dark,pointer:#e66bc0,gutter:#1f1933,separator:#4e437a,scrollbar:#4e437a"},
		{"0.58.0 has them all", Version{0, 58, 0}, true, "dark,pointer:#e66bc0,gutter:#1f1933,separator:#4e437a,scrollbar:#4e437a,input-bg:#2d2547,input-fg:#e4dcf7"},
		{"unreadable: taken as recent", Version{}, false, "dark,pointer:#e66bc0,gutter:#1f1933,separator:#4e437a,scrollbar:#4e437a,input-bg:#2d2547,input-fg:#e4dcf7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := colorArg("dark", colors, tt.version, tt.known); got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
	if got := colorArg("dark", nil, Version{0, 74, 4}, true); got != "" {
		t.Errorf("no colors, but %q", got)
	}
	if got := colorArg("light", []Color{{"input-bg", "#ded4f2"}}, Version{0, 40, 0}, true); got != "" {
		t.Errorf("only a color the version does not know: %q", got)
	}
}

// The colors reach fzf as one --color, with the base scheme first.
func TestColorsReachFzf(t *testing.T) {
	fzf, args := fakeFzf(t)
	run := func(opts Options) string {
		opts.FzfPath, opts.Config, opts.Ring = fzf, config.Default(), ringOf(3, 10)
		opts.Environ = []string{"FAKE_ARGS=" + args, "FAKE_EXIT=0", "FAKE_STDOUT=\n"}
		opts.Version = func(string) (string, error) { return "0.57.0", nil }
		New(opts).Run("", NewestLast)
		data, _ := os.ReadFile(args)
		return string(data)
	}
	got := run(Options{ColorBase: "light", Colors: []Color{{"pointer", "#ae1e81"}, {"input-bg", "#ded4f2"}}})
	if !strings.Contains(got, "--color\nlight,pointer:#ae1e81\n") {
		t.Errorf("0.57.0 knows the pointer but not input-bg: %s", got)
	}
	if got := run(Options{}); strings.Contains(got, "--color") {
		t.Errorf("no colors asked for, but: %s", got)
	}
}
