// Package tty puts the controlling terminal into cbreak mode and reads keys
// from it. mgnt's stdin is the log, so keys come from /dev/tty instead.
package tty

import (
	"errors"
	"time"
)

// ErrUnsupported is returned where mgnt cannot read keys from a terminal.
var ErrUnsupported = errors.New("keyboard input from the terminal is not supported on this platform")

// Terminal is the keyboard side of the controlling terminal. It is an
// interface so that the stream view can be tested without a terminal.
type Terminal interface {
	// Cbreak turns off line editing and echo, so single keys arrive at once.
	// Output processing and signal keys (Ctrl-C) are left as they are, so "\n"
	// still starts a new line and Ctrl-C still interrupts.
	Cbreak() error
	// Restore puts back the mode that was found before the first Cbreak. It is
	// safe to call at any time, any number of times.
	Restore() error
	// StartKeys starts reading keys and returns them one byte at a time.
	StartKeys() <-chan byte
	// StopKeys stops reading and returns only once nothing reads the terminal
	// any more, so that another program (fzf) can take it over. Keys typed
	// meanwhile stay in the terminal's input queue for that program.
	StopKeys()
	// Query writes a request to the terminal and waits up to timeout for its
	// reply, which ends with BEL or ESC \. It must be called while no keys are
	// being read (before StartKeys or after StopKeys). A terminal that does not
	// answer gives ok false.
	Query(request string, timeout time.Duration) (reply string, ok bool)
	// Size returns the number of rows and columns, or 24 and 80 if unknown.
	Size() (rows, cols int)
	// Close restores the mode and releases the terminal.
	Close() error
}

// Key bytes mgnt reacts to.
const (
	KeySearch = '/'
	KeyQuit   = 'q'
	KeyPage   = ' '
	KeyLine   = '\r'
)

// endsReply says whether a reply to Query is complete: it ends with BEL or
// with ESC followed by a backslash.
func endsReply(b []byte) bool {
	for i, c := range b {
		if c == 0x07 || (c == 0x1b && i+1 < len(b) && b[i+1] == '\\') {
			return true
		}
	}
	return false
}
