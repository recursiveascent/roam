// Package status prints roam's brief state-change lines to stderr.
package status

import (
	"fmt"
	"io"
	"os"
	"sync"
)

// ANSI SGR codes for the transient status color. Respected only when the
// writer is a terminal, quiet is not set, and NO_COLOR is unset.
// See https://no-color.org
const (
	ansiDim   = "\x1b[2m"
	ansiReset = "\x1b[0m"
)

// Printer renders transient status in place while no ssh child owns the
// terminal. Child stderr and verbose logs share the same lock so roam never
// clears a row after handing the terminal back to ssh.
type Printer struct {
	mu         sync.Mutex
	w          io.Writer
	enabled    bool
	verbose    bool
	color      bool
	transient  string
	child      bool
	afterChild bool
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

func (p *Printer) LinkDown()     { p.showTransient("link down — waiting for network") }
func (p *Printer) Reconnecting() { p.showTransient("reconnecting…") }
func (p *Printer) ReconnectNow() { p.durable("reconnecting…") }

func (p *Printer) Reconnected() {
	if p.verbose {
		p.durable("reconnected")
	}
}

func (p *Printer) PersistentFailures() {
	p.showTransient("persistent failures — check args; ^C to quit")
}

// PrepareChild relinquishes roam's transient row before the child can write
// directly to stdout.
func (p *Printer) PrepareChild() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.enabled && p.transient != "" {
		p.clearLocked()
	}
	p.transient = ""
	p.afterChild = false
}

func (p *Printer) ChildStarted() {
	p.mu.Lock()
	p.child = true
	p.afterChild = false
	p.mu.Unlock()
}

func (p *Printer) ChildFinished() {
	p.mu.Lock()
	p.child = false
	p.afterChild = true
	p.mu.Unlock()
}

// Write forwards child stderr without changing terminal-row ownership.
func (p *Printer) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.w.Write(b)
}

// Debugf writes a durable verbose record. If a childless transient is active,
// it is redrawn after the log entry.
func (p *Printer) Debugf(format string, args ...any) {
	if !p.verbose {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	transient := p.transient
	if transient != "" {
		p.clearLocked()
		p.transient = ""
	}
	if p.child || p.afterChild {
		_, _ = io.WriteString(p.w, "\r\n")
		p.afterChild = false
	}
	_, _ = fmt.Fprintf(p.w, "[roam: "+format+"]\r\n", args...)
	if transient != "" && !p.child {
		p.renderTransientLocked(transient)
	}
}

// showTransient writes on roam's current row. After a child exits, it first
// establishes a fresh row because direct stdout may have left the cursor
// anywhere.
func (p *Printer) showTransient(text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.enabled || p.child {
		return
	}
	if p.afterChild {
		_, _ = io.WriteString(p.w, "\r\n")
		p.afterChild = false
	}
	p.renderTransientLocked(text)
}

func (p *Printer) renderTransientLocked(text string) {
	_, _ = fmt.Fprintf(p.w, "\r\x1b[K%s[roam: %s]%s", p.dim(), text, p.reset())
	p.transient = text
}

func (p *Printer) durable(text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.enabled {
		return
	}
	if p.transient != "" {
		p.clearLocked()
	}
	_, _ = fmt.Fprintf(p.w, "\r\n\r\x1b[K%s[roam: %s]%s\r\n", p.dim(), text, p.reset())
	p.transient = ""
	p.afterChild = false
}

func (p *Printer) clearLocked() {
	_, _ = io.WriteString(p.w, "\r\x1b[K")
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
