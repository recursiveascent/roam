package supervisor

import (
	"context"
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
	kill()
}

// procRunner runs the real ssh binary with the session's arguments. The
// child inherits the terminal directly: no pipes, no PTY proxy.
type procRunner struct {
	sshPath    string
	args       []string
	term       tty.State
	escalation time.Duration // 0 = killEscalation
	cmd        *exec.Cmd
	cancel     context.CancelFunc
}

func (r *procRunner) start() error {
	ctx, cancel := context.WithCancel(context.Background())
	c := exec.CommandContext(ctx, r.sshPath, r.args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
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
	_ = r.cmd.Wait()
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

func (r *procRunner) kill() {
	if r.cancel != nil {
		r.cancel()
	}
}
