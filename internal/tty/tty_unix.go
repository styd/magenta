//go:build darwin || dragonfly || freebsd || netbsd || openbsd || linux

package tty

import (
	"errors"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Dev is a real terminal device.
type Dev struct {
	fd     int
	wakeR  int // the key reader also waits on this pipe so it can be stopped at once
	wakeW  int
	mu     sync.Mutex
	saved  unix.Termios
	active bool // cbreak is on
	have   bool // saved holds the original mode
	exited chan struct{}
	closed bool
}

// Open opens the controlling terminal, /dev/tty.
func Open() (Terminal, error) {
	d, err := OpenPath("/dev/tty")
	if err != nil {
		return nil, err
	}
	return d, nil
}

// OpenPath opens a terminal device by path.
func OpenPath(path string) (*Dev, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var p [2]int
	if err := unix.Pipe(p[:]); err != nil {
		unix.Close(fd)
		return nil, err
	}
	unix.CloseOnExec(p[0])
	unix.CloseOnExec(p[1])
	return &Dev{fd: fd, wakeR: p[0], wakeW: p[1]}, nil
}

// Cbreak implements Terminal.
func (d *Dev) Cbreak() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	t, err := unix.IoctlGetTermios(d.fd, ioctlGetTermios)
	if err != nil {
		return err
	}
	if !d.have {
		d.saved, d.have = *t, true
	}
	n := *t
	n.Lflag &^= unix.ICANON | unix.ECHO
	n.Cc[unix.VMIN], n.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(d.fd, ioctlSetTermios, &n); err != nil {
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
	return unix.IoctlSetTermios(d.fd, ioctlSetTermios, &d.saved)
}

// Size implements Terminal.
func (d *Dev) Size() (rows, cols int) {
	ws, err := unix.IoctlGetWinsize(d.fd, unix.TIOCGWINSZ)
	if err != nil || ws.Row == 0 || ws.Col == 0 {
		return 24, 80
	}
	return int(ws.Row), int(ws.Col)
}

// Query implements Terminal.
func (d *Dev) Query(request string, timeout time.Duration) (string, bool) {
	if _, err := unix.Write(d.fd, []byte(request)); err != nil {
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
		var rs unix.FdSet
		rs.Set(d.fd)
		tv := unix.NsecToTimeval(left.Nanoseconds())
		n, err := unix.Select(d.fd+1, &rs, nil, nil, &tv)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return "", false
		}
		if n == 0 {
			return "", false
		}
		m, err := unix.Read(d.fd, buf[:])
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
				continue
			}
			return "", false
		}
		reply = append(reply, buf[:m]...)
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
	d.exited = make(chan struct{})
	go d.readKeys(out, d.exited)
	return out
}

func (d *Dev) readKeys(out chan<- byte, exited chan<- struct{}) {
	defer close(exited)
	defer close(out)
	var buf [64]byte
	for {
		var rs unix.FdSet
		rs.Set(d.fd)
		rs.Set(d.wakeR)
		if _, err := unix.Select(max(d.fd, d.wakeR)+1, &rs, nil, nil, nil); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return
		}
		if rs.IsSet(d.wakeR) {
			unix.Read(d.wakeR, buf[:]) // consume the wake-up
			return
		}
		if rs.IsSet(d.fd) {
			n, err := unix.Read(d.fd, buf[:])
			if err != nil {
				if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
					continue
				}
				return
			}
			if n == 0 {
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
}

// StopKeys implements Terminal.
func (d *Dev) StopKeys() {
	d.mu.Lock()
	exited := d.exited
	d.mu.Unlock()
	if exited == nil {
		return
	}
	unix.Write(d.wakeW, []byte{0})
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
		unix.Close(d.fd)
		unix.Close(d.wakeR)
		unix.Close(d.wakeW)
	}
	return err
}

var _ Terminal = (*Dev)(nil)
