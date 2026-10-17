//go:build !windows

package mgnt

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// numbered returns "line01".."lineNN".
func numbered(n int) []string {
	var out []string
	for i := 1; i <= n; i++ {
		out = append(out, fmt.Sprintf("line%02d", i))
	}
	return out
}

// bulk writes the lines in one go, so that they arrive as a single batch, the
// way a command that has output ready does.
func (h *harness) bulk(lines []string) {
	h.t.Helper()
	if _, err := io.WriteString(h.in, strings.Join(lines, "\n")+"\n"); err != nil {
		h.t.Fatal(err)
	}
}

// lastFrame is the output since the pager last painted the whole screen.
func lastFrame(out string) string {
	if i := strings.LastIndex(out, "\x1b[H\x1b[J"); i >= 0 {
		return out[i:]
	}
	return ""
}

// screen is what the pager's latest frame shows, on a terminal of the
// harness's current size.
func (h *harness) screen() []string {
	rows, cols := h.term.Size()
	return screenOf(lastFrame(h.stdout.String()), rows, cols).lines()
}

func (h *harness) waitScreen(what string, cond func([]string) bool) []string {
	h.t.Helper()
	var last []string
	h.eventually(what, func() bool { last = h.screen(); return cond(last) })
	return last
}

// waitStatus waits for a status line starting with prefix.
func (h *harness) waitStatus(prefix string) []string {
	h.t.Helper()
	return h.waitScreen("status "+strconv.Quote(prefix), func(l []string) bool {
		return strings.HasPrefix(strings.TrimLeft(l[len(l)-1], " "), strings.TrimLeft(prefix, " "))
	})
}

// waitHints waits for a message in the hint row, the one above the position.
func (h *harness) waitHints(prefix string) []string {
	h.t.Helper()
	return h.waitScreen("hints "+strconv.Quote(prefix), func(l []string) bool {
		return strings.HasPrefix(strings.TrimLeft(l[len(l)-2], " "), strings.TrimLeft(prefix, " "))
	})
}

func (h *harness) inAlt() bool {
	rows, cols := h.term.Size()
	return screenOf(h.stdout.String(), rows, cols).alt
}

func (h *harness) waitPager() {
	h.t.Helper()
	h.eventually("the pager", func() bool { return h.inAlt() })
}

// expectPage checks the content rows of a screen against lines first..last of all.
func expectPage(t *testing.T, got []string, all []string, first, last int) {
	t.Helper()
	want := all[first-1 : last]
	content := got[:len(got)-footerRows]
	for i, w := range want {
		if content[i] != w {
			t.Errorf("row %d: %q, want %q\nscreen: %q", i, content[i], w, got)
			return
		}
	}
	for i := len(want); i < len(content); i++ {
		if content[i] != "" {
			t.Errorf("row %d should be empty: %q", i, content[i])
		}
	}
}

func TestShortOutputIsJustPrinted(t *testing.T) {
	h := newHarness(t)
	h.term.rows = 24
	h.start()
	h.bulk(numbered(23)) // exactly the 23 rows above the status row
	h.in.Close()
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d", code)
	}
	if got := h.stdout.String(); got != strings.Join(numbered(23), "\n")+"\n" {
		t.Errorf("output %q", got)
	}
	h.restored()
}

func TestBulkOutputOpensAFullScreenPager(t *testing.T) {
	h := newHarness(t)
	h.term.rows, h.term.cols = 11, 60
	h.start()
	all := numbered(30)
	h.bulk(all)
	h.waitPager()
	got := h.waitStatus(" lines 1-9 of 30+  30%")
	expectPage(t, got, all, 1, 9)
	if cb, reading := h.term.state(); !cb || !reading {
		t.Error("the pager must read keys in cbreak mode")
	}
	if !strings.HasPrefix(h.stdout.String(), altOn) {
		t.Errorf("the first thing written must switch to the alternate screen: %q", h.stdout.String()[:20])
	}
	// Nothing was printed to the normal screen first.
	if !strings.HasPrefix(altOn, "\x1b[?1049h") || !strings.Contains(altOn, "?1007h") {
		t.Error("the mouse wheel should scroll the pager (alternate scroll mode)")
	}
}

