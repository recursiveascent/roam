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

// IsTerminal reports whether f has terminal attributes.
func IsTerminal(f *os.File) bool {
	_, ok := terminalAttrs(f)
	return ok
}

// Save captures f's terminal attributes. On a non-terminal, the returned
// State's Restore does nothing.
func Save(f *os.File) State {
	attr, ok := terminalAttrs(f)
	return State{fd: int(f.Fd()), attr: attr, ok: ok}
}

func terminalAttrs(f *os.File) (syscall.Termios, bool) {
	var attr syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(),
		uintptr(syscall.TIOCGETA), uintptr(unsafe.Pointer(&attr)))
	return attr, errno == 0
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
