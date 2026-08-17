# roam

roam is a modern, event-driven successor to [autossh](https://github.com/autossh/autossh)
for interactive ssh sessions on macOS. It supervises the system `ssh` binary
and reconnects the moment the network returns or migrates, instead of waiting
for a socket timeout or a poll interval. Where autossh polls, roam listens: it
uses macOS `nw_path_monitor` path events as its primary signal, with keepalives
as a backstop.

Typical reconnects: 2–3 s after the network returns, 3–5 s across a live
migration (the debounce window absorbs interface flapping); 10–15 s for silent
link death that only keepalives can see.

For persistent remote sessions, pair roam with a detachable session manager
such as [zmx](https://github.com/neurosnap/zmx):

    roam <remote_host> -t zmx attach <session_name>

roam is generic, though — any interactive ssh session benefits.

## Requirements

macOS 11.0 (Big Sur) or later on Apple Silicon; macOS 10.15 (Catalina) or
later on Intel.

## Install

    go install github.com/paulsmith/roam@latest

## Usage

    roam [--flags] <ssh args...>

roam's flags come first and use `--name` or `--name=value` form. Everything
from the first argument not starting with `--` onward passes to ssh verbatim.
A bare `--` also ends roam's flags.

| Flag | Default | Purpose |
|---|---|---|
| `--debounce=<dur>` | 2s | Wait before acting on a path change; absorbs flapping |
| `--max-backoff=<dur>` | 30s | Longest retry delay while the path is up but connects fail |
| `--quiet` | off | Suppress status lines |
| `--verbose` | off | Log state transitions |
| `--no-defaults` | off | Do not inject the ssh defaults below |
| `--ssh=<path>` | `ssh` from PATH | ssh binary to run |
| `--netmon-debug` | — | Dump raw network path events; ^C to exit (diagnostic) |
| `--version` | — | Print version |

### Examples

    roam myhost                      # plain interactive session
    roam -t myhost zmx attach main   # persistent session via zmx
    roam --max-backoff=10s myhost    # cap retry spacing
    roam --netmon-debug              # inspect raw path events, ^C to exit
    kill -USR1 $(pgrep roam)         # force an immediate reconnect

## Resumable remote sessions

Adapted from [zmx's ssh workflow](https://github.com/neurosnap/zmx#ssh-workflow):
a wildcard Host entry in `~/.ssh/config` turns each ssh alias into its own
persistent session:

    Host d.*
        HostName 192.168.1.xxx
        RemoteCommand zmx attach %k
        RequestTTY yes

`%k` expands to the alias you typed, so each alias names its own session —
one native terminal window each, no `-t` needed (`RequestTTY` covers it):

    roam d.term
    roam d.irc
    roam d.dotfiles

`zmx attach` creates or reattaches, so a reconnect after a network drop
lands back in the same session. Detaching or `exit` ends roam cleanly; only
a connection error reconnects.

Where zmx's README wraps this in an `autossh -M 0 -q` alias, roam needs no
alias — `roam d.term` is already the resumable command. The original entry
also sets `ControlMaster`/`ControlPersist` to share one TCP connection; omit
those with roam (see Behavior below) — each window reconnects independently.

## Injected ssh defaults

Unless you pass the option yourself (or use `--no-defaults`), roam prepends:

    -o ServerAliveInterval=5  -o ServerAliveCountMax=2  -o ConnectTimeout=5

Keepalives detect drops the path monitor cannot see. roam scans only the ssh
options before the destination (walking grouped short options the way ssh's
getopt does), and option names are matched case-insensitively in both
`Name=value` and `Name value` form.

Caveat: a command-line `-o` overrides `ssh_config`, so an injected default
overrides a per-host config value. The escape hatches are `--no-defaults` or
passing the option yourself. roam does not inject `-t`; tty allocation stays
in ssh_config or your args.

## Behavior

- ssh owns your terminal directly — roam does no PTY proxying. Escape
  sequences, prompts, and raw mode work exactly as with bare ssh. roam
  snapshots your terminal attributes at startup and restores them after
  each child exit, so a force-killed ssh cannot leave the terminal raw.
- ssh exit status 255 means "connection error": roam reconnects. Any other
  exit (zmx detach, `exit`, a finished command) ends roam with that status.
  This is ssh's own ambiguity: a remote command that itself exits 255 is
  indistinguishable from a connection error, so roam retries it; `^C` stops
  the loop.
- **Quitting:** `~.` makes ssh exit 255, so roam reconnects rather than
  quits. To quit entirely, type `~.` and then `^C` during the visible
  `[roam: reconnecting…]` gap. `^C` always quits roam while the link is
  down. `~^Z` is unsupported (ssh suspends alone, not roam).
- `kill -USR1 <roam pid>` forces an immediate reconnect.
- roam watches the machine's default network path; when that path runs
  entirely over a tunnel (full-tunnel VPN, Tailscale exit node), it also
  watches the wifi and wired underlay so physical migrations stay visible.
  Changes below the fingerprint's resolution (relay switches, ProxyJump
  hops, a hotspot handoff on the same wifi interface) are covered by the
  injected keepalives within 10–15 s.
- **ControlPersist is discouraged with roam.** roam does not manage mux
  masters; a dead persistent master squatting on the control socket can
  break reconnects until it exits on its own.

### Exit status

roam exits with the child ssh's exit status for any non-255 exit. For
connection errors it loops under backoff until interrupted. On signals:
`^C` (SIGINT) exits 130, `SIGTERM` exits 143. A remote command that exits
255 loops — stop it with `^C`.

## Coming from autossh

roam is a native macOS replacement for autossh for interactive sessions.
The mental model is the same — a supervisor that restarts ssh — but the
trigger is a path event, not a timer or a socket probe.

- `kill -USR1 <roam pid>` forces a reconnect, just like autossh.
- `~.` reconnects rather than quits, the same ambiguity autossh has; quit
  with `~.` then `^C` during the reconnect gap.
- No daemon mode, no forward health checks, no `AUTOSSH_GATETIME`. roam is
  for interactive sessions. Unattended tunnels mostly work through the
  exit-255 rules but are not a design target; keep autossh for that job.
- ControlPersist: autossh can coexist with a mux master you set up; roam
  does not manage masters and a stale master can block reconnects, so
  avoid ControlPersist with roam.
- macOS only. On other platforms, keep autossh.

## Diagnostics

- `roam --verbose` logs state transitions with reasons.
- `roam --netmon-debug` prints raw path events (`satisfied=… fingerprint=…`)
  so you can see exactly what roam sees; `^C` to exit. Useful when a
  migration isn't being detected.
- `roam --quiet` silences status lines for non-interactive use.
