# roam — design

Date: 2026-08-16
Status: approved pending final review

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

Reconnect latency is the event delivery time (near zero) plus the ssh
connect time (1–2 s). No timeout or poll interval applies.

## Goals

- Reconnect within about 2 s after the network returns or migrates
  (wifi→ethernet, wifi→hotspot). Do not wait for a timeout.
- Detect silent link death within 10–15 s when no path event fires (for
  example, the remote side goes away while the local network stays up).
  Injected keepalive defaults provide this.
- Work as a drop-in for the `ash` alias: `alias ash='roam'`. All ssh
  behavior (tty allocation, remote command, host aliases) stays in
  ssh_config.
- Never touch the tty. ssh owns the terminal directly. Escape sequences,
  prompts, and raw mode work the same as with bare ssh.
- Show disconnection clearly. Print brief status lines instead of silence.

## Non-goals

- Unattended tunnels as a design target. `-N` port forwards mostly work
  through the exit-status rules. There are no forward health checks and no
  daemon mode. autossh remains available for that job.
- zmx awareness. roam is a generic ssh wrapper. The alias supplies
  `zmx attach`.
- A native SSH implementation. roam runs the system ssh binary.
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
also ends roam's flags.

Flags:

| Flag | Default | Purpose |
|---|---|---|
| `--debounce <dur>` | 2s | Wait before roam acts on a path change; absorbs flapping |
| `--max-backoff <dur>` | 30s | Longest delay between retries when the path is up but connects fail |
| `--quiet` | off | Suppress status lines |
| `--verbose` | off | Log state transitions with reasons |
| `--no-defaults` | off | Do not inject ssh option defaults |
| `--ssh <path>` | `ssh` from `PATH` | ssh binary to run |
| `--netmon-debug` | — | Dump raw path events; exit with ^C (debug aid) |
| `--version` | — | Print version |

## Injected ssh defaults

roam injects these options ahead of user arguments. It injects each option
only if the user did not pass that option. roam scans the ssh args for `-o`
equivalents first. ssh option names are case-insensitive; the scan is too.

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
  components, registers OS signals.
- **`internal/supervisor`** — the functional core plus its shell.
  - Core: `decide(state, event) → (state', []command)`. Pure: no I/O, no
    clocks, no goroutines. Events: path up, path down, path changed, child
    exited (with exit status), timer fired, signal received. Commands:
    spawn child, kill child, start timer (settle, debounce, or backoff),
    reset mux, print status, exit with code.
  - Shell: one event loop. It reads one channel, calls `decide`, and
    executes the commands. It contains no branches except command dispatch.
- **`internal/netmon`** — cgo layer over `nw_path_monitor` (Network.framework
  C API; a dispatch queue plus an exported Go callback; isolated in one
  `_darwin.go` file with about 100 lines of C glue). It emits
  `{satisfied bool, fingerprint string}` on a channel. The fingerprint
  identifies the path: primary interface name plus local address. netmon
  holds no policy and no debouncing. It is replaceable without changes to
  the core.
- **`internal/status`** — stderr printer. It prints only when stderr is a
  tty and `--quiet` is not set. Lines look like
  `[roam: link down — waiting for network]` and `[roam: reconnecting…]`.
  They are plain lines with no cursor movement. After a successful
  reattach, the session repaint follows them; the repaint is the success
  signal. `[roam: reconnected]` prints only under `--verbose`.

All identifiers are unexported except what `main` needs.

### State machine

States: `running` (ssh child alive), `waiting` (no child, path down),
`backoff` (no child, path up, recent failure), `done` (terminal).

Transitions:

- **Start.** If the path is satisfied: spawn, go to `running`. If not: go
  to `waiting`.
