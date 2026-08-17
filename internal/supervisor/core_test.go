package supervisor

import (
	"reflect"
	"testing"
	"time"
)

// decideAll feeds events through decide in order and returns the final state
// plus every command emitted, for tests that walk a scenario.
func decideAll(t *testing.T, s state, evs ...event) (state, []command) {
	t.Helper()
	var all []command
	for _, ev := range evs {
		var cmds []command
		s, cmds = decide(s, ev)
		all = append(all, cmds...)
	}
	return s, all
}

func TestFirstPathUpSpawnsImmediately(t *testing.T) {
	s := initialState(defaultConfig())
	s, cmds := decide(s, pathEvent{satisfied: true, fingerprint: "en0/1"})

	want := []command{
		spawnChild{},
		startTimer{id: 1, d: defaultConfig().establish},
	}
	if !reflect.DeepEqual(cmds, want) {
		t.Fatalf("cmds = %#v, want %#v", cmds, want)
	}
	if s.kind != stateRunning {
		t.Fatalf("kind = %v, want stateRunning", s.kind)
	}
	if s.childFP != "en0/1" {
		t.Fatalf("childFP = %q", s.childFP)
	}
}

func TestStartWithPathDownWaits(t *testing.T) {
	s := initialState(defaultConfig())
	s, cmds := decide(s, pathEvent{satisfied: false, fingerprint: "none"})
	if len(cmds) != 0 {
		t.Fatalf("cmds = %#v, want none", cmds)
	}
	if s.kind != stateWaiting {
		t.Fatalf("kind = %v, want stateWaiting", s.kind)
	}
}

// running returns a state with one child spawned on fingerprint fp.
func running(t *testing.T, fp string) state {
	t.Helper()
	s := initialState(defaultConfig())
	s, _ = decide(s, pathEvent{satisfied: true, fingerprint: fp})
	if s.kind != stateRunning {
		t.Fatalf("setup: kind = %v, want stateRunning", s.kind)
	}
	return s
}

func TestPathDownDebouncesThenKills(t *testing.T) {
	s := running(t, "en0/1")

	s, cmds := decide(s, pathEvent{satisfied: false, fingerprint: "none"})
	want := []command{startTimer{id: 2, d: s.cfg.debounce}}
	if !reflect.DeepEqual(cmds, want) {
		t.Fatalf("on path down: cmds = %#v, want %#v", cmds, want)
	}
	if s.kind != stateRunning {
		t.Fatalf("kind = %v, want stateRunning while debouncing", s.kind)
	}

	s, cmds = decide(s, timerFired{id: 2})
	if !reflect.DeepEqual(cmds, []command{killChild{}}) {
		t.Fatalf("on debounce expiry: cmds = %#v, want killChild", cmds)
	}

	// The kill lands; child exits; path still down -> waiting + status line.
	s, cmds = decide(s, childExited{signaled: true, signum: 15})
	if !reflect.DeepEqual(cmds, []command{report{statusLinkDown}}) {
		t.Fatalf("after killed child exits: cmds = %#v", cmds)
	}
	if s.kind != stateWaiting {
		t.Fatalf("kind = %v, want stateWaiting", s.kind)
	}
}

func TestDuplicatePathReportIsIgnored(t *testing.T) {
	s := running(t, "en0/1")
	s2, cmds := decide(s, pathEvent{satisfied: true, fingerprint: "en0/1"})
	if len(cmds) != 0 {
		t.Fatalf("duplicate report: cmds = %#v, want none", cmds)
	}
	if !reflect.DeepEqual(s, s2) {
		t.Fatalf("duplicate report changed state: %#v -> %#v", s, s2)
	}
}

func TestPathFlapWithinDebounceIsIgnored(t *testing.T) {
	s := running(t, "en0/1")

	s, _ = decide(s, pathEvent{satisfied: false, fingerprint: "none"})
	// Path comes back on the same fingerprint before the debounce expires.
	s, cmds := decide(s, pathEvent{satisfied: true, fingerprint: "en0/1"})
	if len(cmds) != 0 {
		t.Fatalf("flap recovery: cmds = %#v, want none", cmds)
	}
	// The stale debounce timer fires and must be ignored.
	s, cmds = decide(s, timerFired{id: 2})
	if len(cmds) != 0 {
		t.Fatalf("stale timer: cmds = %#v, want none", cmds)
	}
	if s.kind != stateRunning {
		t.Fatalf("kind = %v, want stateRunning", s.kind)
	}
}

