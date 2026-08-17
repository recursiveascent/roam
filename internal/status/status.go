// Package status prints roam's brief state-change lines to stderr.
package status

import (
	"fmt"
	"io"
)

// Printer writes one line per state change. It stays silent when the writer
// is not a terminal or when quiet is set, so non-interactive use produces no
// incidental noise.
type Printer struct {
	w       io.Writer
	enabled bool
	verbose bool
}

func New(w io.Writer, isTTY, quiet, verbose bool) *Printer {
	return &Printer{w: w, enabled: isTTY && !quiet, verbose: verbose}
}

func (p *Printer) LinkDown()     { p.line("link down — waiting for network") }
func (p *Printer) Reconnecting() { p.line("reconnecting…") }

// Reconnected prints only under verbose: after a reattach the session
// repaint itself is the success signal.
func (p *Printer) Reconnected() {
	if p.verbose {
		p.line("reconnected")
	}
}

func (p *Printer) PersistentFailures() {
	p.line("persistent failures — check args; ^C to quit")
}

func (p *Printer) line(text string) {
	if !p.enabled {
		return
	}
	fmt.Fprintf(p.w, "[roam: %s]\n", text)
}
