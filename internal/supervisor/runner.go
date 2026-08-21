package supervisor

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/recursiveascent/roam/internal/tty"
)

// killEscalation is how long a child gets to honor SIGTERM before SIGKILL.
const killEscalation = 2 * time.Second

// runner abstracts child-process control for the shell.
type runner interface {
	start() error
	wait() childExited
	finishOutput(discardDisconnect bool) error
	kill()
}

// procRunner runs the real ssh binary with the session's arguments. The
// child inherits the terminal directly: no pipes, no PTY proxy.
type procRunner struct {
	sshPath       string
	args          []string
	reconnectArgs []string
	stderr        io.Writer
	filter        *sshStderr
	waitErr       error
	term          tty.State
	escalation    time.Duration // 0 = killEscalation
	started       bool
	cmd           *exec.Cmd
	cancel        context.CancelFunc
}

func (r *procRunner) start() error {
	args := r.args
	if r.started && r.reconnectArgs != nil {
		args = r.reconnectArgs
	}
	r.started = true
	ctx, cancel := context.WithCancel(context.Background())
	c := exec.CommandContext(ctx, r.sshPath, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	if r.stderr == nil {
		c.Stderr = os.Stderr
	} else {
		r.filter = newSSHStderr(r.stderr)
		c.Stderr = r.filter
	}
	// On cancel, send SIGTERM. If the child remains alive after WaitDelay,
	// exec.Cmd sends SIGKILL.
	c.Cancel = func() error { return c.Process.Signal(syscall.SIGTERM) }
	c.WaitDelay = r.escalation
	if c.WaitDelay == 0 {
		c.WaitDelay = killEscalation
	}
	if err := c.Start(); err != nil {
		cancel()
		return err
	}
	r.cmd, r.cancel = c, cancel
	return nil
}

func (r *procRunner) wait() childExited {
	if r.cmd == nil {
		// Defensive: no child was started; report a connection error.
		return childExited{code: 255}
	}
	err := r.cmd.Wait()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) && !errors.Is(err, context.Canceled) {
		r.waitErr = err
	}
	if r.cancel != nil {
		r.cancel()
	}
	// A SIGKILLed ssh cannot restore the terminal itself.
	r.term.Restore()
	ps := r.cmd.ProcessState
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return childExited{signaled: true, signum: int(ws.Signal())}
	}
	return childExited{code: ps.ExitCode()}
}

func (r *procRunner) finishOutput(discardDisconnect bool) error {
	var outputErr error
	if r.filter != nil {
		outputErr = r.filter.finish(discardDisconnect)
	}
	err := errors.Join(outputErr, r.waitErr)
	r.filter = nil
	r.waitErr = nil
	return err
}

func (r *procRunner) kill() {
	if r.cancel != nil {
		r.cancel()
	}
}