- **`running`, the child exits by itself:**
  - Exit status **255** (ssh's connection-error code): link death. If the
    path is up: go to `backoff` and attempt at once. If the path is down:
    go to `waiting`.
  - Any other status: user intent (zmx detach, `exit`, remote command
    finished). Go to `done` and propagate the code. If a signal that roam
    did not send killed the child: go to `done`, exit `128+signum`.
- **`running`, the path becomes unsatisfied (after debounce):** kill the
  child, go to `waiting`. A dead path means the TCP connection cannot
  recover, and zmx makes reattach cheap.
- **`running`, the path fingerprint changes while the path stays satisfied
  (after debounce):** interface migration or a new lease. Kill the child
  and respawn at once. This makes the reconnect deterministic at about
  2 s. Without it, a migration over Tailscale can stall the session for up
  to a minute: the stable 100.x endpoint keeps the TCP connection alive
  across the migration, but TCP retransmission backoff delays recovery.
- **`waiting`, the path becomes satisfied:** wait the settle delay (500 ms
  constant; DNS and routes need a moment), spawn, go to `running`.
- **`backoff`:** exponential delay between attempts, 1 s up to
  `--max-backoff`. A fresh path-up event cancels the delay and attempts at
  once. A child that stays alive for 5 s or more counts as established and
  resets the backoff. The 5 s threshold is an internal constant, the
  analog of autossh's "gatetime".

Kill procedure: SIGTERM, then SIGKILL after 2 s.

### Child management

The ssh child inherits stdin, stdout, and stderr directly. No pipes, no
PTY proxy. It shares roam's process group and the real tty.

**Stale ControlMaster reset.** Before each respawn that follows an abnormal
exit, roam runs `ssh -O exit <destination>` and ignores the result. When no
mux master exists, this is a fast local no-op. When ssh_config enables
ControlPersist and a dead master holds the control socket, this clears it.
Unconditional; no flag.

To extract `<destination>`, roam scans the ssh args minimally. It skips
flags, and consumes one extra token for each ssh flag known to take a value
(`-o`, `-p`, `-L`, `-R`, `-i`, and the rest; ssh's option set is stable and
enumerable from its getopt string). The first remaining argument is the
destination. If the scan fails, roam skips the mux reset. It does not
guess.

### Signals and escape sequences

- While connected, ssh holds the tty in raw mode. ^C travels to the remote
  as a byte; roam never sees it. While disconnected, the tty is cooked, so
  ^C reaches roam and roam exits cleanly. During an outage, ^C quits. The
  status line shows when that window is open.
- SIGTERM: kill the child, exit.
- SIGUSR1: kill the child and reconnect at once (autossh compatibility;
  scriptable prod).
- ssh escape sequences work because roam never touches the tty. But `~.`
  makes ssh exit 255, which is indistinguishable from link death. So roam
  reconnects, as autossh does. The documented chord for a full exit: `~.`,
  then `^C` during the visible reconnect gap. The design rejects stderr
  scraping to detect `~.`: it breaks under `-q`, and it is fragile.
- `~^Z` suspends the ssh process alone, not roam. This is an odd
  job-control corner. It is unsupported and documented as such. autossh has
  the same limitation.

### Edge cases

- **Path flapping.** macOS can report satisfied→unsatisfied→satisfied
  bounces around wifi transitions. The debounce window absorbs them in
  both directions.
- **Captive portal (path up, no internet).** Connect attempts fail fast
  (ConnectTimeout 5). Backoff caps the retry rate. Each fresh path event
  triggers an immediate retry.
- **Bad arguments, host key changes, auth prompts.** ssh owns the tty. Its
  prompts and errors appear normally, and host-key interaction works on
  reconnect. roam never exits by itself on 255. After 3 rapid consecutive
  failures, it prints `persistent failures — check args; ^C to quit` and
  continues to retry at max backoff.
- **Wake from sleep.** Interfaces reattach on wake and `nw_path_monitor`
  fires. Keepalives are the backstop. IOKit wake notifications are future
  work; add them only if gaps appear in practice.
- **No tty** (for example, under launchd). Status lines are suppressed.
  Tunnels work incidentally through the exit-255 rules.

## Testing

- **Core.** `decide` is pure. Table-driven unit tests cover the transition
  matrix: flap within debounce, path change while satisfied, 255 vs 0
  exits, backoff growth and reset, signal handling, settle delay. No fakes
  are needed.
- **Shell.** A scripted event source drives the event loop. The child
  runner runs real short-lived processes (for example,
  `/bin/sh -c 'exit 255'`). Real processes, not mocks, at the interface
  seam.
- **netmon.** Not meaningfully unit-testable. `--netmon-debug` dumps raw
  events for human verification during wifi toggles.
- **End to end.** A documented manual checklist against a real host: wifi
  off/on, wifi→hotspot, lid close/open, `~.`, zmx detach, ^C during an
  outage.
- TDD for the core and shell during implementation.

## Repository layout

```
roam/
  go.mod                    # module github.com/paulsmith/roam, go 1.26
  main.go                   # flag partitioning, wiring, signal registration
  internal/supervisor/      # core (pure) + shell (event loop)
  internal/netmon/          # cgo nw_path_monitor wrapper, darwin-only
  internal/status/          # stderr status printer
  docs/superpowers/specs/   # this document
```

Go stdlib only. The sole non-Go dependency is the Apple system framework
linked through cgo (`-framework Network`). Version control: jj.

## Future work (explicitly deferred)

- IOKit sleep/wake notifications, if path events prove insufficient on
  wake.
- Forward health checks and daemon mode for tunnels.
- `SCDynamicStore` fallback event source, if `nw_path_monitor` misbehaves.
