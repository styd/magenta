package render

// Lines are cut into columns here, not by the terminal, so that the pager can
// put a gutter in front of them, show only part of a long line, and know
// exactly how many rows a line takes. Color sequences take no room and are
// carried over from one row to the next, and tabs become spaces.

// token is one escape sequence of color (sgr) or one visible character.
type token struct {
	b   []byte
	w   int // columns, 0 for a color sequence and for a combining mark
	sgr bool
}

// tokenize splits a line, turning each tab into the spaces up to the next
// multiple of 8.
func tokenize(text []byte) []token {
	var toks []token
	col := 0
	for i := 0; i < len(text); {
		if n := sgrLen(text[i:]); n > 0 {
			toks = append(toks, token{b: text[i : i+n], sgr: true})
			i += n
			continue
		}
		r, size := decodeRune(text[i:])
		if r == '\t' {
			for n := 8 - col%8; n > 0; n-- {
				toks = append(toks, token{b: []byte{' '}, w: 1})
				col++
			}
			i += size
			continue
		}
		w := runeWidth(r)
		toks = append(toks, token{b: text[i : i+size], w: w})
		col += w
		i += size
	}
	return toks
}

// colorState tracks which color sequences are in effect, so that a row can
// start with them again.
type colorState struct{ active [][]byte }

func (c *colorState) add(seq []byte) {
	if string(seq) == "\x1b[0m" || string(seq) == "\x1b[m" {
		c.active = c.active[:0]
		return
	}
	c.active = append(c.active, seq)
}

func (c *colorState) replay(dst []byte) []byte {
	for _, s := range c.active {
		dst = append(dst, s...)
	}
	return dst
}

// finish ends a row: colors still in effect are switched off, so that what is
// drawn next to the row does not take them on.
func (c *colorState) finish(dst []byte) []byte {
	if len(c.active) > 0 {
		dst = append(dst, Reset...)
	}
	return dst
}

// Wrap splits a line into rows of at most width columns. A character that does
// not fit goes to the next row. A row that continues a colored stretch starts
// with the color again and each row ends with a reset, so the rows can be
// drawn one by one. An empty line is one empty row.
func Wrap(text []byte, width int) [][]byte {
	width = max(width, 1)
	var rows [][]byte
	var cur []byte
	var cs colorState
	col := 0
	for _, t := range tokenize(text) {
		if t.sgr {
			cur = append(cur, t.b...)
			cs.add(t.b)
			continue
		}
		if t.w > 0 && col > 0 && col+t.w > width {
			rows = append(rows, cs.finish(cur))
			cur = cs.replay(nil)
			col = 0
		}
		cur = append(cur, t.b...)
		col += t.w
	}
	return append(rows, cs.finish(cur))
}

// Count is len(Wrap(text, width)) without building the rows.
func Count(text []byte, width int) int {
	width = max(width, 1)
	rows, col := 1, 0
	for _, t := range tokenize(text) {
		if t.sgr || t.w == 0 {
			continue
		}
		if col > 0 && col+t.w > width {
			rows++
			col = 0
		}
		col += t.w
	}
	return rows
}

// Cut returns the part of a line from column from, at most width columns wide,
// in one row: what a terminal that does not wrap would show after the line was
// scrolled sideways. Colors that were switched on before the cut are switched
// on again. A wide character cut in half becomes a space.
func Cut(text []byte, from, width int) []byte {
	width = max(width, 1)
	var out []byte
	var cs colorState
	col, used := 0, 0
	started := false
	baseKept := false // the last visible character was put in the output
	for _, t := range tokenize(text) {
		if t.sgr {
			if started {
				out = append(out, t.b...)
			}
			cs.add(t.b)
			continue
		}
		if t.w == 0 { // a combining mark belongs to the character before it
			if baseKept {
				out = append(out, t.b...)
			}
			continue
		}
		if col+t.w <= from { // before the window
			baseKept = false
			col += t.w
			continue
		}
		if !started {
			started = true
			out = cs.replay(out)
			if col < from { // the left half of a wide character was cut off
				pad := min(col+t.w-from, width)
				for i := 0; i < pad; i++ {
					out = append(out, ' ')
				}
				used += pad
				col += t.w
				baseKept = false
				continue
			}
		}
		if used+t.w > width {
			if used < width { // a wide character that does not fit at the right edge
				out = append(out, ' ')
			}
			break
		}
		out = append(out, t.b...)
		used += t.w
		col += t.w
		baseKept = true
	}
	return cs.finish(out)
}

// Width is the number of columns a line takes on one row, with tabs expanded.
func Width(text []byte) int {
	w := 0
	for _, t := range tokenize(text) {
		w += t.w
	}
	return w
}
