package supervisor

import (
	"bytes"
	"io"
)

const maxDisconnectLine = 512

var disconnectPrefixes = [][]byte{
	[]byte("Read from remote host "),
	[]byte("Connection to "),
	[]byte("Write failed: "),
	[]byte("client_loop: send disconnect: "),
}

// sshStderr retains one final OpenSSH disconnect record so the supervisor can
// either flush it or replace it with roam status after classifying the exit.
type sshStderr struct {
	dst         io.Writer
	line        []byte
	pending     []byte
	passthrough bool
	writeErr    error
}

func newSSHStderr(dst io.Writer) *sshStderr {
	return &sshStderr{dst: dst}
}

func (w *sshStderr) Write(p []byte) (consumed int, err error) {
	defer func() {
		if err != nil && w.writeErr == nil {
			w.writeErr = err
		}
	}()
	for len(p) > 0 {
		if len(w.pending) > 0 {
			if err := writeAll(w.dst, w.pending); err != nil {
				return consumed, err
			}
			w.pending = nil
		}
		if w.passthrough {
			n := len(p)
			if i := bytes.IndexByte(p, '\n'); i >= 0 {
				n = i + 1
			}
			written, err := w.dst.Write(p[:n])
			consumed += written
			if err != nil {
				return consumed, err
			}
			if written != n {
				return consumed, io.ErrShortWrite
			}
			w.passthrough = p[n-1] != '\n'
			p = p[n:]
			continue
		}

		b := p[0]
		p = p[1:]
		w.line = append(w.line, b)
		consumed++
		if len(w.line) > maxDisconnectLine || !couldBeDisconnect(w.line) {
			if err := writeAll(w.dst, w.line); err != nil {
				return consumed, err
			}
			w.passthrough = b != '\n'
			w.line = nil
			continue
		}
		if b != '\n' {
			continue
		}
		if isDisconnectRecord(w.line) {
			w.pending = bytes.Clone(w.line)
		} else if err := writeAll(w.dst, w.line); err != nil {
			return consumed, err
		}
		w.line = nil
	}
	return consumed, nil
}

func (w *sshStderr) finish(discard bool) error {
	defer func() {
		w.line = nil
		w.pending = nil
		w.passthrough = false
		w.writeErr = nil
	}()
	if w.writeErr != nil {
		return w.writeErr
	}
	if len(w.line) > 0 {
		if err := writeAll(w.dst, w.line); err != nil {
			return err
		}
	}
	if !discard && len(w.pending) > 0 {
		return writeAll(w.dst, w.pending)
	}
	return nil
}

func couldBeDisconnect(line []byte) bool {
	for _, prefix := range disconnectPrefixes {
		if bytes.HasPrefix(prefix, line) || bytes.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func isDisconnectRecord(line []byte) bool {
	text := bytes.TrimSuffix(line, []byte("\n"))
	text = bytes.TrimSuffix(text, []byte("\r"))
	switch {
	case bytes.HasPrefix(text, disconnectPrefixes[0]):
		rest := text[len(disconnectPrefixes[0]):]
		host, reason, ok := bytes.Cut(rest, []byte(": "))
		return ok && len(host) > 0 && len(reason) > 0
	case bytes.HasPrefix(text, disconnectPrefixes[1]):
		return bytes.HasSuffix(text, []byte(" closed by remote host.")) ||
			bytes.HasSuffix(text, []byte(" closed."))
	case bytes.HasPrefix(text, disconnectPrefixes[2]):
		return len(text) > len(disconnectPrefixes[2])
	case bytes.HasPrefix(text, disconnectPrefixes[3]):
		return len(text) > len(disconnectPrefixes[3])
	}
	return false
}

func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}