func TestScrollingBothWays(t *testing.T) {
	h := newHarness(t)
	h.term.rows, h.term.cols = 11, 60 // 9 lines per screen
	h.start()
	all := numbered(30)
	h.bulk(all)
	h.in.Close() // so that the pager knows the end: 30 lines
	h.waitStatus(" lines 1-9 of 30")

	steps := []struct {
		name, keys  string
		first, last int
	}{
		{"j: a line down", "j", 2, 10},
		{"k: a line up", "k", 1, 9},
		{"enter: a line down", "\r", 2, 10},
		{"up arrow", "\x1b[A", 1, 9},
		{"down arrow", "\x1b[B", 2, 10},
		{"ctrl-p / ctrl-n", "\x10\x0e", 2, 10},
		{"space: next page", " ", 11, 19},
		{"f: next page", "f", 20, 28},
		{"space at the end stops with the last line at the bottom", " ", 22, 30},
		{"b: previous page", "b", 13, 21},
		{"page down key", "\x1b[6~", 22, 30},
		{"page up key", "\x1b[5~", 13, 21},
		{"d: half a screen down", "d", 17, 25},
		{"u: half a screen up", "u", 13, 21},
		{"g: the top", "g", 1, 9},
		{"G: the end", "G", 22, 30},
		{"home key", "\x1b[H", 1, 9},
		{"end key", "\x1b[F", 22, 30},
		{"k at the top stays", "g" + "k", 1, 9},
		{"j at the end stays", "G" + "j", 22, 30},
		{"ctrl-b / ctrl-f", "\x02", 13, 21},
	}
	for _, s := range steps {
		h.term.press(s.keys)
		got := h.waitStatus(fmt.Sprintf(" lines %d-%d of 30", s.first, s.last))
		t.Run(s.name, func(t *testing.T) { expectPage(t, got, all, s.first, s.last) })
	}
	if end := h.screen(); !strings.Contains(end[len(end)-1], "lines 13-21 of 30") {
		t.Errorf("%q", end[len(end)-1])
	}
	h.term.press("G")
	if got := h.waitStatus(" lines 22-30 of 30  100% (END)"); got == nil {
		t.Fatal("no END marker at the end of a finished input")
	}
	h.term.press("q")
	h.wait()
}

func TestQuitGivesTheScreenBack(t *testing.T) {
	h := newHarness(t)
	h.term.rows = 8
	h.start()
	h.bulk(numbered(40))
	h.waitPager()
	h.term.press("j")
	h.term.press("q")
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d", code)
	}
	out := h.stdout.String()
	if !strings.HasSuffix(out, altOff) {
		t.Errorf("the alternate screen was not left last: %q", out[len(out)-30:])
	}
	// The terminal is as it was: nothing was printed to the normal screen.
	rows, cols := h.term.Size()
	end := screenOf(out, rows, cols)
	if end.alt || strings.Join(end.lines(), "") != "" {
		t.Errorf("leftovers on the normal screen: %q", end.lines())
	}
	h.restored()
}

func TestWrappedLinesTakeSeveralRows(t *testing.T) {
	h := newHarness(t)
	h.term.rows, h.term.cols = 9, 20 // 7 rows of 20 columns
	h.start()
	long := func(c string) string { return strings.Repeat(c, 45) } // 3 rows each
	h.bulk([]string{long("a"), long("b"), long("c"), long("d"), long("e")})
	got := h.waitStatus(" lines 1-2 of 5+")
	// Two lines take 6 rows. A third would need 9 > 7, so it is not cut in half: the row stays empty.
	want := []string{strings.Repeat("a", 20), strings.Repeat("a", 20), strings.Repeat("a", 5),
		strings.Repeat("b", 20), strings.Repeat("b", 20), strings.Repeat("b", 5), ""}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("row %d: %q, want %q", i, got[i], w)
		}
	}
	// Scrolling is by whole lines.
	h.term.press("j")
	got = h.waitStatus(" lines 2-3 of 5+")
	if got[0] != strings.Repeat("b", 20) || got[3] != strings.Repeat("c", 20) {
		t.Errorf("%q", got)
	}
	h.term.press("q")
	h.wait()
}

func TestTabsAndColorDoNotCountAsWidth(t *testing.T) {
	h := newHarness(t)
	delete(h.vars, "NO_COLOR")
	h.vars["COLORTERM"] = "truecolor"
	h.term.rows, h.term.cols = 7, 40
	h.start()
	colored := "\x1b[31m" + strings.Repeat("x", 38) + "\x1b[0m"
	var lines []string
	for i := 0; i < 12; i++ {
		lines = append(lines, colored)
	}
	h.bulk(lines)
	got := h.waitStatus(" lines 1-5 of 12")
	for i := 0; i < 5; i++ {
		if got[i] != strings.Repeat("x", 38) {
			t.Errorf("row %d: %q", i, got[i])
		}
	}
	h.term.press("q")
	h.wait()
}

