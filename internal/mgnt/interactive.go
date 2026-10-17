package mgnt

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/styd/magenta/internal/background"
	"github.com/styd/magenta/internal/buffer"
	"github.com/styd/magenta/internal/pipe"
	"github.com/styd/magenta/internal/render"
	"github.com/styd/magenta/internal/search"
	"github.com/styd/magenta/internal/tty"
)

// The ring is never smaller than pagerMinRing, so that lines which are read but
// not yet shown can never be evicted: the input in flight (one batch being
// handled, a few queued) is about 25,000 lines. It is a variable so that tests
// can make the ring small.
var pagerMinRing = 65536

// limitReadAhead turns the read-ahead limits on. Tests with a tiny ring turn
// them off.
var limitReadAhead = true

// decideAfter is how long the input must be quiet, once some lines have
// arrived, for mgnt to conclude that it is a log that trickles in.
const decideAfter = 60 * time.Millisecond

// mode is what the view does with the lines.
type mode int

const (
	// undecided: the first lines are being collected, to tell a command that
	// has a lot of output ready from a log that trickles in.
	undecided mode = iota
	// follow: lines are printed as they arrive, into the normal scrollback. For
	// a log that trickles in (tail -f) and for output that fits on the screen.
	follow
	// paging: a full-screen pager that can scroll both ways. For output that
	// does not fit on the screen and arrives at once, such as git log.
	paging
)

// view is mgnt's terminal view. Lines go into a ring buffer first. Output that
// fits on the screen, or a log that trickles in, is printed as it comes. A lot
// of output that arrives at once opens a pager, like `git log | less`, which
// also stops reading when it has enough, so the producer is held back by the
// pipe. "/" opens fzf on the ring in either mode.
type view struct {
	s    *session
	term tty.Terminal
	w    *bufio.Writer
	ring *buffer.Ring
	srch *search.Searcher // nil when fzf is not installed

	keys       <-chan byte // nil while keys are not being read
	kp         keyParser
	mode       mode
	canRepaint bool // the terminal can be addressed: not TERM=dumb
	searching  bool
	eof        bool
	werr       error

	dt      *time.Timer // the "has the input gone quiet" timer
	dtArmed bool

	// follow mode
	printed uint64 // the newest ring line already shown
	noNL    map[uint64]bool

	// paging
	alt     bool   // the alternate screen is on
	painted bool   // the terminal has the colors of the colorscheme
	top     uint64 // the first line on screen
	bottom  uint64 // the last line on screen
	seekEnd bool   // reading to the end of the input, to show it
	msg     string // a one-off message for the status line

	following bool // staying at the end as new lines come in (F)
	chop      bool // long lines are cut, not wrapped (w)
	hoff      int  // how far a chopped line is scrolled sideways
	numbers   bool // line numbers in a gutter (#)

	// The last search: its query, and the lines that match it, for n and N.
	query        string
	matches      []uint64
	matchesReady bool
	matchIdx     int // the match n/N is at, -1 if the screen was moved since
	matchCh      chan matchResult

	seekTimer *time.Timer // G: the input has gone quiet, so show the end
	seekArmed bool
}

// seekIdle is how long the input must be quiet for G to settle for the end it
// has: a finite input is read to its real end, but one that trickles in, or
// never ends, would otherwise keep G waiting for ever.
const seekIdle = 150 * time.Millisecond

func (v *view) armSeek() {
	if v.seekTimer == nil {
		v.seekTimer = time.NewTimer(seekIdle)
	} else {
		if !v.seekTimer.Stop() {
			select {
			case <-v.seekTimer.C:
			default:
			}
		}
		v.seekTimer.Reset(seekIdle)
	}
	v.seekArmed = true
}

func (v *view) seekC() <-chan time.Time {
	if v.mode != paging || !v.seekEnd || !v.seekArmed {
		return nil
	}
	return v.seekTimer.C
}

