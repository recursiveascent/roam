package supervisor

import (
	"os"
	"syscall"
	"time"

	"github.com/recursiveascent/roam/internal/tty"
)

// PathEvent reports the network path state. Fingerprint identifies the
// default path (ordered interface list); a change means migration.
type PathEvent struct {
	Satisfied   bool
	Fingerprint string
}

// Reporter receives status-line requests. Implementations decide how (and
// whether) to render them.
type Reporter interface {
	LinkDown()
	Reconnecting()
	Reconnected()
	PersistentFailures()
}

// Options configures Run.
type Options struct {
	SSHPath    string
	SSHArgs    []string
	Debounce   time.Duration // 0 = default 2s
	MaxBackoff time.Duration // 0 = default 30s
	Report     Reporter      // nil = no status output
	PathEvents <-chan PathEvent
	Signals    <-chan os.Signal
	Debugf     func(format string, args ...any) // nil = no debug log
}

// Run supervises ssh until the session ends and returns the exit code.
func Run(o Options) int {
	cfg := defaultConfig()
	if o.Debounce > 0 {
		cfg.debounce = o.Debounce
	}
	if o.MaxBackoff > 0 {
		cfg.maxBackoff = o.MaxBackoff
	}
	r := &procRunner{
		sshPath: o.SSHPath,
		args:    o.SSHArgs,
		term:    tty.Save(os.Stdin),
	}
	return runShell(cfg, r, o)
}

// runShell is the imperative shell: it funnels every input into one event
// channel, calls decide, and executes the returned commands. It holds no
// policy beyond command dispatch. The forwarder goroutines end when their
// input channels close; runShell's return is immediately followed by
// os.Exit in the command.
func runShell(cfg config, r runner, o Options) int {
	events := make(chan event, 16)
	done := make(chan struct{})
	defer close(done)

	go func() {
		for {
			select {
			case p, ok := <-o.PathEvents:
				if !ok {
					return
				}
				select {
				case events <- pathEvent{satisfied: p.Satisfied, fingerprint: p.Fingerprint}:
				case <-done:
					return
				}
			case <-done:
				return
			}
		}
	}()
	go func() {
		for {
			select {
			case sig, ok := <-o.Signals:
				if !ok {
					return
				}
				var ev event
				switch sig {
				case syscall.SIGINT:
					ev = gotSignal{kind: sigInterrupt}
				case syscall.SIGTERM:
					ev = gotSignal{kind: sigTerminate}
				case syscall.SIGUSR1:
					ev = gotSignal{kind: sigReconnect}
				default:
					continue
				}
				select {
				case events <- ev:
				case <-done:
					return
				}
			case <-done:
				return
			}
		}
	}()

	debugf := o.Debugf
	if debugf == nil {
		debugf = func(string, ...any) {}
	}

	s := initialState(cfg)
	// queue holds synchronously generated events (a failed spawn); they are
	// processed before any channel event, so nothing can observe a running
	// state whose child does not exist.
	var queue []event
	for {
		var ev event
		if len(queue) > 0 {
			ev, queue = queue[0], queue[1:]
		} else {
			ev = <-events
		}
		var cmds []command
		s, cmds = decide(s, ev)
		debugf("event %#v -> state %d, %d command(s)", ev, s.kind, len(cmds))
		for _, c := range cmds {
			switch c := c.(type) {
			case spawnChild:
				if err := r.start(); err != nil {
					debugf("spawn failed: %v", err)
					queue = append(queue, childExited{code: 255})
					continue
				}
				go func() {
					childEvent := r.wait()
					select {
					case events <- childEvent:
					case <-done:
					}
				}()
			case killChild:
				r.kill()
			case startTimer:
				id := c.id
				time.AfterFunc(c.d, func() {
					select {
					case events <- timerFired{id: id}:
					case <-done:
					}
				})
			case report:
				doReport(o.Report, c.s)
			case exitProgram:
				return c.code
			}
		}
	}
}

func doReport(rep Reporter, s statusKind) {
	if rep == nil {
		return
	}
	switch s {
	case statusLinkDown:
		rep.LinkDown()
	case statusReconnecting:
		rep.Reconnecting()
	case statusReconnected:
		rep.Reconnected()
	case statusPersistentFailures:
		rep.PersistentFailures()
	}
}
