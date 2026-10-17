//go:build darwin || linux

package tty

import (
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// termios reads the mode of a device. PENDIN is set by the kernel itself when
// a terminal leaves canonical mode with input queued. It is not part of the
// state a program chooses, so it is masked out.
func termios(t *testing.T, fd int) unix.Termios {
	t.Helper()
	tm, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		t.Fatal(err)
	}
	tm.Lflag &^= unix.PENDIN
	return *tm
}

func open(t *testing.T) (d *Dev, master int, slavePath string) {
	t.Helper()
	m, slave := openPty(t)
	d, err := OpenPath(slave)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d, int(m.Fd()), slave
}

func recv(t *testing.T, ch <-chan byte) byte {
	t.Helper()
	select {
	case b, ok := <-ch:
		if !ok {
			t.Fatal("key channel closed")
		}
		return b
	case <-time.After(3 * time.Second):
		t.Fatal("no key arrived")
	}
	return 0
}

func TestCbreakAndRestore(t *testing.T) {
	d, _, _ := open(t)
	before := termios(t, d.fd)
	if before.Lflag&unix.ICANON == 0 || before.Lflag&unix.ECHO == 0 {
		t.Fatal("a new pty should start in canonical mode with echo")
	}
	if err := d.Cbreak(); err != nil {
		t.Fatal(err)
	}
	in := termios(t, d.fd)
	if in.Lflag&unix.ICANON != 0 || in.Lflag&unix.ECHO != 0 {
		t.Error("cbreak must turn off canonical mode and echo")
	}
	// Signals and output processing are kept: Ctrl-C still interrupts and "\n" still works.
	if in.Lflag&unix.ISIG == 0 {
		t.Error("ISIG must stay on")
	}
	if in.Oflag&unix.OPOST == 0 || in.Oflag&unix.ONLCR == 0 {
		t.Error("OPOST/ONLCR must stay on")
	}
	if err := d.Cbreak(); err != nil { // a second call must not overwrite the saved mode
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // Restore is idempotent
		if err := d.Restore(); err != nil {
			t.Fatal(err)
		}
	}
	if after := termios(t, d.fd); after != before {
		t.Errorf("mode not restored:\n before %+v\n after  %+v", before, after)
	}
	// Restore without Cbreak does nothing.
	if err := d.Restore(); err != nil {
		t.Error(err)
	}
}

func TestKeysAndPromptStop(t *testing.T) {
	d, master, _ := open(t)
	if err := d.Cbreak(); err != nil {
		t.Fatal(err)
	}
	keys := d.StartKeys()
	unix.Write(master, []byte("/"))
	if k := recv(t, keys); k != '/' {
		t.Errorf("got %q", k)
	}
	unix.Write(master, []byte("ab"))
	if recv(t, keys) != 'a' || recv(t, keys) != 'b' {
		t.Error("keys out of order")
	}

	// The reader is blocked waiting. Stopping must be immediate.
	t0 := time.Now()
	d.StopKeys()
	if el := time.Since(t0); el > time.Second {
		t.Errorf("StopKeys took %v", el)
	}
	if _, ok := <-keys; ok {
		t.Error("key channel should be closed after StopKeys")
	}
	d.StopKeys() // a second stop is harmless

	// After the stop nothing reads the terminal: a key typed now stays queued
	// for whoever takes over (fzf).
	unix.Write(master, []byte("z"))
	time.Sleep(100 * time.Millisecond)
	var rs unix.FdSet
	rs.Set(d.fd)
	n, err := unix.Select(d.fd+1, &rs, nil, nil, &unix.Timeval{Sec: 1})
	if err != nil || n != 1 {
		t.Fatalf("typed key was consumed after StopKeys: n=%d err=%v", n, err)
	}
	var b [1]byte
	if n, _ := unix.Read(d.fd, b[:]); n != 1 || b[0] != 'z' {
		t.Errorf("queued key is %q", b[:n])
	}

	// Reading can start again.
	keys = d.StartKeys()
	unix.Write(master, []byte("q"))
	if k := recv(t, keys); k != 'q' {
		t.Errorf("got %q", k)
	}
}

func TestCloseRestoresTheMode(t *testing.T) {
	d, _, slave := open(t)
	// A second handle on the same terminal, to look at the mode after d is closed.
	other, err := OpenPath(slave)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	before := termios(t, other.fd)

	d.Cbreak()
	d.StartKeys()
	if termios(t, other.fd).Lflag&unix.ICANON != 0 {
		t.Fatal("cbreak did not take effect")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if after := termios(t, other.fd); after != before {
		t.Errorf("Close left the terminal in the wrong mode:\n before %+v\n after  %+v", before, after)
	}
	if err := d.Close(); err != nil { // closing twice is harmless
		t.Error(err)
	}
	if k, ok := <-d.StartKeys(); ok {
		t.Errorf("StartKeys after Close returned a live channel (%q)", k)
	}
}

func TestSize(t *testing.T) {
	d, master, _ := open(t)
	if r, c := d.Size(); r != 24 || c != 80 {
		t.Errorf("a pty with no size set: %dx%d, want the 24x80 default", r, c)
	}
	if err := unix.IoctlSetWinsize(master, unix.TIOCSWINSZ, &unix.Winsize{Row: 51, Col: 92}); err != nil {
		t.Fatal(err)
	}
	if r, c := d.Size(); r != 51 || c != 92 {
		t.Errorf("size %dx%d, want 51x92", r, c)
	}
}

func TestQuery(t *testing.T) {
	d, master, _ := open(t)
	if err := d.Cbreak(); err != nil {
		t.Fatal(err)
	}
	// The terminal answers after it has seen the question.
	go func() {
		var buf [64]byte
		n, _ := unix.Read(master, buf[:])
		if string(buf[:n]) == "ask" {
			unix.Write(master, []byte("\x1b]11;rgb:ffff/ffff/ffff\x1b\\"))
		}
	}()
	reply, ok := d.Query("ask", 3*time.Second)
	if !ok || reply != "\x1b]11;rgb:ffff/ffff/ffff\x1b\\" {
		t.Errorf("got %q %v", reply, ok)
	}

	// A reply that ends with BEL, split over two writes.
	go func() {
		var buf [64]byte
		unix.Read(master, buf[:])
		unix.Write(master, []byte("\x1b]11;rgb:00"))
		time.Sleep(20 * time.Millisecond)
		unix.Write(master, []byte("00/0000/0000\x07"))
	}()
	if reply, ok := d.Query("ask", 3*time.Second); !ok || reply != "\x1b]11;rgb:0000/0000/0000\x07" {
		t.Errorf("got %q %v", reply, ok)
	}

	// A terminal that says nothing is given up on at the timeout.
	t0 := time.Now()
	if reply, ok := d.Query("ask", 100*time.Millisecond); ok || reply != "" {
		t.Errorf("got %q %v", reply, ok)
	}
	if el := time.Since(t0); el < 90*time.Millisecond || el > 2*time.Second {
		t.Errorf("waited %v", el)
	}
}
