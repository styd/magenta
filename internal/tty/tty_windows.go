//go:build windows

package tty

import (
	"errors"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// Dev is the Windows console. It reads keys from CONIN$ with virtual terminal
// input turned on, so arrow keys and replies to questions arrive as the same
// escape sequences as on Unix.
type Dev struct {
	in, out windows.Handle
	stop    windows.Handle // an event that wakes the key reader
	mu      sync.Mutex
	inMode  uint32
	outMode uint32
	have    bool
	active  bool
	exited  chan struct{}
	closed  bool
}

// Open opens the console, CONIN$ and CONOUT$.
func Open() (Terminal, error) {
	in, err := openConsole("CONIN$")
	if err != nil {
		return nil, err
	}
	out, err := openConsole("CONOUT$")
	if err != nil {
		windows.CloseHandle(in)
		return nil, err
	}
	stop, err := windows.CreateEvent(nil, 1, 0, nil) // manual reset, not signaled
	if err != nil {
		windows.CloseHandle(in)
		windows.CloseHandle(out)
		return nil, err
	}
	return &Dev{in: in, out: out, stop: stop}, nil
}

func openConsole(name string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
}

// Cbreak implements Terminal.
func (d *Dev) Cbreak() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var in, out uint32
	if err := windows.GetConsoleMode(d.in, &in); err != nil {
		return err
	}
	if err := windows.GetConsoleMode(d.out, &out); err != nil {
		return err
	}
	if !d.have {
		d.inMode, d.outMode, d.have = in, out, true
	}
	// No line editing and no echo. Ctrl-C still interrupts (PROCESSED_INPUT).
	n := in&^(windows.ENABLE_LINE_INPUT|windows.ENABLE_ECHO_INPUT) | windows.ENABLE_PROCESSED_INPUT | windows.ENABLE_VIRTUAL_TERMINAL_INPUT
	if err := windows.SetConsoleMode(d.in, n); err != nil {
		return err
	}
	// Colors need escape sequence processing on the output side.
	if err := windows.SetConsoleMode(d.out, out|windows.ENABLE_PROCESSED_OUTPUT|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING); err != nil {
		windows.SetConsoleMode(d.in, d.inMode)
		return err
	}
	d.active = true
	return nil
}

// Restore implements Terminal.
func (d *Dev) Restore() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.active || d.closed {
		return nil
	}
	d.active = false
	err := windows.SetConsoleMode(d.in, d.inMode)
	if err2 := windows.SetConsoleMode(d.out, d.outMode); err == nil {
		err = err2
	}
	return err
}

// Size implements Terminal.
func (d *Dev) Size() (rows, cols int) {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(d.out, &info); err != nil {
		return 24, 80
	}
	rows = int(info.Window.Bottom-info.Window.Top) + 1
	cols = int(info.Window.Right-info.Window.Left) + 1
	if rows <= 0 || cols <= 0 {
		return 24, 80
	}
	return rows, cols
}

// wait blocks until the console has input, the stop event is set, or the
// timeout passes (a negative timeout waits for ever).
func (d *Dev) wait(timeout time.Duration) (input, stopped bool, err error) {
	ms := uint32(windows.INFINITE)
	if timeout >= 0 {
		ms = uint32(timeout / time.Millisecond)
	}
	ev, err := windows.WaitForMultipleObjects([]windows.Handle{d.in, d.stop}, false, ms)
	if err != nil {
		return false, false, err
	}
	switch ev {
	case windows.WAIT_OBJECT_0:
		return true, false, nil
	case windows.WAIT_OBJECT_0 + 1:
		return false, true, nil
	}
	return false, false, nil // timed out
}

// Query implements Terminal.
func (d *Dev) Query(request string, timeout time.Duration) (string, bool) {
	var done uint32
	if err := windows.WriteFile(d.out, []byte(request), &done, nil); err != nil {
		return "", false
	}
	var reply []byte
	deadline := time.Now().Add(timeout)
	var buf [64]byte
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return "", false
		}
		input, _, err := d.wait(left)
		if err != nil || !input {
			return "", false
		}
		var n uint32
		if err := windows.ReadFile(d.in, buf[:], &n, nil); err != nil || n == 0 {
			return "", false
		}
		reply = append(reply, buf[:n]...)
		if endsReply(reply) {
			return string(reply), true
		}
	}
}

// StartKeys implements Terminal.
func (d *Dev) StartKeys() <-chan byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(chan byte, 64)
	if d.exited != nil || d.closed {
		close(out)
		return out
	}
	windows.ResetEvent(d.stop)
	d.exited = make(chan struct{})
	go d.readKeys(out, d.exited)
	return out
}

func (d *Dev) readKeys(out chan<- byte, exited chan<- struct{}) {
	defer close(exited)
	defer close(out)
	var buf [64]byte
	for {
		input, stopped, err := d.wait(-1)
		if err != nil || stopped {
			return
		}
		if !input {
			continue
		}
		var n uint32
		if err := windows.ReadFile(d.in, buf[:], &n, nil); err != nil {
			if errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
				continue
			}
			return
		}
		for _, b := range buf[:n] {
			select {
			case out <- b:
			default: // nobody is listening fast enough. Stray keys do not matter
			}
		}
	}
}

// StopKeys implements Terminal.
func (d *Dev) StopKeys() {
	d.mu.Lock()
	exited := d.exited
	d.mu.Unlock()
	if exited == nil {
		return
	}
	windows.SetEvent(d.stop)
	<-exited
	d.mu.Lock()
	d.exited = nil
	d.mu.Unlock()
}

// Close implements Terminal.
func (d *Dev) Close() error {
	d.StopKeys()
	err := d.Restore()
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closed {
		d.closed = true
		windows.CloseHandle(d.in)
		windows.CloseHandle(d.out)
		windows.CloseHandle(d.stop)
	}
	return err
}

var _ Terminal = (*Dev)(nil)
