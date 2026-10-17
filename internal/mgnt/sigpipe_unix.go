//go:build !windows

package mgnt

import (
	"errors"
	"os"
	"os/signal"
	"syscall"
)

// ignoreSIGPIPE makes writes to a closed pipe fail with EPIPE instead of
// killing the process, so mgnt can exit quietly like a well-behaved filter.
func ignoreSIGPIPE() { signal.Ignore(syscall.SIGPIPE) }

func isEPIPE(err error) bool { return errors.Is(err, syscall.EPIPE) }

// enableVT is only needed on Windows.
func enableVT(any) {}

// notifySignals delivers SIGINT and SIGTERM, which end mgnt, and SIGWINCH,
// which says the terminal was resized.
func notifySignals() (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM, syscall.SIGWINCH)
	return ch, func() { signal.Stop(ch) }
}

func isResize(sig os.Signal) bool { return sig == syscall.SIGWINCH }

func isTerminate(sig os.Signal) bool { return sig == syscall.SIGTERM }
