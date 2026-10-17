package mgnt

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/styd/magenta/internal/buffer"
	"github.com/styd/magenta/internal/render"
	"github.com/styd/magenta/internal/search"
)

// Screen control. The alternate screen is a separate screen that is given back
// when the pager ends, so the terminal looks as it did before. 1007 makes a
// terminal that supports it send arrow keys for the mouse wheel on the
// alternate screen, which is how the wheel scrolls the pager.
const (
	altOn  = "\x1b[?1049h\x1b[?1007h\x1b[?25l"
	altOff = "\x1b[?25h\x1b[?1007l\x1b[?1049l"
)

// paintOff puts back the terminal's own text and background colors (OSC 110
// and 111). paintOn, made from the colorscheme, sets them (OSC 10 and 11).
const paintOff = "\x1b]110\x1b\\\x1b]111\x1b\\"

// readAheadScreens is how far below the screen the pager reads, in screens.
const readAheadScreens = 3

// reserveLines keeps the ring from evicting the line at the top of the screen
// while reading ahead.
const reserveLines = 8192

func (v *view) enterAlt() {
	if !v.alt {
		v.write(altOn)
		v.alt = true
		v.paint()
	}
}

// paint gives the terminal the text and background colors of the colorscheme
// (its ui.background style), until unpaint. It stays on while fzf runs, so the
// search looks like the pager.
func (v *view) paint() {
	if !v.painted && v.s.paintOn != "" {
		v.write(v.s.paintOn)
		v.painted = true
	}
}

// unpaint gives the terminal its own colors back.
func (v *view) unpaint() {
	if v.painted {
		v.write(paintOff)
		v.painted = false
		v.flush()
	}
}

func (v *view) leaveAlt() {
	if v.alt {
		v.write(altOff)
		v.alt = false
		v.flush()
	}
}

// enterPaging switches to the full-screen pager, starting at the first line.
func (v *view) enterPaging() {
	v.mode = paging
	oldest, _ := v.ring.Bounds()
	v.top = oldest
	v.enterAlt()
	v.redraw()
}

// footerRows is the number of rows under the log lines: the key hints, then
// the position, laid out the way fzf lays out its header and info line.
const footerRows = 2

// contentRows is the number of rows for log lines.
func (v *view) contentRows() (content, cols int) {
	rows, cols := v.term.Size()
	return max(rows-footerRows, 1), cols
}

// gutterWidth is the width of the line number column: the digits of the
// highest number, and a space. Zero when numbers are off.
func (v *view) gutterWidth() int {
	if !v.numbers {
		return 0
	}
	_, newest := v.ring.Bounds()
	return len(strconv.FormatUint(newest, 10)) + 1
}

// textCols is the width left for the text of a line.
func (v *view) textCols() int {
	_, cols := v.contentRows()
	return max(cols-v.gutterWidth(), 1)
}

// rowsOf is how many screen rows a line takes: one when long lines are chopped,
// otherwise as many as its wrapped pieces.
func (v *view) rowsOf(text []byte) int {
	if v.chop {
		return 1
	}
	return render.Count(text, v.textCols())
}

// fit returns the lines that fit on the screen starting at seq top, and the
// seq of the last one. A line taller than the screen is shown alone.
func (v *view) fit(top uint64) (lines []buffer.Line, bottom uint64) {
	content, _ := v.contentRows()
	used := 0
	bottom = top - 1
	for _, l := range v.ring.Collect(top, content) {
		r := v.rowsOf(l.Text)
		if used+r > content && used > 0 {
			break
		}
		used += r
		lines = append(lines, l)
		bottom = l.Seq
	}
	return lines, bottom
}

