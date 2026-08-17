// Package supervisor drives ssh reconnection: a pure decision core plus a
// thin shell that executes its commands.
package supervisor

import "time"

type config struct {
	debounce   time.Duration // damping for path flapping
	settle     time.Duration // wait after path-up before spawning
	maxBackoff time.Duration // cap on retry delay while the path is up
	establish  time.Duration // child lifetime that counts as a working connection
}

func defaultConfig() config {
	return config{
		debounce:   2 * time.Second,
		settle:     500 * time.Millisecond,
		maxBackoff: 30 * time.Second,
		establish:  5 * time.Second,
	}
}

// Events are inputs to decide. The shell converts everything that happens
// (path reports, child exits, timer expiries, signals) into one of these.
type event interface{ isEvent() }

type pathEvent struct {
	satisfied   bool
	fingerprint string
}

type childExited struct {
	code     int
	signaled bool
	signum   int
}

type timerFired struct{ id int }

type gotSignal struct{ kind sigKind }

type sigKind int

const (
	sigInterrupt sigKind = iota // SIGINT
	sigTerminate                // SIGTERM
	sigReconnect                // SIGUSR1
)

func (pathEvent) isEvent()   {}
func (childExited) isEvent() {}
func (timerFired) isEvent()  {}
func (gotSignal) isEvent()   {}

// Commands are the side effects decide asks the shell to perform.
type command interface{ isCommand() }

type spawnChild struct{}
type killChild struct{}
type startTimer struct {
	id int
	d  time.Duration
}
type report struct{ s statusKind }
type exitProgram struct{ code int }

func (spawnChild) isCommand()  {}
func (killChild) isCommand()   {}
func (startTimer) isCommand()  {}
func (report) isCommand()      {}
func (exitProgram) isCommand() {}

type statusKind int

const (
	statusLinkDown statusKind = iota
	statusReconnecting
	statusReconnected
	statusPersistentFailures
)

type stateKind int

const (
	stateRunning stateKind = iota // ssh child alive
	stateWaiting                  // no child, path down
	stateBackoff                  // no child, path up, recent failure
	stateDone                     // terminal
)

// pending names the transition a timer is counting down toward.
type pendingKind int

const (
	pendingNone   pendingKind = iota
	pendingDown               // debounce before treating the path as down
	pendingChange             // debounce before acting on a fingerprint change
	pendingSettle             // settle before spawning after path-up
	pendingRetry              // backoff delay before the next attempt
)

type state struct {
	cfg  config
	kind stateKind

	pathUp      bool
	fingerprint string // most recent reported path fingerprint
	childFP     string // fingerprint when the child was spawned
	started     bool   // a first spawn has happened

	nextTimer int // timer id allocator
	estTimer  int // established-timer id; 0 = none pending
	pendTimer int // pending-transition timer id; 0 = none pending
	pending   pendingKind

	killSent   bool // we sent killChild; the next exit is ours
	quitOnExit bool // exit the program once the child dies
	quitCode   int

	reconnectRun  bool          // current child is a reconnect, not the first spawn
	backoffDelay  time.Duration // 0 = retry immediately on failure
	rapidFailures int           // consecutive exits before establishment
}

func initialState(cfg config) state {
	return state{cfg: cfg, kind: stateWaiting}
}

func (s *state) newTimer() int {
	s.nextTimer++
	return s.nextTimer
}

func (s *state) clearPending() {
	s.pending = pendingNone
	s.pendTimer = 0
}

// spawn emits the command sequence that starts a child and arms the
// established timer. reconnect marks respawns that follow an abnormal exit.
func spawn(s *state, reconnect bool) []command {
	var cmds []command
	if reconnect {
		cmds = append(cmds, report{statusReconnecting})
	}
	cmds = append(cmds, spawnChild{})
	s.estTimer = s.newTimer()
	cmds = append(cmds, startTimer{id: s.estTimer, d: s.cfg.establish})
	s.kind = stateRunning
	s.childFP = s.fingerprint
	s.killSent = false
	s.reconnectRun = reconnect
	s.started = true
	s.clearPending()
	return cmds
}

func decide(s state, ev event) (state, []command) {
	if s.kind == stateDone {
		return s, nil
	}
	switch e := ev.(type) {
	case pathEvent:
		return decidePath(s, e)
	case timerFired:
		return decideTimer(s, e)
	case childExited:
		return decideExit(s, e)
	case gotSignal:
		return decideSignal(s, e)
	}
	return s, nil
}