// dim wraps a one-line message of mgnt's own, so it is not mistaken for log text.
func (v *view) dim(msg string) string {
	if v.s.depth == render.None {
		return msg
	}
	return "\x1b[2m" + msg + render.Reset
}

func (v *view) write(s string) {
	if v.werr == nil {
		_, v.werr = v.w.WriteString(s)
	}
}

func (v *view) flush() {
	if v.werr == nil {
		v.werr = v.w.Flush()
	}
}

// println prints a message line of mgnt's own.
func (v *view) println(s string) { v.write(s + "\n") }

// showLine prints one ring line, as it was read: without a newline if it had none.
func (v *view) showLine(l buffer.Line) {
	if v.werr == nil {
		if _, v.werr = v.w.Write(l.Text); v.werr == nil && !v.noNL[l.Seq] {
			v.werr = v.w.WriteByte('\n')
		}
	}
	delete(v.noNL, l.Seq)
	v.printed = l.Seq
}

// pumpFollow prints every line not shown yet.
func (v *view) pumpFollow() {
	if v.searching || v.werr != nil {
		return
	}
	if oldest, newest := v.ring.Bounds(); oldest > v.printed+1 && newest >= oldest {
		v.println(v.dim(fmt.Sprintf("[mgnt] %d line(s) were dropped before they could be shown: the buffer holds %d lines", oldest-v.printed-1, v.s.cfg.Buffer)))
		v.printed = oldest - 1
	}
	for {
		batch := v.ring.Collect(v.printed+1, 1024)
		if len(batch) == 0 {
			return
		}
		for _, l := range batch {
			v.showLine(l)
		}
	}
}

// pendingRows counts the screen rows of the lines not shown yet, stopping once
// the count passes limit.
func (v *view) pendingRows(limit int) int {
	_, cols := v.term.Size()
	n, from := 0, v.printed+1
	for {
		batch := v.ring.Collect(from, 256)
		if len(batch) == 0 {
			return n
		}
		for _, l := range batch {
			if n += render.Rows(l.Text, cols); n > limit {
				return n
			}
		}
		from = batch[len(batch)-1].Seq + 1
	}
}

// decide leaves the undecided mode. More than a screenful already read means
// a command with a lot of output: a pager. Otherwise, once the input has ended
// or has gone quiet (idle), the lines are just printed.
func (v *view) decide(idle bool) {
	rows, _ := v.term.Size()
	page := max(rows-1, 1)
	switch {
	case v.canRepaint && v.pendingRows(page) > page:
		v.enterPaging()
	case v.eof || idle:
		v.mode = follow
		v.pumpFollow()
	}
}

func (v *view) armDecide() {
	if v.dt == nil {
		v.dt = time.NewTimer(decideAfter)
	} else {
		if !v.dt.Stop() {
			select {
			case <-v.dt.C:
			default:
			}
		}
		v.dt.Reset(decideAfter)
	}
	v.dtArmed = true
}

func (v *view) decideC() <-chan time.Time {
	if v.mode != undecided || !v.dtArmed {
		return nil
	}
	return v.dt.C
}

// settle shows what can be shown for the mode, and flushes.
func (v *view) settle() {
	switch v.mode {
	case undecided:
		if v.eof || v.ring.Next() > 1 {
			v.decide(false)
			if v.mode == undecided {
				v.armDecide()
			}
		}
	case follow:
		v.pumpFollow()
	case paging:
		if v.seekEnd {
			v.armSeek() // the end is shown when no more comes for a moment
		}
		if v.following {
			v.setTop(v.maxTop())
		}
		if !v.seekEnd && !v.searching {
			v.redraw()
		}
	}
	v.flush()
}

// done reports whether everything has been shown and no more can come.
func (v *view) done() bool {
	return v.mode == follow && v.eof && !v.searching && v.printed >= v.ring.Next()-1
}

// backlogFull says that enough has been read ahead of what is shown.
func (v *view) backlogFull() bool {
	return limitReadAhead && v.ring.Next()-1-v.printed >= uint64(v.ring.Capacity()/4)
}

