package render

// StripSGR appends src to dst with SGR sequences (ESC [ params m) removed.
// Other bytes, including other escape sequences, are kept.
func StripSGR(dst, src []byte) []byte {
	for i := 0; i < len(src); {
		if n := sgrLen(src[i:]); n > 0 {
			i += n
			continue
		}
		dst = append(dst, src[i])
		i++
	}
	return dst
}

// sgrLen returns the length of the SGR sequence at the start of b, or 0.
func sgrLen(b []byte) int {
	if len(b) < 3 || b[0] != 0x1b || b[1] != '[' {
		return 0
	}
	for i := 2; i < len(b); i++ {
		switch c := b[i]; {
		case c >= '0' && c <= '9', c == ';', c == ':':
		case c == 'm':
			return i + 1
		default:
			return 0
		}
	}
	return 0
}

// VisibleWidth counts the columns a line takes, ignoring SGR sequences. Each
// rune counts as one column, East Asian wide runes count as two.
func VisibleWidth(b []byte) int {
	w := 0
	for i := 0; i < len(b); {
		if n := sgrLen(b[i:]); n > 0 {
			i += n
			continue
		}
		r, size := decodeRune(b[i:])
		w += runeWidth(r)
		i += size
	}
	return w
}

// Rows returns how many terminal rows a line takes when printed on a terminal
// cols columns wide: escape sequences take no room, a tab goes to the next
// multiple of 8, and a character that does not fit starts a new row. An
// empty line takes one row.
func Rows(b []byte, cols int) int {
	if cols < 1 {
		cols = 80
	}
	rows, col := 1, 0
	for i := 0; i < len(b); {
		if n := sgrLen(b[i:]); n > 0 {
			i += n
			continue
		}
		r, size := decodeRune(b[i:])
		i += size
		w := runeWidth(r)
		if r == '\t' {
			w = 8 - col%8
		}
		if w == 0 {
			continue
		}
		if col+w > cols {
			rows++
			col = 0
		}
		col += w
	}
	return rows
}
