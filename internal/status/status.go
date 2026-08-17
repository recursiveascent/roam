// Package status prints roam's brief state-change lines to stderr.
package status

import (
	"fmt"
	"io"
	"os"
)

// ANSI SGR codes for the transient status color. Respected only when the
// writer is a terminal, quiet is not set, and NO_COLOR is unset.
// See https://no-color.org
const (
	ansiDim   = "\x1b[2m"
	ansiReset = "\x1b[0m"
)

// Printer renders transient status in place: each transient line overwrites
// the previous one on the same row, and a successful reconnect clears it
// entirely. It stays silent when the writer is not a terminal or when quiet
// is set, so non-interactive use produces no incidental noise.
type Printer struct {
	w       io.Writer
	enabled bool
	verbose bool
	color   bool
}

// New constructs a Printer. color is gated by NO_COLOR here, so callers can
// pass isTTY unmodified.
func New(w io.Writer, isTTY, quiet, verbose bool) *Printer {
	return &Printer{
		w:       w,
		enabled: isTTY && !quiet,
		verbose: verbose,
		color:   isTTY && !quiet && os.Getenv("NO_COLOR") == "",
	}
}

func (p *Printer) LinkDown()     { p.transient("link down — waiting for network") }
func (p *Printer) Reconnecting() { p.transient("reconnecting…") }

func (p *Printer) Reconnected() {
	p.clear()
	if p.verbose {
		p.transient("reconnected")
	}
}

func (p *Printer) PersistentFailures() {
	p.transient("persistent failures — check args; ^C to quit")
}

// transient writes "[roam: <text>]" on the current row, overwriting whatever
// was there. \r returns to column 0 and \x1b[K erases to end of line so a
// shorter message leaves no stale glyphs. No trailing newline: the row
// stays open and is reclaimed by the next transient or by clear().
func (p *Printer) transient(text string) {
	if !p.enabled {
		return
	}
	fmt.Fprintf(p.w, "\r\x1b[K%s[roam: %s]%s", p.dim(), text, p.reset())
}

// clear erases the current transient row. It runs unconditionally (when
// enabled) on a successful reconnect so the restored session repaints a
// blank row rather than a stale status message.
func (p *Printer) clear() {
	if !p.enabled {
		return
	}
	io.WriteString(p.w, "\r\x1b[K")
}

func (p *Printer) dim() string {
	if p.color {
		return ansiDim
	}
	return ""
}

func (p *Printer) reset() string {
	if p.color {
		return ansiReset
	}
	return ""
}
