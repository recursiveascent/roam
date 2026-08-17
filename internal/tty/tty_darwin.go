//go:build darwin

// Package tty snapshots and restores terminal attributes, so a forcibly
// killed ssh cannot leave the terminal in raw mode.
package tty

import (
	"os"
	"syscall"
	"unsafe"
)

// State is a snapshot of a file's terminal attributes.
type State struct {
	fd   int
	attr syscall.Termios
	ok   bool
}

// Save captures f's terminal attributes. On a non-terminal, the returned
// State's Restore does nothing.
func Save(f *os.File) State {
	s := State{fd: int(f.Fd())}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(s.fd),
		uintptr(syscall.TIOCGETA), uintptr(unsafe.Pointer(&s.attr)))
	s.ok = errno == 0
	return s
}

// Restore reapplies the saved attributes. It is idempotent and safe to call
// after every child exit.
func (s State) Restore() {
	if !s.ok {
		return
	}
	syscall.Syscall(syscall.SYS_IOCTL, uintptr(s.fd),
		uintptr(syscall.TIOCSETA), uintptr(unsafe.Pointer(&s.attr)))
}
