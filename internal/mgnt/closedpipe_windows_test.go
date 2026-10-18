//go:build windows

package mgnt

import "golang.org/x/sys/windows"

// errClosedPipe is what a write to a pipe whose reader has gone returns.
var errClosedPipe error = windows.ERROR_BROKEN_PIPE