// The producer must be held back while the pager has enough, like `git log | less`,
// and let go when the end is asked for.
func TestThePagerHoldsTheProducerBackUntilTheEndIsAsked(t *testing.T) {
	h := newHarness(t)
	h.term.rows = 11
	h.start()
	var chunks atomic.Int64
	finished := make(chan struct{})
	const total, per = 4000, 100 // 400,000 lines, about 24 MB
	go func() {
		defer close(finished)
		n := 0
		for i := 0; i < total; i++ {
			var b strings.Builder
			for j := 0; j < per; j++ {
				n++
				fmt.Fprintf(&b, "2026-10-06 10:00:00 INFO a log line of realistic length number %d\n", n)
			}
			if _, err := io.WriteString(h.in, b.String()); err != nil {
				return
			}
			chunks.Add(1)
		}
		h.in.Close()
	}()
	h.waitStatus(" lines 1-9 of ")
	time.Sleep(400 * time.Millisecond)
	a := chunks.Load()
	time.Sleep(400 * time.Millisecond)
	if b := chunks.Load(); a != b || a >= total/2 {
		t.Fatalf("the producer was not held back: %d chunks written, then %d (of %d)", a, b, total)
	}
	// G reads the rest, then shows the end.
	h.term.press("G")
	select {
	case <-finished:
	case <-time.After(60 * time.Second):
		t.Fatal("the producer never finished after G")
	}
	got := h.waitStatus(" lines 399992-400000 of 400000  100% (END)")
	if !strings.HasSuffix(got[0], "number 399992") || !strings.HasSuffix(got[8], "number 400000") {
		t.Errorf("%q", got)
	}
	// What the buffer still holds can be scrolled back through.
	h.term.press("g")
	top := h.waitScreen("the oldest line still in the buffer", func(l []string) bool {
		info := strings.TrimSpace(l[len(l)-1])
		return strings.HasPrefix(info, "lines ") && !strings.HasPrefix(info, "lines 399992")
	})
	var first int
	fmt.Sscanf(strings.TrimSpace(top[len(top)-1]), "lines %d-", &first)
	if first != 400000-100000+1 { // the default buffer holds 100,000 lines
		t.Errorf("oldest line in the buffer is %d", first)
	}
	h.term.press("q")
	h.wait()
	h.in.CloseWithError(io.ErrClosedPipe)
}

func TestSearchCoversTheWholeLogAndJumpsToTheAnswer(t *testing.T) {
	h := newHarness(t)
	h.term.rows, h.term.cols = 11, 60
	h.start()
	var lines []string
	for i := 1; i <= 20000; i++ {
		lines = append(lines, fmt.Sprintf("line%05d", i))
	}
	h.bulk(lines)
	h.waitStatus(" lines 1-9 of ")
	h.term.press("/")
	h.waitFzf()
	// The pager has given the screen back before fzf runs.
	if h.inAlt() {
		t.Error("still on the alternate screen when fzf started")
	}
	if cb, reading := h.term.state(); cb || reading {
		t.Error("terminal not released")
	}
	// Read from the first line (no --tac), with the list above the prompt as always.
	if args := h.file("args"); !strings.Contains(args, "--layout\ndefault\n") || strings.Contains(args, "--tac") || strings.Contains(args, "reverse-list") {
		t.Errorf("args: %q", args)
	}
	// fzf gets the whole log, not just the screens the pager had read.
	h.eventually("the whole log fed to fzf", func() bool { return strings.Contains(h.file("stdin"), "20000\tline20000\n") })

	h.set("out", "15000\tline15000\n12345\tline12345\n")
	h.release()
	// The pager is back, jumped to the first of the answers (the smaller line number).
	got := h.waitStatus(" lines 12345-12353 of ")
	if got[0] != "line12345" || got[8] != "line12353" {
		t.Errorf("%q", got)
	}
	if !h.inAlt() {
		t.Error("not back on the alternate screen")
	}
	if cb, reading := h.term.state(); !cb || !reading {
		t.Error("keys not read again")
	}
	// And it scrolls on from there.
	h.term.press("k")
	h.waitStatus(" lines 12344-12352 of ")
	h.term.press("q")
	h.wait()
	h.restored()
}

func TestSearchWhoseAnswerIsGoneOrNotGiven(t *testing.T) {
	smallRing(t)
	h := newHarness(t)
	h.config("buffer = 50\n")
	h.term.rows, h.term.cols = 11, 70
	h.start()
	h.bulk(numbered(40))
	h.waitStatus(" lines 1-9 of ")
	h.term.press("/")
	h.waitFzf()
	h.set("exit", "130") // Esc: nothing chosen
	h.release()
	h.waitPager()
	got := h.waitStatus(" lines 1-9 of ")
	if strings.Contains(got[len(got)-2], "fzf failed") {
		t.Errorf("canceling is not a failure: %q", got[len(got)-2])
	}
	h.term.press("/")
	h.eventually("second fzf", func() bool { return h.fzfStarts() == 2 })
	h.set("exit", "2")
	h.release()
	h.waitHints("fzf failed: fzf exited with status 2")
	// The message goes at the next key.
	h.term.press("j")
	h.waitStatus(" lines 2-10 of ")
	h.term.press("q")
	h.wait()
}

func TestPagerWithoutFzf(t *testing.T) {
	h := newHarness(t)
	h.noFzf = true
	h.term.rows = 7
	h.start()
	h.bulk(numbered(20))
	h.waitStatus(" lines 1-5 of ")
	h.term.press("/")
	h.waitHints("fzf was not found, so there is no search")
	h.term.press("j")
	h.waitStatus(" lines 2-6 of ")
	h.term.press("q")
	h.wait()
	h.restored()
}

