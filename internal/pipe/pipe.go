// Package pipe turns raw lines into colored lines: it samples the start of
// the log to choose a rule file, locks on, and re-checks the choice after a
// long run of lines the rule does not recognize.
package pipe

import (
	"time"

	"github.com/styd/magenta/internal/colorscheme"
	"github.com/styd/magenta/internal/detect"
	"github.com/styd/magenta/internal/engine"
	"github.com/styd/magenta/internal/lines"
	"github.com/styd/magenta/internal/render"
	"github.com/styd/magenta/internal/rules"
)

// Config says how to color.
type Config struct {
	ANSI        string // "strip" or "keep"
	Colorscheme *colorscheme.Colorscheme
	Depth       render.Depth
	Loader      rules.Loader
	Filename    string       // "" for stdin
	Warn        func(string) // problems with rule files (may be nil)
}

// Emit receives a colored line. The bytes are only valid during the call.
type Emit func(line []byte, nl bool)

type pendingLine struct {
	text []byte
	nl   bool
}

// Pipeline colors one input. Create one per file. Not safe for concurrent use.
type Pipeline struct {
	cfg     Config
	dets    chan detsResult // filled once, by the loader goroutine
	detList []*rules.Detector
	gotDets bool

	passthrough bool
	decided     bool
	pending     []pendingLine
	rule        string
	h           *engine.Highlighter
	det         *rules.Detector // the locked-on rule's detect patterns
	buf         []byte
	scratch     []byte

	unmatched int
	recent    [detect.SampleLines][]byte
	nrecent   int
}

type detsResult struct {
	dets []*rules.Detector
	errs []error
}

// New starts loading the detectors in the background and returns a pipeline.
func New(cfg Config) *Pipeline {
	p := &Pipeline{cfg: cfg, dets: make(chan detsResult, 1)}
	// With color off or ansi = "keep" there is nothing to color, so nothing
	// to detect either.
	p.passthrough = cfg.Depth == render.None || cfg.ANSI == "keep"
	if p.passthrough {
		p.decided = true
		return p
	}
	go func() {
		d, e := cfg.Loader.Detectors()
		p.dets <- detsResult{d, e}
	}()
	return p
}

// Rule returns the name of the rule file in use, or "" before the choice.
func (p *Pipeline) Rule() string { return p.rule }

// Decided reports whether the rule file has been chosen.
func (p *Pipeline) Decided() bool { return p.decided }

func (p *Pipeline) detectors() []*rules.Detector {
	if !p.gotDets {
		r := <-p.dets
		p.detList, p.gotDets = r.dets, true
		for _, e := range r.errs {
			p.warn(e.Error())
		}
	}
	return p.detList
}

func (p *Pipeline) warn(s string) {
	if p.cfg.Warn != nil {
		p.cfg.Warn(s)
	}
}

// Line handles one input line. While the rule file is still being chosen the
// line is held back. It is emitted by Decide, Timeout, or Finish.
func (p *Pipeline) Line(text []byte, nl bool, emit Emit) {
	if !p.decided {
		p.pending = append(p.pending, pendingLine{append([]byte(nil), text...), nl})
		if len(p.pending) >= detect.SampleLines {
			p.decide(emit)
		}
		return
	}
	p.emitLine(text, nl, emit)
}

// EndBatch is called after the last line of a batch. If the sampling window
// has run out (late is true), whatever has been seen decides.
func (p *Pipeline) EndBatch(late bool, emit Emit) {
	if !p.decided && late && len(p.pending) > 0 {
		p.decide(emit)
	}
}

// Timeout is called when SampleWait has passed.
func (p *Pipeline) Timeout(emit Emit) {
	if !p.decided && len(p.pending) > 0 {
		p.decide(emit)
	}
}

// Finish is called at the end of the input.
func (p *Pipeline) Finish(emit Emit) {
	if !p.decided {
		p.decide(emit)
	}
}

func (p *Pipeline) decide(emit Emit) {
	plain := make([][]byte, len(p.pending))
	for i, l := range p.pending {
		plain[i] = render.StripSGR(nil, l.text)
	}
	res := detect.Choose(p.detectors(), plain, p.cfg.Filename)
	p.lockOn(res.Picked)
	p.decided = true
	pend := p.pending
	p.pending = nil
	for _, l := range pend {
		p.emitLine(l.text, l.nl, emit)
	}
}

