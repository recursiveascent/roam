package supervisor

import (
	"os"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

// scriptRunner runs a real /bin/sh per start() call, one script per
// successive child. It is a test double at the runner seam, but every child
// it runs is a real process.
type scriptRunner struct {
	mu      sync.Mutex
	scripts []string
	i       int
	cmd     *procRunner
}

func (r *scriptRunner) start() error {
	r.mu.Lock()
	script := "exit 0"
	if r.i < len(r.scripts) {
		script = r.scripts[r.i]
	}
	r.i++
	r.cmd = &procRunner{sshPath: "/bin/sh", args: []string{"-c", script}}
	r.mu.Unlock()
	return r.cmd.start()
}

func (r *scriptRunner) wait() childExited { return r.cmd.wait() }
func (r *scriptRunner) kill()             { r.cmd.kill() }

// recordingReporter records which status lines were requested.
type recordingReporter struct {
	mu    sync.Mutex
	lines []string
}

func (r *recordingReporter) add(s string) {
	r.mu.Lock()
	r.lines = append(r.lines, s)
	r.mu.Unlock()
}
func (r *recordingReporter) LinkDown()           { r.add("down") }
func (r *recordingReporter) Reconnecting()       { r.add("reconnecting") }
func (r *recordingReporter) Reconnected()        { r.add("reconnected") }
func (r *recordingReporter) PersistentFailures() { r.add("persistent") }

func testShellConfig() config {
	return config{
		debounce:   10 * time.Millisecond,
		settle:     5 * time.Millisecond,
		maxBackoff: 50 * time.Millisecond,
		establish:  20 * time.Millisecond,
	}
}

func TestShellPropagatesCleanExit(t *testing.T) {
	paths := make(chan PathEvent, 1)
	paths <- PathEvent{Satisfied: true, Fingerprint: "en0/1"}
	r := &scriptRunner{scripts: []string{"exit 7"}}

	code := runShell(testShellConfig(), r, Options{
		PathEvents: paths,
		Signals:    make(chan os.Signal),
	})
	if code != 7 {
		t.Fatalf("runShell = %d, want 7", code)
	}
}

func TestShellReconnectsAfter255(t *testing.T) {
	paths := make(chan PathEvent, 1)
	paths <- PathEvent{Satisfied: true, Fingerprint: "en0/1"}
	r := &scriptRunner{scripts: []string{"exit 255", "exit 3"}}
	rep := &recordingReporter{}

	code := runShell(testShellConfig(), r, Options{
		PathEvents: paths,
		Signals:    make(chan os.Signal),
		Report:     rep,
	})
	if code != 3 {
		t.Fatalf("runShell = %d, want 3", code)
	}
	rep.mu.Lock()
	defer rep.mu.Unlock()
	if len(rep.lines) == 0 || rep.lines[0] != "reconnecting" {
		t.Fatalf("reporter lines = %v, want leading \"reconnecting\"", rep.lines)
	}
}

// memRunner is an in-memory runner for synctest interleaving tests. Fake at
// the seam only; real-process behavior is covered by scriptRunner tests.
type memRunner struct {
	failStart bool
	exits     chan childExited
}

func (r *memRunner) start() error {
	if r.failStart {
		return os.ErrNotExist
	}
	return nil
}
func (r *memRunner) wait() childExited { return <-r.exits }
func (r *memRunner) kill()             {}

func TestShellSpawnFailureWithPendingSignalDoesNotRace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		paths := make(chan PathEvent, 1)
		paths <- PathEvent{Satisfied: true, Fingerprint: "en0/1"}
		sigs := make(chan os.Signal, 1)
		sigs <- syscall.SIGTERM

		r := &memRunner{failStart: true}
		code := runShell(testShellConfig(), r, Options{
			PathEvents: paths,
			Signals:    sigs,
		})
		if code != 143 {
			t.Fatalf("runShell = %d, want 143", code)
		}
	})
}

func TestShellSpawnFailureRetriesAsConnectionError(t *testing.T) {
	paths := make(chan PathEvent, 1)
	paths <- PathEvent{Satisfied: true, Fingerprint: "en0/1"}

	r := &firstFailRunner{scriptRunner: scriptRunner{scripts: []string{"exit 4"}}}
	code := runShell(testShellConfig(), r, Options{
		PathEvents: paths,
		Signals:    make(chan os.Signal),
	})
	if code != 4 {
		t.Fatalf("runShell = %d, want 4", code)
	}
}

type firstFailRunner struct {
	scriptRunner
	failed bool
}

func (r *firstFailRunner) start() error {
	if !r.failed {
		r.failed = true
		return os.ErrNotExist
	}
	return r.scriptRunner.start()
}
