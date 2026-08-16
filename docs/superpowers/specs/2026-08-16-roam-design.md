# roam — design

Date: 2026-08-16
Status: approved pending final review

## Problem

autossh reconnects on a timer: after a network disruption it waits for a
socket timeout or its poll interval before restarting ssh, which turns every
wifi drop, wifi↔ethernet switch, or hotspot handoff into a long, silent hang.
macOS delivers network path change events the moment they happen; nothing in
the autossh model consumes them.

roam is a native macOS replacement for autossh for interactive terminal
sessions: a supervisor for the system `ssh` driven by network events instead
of timers, with sensible keepalive defaults baked in. The primary use case is
persistent remote terminal sessions via zmx (the `ash` alias pattern), over
Tailscale or the open internet.

Reconnect latency becomes `(event delivery ≈ instant) + (ssh connect ≈ 1–2s)`
instead of `(timeout) + (poll interval)`.

## Goals

- Reconnect within ~2s of network restoration or migration (wifi→ethernet,
  wifi→hotspot), without waiting out any timeout.
- Detect silent link death within ~10–15s even when no path event fires
  (far side vanishes while the local network stays up), via injected
  keepalive defaults.
- Drop-in for the `ash` alias: `alias ash='roam'`; everything ssh-side
  (tty allocation, remote command, host aliases) stays in ssh_config.
- Never touch the tty: ssh owns the terminal directly; escape sequences,
  prompts, and raw mode work exactly as with bare ssh.
- Make disconnection visible: brief status lines instead of mysterious
  silence.

## Non-goals

- Unattended tunnels as a design target. `-N` port forwards mostly work via
  the exit-status rules, but there are no forward health checks and no daemon
  mode. autossh remains available for that job.
- zmx awareness. roam is a generic ssh wrapper; the alias supplies
  `zmx attach`.
- A native SSH implementation. roam wraps the system ssh binary.
- Cross-platform support. roam is macOS-only by design.

## Approaches considered

1. **Go supervisor wrapping system ssh; cgo + Network.framework
   (`nw_path_monitor`) for events.** Chosen. Best event semantics
   (satisfied/unsatisfied plus interface identity), zero third-party
   dependencies (Apple system framework via cgo), builds only on macOS —
   which is the point.
2. Pure-Go supervisor driving `scutil`'s interactive notification mode as the
   event source. No cgo, but scrapes semi-documented output, requires
   babysitting a subprocess, and yields lower-fidelity events. Recorded as
   the fallback if `nw_path_monitor` proves unworkable; `SCDynamicStore` via
   cgo is a second fallback with the same supervisor unchanged.
3. Native Go SSH client (`x/crypto/ssh`). Rejected: third-party dependency,
   poorly reimplements ssh_config/ProxyJump/agent/host-key UX, huge surface
   with no benefit to the reconnect logic.

## CLI

```
roam [--flags] <ssh args...>
```

Flag routing: leading `--`-prefixed arguments belong to roam; everything from
the first argument not starting with `--` onward passes to ssh verbatim. ssh
has no long options, so the rule is unambiguous. A bare `--` also ends roam's
flags.

Flags:

| Flag | Default | Purpose |
|---|---|---|
| `--debounce <dur>` | 2s | Damping window for network path flapping |
| `--max-backoff <dur>` | 30s | Retry cap when the path is up but connects fail |
| `--quiet` | off | Suppress status lines |
| `--verbose` | off | Log state transitions with reasons |
| `--no-defaults` | off | Do not inject ssh option defaults |
| `--ssh <path>` | `ssh` from `PATH` | ssh binary to run |
| `--netmon-debug` | — | Dump raw path events and exit on ^C (debug aid) |
| `--version` | — | Print version |

## Injected ssh defaults

Injected ahead of user arguments, each only if the user did not pass that
option themselves (roam scans the ssh args for `-o` equivalents first;
ssh option names are case-insensitive, so the scan is too):

- `-o ServerAliveInterval=5`
- `-o ServerAliveCountMax=2`
- `-o ConnectTimeout=5`

Keepalives are the fallback detector for drops the path monitor cannot see.
Documented caveat: command-line `-o` beats ssh_config, so an injected default
overrides a per-host config value; the escape hatches are `--no-defaults` or
passing the option explicitly.

roam does not inject `-t`; tty allocation stays in ssh_config or user args.

## Architecture

