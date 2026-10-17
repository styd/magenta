// Package engine applies a compiled rule file to lines of text.
package engine

import (
	"bytes"

	"github.com/styd/magenta/internal/render"
	"github.com/styd/magenta/internal/rules"
)

// MaxLine is the longest line that is highlighted. Longer lines pass through.
const MaxLine = 64 << 10

// Highlighter colors lines one at a time. It keeps the state of the context
// state machine between lines, so it must see the lines in order. It is not
// safe for concurrent use.
type Highlighter struct {
	c      *rules.Compiled
	starts []string // ANSI start sequence per scope id
	keep   bool     // ansi = "keep": pass lines through untouched
	plain  bool     // no scope has a style: nothing to add
	ctx    int
	paint  []int32 // scope id per byte of the current line, -1 for none
	clean  []byte  // the current line without SGR sequences
}

// New returns a Highlighter. starts is indexed like c.Scopes (see
// colorscheme.Colorscheme.Starts). ansiMode is "strip" or "keep".
func New(c *rules.Compiled, starts []string, ansiMode string) *Highlighter {
	h := &Highlighter{c: c, starts: starts, keep: ansiMode == "keep", plain: true}
	for _, s := range starts {
		if s != "" {
			h.plain = false
		}
	}
	return h
}

// Reset returns to the default context, as at the start of a log.
func (h *Highlighter) Reset() { h.ctx = 0 }

// Highlight appends the colored line to dst and returns it. line has no
// trailing newline. A trailing carriage return is kept after the colors.
// Removing the escape sequences from the output gives back the input.
func (h *Highlighter) Highlight(dst, line []byte) []byte {
	if len(line) > MaxLine || h.keep {
		return append(dst, line...)
	}
	cr := false
	if n := len(line); n > 0 && line[n-1] == '\r' {
		line, cr = line[:n-1], true
	}
	text := line
	if bytes.IndexByte(line, 0x1b) >= 0 {
		h.clean = render.StripSGR(h.clean[:0], line)
		text = h.clean
	}
	if h.plain {
		dst = append(dst, text...)
	} else {
		dst = h.emit(dst, text, h.process(text))
	}
	if cr {
		dst = append(dst, '\r')
	}
	return dst
}

// process runs the state machine and the rules, filling h.paint.
func (h *Highlighter) process(text []byte) []int32 {
	for range 2 { // leave the context, then process the line again (bounded)
		ctx := h.c.Contexts[h.ctx]
		moved := false
		for i := range ctx.Exits {
			x := &ctx.Exits[i]
			if x.Re.Match(text) != x.Unless {
				h.ctx, moved = x.To, true
				break
			}
		}
		if !moved {
			break
		}
	}
	ctx := h.c.Contexts[h.ctx]

	n := len(text)
	if cap(h.paint) < n {
		h.paint = make([]int32, n, n+n/2)
	}
	paint := h.paint[:n]
	for i := range paint {
		paint[i] = -1
	}
	for i := range ctx.Rules {
		r := &ctx.Rules[i]
		if len(r.Prefix) > 0 && !bytes.HasPrefix(text, r.Prefix) {
			continue
		}
		matched := false
		switch {
		case r.Re == nil:
			fill(paint, 0, n, int32(r.Scope))
			matched = true
		case r.Captures != nil:
			for _, m := range r.Re.FindAllSubmatchIndex(text, -1) {
				matched = true
				for g, sc := range r.Captures {
					if sc >= 0 && m[2+2*g] >= 0 {
						fill(paint, m[2+2*g], m[3+2*g], int32(sc))
					}
				}
			}
		default:
			for _, m := range r.Re.FindAllIndex(text, -1) {
				matched = true
				fill(paint, m[0], m[1], int32(r.Scope))
			}
		}
		if matched && r.Stop {
			break
		}
	}
	for i := range ctx.Enters {
		if e := &ctx.Enters[i]; e.Re.Match(text) {
			h.ctx = e.To
			break
		}
	}
	return paint
}

// fill gives the still-unclaimed bytes of paint[s:e] to scope: the rule that
// ran first keeps a byte.
func fill(paint []int32, s, e int, scope int32) {
	for i := s; i < e; i++ {
		if paint[i] < 0 {
			paint[i] = scope
		}
	}
}

func (h *Highlighter) emit(dst, text []byte, paint []int32) []byte {
	for i := 0; i < len(text); {
		sc := paint[i]
		j := i + 1
		for j < len(text) && paint[j] == sc {
			j++
		}
		if sc >= 0 && h.starts[sc] != "" {
			dst = append(dst, h.starts[sc]...)
			dst = append(dst, text[i:j]...)
			dst = append(dst, render.Reset...)
		} else {
			dst = append(dst, text[i:j]...)
		}
		i = j
	}
	return dst
}
