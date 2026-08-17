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
			"[roam: link down — waiting for network]\n"},
		{"reconnecting", true, false, false, (*Printer).Reconnecting,
			"[roam: reconnecting…]\n"},
		{"persistent failures", true, false, false, (*Printer).PersistentFailures,
			"[roam: persistent failures — check args; ^C to quit]\n"},
		{"reconnected needs verbose", true, false, false, (*Printer).Reconnected, ""},
		{"reconnected verbose", true, false, true, (*Printer).Reconnected,
			"[roam: reconnected]\n"},
		{"quiet suppresses", true, true, false, (*Printer).LinkDown, ""},
		{"non-tty suppresses", false, false, false, (*Printer).LinkDown, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			tt.emit(New(&buf, tt.isTTY, tt.quiet, tt.verbose))
			if got := buf.String(); got != tt.want {
				t.Fatalf("output = %q, want %q", got, tt.want)
			}
		})
	}
}
