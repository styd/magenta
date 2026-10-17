package mgnt

import (
	"strconv"
	"strings"
)

// vscreen is a tiny virtual terminal: enough of the control sequences mgnt
// writes to say what is on the screen. It handles printable text, \r \n \t,
// cursor positioning (H), erasing (J, K), the alternate screen (?1049), and
// the deferred wrap at the last column. Color codes are skipped.
type vscreen struct {
	rows, cols int
	cells      [][]rune
	r, c       int
	wrapNext   bool
	saved      [][]rune // the normal screen while the alternate one is shown
	alt        bool
	altCount   int // how many times the alternate screen was entered
}

func newVScreen(rows, cols int) *vscreen {
	s := &vscreen{rows: rows, cols: cols}
	s.cells = s.blank()
	return s
}

func (s *vscreen) blank() [][]rune {
	out := make([][]rune, s.rows)
	for i := range out {
		out[i] = []rune(strings.Repeat(" ", s.cols))
	}
	return out
}

func (s *vscreen) scroll() {
	copy(s.cells, s.cells[1:])
	s.cells[s.rows-1] = []rune(strings.Repeat(" ", s.cols))
}

func (s *vscreen) newline() {
	if s.r == s.rows-1 {
		s.scroll()
	} else {
		s.r++
	}
}

func (s *vscreen) put(r rune) {
	if s.wrapNext {
		s.c, s.wrapNext = 0, false
		s.newline()
	}
	s.cells[s.r][s.c] = r
	if s.c == s.cols-1 {
		s.wrapNext = true
	} else {
		s.c++
	}
}

// feed writes bytes to the screen.
func (s *vscreen) feed(b []byte) {
	rs := []rune(string(b))
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; {
		case r == '\r':
			s.c, s.wrapNext = 0, false
		case r == '\n':
			s.newline()
		case r == '\t':
			for s.c%8 != 7 && s.c < s.cols-1 {
				s.c++
			}
			s.put(' ')
		case r == 0x1b && i+1 < len(rs) && rs[i+1] == '[':
			j := i + 2
			for j < len(rs) && !(rs[j] >= 0x40 && rs[j] <= 0x7e) {
				j++
			}
			if j < len(rs) {
				s.csi(string(rs[i+2:j]), rs[j])
			}
			i = j
		case r < 0x20:
		default:
			s.put(r)
		}
	}
}

func (s *vscreen) csi(params string, final rune) {
	nums := func() []int {
		var out []int
		for _, p := range strings.Split(strings.TrimPrefix(params, "?"), ";") {
			n, _ := strconv.Atoi(p)
			out = append(out, n)
		}
		return out
	}
	switch final {
	case 'H':
		n := nums()
		row, col := 1, 1
		if len(n) > 0 && n[0] > 0 {
			row = n[0]
		}
		if len(n) > 1 && n[1] > 0 {
			col = n[1]
		}
		s.r, s.c, s.wrapNext = min(row-1, s.rows-1), min(col-1, s.cols-1), false
	case 'J':
		if n := nums(); n[0] == 2 {
			s.cells = s.blank()
			break
		}
		for c := s.c; c < s.cols; c++ {
			s.cells[s.r][c] = ' '
		}
		for r := s.r + 1; r < s.rows; r++ {
			s.cells[r] = []rune(strings.Repeat(" ", s.cols))
		}
	case 'K':
		for c := s.c; c < s.cols; c++ {
			s.cells[s.r][c] = ' '
		}
	case 'h', 'l':
		if strings.HasPrefix(params, "?") && strings.Contains(params, "1049") {
			if final == 'h' && !s.alt {
				s.saved, s.cells, s.alt = s.cells, s.blank(), true
				s.altCount++
			} else if final == 'l' && s.alt {
				s.cells, s.alt = s.saved, false
			}
		}
	}
}

// lines returns the rows with trailing spaces trimmed.
func (s *vscreen) lines() []string {
	out := make([]string, s.rows)
	for i, row := range s.cells {
		out[i] = strings.TrimRight(string(row), " ")
	}
	return out
}

// screenOf replays output on a fresh screen of the given size.
func screenOf(out string, rows, cols int) *vscreen {
	s := newVScreen(rows, cols)
	s.feed([]byte(out))
	return s
}