func TestPathUpFromWaitingSettlesThenRespawns(t *testing.T) {
	s := running(t, "en0/1")
	s, _ = decideAll(t, s,
		pathEvent{satisfied: false, fingerprint: "none"},
		timerFired{id: 2},
		childExited{signaled: true, signum: 15},
	)

	s, cmds := decide(s, pathEvent{satisfied: true, fingerprint: "en1/1"})
	want := []command{startTimer{id: 3, d: s.cfg.settle}}
	if !reflect.DeepEqual(cmds, want) {
		t.Fatalf("on path up: cmds = %#v, want %#v", cmds, want)
	}

	s, cmds = decide(s, timerFired{id: 3})
	want = []command{
		report{statusReconnecting},
		spawnChild{},
		startTimer{id: 4, d: s.cfg.establish},
	}
	if !reflect.DeepEqual(cmds, want) {
		t.Fatalf("on settle expiry: cmds = %#v, want %#v", cmds, want)
	}
	if s.kind != stateRunning {
		t.Fatalf("kind = %v, want stateRunning", s.kind)
	}
	if s.childFP != "en1/1" {
		t.Fatalf("childFP = %q", s.childFP)
	}
}

func TestPathDownDuringSettleCancelsSpawn(t *testing.T) {
	s := running(t, "en0/1")
	s, _ = decideAll(t, s,
		pathEvent{satisfied: false, fingerprint: "none"},
		timerFired{id: 2},
		childExited{signaled: true, signum: 15},
		pathEvent{satisfied: true, fingerprint: "en0/1"},
	)

	s, cmds := decide(s, pathEvent{satisfied: false, fingerprint: "none"})
	if len(cmds) != 0 {
		t.Fatalf("cmds = %#v, want none", cmds)
	}
	// The stale settle timer fires; nothing must spawn.
	s, cmds = decide(s, timerFired{id: 3})
	if len(cmds) != 0 {
		t.Fatalf("stale settle: cmds = %#v, want none", cmds)
	}
	if s.kind != stateWaiting {
		t.Fatalf("kind = %v, want stateWaiting", s.kind)
	}
}

func TestPathChangeDebouncesThenKillsAndRespawns(t *testing.T) {
	s := running(t, "en0/1")

	// Wifi -> ethernet: still satisfied, different fingerprint.
	s, cmds := decide(s, pathEvent{satisfied: true, fingerprint: "en1/1"})
	want := []command{startTimer{id: 2, d: s.cfg.debounce}}
	if !reflect.DeepEqual(cmds, want) {
		t.Fatalf("on change: cmds = %#v, want %#v", cmds, want)
	}

	s, cmds = decide(s, timerFired{id: 2})
	if !reflect.DeepEqual(cmds, []command{killChild{}}) {
		t.Fatalf("on debounce expiry: cmds = %#v, want killChild", cmds)
	}

	// The path is up, so the condemned child's exit respawns at once.
	s, cmds = decide(s, childExited{signaled: true, signum: 15})
	want = []command{
		report{statusReconnecting},
		spawnChild{},
		startTimer{id: 3, d: s.cfg.establish},
	}
	if !reflect.DeepEqual(cmds, want) {
		t.Fatalf("after kill: cmds = %#v, want %#v", cmds, want)
	}
	if s.childFP != "en1/1" {
		t.Fatalf("childFP = %q", s.childFP)
	}
}

func TestPathChangeFlapBackCancels(t *testing.T) {
	s := running(t, "en0/1")

	s, _ = decide(s, pathEvent{satisfied: true, fingerprint: "en1/1"})
	// Back to the child's own path before the debounce expires.
	s, cmds := decide(s, pathEvent{satisfied: true, fingerprint: "en0/1"})
	if len(cmds) != 0 {
		t.Fatalf("flap back: cmds = %#v, want none", cmds)
	}
	s, cmds = decide(s, timerFired{id: 2})
	if len(cmds) != 0 {
		t.Fatalf("stale change timer: cmds = %#v, want none", cmds)
	}
	if s.kind != stateRunning {
		t.Fatalf("kind = %v, want stateRunning", s.kind)
	}
}

func TestCleanExitPropagates(t *testing.T) {
	s := running(t, "en0/1")
	s, cmds := decide(s, childExited{code: 0})
	if !reflect.DeepEqual(cmds, []command{exitProgram{code: 0}}) {
		t.Fatalf("cmds = %#v, want exitProgram{0}", cmds)
	}
	if s.kind != stateDone {
		t.Fatalf("kind = %v, want stateDone", s.kind)
	}
	_, cmds = decide(s, pathEvent{satisfied: true, fingerprint: "en0/1"})
	if len(cmds) != 0 {
		t.Fatalf("after done: cmds = %#v, want none", cmds)
	}
}