func (p *Pipeline) lockOn(name string) {
	c, err := p.cfg.Loader.Load(name)
	if err != nil {
		p.warn(err.Error())
		if name != detect.Fallback {
			name = detect.Fallback
			c, err = p.cfg.Loader.Load(name)
		}
		if err != nil {
			p.warn(err.Error())
			p.passthrough = true
			p.rule = ""
			return
		}
	}
	p.rule = name
	p.det = nil
	for _, d := range p.detectors() {
		if d.Name == name {
			p.det = d
		}
	}
	p.h = engine.New(c, p.cfg.Colorscheme.Starts(p.cfg.Depth, c.Scopes), p.cfg.ANSI)
	p.unmatched, p.nrecent = 0, 0
}

func (p *Pipeline) emitLine(text []byte, nl bool, emit Emit) {
	if p.passthrough {
		if p.cfg.ANSI != "keep" {
			p.scratch = render.StripSGR(p.scratch[:0], text)
			text = p.scratch
		}
		emit(text, nl)
		return
	}
	p.buf = p.h.Highlight(p.buf[:0], text)
	emit(p.buf, nl)
	p.watch(text)
}

// watch counts consecutive lines that none of the locked-on rule's detect
// patterns match (every line, for generic, which has none) and, after
// detect.RedetectAfter of them, looks at the choice again using the most
// recent lines.
func (p *Pipeline) watch(text []byte) {
	i := p.nrecent % len(p.recent)
	p.recent[i] = render.StripSGR(p.recent[i][:0], text)
	p.nrecent++
	if p.det != nil && p.matchesDetect(p.recent[i]) {
		p.unmatched = 0
		return
	}
	p.unmatched++
	if p.unmatched < detect.RedetectAfter {
		return
	}
	p.unmatched = 0
	n := min(p.nrecent, len(p.recent))
	sample := make([][]byte, 0, n)
	for k := p.nrecent - n; k < p.nrecent; k++ {
		sample = append(sample, p.recent[k%len(p.recent)])
	}
	res := detect.Choose(p.detectors(), sample, p.cfg.Filename)
	// Switch only when another rule clearly fits the recent lines.
	if res.Picked != detect.Fallback && res.Picked != p.rule && res.Scores[0].Lines >= len(sample)*4/5 {
		p.lockOn(res.Picked)
	}
}

func (p *Pipeline) matchesDetect(line []byte) bool {
	for _, pat := range p.det.Patterns {
		if pat.Re.Match(line) {
			return true
		}
	}
	return false
}

// Driver feeds batches to a Pipeline and runs the sampling timer that decides
// the rule file for a slow stream. Drive uses it for the plain filter. The
// interactive view uses it from its own event loop.
type Driver struct {
	p     *Pipeline
	emit  Emit
	timer *time.Timer
	late  bool // the sampling window has run out
}

// NewDriver starts the sampling timer if the rule file is not chosen yet.
func NewDriver(p *Pipeline, emit Emit) *Driver {
	d := &Driver{p: p, emit: emit}
	if !p.Decided() {
		d.timer = time.NewTimer(detect.SampleWait)
	}
	return d
}

// C is the timer channel, or nil when there is nothing to wait for. After it
// fires, call Tick.
func (d *Driver) C() <-chan time.Time {
	if d.timer == nil || d.p.Decided() {
		return nil
	}
	return d.timer.C
}

// Tick handles the timer firing.
func (d *Driver) Tick() {
	d.late = true
	d.p.Timeout(d.emit)
}

// Handle processes a batch and releases it. eof is true after the last batch
// of the input, and err is a read error.
func (d *Driver) Handle(b *lines.Batch) (eof bool, err error) {
	for i := 0; i < b.Len(); i++ {
		text, nl := b.At(i)
		d.p.Line(text, nl, d.emit)
	}
	d.p.EndBatch(d.late, d.emit)
	eof, err = b.EOF, b.Err
	b.Release()
	if eof {
		d.p.Finish(d.emit)
	}
	return eof, err
}

// Stop releases the timer.
func (d *Driver) Stop() {
	if d.timer != nil {
		d.timer.Stop()
	}
}

// Drive runs the pipeline over batches from lines.Read until the input ends.
// idle is called whenever the input has drained, which is the moment to flush
// output. If it returns true Drive stops. It returns the read error, if any.
func Drive(in <-chan *lines.Batch, p *Pipeline, emit Emit, idle func() bool) error {
	d := NewDriver(p, emit)
	defer d.Stop()
	for {
		select {
		case b, ok := <-in:
			if !ok {
				p.Finish(emit)
				idle()
				return nil
			}
			eof, err := d.Handle(b)
			if eof {
				idle()
				return err
			}
			if len(in) == 0 && idle() {
				return nil
			}
		case <-d.C():
			d.Tick()
			if idle() {
				return nil
			}
		}
	}
}