// maxTop is the highest top line that still fills the screen: scrolling stops
// when the last line read is at the bottom.
func (v *view) maxTop() uint64 {
	content, _ := v.contentRows()
	oldest, newest := v.ring.Bounds()
	if newest == 0 {
		return 0
	}
	n := min(content, int(newest-oldest)+1)
	batch := v.ring.Collect(newest-uint64(n)+1, n)
	used, top := 0, newest+1
	for i := len(batch) - 1; i >= 0; i-- {
		r := v.rowsOf(batch[i].Text)
		if used+r > content && used > 0 {
			break
		}
		used += r
		top = batch[i].Seq
	}
	return top
}

// setTop moves the screen so that line seq is at the top, within the lines the
// ring still holds and not past the point where the last line is at the bottom.
func (v *view) setTop(seq uint64) {
	oldest, _ := v.ring.Bounds()
	if m := v.maxTop(); seq > m {
		seq = m
	}
	if seq < oldest {
		seq = oldest
	}
	v.top = seq
}

// pageUpTop is the top line of the screen before the current one.
func (v *view) pageUpTop() uint64 {
	content, _ := v.contentRows()
	oldest, _ := v.ring.Bounds()
	if v.top <= oldest {
		return oldest
	}
	from := oldest
	if v.top-oldest > uint64(content) {
		from = v.top - uint64(content)
	}
	batch := v.ring.Collect(from, int(v.top-from))
	used, top := 0, v.top
	for i := len(batch) - 1; i >= 0; i-- {
		r := v.rowsOf(batch[i].Text)
		if used+r > content && used > 0 {
			break
		}
		used += r
		top = batch[i].Seq
	}
	return top
}

// wantMore says whether the pager should read more input now. It reads a few
// screens ahead of what is shown, everything while a search is open or the end
// was asked for, and as it comes while following, but never so far that the
// line at the top would be evicted.
func (v *view) wantMore() bool {
	if v.eof {
		return false
	}
	_, newest := v.ring.Bounds()
	if limitReadAhead && newest-v.top >= uint64(v.ring.Capacity()-reserveLines) && !v.seekEnd && !v.following {
		return false
	}
	if v.seekEnd || v.searching || v.following || !limitReadAhead {
		return true
	}
	rows, _ := v.term.Size()
	return newest-v.bottom < uint64(readAheadScreens*rows)
}

// redraw paints the whole screen: the lines from v.top and the footer.
func (v *view) redraw() {
	rows, cols := v.term.Size()
	oldest, newest := v.ring.Bounds()
	if newest == 0 {
		v.top = 0
	} else {
		if v.top < oldest {
			v.top = oldest
		}
		if v.top > newest {
			v.top = newest
		}
	}
	lines, bottom := v.fit(v.top)
	v.bottom = bottom

	gw, text := v.gutterWidth(), v.textCols()
	if v.chop { // do not scroll sideways past the longest line on screen
		widest := 0
		for _, l := range lines {
			widest = max(widest, render.Width(l.Text))
		}
		v.hoff = max(min(v.hoff, widest-1), 0)
	}
	v.write("\x1b[H\x1b[J")
	for _, l := range lines {
		var pieces [][]byte
		if v.chop {
			pieces = [][]byte{render.Cut(l.Text, v.hoff, text)}
		} else {
			pieces = render.Wrap(l.Text, text)
		}
		for i, piece := range pieces {
			if gw > 0 {
				if i == 0 {
					v.write(v.gutterStyle() + fmt.Sprintf("%*d", gw-1, l.Seq) + v.resetStyle() + " ")
				} else {
					v.write(strings.Repeat(" ", gw))
				}
			}
			v.write(string(piece))
			v.write("\r\n")
		}
	}
	hints, info, rule := v.footer(cols)
	v.write(fmt.Sprintf("\x1b[%d;1H", max(rows-1, 1)))
	v.write(v.hintStyle(true) + hints + v.resetStyle())
	v.write(fmt.Sprintf("\x1b[%d;1H", max(rows, 1)))
	v.write(v.hintStyle(false) + info + v.resetStyle())
	if rule != "" {
		v.write(v.ruleStyle() + rule + v.resetStyle())
	}
}

