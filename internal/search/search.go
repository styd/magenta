package search

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/styd/magenta/internal/buffer"
	"github.com/styd/magenta/internal/config"
)

// Options configures a Searcher.
type Options struct {
	FzfPath string
	Config  config.Config
	Ring    *buffer.Ring
	// Environ is the environment fzf runs in, normally os.Environ(). It is how
	// FZF_DEFAULT_OPTS and the user's other fzf settings reach fzf.
	Environ []string
	// Stderr is fzf's standard error. fzf draws on the terminal by itself, so
	// this is normally os.Stderr.
	Stderr io.Writer
	// Version returns the output of `fzf --version`. It is called at most
	// once, on the first search.
	Version func(path string) (string, error)
	// Preview, if set, serves the preview pane (see preview.go).
	Preview *PreviewOptions
	// Colors are colors for fzf's --color, on top of the base scheme ColorBase
	// ("dark" or "light"). Those that the installed fzf does not know are left
	// out. With none, fzf keeps its own colors.
	Colors    []Color
	ColorBase string
}

// Selection is one line chosen in fzf.
type Selection struct {
	Seq  uint64 // sequence number of the line, 0 if fzf's output could not be parsed
	Text string // the line as fzf printed it, without the sequence prefix and without colors
}

// Result is what a search produced.
type Result struct {
	// Code is fzf's exit status: 0 selections made, 1 no match, 130 canceled,
	// 2 or anything else an error. It is -1 if fzf did not start.
	Code     int
	Selected []Selection
	// Query is what was typed when fzf was left with Enter or ctrl-o, even if
	// nothing matched. It is empty when fzf was canceled.
	Query string
	Err   error
	// Notices are one-line explanations for the user, such as an fzf that is
	// too old for the preview pane. Each is returned only the first time.
	Notices []string
}

// Searcher runs fzf over the ring. Run may be called many times.
type Searcher struct {
	o Options

	once   sync.Once
	plan   Plan
	known  bool
	vers   Version
	notice []string

	mu  sync.Mutex
	cmd *exec.Cmd
}

// New returns a Searcher. It runs nothing: `fzf --version` waits for the
// first search so that start-up stays fast.
func New(o Options) *Searcher { return &Searcher{o: o} }

func (s *Searcher) detectVersion() {
	s.once.Do(func() {
		var out string
		var err error
		if s.o.Version != nil {
			out, err = s.o.Version(s.o.FzfPath)
		}
		if err == nil {
			s.vers, s.known = ParseVersion(out)
		}
		s.plan = PlanFor(s.vers, s.known, s.o.Config)
		if s.o.Ring != nil && SupportsTail(s.vers, s.known) {
			// fzf keeps every line it is fed. Tell it to keep as many as the ring
			// does, so that a search left open on a live log does not grow without
			// bound while the ring stays small.
			s.plan.Tail = s.o.Ring.Capacity()
		}
		s.plan.Colors = colorArg(s.o.ColorBase, s.o.Colors, s.vers, s.known)
		s.notice = s.plan.Notices
	})
}

// Stop kills a running fzf, if any.
func (s *Searcher) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

// baseArgs are the flags every search needs. Fields are separated by a tab.
// the first, the sequence number, is hidden from the display and from matching.
var baseArgs = []string{"--ansi", "--no-sort", "--multi", "--delimiter", `\t`, "--with-nth", "2.."}

// Order is where in the log the list starts. The prompt is always at the
// bottom and the list always grows upward from it, so a few matches sit right
// above the prompt instead of floating at the top of the screen.
type Order int

const (
	// NewestLast: the log is read from its end. The last line is nearest the
	// prompt, like in a terminal (--tac). For a log that is still arriving,
	// where the end is what matters.
	NewestLast Order = iota
	// FromTop: the log is read from its first line. The first line is nearest
	// the prompt, with the cursor on it, and later lines stack above it (no
	// --tac). For a log that was already there, such as the output of git log,
	// which lists the newest commit first.
	FromTop
)

