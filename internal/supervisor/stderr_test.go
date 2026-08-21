package supervisor

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

var disconnectRecords = []string{
	"Read from remote host oberon: Operation timed out\r\n",
	"Read from remote host oberon: Connection reset by peer\n",
	"Connection to oberon closed by remote host.\r\n",
	"Connection to oberon closed.\n",
	"Write failed: Broken pipe\r\n",
	"client_loop: send disconnect: Broken pipe\n",
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestSSHStderrFinishReportsPriorWriteErrorWhenDiscarding(t *testing.T) {
	wantErr := errors.New("write failed")
	w := newSSHStderr(failingWriter{err: wantErr})
	input := disconnectRecords[0] + "ordinary stderr\n"
	if _, err := w.Write([]byte(input)); !errors.Is(err, wantErr) {
		t.Fatalf("Write error = %v, want %v", err, wantErr)
	}
	if err := w.finish(true); !errors.Is(err, wantErr) {
		t.Fatalf("finish error = %v, want %v", err, wantErr)
	}
}

func TestSSHStderrDiscardsFinalDisconnectRecord(t *testing.T) {
	for _, record := range disconnectRecords {
		t.Run(strings.TrimSpace(record), func(t *testing.T) {
			var dst bytes.Buffer
			w := newSSHStderr(&dst)
			for _, fragment := range []string{record[:1], record[1 : len(record)-1], record[len(record)-1:]} {
				if _, err := w.Write([]byte(fragment)); err != nil {
					t.Fatal(err)
				}
			}
			if got := dst.String(); got != "" {
				t.Fatalf("output before finish = %q, want empty", got)
			}
			if err := w.finish(true); err != nil {
				t.Fatal(err)
			}
			if got := dst.String(); got != "" {
				t.Fatalf("output after discard = %q, want empty", got)
			}
		})
	}
}

func TestSSHStderrFlushesFinalDisconnectRecord(t *testing.T) {
	for _, record := range disconnectRecords {
		t.Run(strings.TrimSpace(record), func(t *testing.T) {
			var dst bytes.Buffer
			w := newSSHStderr(&dst)
			if _, err := w.Write([]byte(record)); err != nil {
				t.Fatal(err)
			}
			if err := w.finish(false); err != nil {
				t.Fatal(err)
			}
			if got := dst.String(); got != record {
				t.Fatalf("output = %q, want %q", got, record)
			}
		})
	}
}

func TestSSHStderrFlushesMatchBeforeSubsequentData(t *testing.T) {
	const record = "Write failed: Broken pipe\r\n"
	var dst bytes.Buffer
	w := newSSHStderr(&dst)
	if _, err := w.Write([]byte(record + "remote stderr\n")); err != nil {
		t.Fatal(err)
	}
	if got := dst.String(); got != record+"remote stderr\n" {
		t.Fatalf("output = %q, want both records", got)
	}
	if err := w.finish(true); err != nil {
		t.Fatal(err)
	}
}

func TestSSHStderrPassesOrdinaryAndIncompleteData(t *testing.T) {
	const input = "ordinary stderr\nRead from remote host oberon: partial"
	var dst bytes.Buffer
	w := newSSHStderr(&dst)
	if _, err := w.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	if got := dst.String(); got != "ordinary stderr\n" {
		t.Fatalf("output before finish = %q, want completed ordinary line", got)
	}
	if err := w.finish(true); err != nil {
		t.Fatal(err)
	}
	if got := dst.String(); got != input {
		t.Fatalf("output = %q, want %q", got, input)
	}
}

func TestSSHStderrPassesOverlongCandidate(t *testing.T) {
	input := "Read from remote host " + strings.Repeat("x", maxDisconnectLine) + "\n"
	var dst bytes.Buffer
	w := newSSHStderr(&dst)
	if _, err := w.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	if err := w.finish(true); err != nil {
		t.Fatal(err)
	}
	if got := dst.String(); got != input {
		t.Fatalf("output length = %d, want %d", len(got), len(input))
	}
}

func TestSSHStderrRetainsOnlyLastMatchingRecord(t *testing.T) {
	first := disconnectRecords[0]
	last := disconnectRecords[1]
	var dst bytes.Buffer
	w := newSSHStderr(&dst)
	if _, err := w.Write([]byte(first + last)); err != nil {
		t.Fatal(err)
	}
	if got := dst.String(); got != first {
		t.Fatalf("output before finish = %q, want first record", got)
	}
	if err := w.finish(true); err != nil {
		t.Fatal(err)
	}
	if got := dst.String(); got != first {
		t.Fatalf("output after finish = %q, want first record", got)
	}
}