// gutterStyle dims the line numbers, so they stay out of the way of the text.
func (v *view) gutterStyle() string {
	if v.s.depth == render.None {
		return ""
	}
	return "\x1b[2m"
}

// uiStyle is the ANSI start sequence of a ui style of the colorscheme, or ""
// when there is no color or the colorscheme has no such style.
func (v *view) uiStyle(scope string) string {
	if v.s.depth == render.None {
		return ""
	}
	if st, ok := v.s.scheme.Resolve(scope); ok {
		return st.Start(v.s.depth)
	}
	return ""
}

// ruleStyle is the color of the rule after the info line, the same as fzf's
// separator: the ui.border style, or without one fzf's own, a dim gray (59) on
// 256 colors and dim default text on 16 colors.
func (v *view) ruleStyle() string {
	if s := v.uiStyle("ui.border"); s != "" {
		return s
	}
	switch v.s.depth {
	case render.None:
		return ""
	case render.Basic16:
		return "\x1b[2m\x1b[39m"
	}
	return "\x1b[38;5;59m"
}

// hintStyle is the color of the keys on top (hints, true) or of the text of the
// info line (false), the same as fzf's header and info: the ui.hint and ui.info
// styles, or without them fzf's own defaults, 109 and 144 on a 256-color
// terminal and cyan and yellow on a 16-color one.
func (v *view) hintStyle(hints bool) string {
	scope := "ui.info"
	if hints {
		scope = "ui.hint"
	}
	if s := v.uiStyle(scope); s != "" {
		return s
	}
	switch v.s.depth {
	case render.None:
		return ""
	case render.Basic16:
		if hints {
			return "\x1b[36m"
		}
		return "\x1b[33m"
	}
	if hints {
		return "\x1b[38;5;109m"
	}
	return "\x1b[38;5;144m"
}

func (v *view) resetStyle() string {
	if v.s.depth == render.None {
		return ""
	}
	return render.Reset
}

// footerIndent is the gutter fzf leaves before its header and info line.
const footerIndent = "  "

// hint is one entry of the key hints. When they do not fit, the entries with
// the lowest keep value go first, "q: quit" is never dropped.
type hint struct {
	text string
	keep int
}

// hints lists what applies right now, in the order they are shown.
func (v *view) hints() []hint {
	h := []hint{{"space/b: page", 9}, {"j/k: line", 7}, {"g/G: top/end", 5}}
	if v.query != "" && v.matchesReady {
		h = append(h, hint{"n/N: match", 6})
	}
	if !v.eof {
		h = append(h, hint{"F: follow", 4})
	}
	if v.chop {
		h = append(h, hint{"w: wrap", 2}, hint{"h/l: side", 3})
	} else {
		h = append(h, hint{"w: chop", 2})
	}
	h = append(h, hint{"#: numbers", 1})
	if v.srch != nil {
		h = append(h, hint{"/: search", 8})
	}
	return h
}

