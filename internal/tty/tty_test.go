package tty

import (
	"os"
	"testing"
)

func TestSaveOnNonTerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	s := Save(r)
	if s.ok {
		t.Fatal("Save on a pipe must not report a terminal")
	}
	s.Restore()
}
