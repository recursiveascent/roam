# roam — design

Date: 2026-08-16
Status: approved; amended after design review

## Problem

autossh reconnects on a timer. After a network disruption, it waits for a
socket timeout or for its poll interval before it restarts ssh. Each wifi
drop, wifi↔ethernet switch, or hotspot handoff becomes a long, silent hang.
macOS reports network path changes immediately. The autossh model does not
use these events.

roam is a native macOS replacement for autossh for interactive terminal
sessions. It supervises the system `ssh` binary. Network events drive it,
not timers. It injects keepalive defaults suited to this use case. The
primary use case is persistent remote terminal sessions with zmx (the `ash`
alias pattern), over Tailscale or the open internet.

Reconnect latency after the network returns is the settle delay (0.5 s)
plus the ssh connect time (1–2 s): about 2–3 s. A migration between live
networks adds the debounce window (2 s): about 3–5 s. No socket timeout or
poll interval applies.

## Goals

- Reconnect within 2–3 s after the network returns, and within 3–5 s
  across a live migration (wifi→ethernet, wifi→hotspot). Do not wait for a
  socket timeout.
- Detect silent link death within 10–15 s when no path event fires (for
  example, the remote side goes away while the local network stays up).
  Injected keepalive defaults provide this.
- Work as a drop-in for the `ash` alias: `alias ash='roam'`. All ssh
  behavior (tty allocation, remote command, host aliases) stays in
  ssh_config.
- No PTY proxying. ssh owns the terminal directly. roam snapshots terminal
  attributes at startup and restores them after each child exit, because a
  forced kill can leave the terminal in raw mode.
- Show disconnection clearly. Print brief status lines instead of silence.

## Non-goals

- Unattended tunnels as a design target. `-N` port forwards mostly work
  through the exit-status rules. There are no forward health checks and no
  daemon mode. autossh remains available for that job.
- zmx awareness. roam is a generic ssh wrapper. The alias supplies
  `zmx attach`.
- A native SSH implementation. roam runs the system ssh binary.
- ControlMaster management. roam neither creates nor destroys mux masters
  (see Child management).
- Cross-platform support. roam is macOS-only by design.

## Approaches considered

1. **Go supervisor that runs the system ssh; cgo + Network.framework
   (`nw_path_monitor`) for events.** Chosen. It has the best event
   semantics: satisfied/unsatisfied state plus interface identity. It has
   zero third-party dependencies; it links an Apple system framework
   through cgo. It builds only on macOS, which matches the goal.
2. Pure-Go supervisor that drives `scutil` in its interactive notification
   mode. No cgo. But it scrapes semi-documented output, it must supervise a
   subprocess, and its events carry less information. Recorded as the
   fallback if `nw_path_monitor` fails to work. `SCDynamicStore` through
   cgo is a second fallback. Both fallbacks leave the supervisor unchanged.
3. Native Go SSH client (`x/crypto/ssh`). Rejected. It is a third-party
   dependency. It reimplements ssh_config, ProxyJump, agent support, and
   host-key UX badly. It adds a large surface with no benefit to the
   reconnect logic.

## CLI

```
roam [--flags] <ssh args...>
```

Flag routing: leading arguments that start with `--` belong to roam. All
arguments from the first argument that does not start with `--` pass to ssh
verbatim. ssh has no long options, so the rule is unambiguous. A bare `--`
also ends roam's flags. roam value flags use `--name=value` form, so each
roam argument is self-contained.

Flags:

| Flag | Default | Purpose |
|---|---|---|
| `--debounce=<dur>` | 2s | Wait before roam acts on a path change; absorbs flapping |
| `--max-backoff=<dur>` | 30s | Longest delay between retries when the path is up but connects fail |
| `--quiet` | off | Suppress status lines |
| `--verbose` | off | Log state transitions with reasons |
| `--no-defaults` | off | Do not inject ssh option defaults |
| `--ssh=<path>` | `ssh` from `PATH` | ssh binary to run |
| `--netmon-debug` | — | Dump raw path events; exit with ^C (debug aid) |
| `--version` | — | Print version |