// footer builds the two rows under the lines, like fzf's header and info line:
// the keys, in plain muted text, and the position followed by a rule across
// the rest of the width (returned separately, since fzf colors it differently).
func (v *view) footer(cols int) (hints, info, rule string) {
	_, newest := v.ring.Bounds()
	list := v.hints()
	// The most hints that fit, dropping those that matter least first.
	for {
		parts := make([]string, 0, len(list)+1)
		for _, h := range list {
			parts = append(parts, h.text)
		}
		parts = append(parts, "q: quit")
		text := footerIndent + strings.Join(parts, "   ")
		if len([]rune(text)) <= cols || len(list) == 0 {
			hints = text
			break
		}
		drop := 0
		for i, h := range list {
			if h.keep < list[drop].keep {
				drop = i
			}
		}
		list = append(list[:drop:drop], list[drop+1:]...)
	}
	if v.msg != "" {
		hints = footerIndent + v.msg
		if r := []rune(hints); len(r) > cols {
			hints = string(r[:cols])
		}
	}

	more := "+"
	if v.eof {
		more = ""
	}
	pct := 100
	if newest > 0 {
		pct = int(v.bottom * 100 / newest)
	}
	end := ""
	if v.eof && v.bottom >= newest {
		end = " (END)"
	}
	var mode, modeShort []string
	if v.following {
		mode, modeShort = append(mode, "following"), append(modeShort, "following")
	}
	if v.chop {
		mode = append(mode, fmt.Sprintf("chop, column %d", v.hoff+1))
		modeShort = append(modeShort, fmt.Sprintf("chop %d", v.hoff+1))
	}
	suffix, suffixShort := "", ""
	if len(mode) > 0 {
		suffix = "  [" + strings.Join(mode, ", ") + "]"
		suffixShort = " [" + strings.Join(modeShort, ", ") + "]"
	}
	pos := fmt.Sprintf("%slines %d-%d", footerIndent, v.top, v.bottom)
	of := fmt.Sprintf("%s of %d%s", pos, newest, more)
	// The most that fits, never wider than the terminal or the row would wrap.
	// A mode that is on (following, chopping) matters more than the percentage.
	forms := []string{
		fmt.Sprintf("%s  %d%%%s%s", of, pct, end, suffix),
		of + suffix,
		pos + suffix,
		pos + suffixShort,
		fmt.Sprintf("%s  %d%%%s", of, pct, end),
		fmt.Sprintf("%s%s", of, end),
		pos,
	}
	for _, form := range forms {
		info = form
		if len([]rune(info)) <= cols {
			break
		}
	}
	if r := []rune(info); len(r) > cols {
		info = string(r[:cols])
	}
	if n := cols - len([]rune(info)) - 1; n > 0 {
		rule = " " + strings.Repeat("─", n)
	}
	return hints, info, rule
}

// pagerKey handles a key in the pager.
func (v *view) pagerKey(k key) {
	v.msg = ""
	if v.following { // any key stops following. Movement keys then also act
		v.following = false
		if k == 'F' {
			v.redraw()
			return
		}
	}
	if k != 'G' && k != '>' && k != keyEnd {
		v.seekEnd = false
	}
	if k != 'n' && k != 'N' {
		v.matchIdx = -1
	}
	content, _ := v.contentRows()
	_, newest := v.ring.Bounds()
	line := func(n int) {
		if n < 0 {
			up := uint64(-n)
			if v.top > up {
				v.setTop(v.top - up)
			} else {
				v.setTop(0)
			}
		} else {
			v.setTop(v.top + uint64(n))
		}
	}
	side := max(v.textCols()/2, 1)
	switch k {
	case 'j', 'e', 5, 14, 10, '\r', keyDown: // also ctrl-e, ctrl-n, ctrl-j
		line(1)
	case 'k', 'y', 25, 16, 11, keyUp: // also ctrl-y, ctrl-p, ctrl-k
		line(-1)
	case ' ', 'f', 6, keyPgDn: // also ctrl-f
		if v.bottom >= newest {
			v.setTop(v.maxTop())
		} else {
			v.setTop(v.bottom + 1)
		}
	case 'b', 2, keyPgUp: // also ctrl-b
		v.setTop(v.pageUpTop())
	case 'd', 4:
		line(max(content/2, 1))
	case 'u', 21:
		line(-max(content/2, 1))
	case 'g', '<', keyHome:
		v.setTop(0)
	case 'G', '>', keyEnd:
		if v.eof {
			v.setTop(v.maxTop())
		} else {
			v.seekEnd = true // read what there is, then show the end
			v.armSeek()
			v.msg = "reading to the end of the input..."
		}
	case 'F':
		if v.eof {
			v.msg = "the input has ended: there is nothing to follow"
			break
		}
		v.following = true
		v.setTop(v.maxTop())
		v.msg = "following new lines... (any key stops)"
	case 'w':
		v.chop, v.hoff = !v.chop, 0
		if v.chop {
			v.msg = "long lines are cut: h/l or the left and right arrows scroll sideways, w wraps them again"
		} else {
			v.msg = "long lines are wrapped"
		}
		v.setTop(v.top) // the rows per line changed
	case '#':
		v.numbers = !v.numbers
		v.setTop(v.top)
	case 'l', keyRight:
		if !v.chop {
			v.msg = "long lines are wrapped: press w to cut them and scroll sideways"
			break
		}
		v.hoff += side
	case 'h', keyLeft:
		if !v.chop {
			v.msg = "long lines are wrapped: press w to cut them and scroll sideways"
			break
		}
		v.hoff = max(v.hoff-side, 0)
	case 'n', 'N':
		v.jumpToMatch(k == 'n')
	default:
		return
	}
	v.redraw()
}

