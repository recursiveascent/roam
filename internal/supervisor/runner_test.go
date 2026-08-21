package supervisor

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"
)

type notifyingWriter struct {
	once  sync.Once
	ready chan struct{}
}

func (w *notifyingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.ready) })
	return len(p), nil
}

func TestProcRunnerExpectedCancellationIsNotOutputError(t *testing.T) {
	stderr := &notifyingWriter{ready: make(chan struct{})}
	r := &procRunner{
		sshPath: "/bin/sh",
		args: []string{"-c",
			`trap 'exit 0' TERM; printf ready >&2; while :; do :; done`},
		stderr: stderr,
	}
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	<-stderr.ready
	done := make(chan childExited, 1)
	go func() { done <- r.wait() }()
	r.kill()
	select {
	case got := <-done:
		if got.code != 0 || got.signaled {
			t.Fatalf("wait() = %+v, want clean exit", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child did not exit after cancellation")
	}
	if err := r.finishOutput(false); err != nil {
		t.Fatalf("finishOutput error = %v, want nil", err)
	}
}

func TestProcRunnerFinishReportsWaitDelay(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "hold")
	pidPath := filepath.Join(dir, "pid")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOLD_FIFO", fifo)
	t.Setenv("HOLD_PID", pidPath)

	r := &procRunner{
		sshPath:    "/bin/sh",
		args:       []string{"-c", `cat "$HOLD_FIFO" & printf '%d' "$!" > "$HOLD_PID"`},
		stderr:     &bytes.Buffer{},
		escalation: 10 * time.Millisecond,
	}
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	if got := r.wait(); got.code != 0 {
		t.Fatalf("wait() = %+v, want exit 0", got)
	}
	pidBytes, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(pidBytes))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGTERM) })

	if err := r.finishOutput(false); !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("finishOutput error = %v, want %v", err, exec.ErrWaitDelay)
	}
}

func TestProcRunnerDiscardsBufferedDisconnect(t *testing.T) {
	var stderr bytes.Buffer
	r := &procRunner{
		sshPath: "/bin/sh",
		args: []string{"-c",
			`printf 'Read from remote host oberon: Operation timed out\r\n' >&2; exit 255`},
		stderr: &stderr,
	}
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	if got := r.wait(); got.code != 255 {
		t.Fatalf("wait() = %+v, want exit 255", got)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr before finish = %q, want empty", got)
	}
	if err := r.finishOutput(true); err != nil {
		t.Fatal(err)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr after discard = %q, want empty", got)
	}
}

func TestProcRunnerFlushesBufferedDisconnect(t *testing.T) {
	const want = "Connection to oberon closed.\r\n"
	var stderr bytes.Buffer
	r := &procRunner{
		sshPath: "/bin/sh",
		args:    []string{"-c", `printf 'Connection to oberon closed.\r\n' >&2`},
		stderr:  &stderr,
	}
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	if got := r.wait(); got.code != 0 {
		t.Fatalf("wait() = %+v, want exit 0", got)
	}
	if err := r.finishOutput(false); err != nil {
		t.Fatal(err)
	}
	if got := stderr.String(); got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestProcRunnerPipesConfiguredStderr(t *testing.T) {
	var stderr bytes.Buffer
	r := &procRunner{
		sshPath: "/bin/sh",
		args:    []string{"-c", `if test -t 2; then printf tty >&2; else printf pipe >&2; fi`},
		stderr:  &stderr,
	}
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	if got := r.wait(); got.code != 0 {
		t.Fatalf("wait() = %+v, want exit 0", got)
	}
	if err := r.finishOutput(false); err != nil {
		t.Fatal(err)
	}
	if got := stderr.String(); got != "pipe" {
		t.Fatalf("stderr = %q, want pipe", got)
	}
}

func TestProcRunnerUsesReconnectArgsAfterFirstStart(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "args")
	scriptPath := filepath.Join(t.TempDir(), "fake-ssh")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$ARGS_LOG\"\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARGS_LOG", logPath)

	r := &procRunner{
		sshPath:       scriptPath,
		args:          []string{"initial"},
		reconnectArgs: []string{"reconnect"},
	}
	for range 2 {
		if err := r.start(); err != nil {
			t.Fatal(err)
		}
		if got := r.wait(); got.code != 0 {
			t.Fatalf("wait() = %+v, want exit 0", got)
		}
	}
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := "initial\nreconnect\n"; string(got) != want {
		t.Fatalf("args log = %q, want %q", got, want)
	}
}

func TestProcRunnerReportsExitCode(t *testing.T) {
	r := &procRunner{sshPath: "/bin/sh", args: []string{"-c", "exit 7"}}
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	got := r.wait()
	want := childExited{code: 7}
	if got != want {
		t.Fatalf("wait() = %+v, want %+v", got, want)
	}
}

func TestProcRunnerReportsSignalDeath(t *testing.T) {
	r := &procRunner{sshPath: "/bin/sh", args: []string{"-c", "kill -TERM $$"}}
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	got := r.wait()
	want := childExited{signaled: true, signum: int(syscall.SIGTERM)}
	if got != want {
		t.Fatalf("wait() = %+v, want %+v", got, want)
	}
}

func TestProcRunnerKillTerminatesChild(t *testing.T) {
	r := &procRunner{sshPath: "/bin/sh", args: []string{"-c", "sleep 30"}}
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan childExited, 1)
	go func() { done <- r.wait() }()
	r.kill()
	select {
	case got := <-done:
		if !got.signaled || got.signum != int(syscall.SIGTERM) {
			t.Fatalf("wait() = %+v, want SIGTERM death", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child did not die within 5s of kill()")
	}
}

func TestProcRunnerKillEscalatesToSIGKILL(t *testing.T) {
	r := &procRunner{
		sshPath:    "/bin/sh",
		args:       []string{"-c", `trap "" TERM; while :; do :; done`},
		escalation: 100 * time.Millisecond,
	}
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	done := make(chan childExited, 1)
	go func() { done <- r.wait() }()
	r.kill()
	select {
	case got := <-done:
		if !got.signaled || got.signum != int(syscall.SIGKILL) {
			t.Fatalf("wait() = %+v, want SIGKILL death after escalation", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child survived SIGKILL escalation window")
	}
}

func TestProcRunnerStartErrorOnMissingBinary(t *testing.T) {
	r := &procRunner{sshPath: "/nonexistent/ssh"}
	if err := r.start(); err == nil {
		t.Fatal("start() = nil error, want failure")
	}
}

func TestProcRunnerDefensiveWithoutChild(t *testing.T) {
	r := &procRunner{sshPath: "/bin/sh"}
	r.kill()
	got := r.wait()
	if got.code != 255 {
		t.Fatalf("wait() without child = %+v, want code 255", got)
	}
}
