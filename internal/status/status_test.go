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
		{"reconnected clears silently", true, false, false, (*Printer).Reconnected,
			"\r\x1b[K"},
		{"reconnected verbose", true, false, true, (*Printer).Reconnected,
			"\r\x1b[K\r\x1b[K\x1b[2m[roam: reconnected]\x1b[0m"},
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
	const want = "\r\x1b[K\r\x1b[K[roam: reconnected]"
	if got := buf.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