// jumpToMatch moves to the next (forward) or previous match of the last
// search, wrapping around at the ends of the buffer.
func (v *view) jumpToMatch(forward bool) {
	switch {
	case v.query == "":
		v.msg = "no search yet: press / first"
		return
	case !v.matchesReady:
		v.msg = "still finding the matches of " + strconv.Quote(v.query) + "..."
		return
	case len(v.matches) == 0:
		v.msg = "no line matches " + strconv.Quote(v.query)
		return
	}
	n := len(v.matches)
	idx := v.matchIdx
	wrapped := false
	if idx < 0 { // from where the screen is: the first match below the top, or above it
		idx = sort.Search(n, func(i int) bool { return v.matches[i] > v.top })
		if forward {
			if idx >= n {
				idx, wrapped = 0, true
			}
		} else {
			idx--
			if idx < 0 {
				idx, wrapped = n-1, true
			}
		}
	} else if forward {
		if idx++; idx >= n {
			idx, wrapped = 0, true
		}
	} else {
		if idx--; idx < 0 {
			idx, wrapped = n-1, true
		}
	}
	v.matchIdx = idx
	if oldest, _ := v.ring.Bounds(); v.matches[idx] < oldest {
		v.msg = "that match is no longer in the buffer"
		return
	}
	v.setTop(v.matches[idx])
	v.msg = fmt.Sprintf("match %d of %d, line %d", idx+1, n, v.matches[idx])
	if wrapped {
		v.msg += " (searching from the other end)"
	}
}

// afterPagerSearch applies a search result in the pager. Accepting a line
// jumps to it, the way a search in a pager does. The lines are not printed
// anywhere, since the pager is the view of them. The query is kept, and its
// matches are looked up in the background for n and N.
func (v *view) afterPagerSearch(res search.Result) {
	var msgs []string
	msgs = append(msgs, res.Notices...)
	switch {
	case res.Err != nil:
		msgs = append(msgs, "fzf failed: "+res.Err.Error())
	case res.Code == 0 && len(res.Selected) > 0:
		sort.SliceStable(res.Selected, func(i, j int) bool { return res.Selected[i].Seq < res.Selected[j].Seq })
		target := res.Selected[0].Seq
		if oldest, newest := v.ring.Bounds(); target >= oldest && target <= newest {
			v.setTop(target)
		} else {
			msgs = append(msgs, "that line is no longer in the buffer")
		}
	}
	if res.Query != "" && res.Err == nil {
		v.query, v.matches, v.matchesReady, v.matchIdx = res.Query, nil, false, -1
		v.findMatches(res.Query)
	}
	v.msg = strings.Join(msgs, ". ")
	v.redraw()
}

// findMatches asks fzf, in the background, which lines match the query.
func (v *view) findMatches(query string) {
	if v.srch == nil || v.matchCh == nil {
		return
	}
	opts := v.s.env.mgntFzfOpts()
	go func() {
		m, err := v.srch.Matches(query, opts)
		v.matchCh <- matchResult{query, m, err}
	}()
}

// matchResult is the answer of findMatches.
type matchResult struct {
	query   string
	matches []uint64
	err     error
}
