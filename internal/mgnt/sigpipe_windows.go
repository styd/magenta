//go:build windows

package mgnt

import (
	"errors"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/windows"
)

func ignoreSIGPIPE() {}

func isEPIPE(err error) bool {
	return errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, windows.ERROR_NO_DATA)
}

// enableVT turns on ANSI escape processing for a console.
func enableVT(f any) {
	file, ok := f.(*os.File)
	if !ok {
		return
	}
	h := windows.Handle(file.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) == nil {
		_ = windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	}
}

func notifySignals() (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	return ch, func() { signal.Stop(ch) }
}

func isResize(os.Signal) bool { return false }

func isTerminate(sig os.Signal) bool { return sig == syscall.SIGTERM }
