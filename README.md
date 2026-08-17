# roam

roam supervises the system `ssh` for interactive sessions on macOS. It
listens for network path events (`nw_path_monitor`) and reconnects the
moment the network returns or migrates — wifi to ethernet, wifi to
hotspot — instead of waiting for a socket timeout. autossh in spirit,
event-driven in practice. Typical reconnects: 2–3 s after the network
returns, 3–5 s across a live migration (the debounce window absorbs
interface flapping).

Designed to pair with [zmx](https://github.com/neurosnap/zmx) for
persistent remote sessions:

    alias ash='roam'
    ash d.term        # ssh_config supplies tty allocation and the zmx command

## Install

    go install github.com/paulsmith/roam@latest

Requires macOS. Builds with cgo against Network.framework.

## Usage

    roam [--flags] <ssh args...>

roam's flags come first and use `--name` or `--name=value` form. Everything
from the first argument not starting with `--` onward passes to ssh
verbatim.

| Flag | Default | Purpose |
|---|---|---|
| `--debounce=<dur>` | 2s | Wait before acting on a path change; absorbs flapping |
| `--max-backoff=<dur>` | 30s | Longest retry delay while the path is up but connects fail |
| `--quiet` | off | Suppress status lines |
| `--verbose` | off | Log state transitions |
| `--no-defaults` | off | Do not inject the ssh defaults below |
| `--ssh=<path>` | `ssh` from PATH | ssh binary to run |
| `--netmon-debug` | — | Dump raw network path events; ^C to exit |
| `--version` | — | Print version |

## Injected ssh defaults

Unless you pass the option yourself (or use `--no-defaults`), roam injects:

    -o ServerAliveInterval=5  -o ServerAliveCountMax=2  -o ConnectTimeout=5

Keepalives detect drops the path monitor cannot see. Note: command-line
`-o` overrides `ssh_config`, so an injected default overrides a per-host
config value.

## Behavior

- ssh owns your terminal directly — roam does no PTY proxying. Escape
  sequences, prompts, and raw mode work exactly as with bare ssh. roam
  snapshots your terminal attributes at startup and restores them after
  each child exit, so a force-killed ssh cannot leave the terminal raw.
- ssh exit status 255 means "connection error": roam reconnects. Any other
  exit (zmx detach, `exit`, a finished command) ends roam with that
  status. This is ssh's own ambiguity: a remote command that itself exits
  255 is indistinguishable from a connection error, so roam retries it;
  `^C` stops the loop.
- `~.` therefore reconnects rather than quits (ssh reports it as exit
  255). **To quit entirely: `~.` then `^C`** during the visible
  `[roam: reconnecting…]` gap. `^C` always quits roam while the link is
  down. `~^Z` is unsupported.
- `kill -USR1 <roam pid>` forces an immediate reconnect.
- roam watches the machine's default network path; when that path runs
  entirely over a tunnel (full-tunnel VPN, Tailscale exit node), it also
  watches the wifi and wired underlay so physical migrations stay
  visible. Changes below the fingerprint's resolution (relay switches,
  ProxyJump hops, a hotspot handoff on the same wifi interface) are
  covered by the injected keepalives within 10–15 s.
- **ControlPersist is discouraged with roam.** roam does not manage mux
  masters; a dead persistent master squatting on the control socket can
  break reconnects until it exits on its own.