## Injected ssh defaults

roam injects these options ahead of user arguments. It injects each option
only if the user did not pass that option. roam scans only the ssh options
that come before the destination. The scan walks grouped short options the
way ssh's getopt does (`-tp 2222` is `-t` plus `-p 2222`; `-vo X` is `-v`
plus `-o X`; glued `-oX` too) and accepts both option forms, `Name=value`
and ssh_config's `Name value`. Option names are case-insensitive, so the
scan is too. Arguments after the destination are the remote command; roam
does not scan them.

- `-o ServerAliveInterval=5`
- `-o ServerAliveCountMax=2`
- `-o ConnectTimeout=5`

Keepalives are the fallback detector. They catch drops that the path
monitor cannot see. Caveat: a command-line `-o` overrides ssh_config, so an
injected default overrides a per-host config value. The escape hatches are
`--no-defaults` or an explicit `-o`.

roam does not inject `-t`. Tty allocation stays in ssh_config or user args.

## Architecture

Functional core, imperative shell
(https://testing.googleblog.com/2025/10/simplify-your-code-functional-core.html).
All decisions live in a pure transition function. All side effects live in
a thin shell. The shell executes what the core decides.

### Components

- **`main`** — partitions argv with the flag-routing rule, wires
  components, registers OS signals. Its netmon adapter fails open: if no
  path report arrives within 3 s of start, it synthesizes a satisfied
  event and lets ssh's ConnectTimeout and keepalives judge the network.
- **`internal/supervisor`** — the functional core plus its shell.
  - Core: `decide(state, event) → (state', []command)`. Pure: no I/O, no
    clocks, no goroutines. Events: path report, child exited (with exit
    status), timer fired, signal received. Commands: spawn child, kill
    child, start timer (settle, debounce, or backoff), print status, exit
    with code. The core drops a path report that duplicates the last
    reported state; a duplicate must not cancel a backoff delay.
  - Shell: one event loop. It reads one channel, calls `decide`, and
    executes the commands. A failed spawn is handled synchronously: the
    failure becomes a child-exit event processed before any other event,
    so no signal or path event can observe a running state without a
    child.
- **`internal/netmon`** — cgo layer over `nw_path_monitor` (Network.framework
  C API; one serial dispatch queue plus an exported Go callback). Three
  monitors feed one combined snapshot: the default path, plus wifi and
  wired underlay monitors. The first report waits until all three have
  delivered their initial state, so partial startup snapshots cannot look
  like migrations. The fingerprint carries the default path's ordered
  interface list (name and type); when the default path runs entirely
  over tunnels (full-tunnel VPN, Tailscale exit node), the wifi and wired
  underlay sections join it, so physical migrations stay visible. The Go
  side performs a latest-wins channel send: when the channel is full, it
  drops the oldest report, because only the newest path state matters.
  netmon holds no policy and no debouncing.
- **`internal/status`** — stderr printer. It prints only when stderr is a
  tty and `--quiet` is not set. Lines look like
  `[roam: link down — waiting for network]` and `[roam: reconnecting…]`.
  They are plain lines with no cursor movement. After a successful
  reattach, the session repaint follows them; the repaint is the success
  signal. `[roam: reconnected]` prints only under `--verbose`.
- **`internal/tty`** — snapshots terminal attributes (`TIOCGETA`) at
  startup and restores them (`TIOCSETA`) after each child exit. A forced
  SIGKILL can prevent ssh from restoring the terminal itself; without this
  restore, the documented `^C`-while-disconnected behavior would break.

All identifiers are unexported except what `main` needs.

### What the fingerprint means

The fingerprint identifies the machine's default network path, not the
route ssh traffic takes. When the default path includes a physical
interface, the fingerprint is that path alone; when it runs entirely over
tunnels, the wifi and wired underlay interfaces join it, so a physical
migration behind a full-tunnel VPN still changes the fingerprint.
ProxyJump, ProxyCommand, and per-destination routing remain invisible.
Changes those layers absorb without a fingerprint change are covered by
the keepalive fallback, not by path events. The fingerprint deliberately
excludes addresses: IPv6 temporary-address rotation must not reconnect a
healthy session, and a same-interface change (a hotspot switch that keeps
the same wifi interface) is caught by keepalives within 10–15 s.

netmon is built and human-verified first (a spike task) against wifi
toggles, hotspot handoff, ethernet plug-in, and Tailscale up/down, before
the supervisor depends on its semantics.

### State machine

States: `running` (ssh child alive), `waiting` (no child, path down),
`backoff` (no child, path up, recent failure), `done` (terminal).

Transitions:

- **Start.** If the path is satisfied: spawn, go to `running`. If not: go
  to `waiting`.
- **`running`, the child exits by itself:**
  - Exit status **255**: link death (see the exit-255 policy below). If
    the path is up: go to `backoff` and attempt at once. If the path is
    down: go to `waiting`.
  - Any other status: user intent (zmx detach, `exit`, remote command
    finished). Go to `done` and propagate the code. If a signal that roam
    did not send killed the child: go to `done`, exit `128+signum`.
- **`running`, the path becomes unsatisfied (after debounce):** kill the
  child, go to `waiting`. A dead path means the TCP connection cannot
  recover, and zmx makes reattach cheap.
- **`running`, the path fingerprint changes while the path stays satisfied
  (after debounce):** interface migration. Kill the child and respawn at
  once. This makes the reconnect deterministic at about 3–5 s. Without
  it, a migration over Tailscale can stall the session for up to a
  minute: the stable 100.x endpoint keeps the TCP connection alive across
  the migration, but TCP retransmission backoff delays recovery.
- **`waiting`, the path becomes satisfied:** wait the settle delay (500 ms
  constant; DNS and routes need a moment), spawn, go to `running`.
- **`backoff`:** exponential delay between attempts, 1 s up to
  `--max-backoff`. A changed path report cancels the delay and attempts at
  once; an unchanged duplicate does not. A child that stays alive for 5 s
  or more counts as established and resets the backoff. The 5 s threshold
  is an internal constant, the analog of autossh's "gatetime".

Kill procedure: `exec.Cmd` with a `Cancel` function that sends SIGTERM and
`WaitDelay` of 2 s, after which the runtime sends SIGKILL. After each
child exit, roam restores the saved terminal attributes.

### The exit-255 policy

ssh's exit status is ambiguous by design: 255 marks an ssh connection
error, and a remote command that itself exits 255 is indistinguishable
from one. roam's policy is uniform: every 255 is a connection error,
retried under backoff. A remote command that legitimately exits 255
therefore loops; `^C` ends it deterministically. autossh has the same
semantics. A command mode that propagates 255 instead of retrying is
future work.

### Child management

The ssh child inherits stdin, stdout, and stderr directly. No pipes, no
PTY proxy. It shares roam's process group and the real tty.

roam does not manage ControlMaster sockets. `ssh -O exit` terminates the
persistent master and would disrupt sessions that share it, and a reset
that omits the user's `-F`, `-S`, `-p`, and `-l` arguments can target the
wrong socket. Under ControlPersist, a dead master squatting on the control
socket can therefore break reconnects: using ControlPersist together with
roam is discouraged and documented. Master-aware handling is future work.

### Signals and escape sequences

- While connected with a tty, ssh holds the terminal in raw mode. ^C
  travels to the remote as a byte; roam never sees it. While disconnected,
  the terminal is cooked (roam restores it if ssh could not), so ^C
  reaches roam and roam exits cleanly. During an outage, ^C quits. The
  status line shows when that window is open. In sessions without a tty,
  ^C delivers SIGINT to both processes and roam exits either way.
- SIGTERM: kill the child, exit.
- SIGUSR1: kill the child and reconnect at once (autossh compatibility;
  scriptable prod).
- ssh escape sequences work because roam never touches the byte stream.
  But `~.` makes ssh exit 255, which is indistinguishable from link death.
  So roam reconnects, as autossh does. The documented chord for a full
  exit: `~.`, then `^C` during the visible reconnect gap. The design
  rejects stderr scraping to detect `~.`: it breaks under `-q`, and it is
  fragile.
- `~^Z` suspends the ssh process alone, not roam. This is an odd
  job-control corner. It is unsupported and documented as such. autossh has
  the same limitation.

### Edge cases

- **Path flapping.** macOS can report satisfied→unsatisfied→satisfied
  bounces around wifi transitions. The debounce window absorbs them in
  both directions, and exact-duplicate reports are dropped outright.
- **Captive portal (path up, no internet).** Connect attempts fail fast
  (ConnectTimeout 5). Backoff caps the retry rate. A changed path report
  triggers an immediate retry; unchanged duplicates do not defeat the cap.
- **Bad arguments, host key changes, auth prompts.** ssh owns the tty. Its
  prompts and errors appear normally, and host-key interaction works on
  reconnect. roam never exits by itself on 255. After 3 rapid consecutive
  failures, it prints `persistent failures — check args; ^C to quit` and
  continues to retry at max backoff.
- **Monitor never reports.** The netmon adapter synthesizes a satisfied
  event with an empty fingerprint after 3 s and roam proceeds; ssh's own
  timeouts govern from there. An empty baseline means "unknown": the core
  adopts the monitor's first real fingerprint instead of treating it as a
  migration, so a late-waking monitor cannot reconnect a healthy session.
- **A kill races a clean exit.** After roam dispatches a kill, a child
  exit that is neither a signal death nor ssh's 255 means the user beat
  the kill (zmx detach, `exit`). roam propagates that exit instead of
  reconnecting.
- **Wake from sleep.** Interfaces reattach on wake and `nw_path_monitor`
  fires. Keepalives are the backstop. IOKit wake notifications are future
  work; add them only if gaps appear in practice.
- **No tty** (for example, under launchd). Status lines are suppressed.
  Tunnels work incidentally through the exit-255 rules.

## Testing

- **Core.** `decide` is pure. Table-driven unit tests cover the transition
  matrix: flap within debounce, duplicate path reports (including in
  backoff), path change while satisfied, 255 vs 0 exits, backoff growth
  and reset, signal handling, settle delay. No fakes are needed.
- **Shell.** Two layers. `testing/synctest` (stable since Go 1.25) drives
  interleaving tests deterministically with an in-memory runner at the
  runner seam: spawn failure with a pending signal, timer-heavy retry
  paths. Real-process integration tests use `/bin/sh` children for exit
  codes, SIGTERM death, and SIGKILL escalation (a child that traps
  SIGTERM).
- **netmon.** Built and human-verified first (spike task): wifi toggle,
  hotspot handoff, ethernet plug-in, Tailscale up/down, observed through
  `--netmon-debug`.
- **tty.** Unit test: a snapshot of a non-terminal reports not-a-terminal
  and its restore is a no-op. Raw-mode restoration is on the manual
  checklist.
- **End to end.** A documented manual checklist against a real host: wifi
  off/on, wifi→hotspot, lid close/open, `~.`, zmx detach, ^C during an
  outage, a remote command that exits 255.
- TDD for the core and shell during implementation.

## Repository layout

```
roam/
  go.mod                    # module github.com/paulsmith/roam, go 1.26
  main.go                   # flag partitioning, wiring, signal registration
  internal/supervisor/      # core (pure) + shell (event loop) + runner
  internal/netmon/          # cgo nw_path_monitor wrapper, darwin-only
  internal/status/          # stderr status printer
  internal/tty/             # terminal attribute snapshot/restore
  docs/superpowers/specs/   # this document
```

Go stdlib only. The sole non-Go dependency is the Apple system framework
linked through cgo (`-framework Network`). The `go.mod` directive is
`go 1.26`; build and CI use the latest 1.26 patch release. Version
control: jj.

## Future work (explicitly deferred)

- Master-aware ControlPersist handling (detect and clear a stale mux
  master with the user's full effective configuration).
- A command mode that propagates remote exit 255 instead of retrying.
- IOKit sleep/wake notifications, if path events prove insufficient on
  wake.
- Forward health checks and daemon mode for tunnels.
- `SCDynamicStore` fallback event source, if `nw_path_monitor` misbehaves.