func TestResizeRepaints(t *testing.T) {
	h := newHarness(t)
	h.term.rows, h.term.cols = 11, 40
	h.start()
	h.bulk(numbered(50))
	h.waitStatus(" lines 1-9 of ")
	h.term.mu.Lock()
	h.term.rows = 7
	h.term.mu.Unlock()
	h.sigs <- syscall.SIGWINCH
	got := h.waitStatus(" lines 1-5 of ")
	if len(got) != 7 {
		t.Errorf("%d rows", len(got))
	}
	h.term.press("q")
	h.wait()
}

func TestSignalsLeaveTheAlternateScreen(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		h := newHarness(t)
		h.term.rows = 6
		h.start()
		h.bulk(numbered(30))
		h.waitPager()
		h.sigs <- sig
		want := map[syscall.Signal]int{syscall.SIGINT: 130, syscall.SIGTERM: 143}[sig]
		if code := h.wait(); code != want {
			t.Errorf("%v: exit %d", sig, code)
		}
		if !strings.HasSuffix(h.stdout.String(), altOff) || h.inAlt() {
			t.Errorf("%v: left on the alternate screen", sig)
		}
		h.restored()
	}
}

func TestPanicLeavesTheAlternateScreen(t *testing.T) {
	h := newHarness(t)
	h.term.panicAt = 2 // the Cbreak call after the search
	h.term.rows = 6
	pr, pw := io.Pipe()
	defer pw.Close()
	finished := make(chan any, 1)
	go func() {
		defer func() { finished <- recover() }()
		Run(h.env(pr), nil)
	}()
	io.WriteString(pw, strings.Join(numbered(30), "\n")+"\n")
	h.waitPager()
	h.term.press("/")
	h.waitFzf()
	h.release()
	select {
	case r := <-finished:
		if r == nil {
			t.Fatal("expected the panic to propagate")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no panic")
	}
	if h.inAlt() {
		t.Error("the alternate screen was left on after a panic")
	}
	if cb, _ := h.term.state(); cb {
		t.Error("terminal left in cbreak mode")
	}
}

// Logs that trickle in are not paged, and that includes the screen: no
// alternate screen, no repainting.
func TestTrickleOfLinesIsNeverPaged(t *testing.T) {
	h := newHarness(t)
	h.term.rows = 6 // a page is 5 lines. The log is 40
	h.start()
	for _, l := range numbered(40) {
		h.send(l)
		h.waitOut(l + "\n") // one line at a time, like tail -f
	}
	if out := h.stdout.String(); strings.Contains(out, "\x1b[?1049") || out != strings.Join(numbered(40), "\n")+"\n" {
		t.Errorf("%q", out)
	}
	h.in.Close()
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d", code)
	}
	h.restored()
}

func TestAFollowedLogStartsItsSearchAtItsEnd(t *testing.T) {
	h := newHarness(t)
	h.term.rows = 6
	h.start()
	for _, l := range numbered(4) {
		h.send(l)
		h.waitOut(l + "\n")
	}
	h.term.press("/")
	h.waitFzf()
	if args := h.file("args"); !strings.Contains(args, "--tac\n") || !strings.Contains(args, "--layout\ndefault\n") || strings.Contains(args, "reverse-list") {
		t.Errorf("a followed log must start at its end: %q", args)
	}
	h.release()
	h.in.Close()
	h.wait()
}

func TestADumbTerminalIsNeverRepainted(t *testing.T) {
	h := newHarness(t)
	h.vars["TERM"] = "dumb"
	h.term.rows = 6
	h.start()
	h.bulk(numbered(40))
	h.in.Close()
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d", code)
	}
	if got := h.stdout.String(); got != strings.Join(numbered(40), "\n")+"\n" {
		t.Errorf("%q", got)
	}
}

func TestEmptyInput(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.in.Close()
	if code := h.wait(); code != 0 || h.stdout.String() != "" {
		t.Errorf("exit %d output %q", code, h.stdout.String())
	}
	h.restored()
}

func TestOneLineMoreThanAScreenIsPaged(t *testing.T) {
	h := newHarness(t)
	h.term.rows = 10
	h.start()
	h.bulk(numbered(10)) // 9 rows are printed as they are. Ten are paged, two rows going to the footer
	h.waitPager()
	h.waitStatus(" lines 1-8 of 10+")
	h.term.press("q")
	h.wait()

	h = newHarness(t)
	h.term.rows = 10
	h.start()
	h.bulk(numbered(9)) // exactly a screenful: printed
	h.in.Close()
	if code := h.wait(); code != 0 || strings.Contains(h.stdout.String(), "\x1b[?1049") {
		t.Errorf("exit %d output %q", code, h.stdout.String())
	}
}

