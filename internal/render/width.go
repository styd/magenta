package render

import "unicode/utf8"

func decodeRune(b []byte) (rune, int) { return utf8.DecodeRune(b) }

// runeWidth is a small approximation of the terminal width of r: combining
// marks take no space, wide East Asian characters and emoji take two.
func runeWidth(r rune) int {
	switch {
	case r == '\t':
		return 1
	case r < 0x20 || (r >= 0x7f && r < 0xa0):
		return 0
	case r >= 0x300 && r <= 0x36f, r == 0x200b, r == 0x200d, r >= 0xfe00 && r <= 0xfe0f:
		return 0
	case r >= 0x1100 && r <= 0x115f,
		r >= 0x2e80 && r <= 0xa4cf,
		r >= 0xac00 && r <= 0xd7a3,
		r >= 0xf900 && r <= 0xfaff,
		r >= 0xfe30 && r <= 0xfe6f,
		r >= 0xff00 && r <= 0xff60,
		r >= 0xffe0 && r <= 0xffe6,
		r >= 0x1f300 && r <= 0x1f64f,
		r >= 0x1f900 && r <= 0x1f9ff,
		r >= 0x20000 && r <= 0x3fffd:
		return 2
	}
	return 1
}