// mayRead says whether to take more input now. When it is not, the producer
// is held back by the pipe.
func (v *view) mayRead() bool {
	if v.mode == paging {
		return v.wantMore()
	}
	return !v.backlogFull()
}

// interactive runs the terminal view over the files or stdin. fzf is the path
// of fzf, or "" if it is not installed (there is then no "/" search). It
// returns the exit status. The terminal is restored on every way out, panics
// included.
func (s *session) interactive(files []string, fzf string) (status int) {
	term, err := s.env.OpenTTY()
	if err != nil {
		warnf(s.env, "cannot open the terminal for paging and search (%v), so mgnt only colors the output", err)
		return s.plain(files)
	}
	defer term.Close()
	if err := term.Cbreak(); err != nil {
		warnf(s.env, "cannot set up the terminal for paging and search (%v), so mgnt only colors the output", err)
		return s.plain(files)
	}

	if s.cfg.Theme == background.Auto && s.depth != render.None {
		// Ask the terminal now, while nothing else is reading from it.
		s.scheme = loadColorscheme(s.env, s.cfg.Colorscheme, s.cfg.Theme, term)
	}

	s.paintOn = s.paintSequence()

	ring := buffer.New(max(s.cfg.Buffer, pagerMinRing))
	v := &view{s: s, term: term, w: bufio.NewWriterSize(s.env.Stdout, 64<<10), ring: ring, noNL: map[uint64]bool{},
		canRepaint: s.env.Getenv("TERM") != "dumb"}
	if !v.canRepaint {
		v.mode = follow // a terminal that cannot be addressed cannot be paged
	}
	if fzf != "" {
		exe := ""
		if s.env.Executable != nil {
			exe, _ = s.env.Executable()
		}
		base, colors := s.searchColors()
		v.srch = search.New(search.Options{
			FzfPath: fzf, Config: s.cfg, Ring: ring, Environ: s.env.Environ,
			Stderr: s.env.Stderr, Version: s.env.FzfVersion,
			Preview:   &search.PreviewOptions{Exe: exe, Color: s.depth != render.None},
			Colors:    colors,
			ColorBase: base,
		})
	}
	defer func() {
		if r := recover(); r != nil {
			if v.srch != nil {
				v.srch.Stop()
			}
			if v.alt {
				fmt.Fprint(s.env.Stdout, altOff)
			}
			if v.painted {
				fmt.Fprint(s.env.Stdout, paintOff)
			}
			term.Restore()
			panic(r)
		}
	}()

	var sigs <-chan os.Signal
	if s.env.Signals != nil {
		var stopSigs func()
		sigs, stopSigs = s.env.Signals()
		defer stopSigs()
	}
	done := make(chan struct{})
	defer close(done)
	items := readSources(s.env, files, done)
	v.matchCh = make(chan matchResult, 1)
	v.matchIdx = -1
	v.keys = term.StartKeys()

	var searchDone chan search.Result
	var drv *pipe.Driver
	var eofStatus int
	emit := func(line []byte, nl bool) {
		seq := ring.Append(line)
		if !nl {
			v.noNL[seq] = true
		}
	}
	timer := func() <-chan time.Time {
		if drv == nil {
			return nil
		}
		return drv.C()
	}

	for {
		if v.werr != nil {
			return v.finish(searchDone, eofStatus, v.werr)
		}
		if v.done() {
			return v.finish(nil, eofStatus, nil)
		}
		// Stop reading when enough is queued up unshown: the producer is then
		// held back by the pipe.
		itemsC := items
		if !v.mayRead() {
			itemsC = nil
		}
		select {
		case it, ok := <-itemsC:
			if !ok {
				items = nil
				v.eof = true
				ring.Close()
				if v.mode == paging && (v.seekEnd || v.following) {
					v.seekEnd, v.following = false, false
					v.msg = "" // "reading to the end..." and "following..." are over
					v.setTop(v.maxTop())
				}
				v.settle()
				continue
			}
			switch {
			case it.err != nil:
				fmt.Fprintf(s.env.Stderr, "mgnt: %v\n", it.err)
				eofStatus = 1
			case it.start:
				if drv != nil {
					drv.Stop()
				}
				drv = pipe.NewDriver(pipe.New(s.pipeConfig(it.filename)), emit)
			default:
				_, rerr := drv.Handle(it.batch)
				if rerr != nil {
					fmt.Fprintf(s.env.Stderr, "mgnt: %v\n", rerr)
					eofStatus = 1
				}
				v.settle()
			}
		case <-v.seekC():
			v.seekArmed, v.seekEnd = false, false
			v.msg = ""
			v.setTop(v.maxTop())
			v.redraw()
			v.flush()
		case <-v.decideC():
			v.dtArmed = false
			v.decide(true)
			v.settle()
		case <-timer():
			drv.Tick()
			v.settle()
		case b, ok := <-v.keys:
			if !ok {
				v.keys = nil
				break
			}
			k, isKey := v.kp.feed(b)
			if !isKey {
				break
			}
			switch {
			case k == 'q':
				return v.finish(searchDone, eofStatus, v.werr)
			case k == '/' && !v.searching:
				if v.srch == nil {
					v.noSearch()
					break
				}
				if v.mode == undecided {
					v.decide(true) // the lines so far are all there is to show
				}
				// A log that was already there, as in a pager, is read from its
				// first line. One that is still trickling in, from its end.
				order := search.NewestLast
				if v.mode == paging {
					order = search.FromTop
					v.leaveAlt()
				}
				v.paint()
				v.flush()
				v.searching = true
				v.term.StopKeys()
				v.keys = nil
				v.term.Restore()
				searchDone = make(chan search.Result, 1)
				go func() { searchDone <- v.srch.Run(s.env.mgntFzfOpts(), order) }()
			case v.mode == paging && !v.searching:
				v.pagerKey(k)
				v.flush()
			}
		case m := <-v.matchCh:
			if m.query == v.query { // not the answer to an older search
				v.matches, v.matchesReady = m.matches, true
				if m.err != nil {
					v.msg = "could not find the matches: " + m.err.Error()
				}
				if v.mode == paging && !v.searching {
					v.redraw()
					v.flush()
				}
			}
		case res := <-searchDone:
			searchDone = nil
			v.searching = false
			if v.mode == paging {
				v.enterAlt()
				v.afterPagerSearch(res)
			} else {
				v.unpaint()
				v.afterSearch(res)
			}
			v.settle()
			if v.done() {
				return v.finish(nil, eofStatus, v.werr)
			}
			if err := term.Cbreak(); err != nil {
				warnf(s.env, "cannot set up the terminal again (%v)", err)
				return v.finish(nil, 1, nil)
			}
			v.keys = term.StartKeys()
		case sig := <-sigs:
			if isResize(sig) {
				if v.mode == paging && !v.searching {
					v.redraw()
					v.flush()
				}
				break
			}
			code := 130
			if isTerminate(sig) {
				code = 143
			}
			return v.finish(searchDone, code, nil)
		}
	}
}