func TestNoInteractiveNeverPages(t *testing.T) {
	h := newHarness(t)
	h.term.rows = 5
	pr, pw := io.Pipe()
	env := h.env(pr)
	go func() {
		io.WriteString(pw, strings.Join(numbered(100), "\n")+"\n")
		pw.Close()
	}()
	if code := Run(env, []string{"--no-interactive"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if h.stdout.String() != strings.Join(numbered(100), "\n")+"\n" || h.opened != 0 {
		t.Error("--no-interactive paged or opened the terminal")
	}
}

// The footer looks like fzf's own header and info line: muted text with the
// same two-space gutter, then the position and a rule across the width. It
// shrinks to fit the width instead of being cut off mid-word.
func TestFooterLooksLikeFzfs(t *testing.T) {
	h := newHarness(t)
	h.term.rows, h.term.cols = 9, 80
	h.start()
	h.bulk(numbered(40))
	got := h.waitStatus("lines 1-7 of 40+")
	hints, info := got[len(got)-2], got[len(got)-1]
	// 80 columns hold these, "w: chop" and "#: numbers" are the first to go.
	if hints != "  space/b: page   j/k: line   g/G: top/end   F: follow   /: search   q: quit" {
		t.Errorf("hints %q", hints)
	}
	// The position, then a rule to the right edge, like fzf's "56/662 (0) ────".
	if !strings.HasPrefix(info, "  lines 1-7 of 40+  17% ─") || len([]rune(info)) != 80 || strings.Trim(info[strings.Index(info, "─"):], "─") != "" {
		t.Errorf("info %q (%d wide)", info, len([]rune(info)))
	}
	// Nothing is shown in reverse video.
	if strings.Contains(h.stdout.String(), "\x1b[7m") {
		t.Error("the footer must not be a reversed bar")
	}
	h.term.press("q")
	h.wait()
}

// The footer of the pager looks like the top and the count of fzf, so it takes
// the same colors: ui.hint, ui.info, and ui.border of the colorscheme.
func TestFooterColorsAreTheColorschemes(t *testing.T) {
	for _, tt := range []struct {
		theme, hints, inf, rul string
	}{
		{"dark", "\x1b[38;2;230;107;192m", "\x1b[38;2;154;140;198m", "\x1b[38;2;78;67;122m"},
		{"light", "\x1b[38;2;174;30;129m", "\x1b[38;2;106;90;153m", "\x1b[38;2;191;174;221m"},
	} {
		t.Run(tt.theme, func(t *testing.T) {
			h := newHarness(t)
			h.vars = map[string]string{"COLORTERM": "truecolor", "MGNT_BACKGROUND": tt.theme}
			h.term.rows, h.term.cols = 9, 70
			h.start()
			h.bulk(numbered(40))
			h.waitStatus("lines 1-7 of")
			out := lastFrame(h.stdout.String())
			hintsAt, infoAt, ruleAt := strings.Index(out, "space/b"), strings.Index(out, "lines 1-7"), strings.Index(out, "─")
			if !strings.Contains(out[:hintsAt], tt.hints) || !strings.Contains(out[hintsAt:infoAt], tt.inf) || !strings.Contains(out[infoAt:ruleAt], tt.rul) {
				t.Errorf("footer colors: %q", out[max(0, len(out)-260):])
			}
			h.term.press("q")
			h.wait()
		})
	}
}

// A colorscheme without ui styles, such as mono, leaves the footer with the
// colors fzf has by default.
func TestFooterColorsAreFzfsDefaults(t *testing.T) {
	for _, tt := range []struct {
		name            string
		vars            map[string]string
		hints, inf, rul string
	}{
		{"256 colors", map[string]string{"TERM": "xterm-256color"}, "\x1b[38;5;109m", "\x1b[38;5;144m", "\x1b[38;5;59m"},
		{"truecolor", map[string]string{"COLORTERM": "truecolor"}, "\x1b[38;5;109m", "\x1b[38;5;144m", "\x1b[38;5;59m"},
		{"16 colors", map[string]string{"TERM": "xterm"}, "\x1b[36m", "\x1b[33m", "\x1b[2m\x1b[39m"}, // cyan, yellow, dim
		{"no color", map[string]string{"NO_COLOR": "1"}, "", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.vars = tt.vars
			h.config("colorscheme = \"mono\"\n")
			h.term.rows, h.term.cols = 9, 70
			h.start()
			h.bulk(numbered(40))
			h.waitStatus("lines 1-7 of")
			out := lastFrame(h.stdout.String())
			hintsAt := strings.Index(out, "space/b")
			infoAt := strings.Index(out, "lines 1-7")
			if tt.hints == "" {
				if strings.Contains(out, "\x1b[3") || strings.Contains(out, "\x1b[2m") || strings.Contains(out, "\x1b[0m") {
					t.Errorf("color in NO_COLOR output: %q", out)
				}
			} else {
				ruleAt := strings.Index(out, "─")
				if !strings.Contains(out[:hintsAt], tt.hints) || !strings.Contains(out[hintsAt:infoAt], tt.inf) {
					t.Errorf("hint or info colors wrong: %q", out[len(out)-200:])
				}
				// The rule has its own, dimmer color, set just before it, like fzf does.
				if !strings.Contains(out[infoAt:ruleAt], tt.rul) {
					t.Errorf("rule color wrong: %q", out[infoAt:ruleAt])
				}
			}
			// The log text itself is not touched.
			if got := h.screen(); got[0] != "line01" {
				t.Errorf("%q", got[0])
			}
			h.term.press("q")
			h.wait()
		})
	}
}

func TestFooterShrinksToFit(t *testing.T) {
	full := "  space/b: page   j/k: line   g/G: top/end   F: follow   w: chop   #: numbers   /: search   q: quit"
	noNumbers := "  space/b: page   j/k: line   g/G: top/end   F: follow   w: chop   /: search   q: quit"
	noChop := "  space/b: page   j/k: line   g/G: top/end   F: follow   /: search   q: quit"
	noFollow := "  space/b: page   j/k: line   g/G: top/end   /: search   q: quit"
	noEnds := "  space/b: page   j/k: line   /: search   q: quit"
	noLine := "  space/b: page   /: search   q: quit"
	pageOnly := "  space/b: page   q: quit"
	quitOnly := "  q: quit"
	for _, tt := range []struct {
		cols  int
		hints string
	}{
		{120, full}, {len([]rune(full)), full},
		{len([]rune(full)) - 1, noNumbers}, {len([]rune(noNumbers)), noNumbers},
		{len([]rune(noNumbers)) - 1, noChop}, {len([]rune(noChop)), noChop},
		{len([]rune(noChop)) - 1, noFollow}, {len([]rune(noFollow)), noFollow},
		{len([]rune(noFollow)) - 1, noEnds}, {len([]rune(noEnds)), noEnds},
		{len([]rune(noEnds)) - 1, noLine}, {len([]rune(noLine)), noLine},
		{len([]rune(noLine)) - 1, pageOnly}, {len([]rune(pageOnly)), pageOnly},
		{len([]rune(pageOnly)) - 1, quitOnly}, {20, quitOnly},
	} {
		h := newHarness(t)
		h.term.rows, h.term.cols = 9, tt.cols
		h.start()
		h.bulk(numbered(40))
		got := h.waitStatus("lines 1-7 of 40+")
		hints, info := got[len(got)-2], got[len(got)-1]
		if hints != tt.hints {
			t.Errorf("%d columns: hints %q, want %q", tt.cols, hints, tt.hints)
		}
		if len([]rune(hints)) > tt.cols || len([]rune(info)) > tt.cols {
			t.Errorf("%d columns: rows are %d and %d wide", tt.cols, len([]rune(hints)), len([]rune(info)))
		}
		h.term.press("q")
		h.wait()
	}
}

// ---- following, chopping, line numbers, n and N ----------------------------------

// lastRow is the screen row of the log text, counted from the bottom of the content.
func contentOf(got []string) []string { return got[:len(got)-footerRows] }

func hintsOf(got []string) string { return got[len(got)-2] }

func TestFollowKeepsTheEndInView(t *testing.T) {
	h := newHarness(t)
	h.term.rows, h.term.cols = 9, 60 // 7 content rows
	h.start()
	h.bulk(numbered(30))
	h.waitStatus("lines 1-7 of 30+")

	h.term.press("F")
	got := h.waitStatus("lines 24-30 of 30+")
	if !strings.Contains(hintsOf(got), "following new lines") || !strings.Contains(got[len(got)-1], "[following]") {
		t.Errorf("following is not shown: %q", got[len(got)-2:])
	}
	// New lines scroll in at the bottom, and the screen stays on the end.
	for i := 31; i <= 33; i++ {
		h.send(fmt.Sprintf("late%02d", i))
	}
	got = h.waitStatus("lines 27-33 of 33+")
	if contentOf(got)[6] != "late33" {
		t.Errorf("%q", got)
	}
	// A movement key stops following and still moves. Lines that come later no longer drag the screen.
	h.term.press("k")
	got = h.waitStatus("lines 26-32 of 33+")
	if strings.Contains(got[len(got)-1], "following") || strings.Contains(hintsOf(got), "following") {
		t.Errorf("still following: %q", got[len(got)-2:])
	}
	h.send("late34")
	time.Sleep(150 * time.Millisecond)
	if got = h.screen(); !strings.Contains(got[len(got)-1], "lines 26-32 of") {
		t.Errorf("the screen moved after following was stopped: %q", got[len(got)-1])
	}
	// F again, then the input ends: following stops by itself and the end is marked.
	h.term.press("F")
	h.waitStatus("lines 28-34 of 34+")
	h.in.Close()
	got = h.waitStatus("lines 28-34 of 34  100% (END)")
	if strings.Contains(got[len(got)-1], "following") {
		t.Errorf("%q", got[len(got)-1])
	}
	// With the input over there is nothing to follow, and the hint for it is gone.
	h.term.press("F")
	h.waitScreen("a message that there is nothing to follow", func(l []string) bool {
		return strings.Contains(hintsOf(l), "nothing to follow")
	})
	h.term.press("j")
	got = h.screen()
	if strings.Contains(hintsOf(got), "F: follow") {
		t.Errorf("F is offered after the end: %q", hintsOf(got))
	}
	h.term.press("q")
	h.wait()
}

func TestGShowsTheEndOfAnInputThatStaysOpen(t *testing.T) {
	h := newHarness(t)
	h.term.rows, h.term.cols = 9, 60
	h.start()
	h.bulk(numbered(30)) // and then nothing more comes
	h.waitStatus("lines 1-7 of 30+")
	h.term.press("G")
	// It does not wait for ever: once the input has been quiet for a moment the end is shown.
	got := h.waitStatus("lines 24-30 of 30+")
	if contentOf(got)[6] != "line30" || strings.Contains(hintsOf(got), "reading") {
		t.Errorf("%q", got)
	}
	h.term.press("q")
	h.wait()
}

func TestChopAndScrollSideways(t *testing.T) {
	h := newHarness(t)
	h.term.rows, h.term.cols = 9, 30 // 7 content rows of 30 columns
	h.start()
	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, fmt.Sprintf("%02d:%s", i, strings.Repeat("abcdefghij", 7))) // 73 columns
	}
	h.bulk(lines)
	got := h.waitStatus("lines 1-2 of 20+") // wrapped: 3 rows a line, two lines and a gap
	if contentOf(got)[0] != "01:abcdefghijabcdefghijabcdefg" {
		t.Errorf("wrapped: %q", contentOf(got))
	}

	h.term.press("w")                // cut instead of wrap: a line is one row
	got = h.waitStatus("lines 1-7 ") // at this width the footer keeps the mode and drops "of 20+"
	if contentOf(got)[0] != "01:abcdefghijabcdefghijabcdefg" || contentOf(got)[1] != "02:abcdefghijabcdefghijabcdefg" {
		t.Errorf("chopped: %q", contentOf(got))
	}
	if !strings.Contains(got[len(got)-1], "[chop, column 1]") || !strings.Contains(hintsOf(got), "long lines are cut") {
		t.Errorf("not shown: %q", got[len(got)-2:])
	}

	// Right and left scroll by half a screen (15 columns): the text moves under the same rows.
	h.term.press("l")
	got = h.waitScreen("column 16", func(l []string) bool { return strings.Contains(l[len(l)-1], "column 16") })
	if contentOf(got)[0] != "cdefghijabcdefghijabcdefghijab" { // columns 16 to 45 of line 1
		t.Errorf("scrolled: %q", contentOf(got)[0])
	}
	h.term.press("\x1b[C") // right arrow
	h.waitScreen("column 31", func(l []string) bool { return strings.Contains(l[len(l)-1], "column 31") })
	h.term.press("h")
	h.waitScreen("column 16 again", func(l []string) bool { return strings.Contains(l[len(l)-1], "column 16") })
	h.term.press("\x1b[D")
	h.waitScreen("column 1 again", func(l []string) bool { return strings.Contains(l[len(l)-1], "column 1]") })
	h.term.press("h") // not past the start
	h.term.press("j")
	h.waitStatus("lines 2-8 ")
	// Far to the right it stops where the longest line on screen ends.
	for i := 0; i < 10; i++ {
		h.term.press("l")
	}
	got = h.waitScreen("the last column", func(l []string) bool {
		var col int
		fmt.Sscanf(l[len(l)-1][strings.Index(l[len(l)-1], "column ")+7:], "%d", &col)
		return col > 60
	})
	var col int
	fmt.Sscanf(got[len(got)-1][strings.Index(got[len(got)-1], "column ")+7:], "%d", &col)
	if col != 73 { // the longest line is 73 wide. Its last column is the first one left
		t.Errorf("scrolled to column %d", col)
	}
	if contentOf(got)[0] != "j" {
		t.Errorf("the end of the line: %q", contentOf(got)[0])
	}

	h.term.press("w") // back to wrapping: the column goes with it
	got = h.waitStatus("lines 2-3 ")
	if strings.Contains(got[len(got)-1], "chop") {
		t.Errorf("%q", got[len(got)-1])
	}
	h.term.press("l") // sideways keys explain themselves while wrapping
	h.waitHints("long lines are wrapped")
	h.term.press("q")
	h.wait()
}