func TestNonzeroNon255ExitPropagates(t *testing.T) {
	s := running(t, "en0/1")
	_, cmds := decide(s, childExited{code: 7})
	if !reflect.DeepEqual(cmds, []command{exitProgram{code: 7}}) {
		t.Fatalf("cmds = %#v, want exitProgram{7}", cmds)
	}
}

func TestForeignSignalDeathPropagates(t *testing.T) {
	s := running(t, "en0/1")
	_, cmds := decide(s, childExited{signaled: true, signum: 9})
	if !reflect.DeepEqual(cmds, []command{exitProgram{code: 137}}) {
		t.Fatalf("cmds = %#v, want exitProgram{137}", cmds)
	}
}

func TestExit255PathUpRespawnsImmediatelyFirstTime(t *testing.T) {
	s := running(t, "en0/1")
	s, cmds := decide(s, childExited{code: 255})
	want := []command{
		report{statusReconnecting},
		spawnChild{},
		startTimer{id: 2, d: s.cfg.establish},
	}
	if !reflect.DeepEqual(cmds, want) {
		t.Fatalf("cmds = %#v, want %#v", cmds, want)
	}
	if s.backoffDelay != time.Second {
		t.Fatalf("backoffDelay = %v, want 1s", s.backoffDelay)
	}
}

func TestConsecutiveFailuresBackOffExponentially(t *testing.T) {
	s := running(t, "en0/1")
	s, _ = decide(s, childExited{code: 255})

	s, cmds := decide(s, childExited{code: 255})
	want := []command{startTimer{id: 3, d: time.Second}}
	if !reflect.DeepEqual(cmds, want) {
		t.Fatalf("second failure: cmds = %#v, want %#v", cmds, want)
	}
	if s.kind != stateBackoff {
		t.Fatalf("kind = %v, want stateBackoff", s.kind)
	}
	if s.backoffDelay != 2*time.Second {
		t.Fatalf("backoffDelay = %v, want 2s", s.backoffDelay)
	}

	s, _ = decide(s, timerFired{id: 3})
	s, cmds = decide(s, childExited{code: 255})
	want = []command{report{statusPersistentFailures}, startTimer{id: 5, d: 2 * time.Second}}
	if !reflect.DeepEqual(cmds, want) {
		t.Fatalf("third failure: cmds = %#v, want %#v", cmds, want)
	}
}

func TestBackoffDelayIsCapped(t *testing.T) {
	s := running(t, "en0/1")
	s.backoffDelay = s.cfg.maxBackoff
	s, _ = decide(s, childExited{code: 255})
	if s.backoffDelay != s.cfg.maxBackoff {
		t.Fatalf("backoffDelay = %v, want cap %v", s.backoffDelay, s.cfg.maxBackoff)
	}
}

func TestInitialBackoffRespectsSmallCap(t *testing.T) {
	cfg := defaultConfig()
	cfg.maxBackoff = 100 * time.Millisecond
	s := initialState(cfg)
	s, _ = decide(s, pathEvent{satisfied: true, fingerprint: "en0/1"})
	s, _ = decide(s, childExited{code: 255})
	_, cmds := decide(s, childExited{code: 255})
	if !reflect.DeepEqual(cmds, []command{startTimer{id: 3, d: 100 * time.Millisecond}}) {
		t.Fatalf("cmds = %#v, want capped retry timer", cmds)
	}
}

func TestEstablishmentResetsBackoff(t *testing.T) {
	s := running(t, "en0/1")
	s, _ = decide(s, childExited{code: 255})

	s, cmds := decide(s, timerFired{id: 2})
	if !reflect.DeepEqual(cmds, []command{report{statusReconnected}}) {
		t.Fatalf("on establish: cmds = %#v, want reconnected report", cmds)
	}
	if s.backoffDelay != 0 || s.rapidFailures != 0 {
		t.Fatalf("backoffDelay = %v rapidFailures = %d, want 0 and 0",
			s.backoffDelay, s.rapidFailures)
	}

	_, cmds = decide(s, childExited{code: 255})
	if _, ok := cmds[len(cmds)-1].(startTimer); !ok {
		t.Fatalf("cmds = %#v, want trailing establish startTimer", cmds)
	}
	if !containsCommand(cmds, spawnChild{}) {
		t.Fatalf("cmds = %#v, want spawnChild", cmds)
	}
}