// noSearch answers "/" when fzf is not installed.
func (v *view) noSearch() {
	const msg = "fzf was not found, so there is no search (install fzf to get one)"
	if v.mode == paging {
		v.msg = msg
		v.redraw()
	} else {
		v.println(v.dim("[mgnt] " + msg))
	}
	v.flush()
}

// finish leaves the view: it stops fzf if it is running, leaves the alternate
// screen, puts the terminal back and flushes what is left. werr is a failure
// writing output.
func (v *view) finish(searchDone chan search.Result, status int, werr error) int {
	if searchDone != nil {
		v.srch.Stop()
		<-searchDone
	}
	v.term.StopKeys()
	if werr == nil {
		v.leaveAlt()
		v.unpaint()
		werr = v.w.Flush()
	}
	v.term.Restore()
	if werr != nil && !isEPIPE(werr) {
		fmt.Fprintf(v.s.env.Stderr, "mgnt: %v\n", werr)
		return 1
	}
	return status
}

// afterSearch prints what a search left to show in follow mode: its notices and
// the selected lines. The lines that arrived while fzf was open are still in
// the ring and are shown as usual.
func (v *view) afterSearch(res search.Result) {
	for _, n := range res.Notices {
		v.println(v.dim("[mgnt] " + n))
	}
	switch {
	case res.Err != nil:
		v.println(v.dim("[mgnt] fzf failed: " + res.Err.Error()))
	case res.Code == 0:
		// fzf lists the newest line first (--tac) and prints a multi-selection in
		// list order. The scrollback should read oldest first.
		sort.SliceStable(res.Selected, func(i, j int) bool { return res.Selected[i].Seq < res.Selected[j].Seq })
		for _, sel := range res.Selected {
			if text, ok := v.ring.Get(sel.Seq); ok {
				v.write(string(text) + "\n")
			} else {
				v.println(sel.Text) // evicted: fzf's own copy, without colors
			}
		}
	}
}