func TestLineNumbers(t *testing.T) {
	h := newHarness(t)
	h.term.rows, h.term.cols = 9, 20 // 7 content rows
	h.start()
	var lines []string
	for i := 1; i <= 120; i++ {
		lines = append(lines, "x")
	}
	lines[1] = strings.Repeat("y", 30) // wraps at 20 columns (16 once there is a gutter)
	h.bulk(lines)
	h.waitStatus("lines 1-")
	h.term.press("#")
	got := h.waitScreen("a gutter", func(l []string) bool { return strings.HasPrefix(l[0], "  1 x") })
	// Three digits wide plus a space. The continuation of a wrapped line has an empty gutter.
	want := []string{"  1 x", "  2 " + strings.Repeat("y", 16), "    " + strings.Repeat("y", 14), "  3 x", "  4 x", "  5 x", "  6 x"}
	for i, w := range want {
		if contentOf(got)[i] != w {
			t.Errorf("row %d: %q, want %q", i, contentOf(got)[i], w)
		}
	}
	h.term.press("G")
	h.waitScreen("the numbers at the end", func(l []string) bool {
		return strings.Contains(hintsOf(l), "reading") || strings.HasPrefix(l[0], "11") || strings.HasPrefix(l[0], " 11")
	})
	h.term.press("#")
	h.waitScreen("no gutter", func(l []string) bool { return !strings.HasPrefix(l[0], " ") && strings.HasPrefix(l[0], "x") })
	h.term.press("q")
	h.wait()
}

