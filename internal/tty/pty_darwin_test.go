package tty

import (
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// openPty returns the master side of a new pseudo-terminal and the path of
// its slave side.
func openPty(t *testing.T) (*os.File, string) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no pseudo-terminals here: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	fd := m.Fd()
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, fd, unix.TIOCPTYGRANT, 0); e != 0 {
		t.Fatal(e)
	}
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, fd, unix.TIOCPTYUNLK, 0); e != 0 {
		t.Fatal(e)
	}
	var name [128]byte
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, fd, unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); e != 0 {
		t.Fatal(e)
	}
	n := 0
	for n < len(name) && name[n] != 0 {
		n++
	}
	return m, string(name[:n])
}