func decidePath(s state, e pathEvent) (state, []command) {
	// Exact duplicates carry no information and must not cancel a pending
	// transition or a backoff delay.
	if e.satisfied == s.pathUp && e.fingerprint == s.fingerprint {
		return s, nil
	}
	s.pathUp = e.satisfied
	s.fingerprint = e.fingerprint

	switch s.kind {
	case stateRunning:
		if s.killSent {
			// The child is already condemned; the exit handler takes over.
			return s, nil
		}
		if !e.satisfied {
			if s.pending != pendingDown {
				s.pending = pendingDown
				s.pendTimer = s.newTimer()
				return s, []command{startTimer{id: s.pendTimer, d: s.cfg.debounce}}
			}
			return s, nil
		}
		if e.fingerprint == s.childFP {
			// Back on the child's own path: cancel any pending transition.
			s.clearPending()
			return s, nil
		}
		// Satisfied, but on a different path than the child.
		if s.pending != pendingChange {
			s.pending = pendingChange
			s.pendTimer = s.newTimer()
			return s, []command{startTimer{id: s.pendTimer, d: s.cfg.debounce}}
		}
		return s, nil

	case stateWaiting:
		if !e.satisfied {
			s.clearPending()
			return s, nil
		}
		if !s.started {
			return s, spawn(&s, false)
		}
		if s.pending != pendingSettle {
			s.pending = pendingSettle
			s.pendTimer = s.newTimer()
			return s, []command{startTimer{id: s.pendTimer, d: s.cfg.settle}}
		}
		return s, nil

	case stateBackoff:
		if !e.satisfied {
			return s, toWaiting(&s)
		}
		// A changed path report while backing off retries at once.
		return s, spawn(&s, true)
	}
	return s, nil
}

func toWaiting(s *state) []command {
	s.kind = stateWaiting
	s.clearPending()
	s.estTimer = 0
	s.killSent = false
	return []command{report{statusLinkDown}}
}

func decideTimer(s state, e timerFired) (state, []command) {
	if e.id == 0 {
		return s, nil
	}
	if e.id == s.estTimer {
		if s.kind != stateRunning {
			return s, nil
		}
		s.estTimer = 0
		s.backoffDelay = 0
		s.rapidFailures = 0
		if s.reconnectRun {
			return s, []command{report{statusReconnected}}
		}
		return s, nil
	}
	if e.id != s.pendTimer {
		return s, nil
	}

	p := s.pending
	s.clearPending()
	switch p {
	case pendingDown, pendingChange:
		if s.kind == stateRunning && !s.killSent {
			s.killSent = true
			return s, []command{killChild{}}
		}
	case pendingSettle:
		if s.kind == stateWaiting && s.pathUp {
			return s, spawn(&s, true)
		}
	case pendingRetry:
		if s.kind == stateBackoff && s.pathUp {
			return s, spawn(&s, true)
		}
	}
	return s, nil // stale timer
}

func decideExit(s state, e childExited) (state, []command) {
	if s.kind != stateRunning {
		return s, nil
	}
	// The established timer clears itself when it fires; if it is already
	// zero, this child lived long enough to count as a working connection.
	wasEstablished := s.estTimer == 0
	s.estTimer = 0

	if s.killSent {
		if s.quitOnExit {
			s.kind = stateDone
			return s, []command{exitProgram{code: s.quitCode}}
		}
		if s.pathUp {
			return s, spawn(&s, true)
		}
		return s, toWaiting(&s)
	}

	// The child exited on its own.
	if e.signaled {
		s.kind = stateDone
		return s, []command{exitProgram{code: 128 + e.signum}}
	}
	if e.code != 255 {
		// zmx detach, remote exit, or a finished command: user intent.
		s.kind = stateDone
		return s, []command{exitProgram{code: e.code}}
	}

	// Exit 255: a connection error under the exit-255 policy.
	var cmds []command
	if wasEstablished {
		s.rapidFailures = 0
	} else {
		s.rapidFailures++
		if s.rapidFailures == 3 {
			cmds = append(cmds, report{statusPersistentFailures})
		}
	}
	if !s.pathUp {
		return s, append(cmds, toWaiting(&s)...)
	}
	if s.backoffDelay == 0 {
		s.backoffDelay = minDuration(time.Second, s.cfg.maxBackoff)
		return s, append(cmds, spawn(&s, true)...)
	}
	s.kind = stateBackoff
	s.pending = pendingRetry
	s.pendTimer = s.newTimer()
	cmds = append(cmds, startTimer{id: s.pendTimer, d: s.backoffDelay})
	s.backoffDelay = minDuration(2*s.backoffDelay, s.cfg.maxBackoff)
	return s, cmds
}

func decideSignal(s state, e gotSignal) (state, []command) {
	switch e.kind {
	case sigReconnect:
		switch s.kind {
		case stateRunning:
			if !s.killSent {
				s.killSent = true
				return s, []command{killChild{}}
			}
		case stateBackoff:
			if s.pathUp {
				return s, spawn(&s, true)
			}
		}
		return s, nil

	case sigInterrupt, sigTerminate:
		code := 130 // 128 + SIGINT
		if e.kind == sigTerminate {
			code = 143 // 128 + SIGTERM
		}
		if s.kind == stateRunning {
			s.quitOnExit = true
			s.quitCode = code
			if !s.killSent {
				s.killSent = true
				return s, []command{killChild{}}
			}
			return s, nil
		}
		s.kind = stateDone
		return s, []command{exitProgram{code: code}}
	}
	return s, nil
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