func TestNextAndPreviousMatch(t *testing.T) {
	h := newHarness(t)
	h.config("fzf_opts = \"--exact\"\n")
	h.term.rows, h.term.cols = 9, 100
	h.start()
	h.bulk(numbered(300))
	h.waitStatus("lines 1-7 of ")

	h.term.press("n") // before any search
	h.waitHints("no search yet: press / first")

	h.term.press("/")
	h.waitFzf()
	h.set("query", "line\n")
	h.set("out", "100\tline100\n")
	h.set("filter_out", "20\tline20\n100\tline100\n250\tline250\n")
	h.release()
	// Accepting jumps to the chosen line, and fzf was asked which lines match, with the user's options.
	h.waitStatus("lines 100-106 of ")
	h.eventually("the matches to be looked up", func() bool { return strings.Contains(h.file("filter_args"), "--filter=line") })
	fa := h.file("filter_args")
	for _, want := range []string{"--ansi", "--no-sort", "--delimiter", "--exact", "--filter=line"} {
		if !strings.Contains(fa, want+"\n") {
			t.Errorf("filter arguments lack %q:\n%s", want, fa)
		}
	}
	for _, bad := range []string{"--tac", "--layout", "--preview", "--print-query"} {
		if strings.Contains(fa, bad) {
			t.Errorf("the filter run has %q:\n%s", bad, fa)
		}
	}
	// n and N are offered once the matches are in.
	h.eventually("n/N offered", func() bool { return strings.Contains(hintsOf(h.screen()), "n/N: match") })

	step := func(key, status, msg string) {
		t.Helper()
		h.term.press(key)
		got := h.waitStatus(status)
		h.eventually("message "+msg, func() bool { return strings.Contains(hintsOf(h.screen()), msg) })
		_ = got
	}

	step("n", "lines 250-256 of", "match 3 of 3, line 250")
	step("n", "lines 20-26 of", "match 1 of 3, line 20 (searching from the other end)")
	step("n", "lines 100-106 of", "match 2 of 3, line 100")
	step("N", "lines 20-26 of", "match 1 of 3, line 20")
	step("N", "lines 250-256 of", "match 3 of 3, line 250 (searching from the other end)")
	// After moving by hand, n goes on from where the screen is.
	h.term.press("g")
	h.waitStatus("lines 1-7 of")
	step("n", "lines 20-26 of", "match 1 of 3, line 20")
	h.term.press("j")
	h.term.press("j")
	step("N", "lines 20-26 of", "match 1 of 3, line 20") // below the top, 22 > 20: the one above is line 20

	h.term.press("q")
	h.wait()
	h.restored()
}