Functional core, imperative shell
(https://testing.googleblog.com/2025/10/simplify-your-code-functional-core.html):
all decision-making lives in a pure transition function; all side effects
live in a thin shell that executes what the core decides.

### Components

- **`main`** — partitions argv per the flag-routing rule, wires components,
  owns OS signal registration.
- **`internal/supervisor`** — the functional core plus its shell.
  - Core: `decide(state, event) → (state', []command)`. Pure; no I/O, no
    clocks, no goroutines. Events: path up/down/changed, child exited
    (with exit status), timer fired, signal received. Commands: spawn child,
    kill child, start timer (settle/debounce/backoff), reset mux, print
    status, exit with code.
  - Shell: a single event loop that reads one channel, calls `decide`,
    executes commands. Contains no branching beyond command dispatch.
- **`internal/netmon`** — cgo layer over `nw_path_monitor` (Network.framework
  C API; dispatch queue + exported Go callback, isolated in one
  `_darwin.go` file with ~100 lines of C glue). Emits
  `{satisfied bool, fingerprint string}` on a channel, where fingerprint
  identifies the path (primary interface name + local address). Deliberately
  dumb: no debouncing, no policy — replaceable without touching the core.
- **`internal/status`** — stderr printer. Prints only when stderr is a tty
  and `--quiet` is unset. Lines look like
  `[roam: link down — waiting for network]` and `[roam: reconnecting…]`,
  printed as plain lines on state change (no cursor tricks); after a
  successful reattach the session repaint follows them, which is itself the
  success signal — `[roam: reconnected]` prints only under `--verbose`.

All identifiers unexported except what `main` needs.

### State machine

States: `running` (ssh child alive), `waiting` (no child, path down),
`backoff` (no child, path up, recent failure), `done` (terminal).

Transitions:

- **Start** — path satisfied → spawn → `running`; otherwise `waiting`.
- **`running`, child exits by itself:**
  - Exit status **255** (ssh's connection-error code): link death. Path up →
    `backoff` with immediate first attempt; path down → `waiting`.
  - Any other status: user intent (zmx detach, `exit`, remote command
    finished) → `done`, propagate the code. Child killed by a signal roam
    did not send → `done`, exit `128+signum`.
- **`running`, path unsatisfied (debounced):** kill child → `waiting`.
  A dead path means the TCP connection is doomed; zmx makes reattach free.
- **`running`, path fingerprint changes while satisfied (debounced):**
  interface migration or new lease. Kill child → immediate respawn. Converts
  "maybe TCP survives the roam, maybe it stalls for a minute" (the Tailscale
  case, where the stable 100.x endpoint lets connections limp through) into
  a deterministic ~2s reconnect.
- **`waiting`, path satisfied:** settle delay (500ms constant, lets
  DNS/routes land) → spawn → `running`.
- **`backoff`:** exponential 1s → `--max-backoff` between attempts. Any
  fresh path-up event short-circuits the timer. A child that stays alive
  ≥5s counts as established and resets the backoff (internal constant, the
  autossh "gatetime" analog).

Kill procedure: SIGTERM, then SIGKILL after 2s.

### Child management

The ssh child inherits stdin/stdout/stderr directly — no pipes, no PTY
proxying, same process group as roam, sharing the real tty.

**Stale ControlMaster reset:** before every respawn that follows an abnormal
exit, roam runs `ssh -O exit <destination>` and ignores the result. When no
mux master exists this is a fast local no-op; when ssh_config enables
ControlPersist and a dead master is squatting on the control socket, it
clears the way. Unconditional; no flag.

Extracting `<destination>` requires a minimal scan of the ssh args: skip
flags, consuming an extra token for the ssh flags known to take values
(`-o`, `-p`, `-L`, `-R`, `-i`, etc. — ssh's option set is stable and
enumerable from its getopt string); the first remaining argument is the
destination. If the scan fails, roam skips the mux reset rather than
guessing.

### Signals and escape sequences

- While connected, ssh holds the tty in raw mode: ^C travels to the remote
  as a byte and roam never sees it. While disconnected the tty is cooked, so
  ^C reaches roam → clean exit. During an outage, ^C quits — and the status
  line shows exactly when that window is open.
- SIGTERM → kill child, exit.
- SIGUSR1 → kill child and reconnect immediately (autossh compatibility;
  scriptable prod).
- ssh escape sequences work mechanically (roam never touches the tty), but
  `~.` makes ssh exit 255 — indistinguishable from link death — so roam
  reconnects, as autossh does. Documented chord to exit fully: `~.` then
  `^C` during the visible reconnect gap. stderr scraping to detect `~.` was
  considered and rejected (breaks under `-q`; fragile).
- `~^Z` (suspend ssh alone, not roam) is an odd job-control corner:
  unsupported, documented as such. autossh shares this wart.

### Edge cases

- **Path flapping** (macOS reports satisfied→unsatisfied→satisfied bounces
  around wifi transitions): the debounce window absorbs it, both directions.
- **Captive portal / path up, no internet:** connects fail fast
  (ConnectTimeout 5) → capped backoff; every fresh path event retries
  immediately.
- **Bad arguments, host key changes, auth prompts:** ssh owns the tty, so
  its prompts and errors surface normally, and host-key interaction works on
  reconnect. roam never auto-exits on 255; after 3 rapid consecutive
  failures it prints `persistent failures — check args; ^C to quit` and
  keeps retrying at max backoff. Predictable beats clever.
- **Wake from sleep:** interfaces reattach on wake and `nw_path_monitor`
  fires; keepalives are the backstop. Explicit IOKit wake notifications are
  future work only if gaps appear in practice.
- **No tty** (e.g. launchd): status lines suppressed; tunnels work
  incidentally via the exit-255 rules.

## Testing

- **Core:** `decide` is pure — table-driven unit tests over the transition
  matrix (flap within debounce, path change while satisfied, 255 vs 0 exits,
  backoff growth and reset, signal handling, settle delay). No fakes needed.
- **Shell:** the event loop is tested with a scripted event source and a
  child runner that runs real short-lived processes (e.g. `/bin/sh -c
  'exit 255'`) — real processes, not mocks, at the interface seam.
- **netmon:** not unit-testable meaningfully; `--netmon-debug` dumps raw
  events for human verification while toggling wifi.
- **End to end:** documented manual checklist against a real host — wifi
  off/on, wifi→hotspot, lid close/open, `~.`, zmx detach, ^C during outage.
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

Go stdlib only; the sole non-Go dependency is the Apple system framework
linked via cgo (`-framework Network`). Version control: jj.

## Future work (explicitly deferred)

- IOKit sleep/wake notifications, if path events prove insufficient on wake.
- Forward health checks / daemon mode for tunnels.
- `SCDynamicStore` fallback event source if `nw_path_monitor` misbehaves.
