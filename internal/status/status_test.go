package status

import (
	"bytes"
	"testing"
)

func TestLinesAndGating(t *testing.T) {
	tests := []struct {
		name    string
		isTTY   bool
		quiet   bool
		verbose bool
		emit    func(*Printer)
		want    string
	}{
		{"link down", true, false, false, (*Printer).LinkDown,
			"\r\x1b[K\x1b[2m[roam: link down — waiting for network]\x1b[0m"},
		{"reconnecting", true, false, false, (*Printer).Reconnecting,
			"\r\x1b[K\x1b[2m[roam: reconnecting…]\x1b[0m"},
		{"persistent failures", true, false, false, (*Printer).PersistentFailures,
			"\r\x1b[K\x1b[2m[roam: persistent failures — check args; ^C to quit]\x1b[0m"},
		{"reconnected clears silently", true, false, false, (*Printer).Reconnected, ""},
		{"reconnected verbose", true, false, true, (*Printer).Reconnected,
			"\r\n\r\x1b[K\x1b[2m[roam: reconnected]\x1b[0m\r\n"},
		{"quiet suppresses", true, true, false, (*Printer).LinkDown, ""},
		{"non-tty suppresses", false, false, false, (*Printer).LinkDown, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", "")
			var buf bytes.Buffer
			tt.emit(New(&buf, tt.isTTY, tt.quiet, tt.verbose))
			if got := buf.String(); got != tt.want {
				t.Fatalf("output = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNoColorStripsANSI(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	New(&buf, true, false, false).Reconnecting()
	const want = "\r\x1b[K[roam: reconnecting…]"
	if got := buf.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestNoColorAffectsReconnectedVerbose(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	New(&buf, true, false, true).Reconnected()
	const want = "\r\n\r\x1b[K[roam: reconnected]\r\n"
	if got := buf.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestPrepareChildClearsTransientBeforeChildOutput(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	p := New(&buf, true, false, false)
	p.Reconnecting()
	p.PrepareChild()
	p.ChildStarted()
	if _, err := p.Write([]byte("Last login\r\n")); err != nil {
		t.Fatal(err)
	}

	const want = "\r\x1b[K[roam: reconnecting…]\r\x1b[KLast login\r\n"
	if got := buf.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestReconnectNowIsDurable(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	p := New(&buf, true, false, false)
	p.ChildStarted()
	p.ChildFinished()
	p.ReconnectNow()

	const want = "\r\n\r\x1b[K[roam: reconnecting…]\r\n"
	if got := buf.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestChildFinishedStartsFreshTransientRow(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	p := New(&buf, true, false, false)
	p.ChildStarted()
	p.ChildFinished()
	p.LinkDown()

	const want = "\r\n\r\x1b[K[roam: link down — waiting for network]"
	if got := buf.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestDebugfRedrawsChildlessTransient(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	p := New(&buf, true, false, true)
	p.Reconnecting()
	p.Debugf("event %d", 1)

	const want = "\r\x1b[K[roam: reconnecting…]\r\x1b[K[roam: event 1]\r\n\r\x1b[K[roam: reconnecting…]"
	if got := buf.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestDebugfWhileChildOwnedIsDurable(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	p := New(&buf, true, false, true)
	p.ChildStarted()
	p.Debugf("event")

	const want = "\r\n[roam: event]\r\n"
	if got := buf.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestWritePreservesChildStderr(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf, true, false, false)
	p.ChildStarted()
	const input = "remote error without newline"
	if _, err := p.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != input {
		t.Fatalf("output = %q, want %q", got, input)
	}
}