// BuildArgs assembles the fzf command line. previewCmd is the command fzf runs
// for the focused line, or "" for no preview. User options come last, so they
// can override ours.
func BuildArgs(plan Plan, previewCmd string, cfg config.Config, mgntOpts string, order Order) ([]string, error) {
	args := append([]string(nil), baseArgs...)
	if order == NewestLast {
		args = append(args, "--tac")
	}
	if plan.Tail > 0 {
		args = append(args, "--tail", strconv.Itoa(plan.Tail))
	}
	if plan.Layout != "" {
		args = append(args, "--layout", plan.Layout)
	}
	args = append(args, plan.ListFlags...)
	if plan.Colors != "" {
		args = append(args, "--color", plan.Colors)
	}
	if plan.Preview && previewCmd != "" {
		args = append(args, "--preview", previewCmd)
		if plan.PreviewWindow != "" {
			args = append(args, "--preview-window", plan.PreviewWindow)
		}
		if plan.Header != "" {
			// What the accept key does depends on the view: in a followed log the
			// selection is printed, in the pager the pager jumps to it.
			header := plan.Header
			if plan.AcceptKey != "" {
				what := "print selection"
				if order == FromTop {
					what = "go to line"
				}
				header += "   " + plan.AcceptKey + ": " + what
			}
			args = append(args, "--header", header)
		}
		helper := strings.TrimSuffix(previewCmd, " {1}")
		for _, b := range plan.Binds {
			args = append(args, "--bind", strings.ReplaceAll(b, helperToken, helper))
		}
	}
	for _, opts := range []string{cfg.FzfOpts, mgntOpts} {
		extra, err := SplitArgs(opts)
		if err != nil {
			return nil, err
		}
		args = append(args, extra...)
	}
	return args, nil
}

// SplitArgs splits a string into words the way a shell would for simple
// cases: whitespace separates words, single and double quotes group, and a
// backslash escapes the next character outside single quotes.
func SplitArgs(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inWord := false
	var quote rune
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch {
			case r == '"':
				quote = 0
			case r == '\\' && i+1 < len(rs) && strings.ContainsRune(`"\$`+"`", rs[i+1]):
				i++
				cur.WriteRune(rs[i])
			default:
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == '\\' && i+1 < len(rs):
			i++
			cur.WriteRune(rs[i])
			inWord = true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c in %q", quote, s)
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, nil
}

// Run opens fzf on the ring and waits for it to exit. The whole ring is fed to
// fzf first, then every line that arrives while it is open, so its list
// updates live. When the ring is closed, fzf's input is closed and the user
// can finish.
func (s *Searcher) Run(mgntOpts string, order Order) Result {
	s.detectVersion()
	res := Result{Code: -1, Notices: s.takeNotices()}

	plan := s.plan
	var prev *previewServer
	feedGate := newGate()
	previewCmd := ""
	if plan.Preview && s.o.Preview != nil {
		var err error
		if prev, err = startPreview(s.o.Ring, s.o.Preview); err != nil {
			plan.Preview = false
			res.Notices = append(res.Notices, "preview pane unavailable: "+err.Error())
		} else {
			defer prev.Close()
			previewCmd = prev.Command()
			prev.onOpen = feedGate.set // the list holds still while the preview is open
		}
	}
	args, err := BuildArgs(plan, previewCmd, s.o.Config, mgntOpts, order)
	if err != nil {
		res.Err = err
		return res
	}

	// The query is asked for here, not in BuildArgs: --filter prints it too.
	args = append(args, "--print-query")
	cmd := exec.Command(s.o.FzfPath, args...)
	cmd.Env = s.o.Environ
	cmd.Stderr = s.o.Stderr
	// If fzf is killed while a process it started still holds its output
	// pipes, do not wait for that process for ever.
	cmd.WaitDelay = 2 * time.Second
	var out bytes.Buffer
	cmd.Stdout = &out
	in, err := cmd.StdinPipe()
	if err != nil {
		res.Err = err
		return res
	}
	s.mu.Lock()
	err = cmd.Start()
	s.cmd = cmd
	s.mu.Unlock()
	if err != nil {
		res.Err = err
		return res
	}

	stop := make(chan struct{})
	fed := make(chan struct{})
	go func() {
		defer close(fed)
		feed(in, s.o.Ring, stop, feedGate)
	}()
	werr := cmd.Wait()
	close(stop)
	<-fed
	s.mu.Lock()
	s.cmd = nil
	s.mu.Unlock()

	var ee *exec.ExitError
	switch {
	case werr == nil:
		res.Code = 0
	case errors.As(werr, &ee) && ee.ExitCode() >= 0:
		res.Code = ee.ExitCode()
	default:
		res.Err = werr
		return res
	}
	switch res.Code {
	case 0, 1: // 1: nothing matched, but the query is still printed
		res.Query, res.Selected = parseOutput(out.Bytes())
	}
	if res.Code != 0 && res.Code != 1 && res.Code != 130 {
		res.Err = fmt.Errorf("fzf exited with status %d", res.Code)
	}
	return res
}

func (s *Searcher) takeNotices() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.notice
	s.notice = nil
	return n
}

