//go:build !(darwin || dragonfly || freebsd || netbsd || openbsd || linux || windows)

package tty

// Open is not available here, so mgnt is then a plain filter.
func Open() (Terminal, error) { return nil, ErrUnsupported }