func containsCommand(cmds []command, want command) bool {
	for _, c := range cmds {
		if reflect.DeepEqual(c, want) {
			return true
		}
	}
	return false
}

func TestPersistentFailuresReportedAtThree(t *testing.T) {
	s := running(t, "en0/1")
	var all []command
	s, all = decideAll(t, s,
		childExited{code: 255},
		childExited{code: 255},
	)
	if containsCommand(all, report{statusPersistentFailures}) {
		t.Fatal("persistent-failures report emitted too early")
	}
	s, _ = decide(s, timerFired{id: s.pendTimer})
	_, cmds := decide(s, childExited{code: 255})
	if !containsCommand(cmds, report{statusPersistentFailures}) {
		t.Fatalf("cmds = %#v, want persistent-failures report", cmds)
	}
}

func TestExit255PathDownWaits(t *testing.T) {
	s := running(t, "en0/1")
	s, _ = decide(s, pathEvent{satisfied: false, fingerprint: "none"})
	s, cmds := decide(s, childExited{code: 255})
	if !reflect.DeepEqual(cmds, []command{report{statusLinkDown}}) {
		t.Fatalf("cmds = %#v, want linkDown report", cmds)
	}
	if s.kind != stateWaiting {
		t.Fatalf("kind = %v, want stateWaiting", s.kind)
	}
}

func TestChangedPathReportInBackoffRetriesAtOnce(t *testing.T) {
	s := running(t, "en0/1")
	s, _ = decideAll(t, s,
		childExited{code: 255},
		childExited{code: 255},
	)
	s, cmds := decide(s, pathEvent{satisfied: true, fingerprint: "en1/1"})
	if !containsCommand(cmds, spawnChild{}) {
		t.Fatalf("cmds = %#v, want spawnChild", cmds)
	}
	if s.kind != stateRunning {
		t.Fatalf("kind = %v, want stateRunning", s.kind)
	}
}

func TestDuplicatePathReportInBackoffKeepsDelay(t *testing.T) {
	s := running(t, "en0/1")
	s, _ = decideAll(t, s,
		childExited{code: 255},
		childExited{code: 255},
	)
	s2, cmds := decide(s, pathEvent{satisfied: true, fingerprint: "en0/1"})
	if len(cmds) != 0 {
		t.Fatalf("duplicate in backoff: cmds = %#v, want none", cmds)
	}
	if s2.kind != stateBackoff {
		t.Fatalf("kind = %v, want stateBackoff", s2.kind)
	}
}

func TestPathDownInBackoffWaits(t *testing.T) {
	s := running(t, "en0/1")
	s, _ = decideAll(t, s,
		childExited{code: 255},
		childExited{code: 255},
	)
	s, cmds := decide(s, pathEvent{satisfied: false, fingerprint: "none"})
	if !reflect.DeepEqual(cmds, []command{report{statusLinkDown}}) {
		t.Fatalf("cmds = %#v, want linkDown report", cmds)
	}
	if s.kind != stateWaiting {
		t.Fatalf("kind = %v, want stateWaiting", s.kind)
	}
}

func TestEstablishTimerFiresAfterKillIsDropped(t *testing.T) {
	// A reconnect spawns with an establish timer. Before that timer
	// fires, a path change condemns the child. The stale establish
	// timer must not clear backoff/rapidFailures or report reconnected
	// for a session we just killed.
	s := running(t, "en0/1")
	s, _ = decide(s, timerFired{id: s.estTimer}) // established
	s, _ = decide(s, childExited{code: 255})    // respawn; backoffDelay=1s
	s, _ = decide(s, pathEvent{satisfied: true, fingerprint: "en1/1"})
	est := s.estTimer
	s, _ = decide(s, timerFired{id: s.pendTimer}) // kill dispatched
	if !s.killSent {
		t.Fatalf("killSent = false, want true")
	}
	if s.backoffDelay != time.Second {
		t.Fatalf("before stray timer: backoffDelay = %v, want 1s", s.backoffDelay)
	}
	s, cmds := decide(s, timerFired{id: est})
	if containsCommand(cmds, report{statusReconnected}) {
		t.Fatalf("stale establish timer emitted reconnected: %#v", cmds)
	}
	if s.backoffDelay != time.Second {
		t.Fatalf("stale establish timer reset backoffDelay to %v, want 1s", s.backoffDelay)
	}
	if s.estTimer != 0 {
		t.Fatalf("stale establish timer not cleared: estTimer = %d", s.estTimer)
	}
}