// gate lets the preview pause what is written to fzf. While the preview is
// open the list must not move under the user: no new lines are written, and
// those that arrive wait in the ring until the preview is closed. A nil gate
// never pauses.
type gate struct {
	mu     sync.Mutex
	paused bool
	resume chan struct{} // closed when the gate opens again
}

func newGate() *gate { return &gate{resume: make(chan struct{})} }

// set pauses or resumes.
func (g *gate) set(paused bool) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.paused && !paused {
		close(g.resume)
		g.resume = make(chan struct{})
	}
	g.paused = paused
}

// wait blocks while paused. It returns false if stop was closed meanwhile.
func (g *gate) wait(stop <-chan struct{}) bool {
	if g == nil {
		return true
	}
	for {
		g.mu.Lock()
		paused, resume := g.paused, g.resume
		g.mu.Unlock()
		if !paused {
			return true
		}
		select {
		case <-resume:
		case <-stop:
			return false
		}
	}
}

// feed writes "seq<TAB>line" for the whole ring, then for each new line, to w,
// except while g is paused. It closes w once the ring is closed and fully sent,
// or returns when stop is closed or fzf stops reading.
func feed(w io.WriteCloser, ring *buffer.Ring, stop <-chan struct{}, g *gate) {
	bw := bufio.NewWriterSize(w, 64<<10)
	next := uint64(1)
	for {
		if !g.wait(stop) {
			return
		}
		changed := ring.Changed()
		closed := ring.Closed()
		lines := ring.Collect(next, 1024)
		for _, l := range lines {
			bw.WriteString(strconv.FormatUint(l.Seq, 10))
			bw.WriteByte('\t')
			bw.Write(l.Text)
			if err := bw.WriteByte('\n'); err != nil {
				return
			}
			next = l.Seq + 1
		}
		if err := bw.Flush(); err != nil {
			return
		}
		if len(lines) > 0 {
			continue
		}
		if closed {
			w.Close()
			return
		}
		select {
		case <-changed:
		case <-stop:
			return
		}
	}
}

// parseOutput splits what fzf printed with --print-query: the query on the
// first line, then the selected lines.
func parseOutput(out []byte) (query string, sel []Selection) {
	query, rest, _ := strings.Cut(string(out), "\n")
	return query, parseSelections([]byte(rest))
}

func parseSelections(out []byte) []Selection {
	var sel []Selection
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		seq, text, ok := strings.Cut(line, "\t")
		n, err := strconv.ParseUint(seq, 10, 64)
		if !ok || err != nil {
			sel = append(sel, Selection{Text: line})
			continue
		}
		sel = append(sel, Selection{Seq: n, Text: text})
	}
	return sel
}

// Matches returns the sequence numbers of the lines in the ring that match the
// query, in order, by asking fzf to filter them. It is the same matching the
// search used (fzf_opts such as --exact count), so n and N in the pager agree
// with what the search found. The ring is read as it is now.
func (s *Searcher) Matches(query, mgntOpts string) ([]uint64, error) {
	if query == "" {
		return nil, nil
	}
	args := append([]string(nil), baseArgs...)
	for _, opts := range []string{s.o.Config.FzfOpts, mgntOpts} {
		extra, err := SplitArgs(opts)
		if err != nil {
			return nil, err
		}
		args = append(args, extra...)
	}
	args = append(args, "--filter="+query)

	cmd := exec.Command(s.o.FzfPath, args...)
	cmd.Env = s.o.Environ
	cmd.WaitDelay = 2 * time.Second
	var out bytes.Buffer
	cmd.Stdout = &out
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		defer in.Close()
		bw := bufio.NewWriterSize(in, 64<<10)
		for next := uint64(1); ; {
			lines := s.o.Ring.Collect(next, 1024)
			if len(lines) == 0 {
				break
			}
			for _, l := range lines {
				bw.WriteString(strconv.FormatUint(l.Seq, 10))
				bw.WriteByte('\t')
				bw.Write(l.Text)
				bw.WriteByte('\n')
				next = l.Seq + 1
			}
		}
		bw.Flush()
	}()
	err = cmd.Wait()
	var ee *exec.ExitError
	if err != nil && !(errors.As(err, &ee) && ee.ExitCode() == 1) { // 1: no line matched
		return nil, err
	}
	var seqs []uint64
	for _, line := range strings.Split(out.String(), "\n") {
		field, _, _ := strings.Cut(line, "\t")
		if n, err := strconv.ParseUint(field, 10, 64); err == nil {
			seqs = append(seqs, n)
		}
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	return seqs, nil
}