func (s *session) pipeConfig(filename string) pipe.Config {
	return pipe.Config{
		ANSI: s.cfg.ANSI, Colorscheme: s.scheme, Depth: s.depth, Loader: s.loader, Filename: filename,
		Warn: func(m string) { warnf(s.env, "%s", m) },
	}
}

// mgntFzfOpts returns the extra fzf options from the MGNT_FZF_OPTS variable.
func (e Env) mgntFzfOpts() string { return strings.TrimSpace(e.Getenv("MGNT_FZF_OPTS")) }

// paintSequence is what sets the text and background colors of the terminal to
// the ui.background style of the colorscheme (OSC 10 and 11). It is "" when
// there is no color, the terminal cannot be addressed, or the colorscheme has no
// such style, as with mono.
func (s *session) paintSequence() string {
	if s.depth == render.None || s.env.Getenv("TERM") == "dumb" {
		return ""
	}
	st, ok := s.scheme.Resolve("ui.background")
	if !ok || st.BG.Hex() == "" {
		return ""
	}
	seq := ""
	if h := st.FG.Hex(); h != "" {
		seq += "\x1b]10;" + h + "\x1b\\"
	}
	return seq + "\x1b]11;" + st.BG.Hex() + "\x1b\\"
}

// fzfColors says which fzf colors the ui styles of the colorscheme set: the
// text color of a style goes to the fg names, its background to the bg names.
var fzfColors = []struct {
	scope  string
	fg, bg []string
}{
	{"ui.search", []string{"input-fg"}, []string{"input-bg"}},
	{"ui.current", []string{"fg+"}, []string{"bg+"}},
	{"ui.marker", []string{"pointer", "marker"}, nil},
	{"ui.hint", []string{"header"}, nil},
	{"ui.info", []string{"info"}, nil},
	{"ui.border", []string{"separator", "scrollbar", "border"}, nil},
	// The strip left of the list, which fzf paints in its own dark color.
	{"ui.background", nil, []string{"gutter"}},
}

// searchColors gives fzf the colors of the colorscheme: the base scheme for the
// theme in use ("dark" or "light"), and the colors that the ui styles set. There
// are none without color, or when the colorscheme has no ui styles.
func (s *session) searchColors() (base string, colors []search.Color) {
	if s.depth == render.None {
		return "", nil
	}
	for _, m := range fzfColors {
		st, ok := s.scheme.Resolve(m.scope)
		if !ok {
			continue
		}
		for _, name := range m.fg {
			if h := st.FG.Hex(); h != "" {
				colors = append(colors, search.Color{Name: name, Value: h})
			}
		}
		for _, name := range m.bg {
			if h := st.BG.Hex(); h != "" {
				colors = append(colors, search.Color{Name: name, Value: h})
			}
		}
	}
	if len(colors) == 0 {
		return "", nil
	}
	return s.scheme.Theme, colors
}
