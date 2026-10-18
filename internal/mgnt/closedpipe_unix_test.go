//go:build !windows

package mgnt

import "syscall"

// errClosedPipe is what a write to a pipe whose reader has gone returns.
var errClosedPipe error = syscall.EPIPE
