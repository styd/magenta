package mgnt

// key is a key press: a byte for ordinary keys (including control keys), or
// one of the values below for keys that send an escape sequence.
type key int

const (
	keyUp key = 0x100 + iota
	keyDown
	keyPgUp
	keyPgDn
	keyHome
	keyEnd
	keyLeft
	keyRight
)

// keyParser turns the bytes read from the terminal into keys. Arrow keys,
// Home, End, Page Up, and Page Down arrive as escape sequences (ESC [ A, ESC O A,
// ESC [ 5 ~ ...). Everything else is a single byte. A lone ESC is not a key
// mgnt uses: when the byte after it is not part of a sequence, the ESC is
// dropped and that byte is the key.
type keyParser struct {
	state  int // 0 ground, 1 after ESC, 2 inside ESC [, 3 after ESC O, 4 inside an OSC, 5 ESC inside an OSC
	params []byte
}

// feed adds one byte and returns the key it completes, if any.
func (p *keyParser) feed(b byte) (key, bool) {
	switch p.state {
	case 1:
		switch b {
		case '[':
			p.state, p.params = 2, p.params[:0]
			return 0, false
		case 'O':
			p.state = 3
			return 0, false
		case ']':
			p.state = 4 // a reply to a question we asked the terminal. Not a key
			return 0, false
		case 0x1b:
			return 0, false // ESC ESC: still waiting
		}
		p.state = 0
		return key(b), true
	case 2:
		if b >= 0x40 && b <= 0x7e { // the final byte of the sequence
			p.state = 0
			return csiKey(b, p.params)
		}
		p.params = append(p.params, b)
		return 0, false
	case 3:
		p.state = 0
		return csiKey(b, nil)
	case 4:
		switch b {
		case 0x07:
			p.state = 0
		case 0x1b:
			p.state = 5
		}
		return 0, false
	case 5:
		if b == '\\' {
			p.state = 0
		} else {
			p.state = 4
		}
		return 0, false
	}
	if b == 0x1b {
		p.state = 1
		return 0, false
	}
	return key(b), true
}

// csiKey names the key of an ESC [ params final sequence (also used for
// ESC O final, which sends the same letters).
func csiKey(final byte, params []byte) (key, bool) {
	switch final {
	case 'A':
		return keyUp, true
	case 'B':
		return keyDown, true
	case 'C':
		return keyRight, true
	case 'D':
		return keyLeft, true
	case 'H':
		return keyHome, true
	case 'F':
		return keyEnd, true
	case '~':
		switch string(params) {
		case "1", "7":
			return keyHome, true
		case "4", "8":
			return keyEnd, true
		case "5":
			return keyPgUp, true
		case "6":
			return keyPgDn, true
		}
	}
	return 0, false // a key we do not use, such as Left, Right, or F5
}