func TestNoMatchesAndCanceledSearch(t *testing.T) {
	h := newHarness(t)
	h.term.rows, h.term.cols = 9, 100
	h.start()
	h.bulk(numbered(50))
	h.waitStatus("lines 1-7 of ")
	h.term.press("/")
	h.waitFzf()
	h.set("query", "zzz\n")
	h.set("exit", "1") // nothing matched, and the user left with Enter
	h.release()
	h.waitScreen("the pager back", func(l []string) bool { return strings.Contains(l[len(l)-1], "lines 1-7") })
	h.eventually("n/N offered", func() bool { return strings.Contains(hintsOf(h.screen()), "n/N: match") })
	h.term.press("n")
	h.waitHints(`no line matches "zzz"`)
	// A search that is canceled leaves the last query alone.
	h.term.press("/")
	h.eventually("second fzf", func() bool { return h.fzfStarts() == 2 })
	h.set("exit", "130")
	h.release()
	h.waitPager()
	h.term.press("n")
	h.waitHints(`no line matches "zzz"`)
	h.term.press("q")
	h.wait()
}

func TestChopHintsAreOfferedWhileChopping(t *testing.T) {
	h := newHarness(t)
	h.term.rows, h.term.cols = 9, 120
	h.start()
	h.bulk(numbered(40))
	got := h.waitStatus("lines 1-7 of 40+")
	if strings.Contains(hintsOf(got), "h/l: side") || !strings.Contains(hintsOf(got), "w: chop") {
		t.Errorf("wrapping: %q", hintsOf(got))
	}
	h.term.press("w")
	h.term.press("j")
	got = h.waitScreen("hints while chopping", func(l []string) bool { return strings.Contains(hintsOf(l), "w: wrap") })
	if !strings.Contains(hintsOf(got), "h/l: side") || strings.Contains(hintsOf(got), "w: chop") {
		t.Errorf("chopping: %q", hintsOf(got))
	}
	h.term.press("q")
	h.wait()
}