func TestCleanExitDuringKillWindowPropagates(t *testing.T) {
	s := running(t, "en0/1")
	s, _ = decide(s, pathEvent{satisfied: true, fingerprint: "en1/1"}) // change debounce
	s, _ = decide(s, timerFired{id: 2})                                // killChild dispatched
	// the child beat the SIGTERM to a clean self-exit (user typed exit)
	_, cmds := decide(s, childExited{code: 0})
	if !reflect.DeepEqual(cmds, []command{exitProgram{code: 0}}) {
		t.Fatalf("cmds = %#v, want exitProgram{0}", cmds)
	}
}

func TestOurKillStillRespawns(t *testing.T) {
	s := running(t, "en0/1")
	s, _ = decide(s, pathEvent{satisfied: true, fingerprint: "en1/1"})
	s, _ = decide(s, timerFired{id: 2})
	// ssh's own SIGTERM handler exits 255: that is our kill, not user intent
	_, cmds := decide(s, childExited{code: 255})
	if !containsCommand(cmds, spawnChild{}) {
		t.Fatalf("cmds = %#v, want respawn", cmds)
	}
}

func TestUnknownBaselineAdoptsFirstFingerprint(t *testing.T) {
	// fail-open: the child was spawned before the monitor's first report,
	// so its baseline fingerprint is empty
	s := initialState(defaultConfig())
	s, _ = decide(s, pathEvent{satisfied: true, fingerprint: ""})
	if s.kind != stateRunning || s.childFP != "" {
		t.Fatalf("setup: kind = %v childFP = %q", s.kind, s.childFP)
	}
	s, cmds := decide(s, pathEvent{satisfied: true, fingerprint: "d:en0/1,"})
	if len(cmds) != 0 {
		t.Fatalf("first real report: cmds = %#v, want none (adopted)", cmds)
	}
	if s.childFP != "d:en0/1," {
		t.Fatalf("childFP = %q, want adopted baseline", s.childFP)
	}
	// after adoption, a genuinely different report is a migration again
	_, cmds = decide(s, pathEvent{satisfied: true, fingerprint: "d:en1/1,"})
	if len(cmds) != 1 {
		t.Fatalf("post-adoption migration: cmds = %#v, want debounce timer", cmds)
	}
}

func TestInterruptWhileDisconnectedExits(t *testing.T) {
	s := initialState(defaultConfig())
	s, cmds := decide(s, gotSignal{kind: sigInterrupt})
	if !reflect.DeepEqual(cmds, []command{exitProgram{code: 130}}) {
		t.Fatalf("cmds = %#v, want exitProgram{130}", cmds)
	}
	if s.kind != stateDone {
		t.Fatalf("kind = %v, want stateDone", s.kind)
	}
}

func TestTerminateWhileRunningKillsThenExits(t *testing.T) {
	s := running(t, "en0/1")
	s, cmds := decide(s, gotSignal{kind: sigTerminate})
	if !reflect.DeepEqual(cmds, []command{killChild{}}) {
		t.Fatalf("on signal: cmds = %#v, want killChild", cmds)
	}
	_, cmds = decide(s, childExited{signaled: true, signum: 15})
	if !reflect.DeepEqual(cmds, []command{exitProgram{code: 143}}) {
		t.Fatalf("after kill: cmds = %#v, want exitProgram{143}", cmds)
	}
}

func TestUsr1WhileRunningReconnects(t *testing.T) {
	s := running(t, "en0/1")
	s, cmds := decide(s, gotSignal{kind: sigReconnect})
	if !reflect.DeepEqual(cmds, []command{killChild{}}) {
		t.Fatalf("on USR1: cmds = %#v, want killChild", cmds)
	}
	_, cmds = decide(s, childExited{signaled: true, signum: 15})
	if !containsCommand(cmds, spawnChild{}) {
		t.Fatalf("after kill: cmds = %#v, want spawnChild", cmds)
	}
}

func TestUsr1WhileWaitingIsIgnored(t *testing.T) {
	s := initialState(defaultConfig())
	_, cmds := decide(s, gotSignal{kind: sigReconnect})
	if len(cmds) != 0 {
		t.Fatalf("cmds = %#v, want none", cmds)
	}
}

func TestUsr1InBackoffRetriesAtOnce(t *testing.T) {
	s := running(t, "en0/1")
	s, _ = decideAll(t, s,
		childExited{code: 255},
		childExited{code: 255},
	)
	_, cmds := decide(s, gotSignal{kind: sigReconnect})
	if !containsCommand(cmds, spawnChild{}) {
		t.Fatalf("cmds = %#v, want spawnChild", cmds)
	}
}
