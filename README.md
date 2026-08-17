# roam

roam is an event-driven replacement for [autossh](https://github.com/autossh/autossh)
for interactive ssh sessions on macOS. roam supervises the system `ssh`
binary. When the network returns or migrates, roam reconnects immediately.
roam does not wait for a socket timeout or a poll interval. roam uses macOS
`nw_path_monitor` path events as its primary signal. roam also uses ssh
keepalives to detect failures that path events do not show.

Typical reconnect times:

- 2–3 s after the network returns.
- 3–5 s across a live migration. The debounce window prevents reactions to
  rapid interface changes.
- 10–15 s for a link failure that only keepalives can detect.

For persistent remote sessions, use roam with a detachable session manager,
for example [zmx](https://github.com/neurosnap/zmx):

    roam <remote_host> -t zmx attach <session_name>

roam does not require zmx. roam operates with all interactive ssh sessions.

## Requirements

On Apple Silicon, roam requires macOS 11.0 (Big Sur) or later. On Intel,
roam requires macOS 10.15 (Catalina) or later.

## Install

    go install github.com/recursiveascent/roam@latest

## Usage

    roam [--flags] <ssh args...>

Write the roam flags first. Each flag uses the `--name` or `--name=value`
form. The first argument that does not start with `--` starts the ssh
arguments. roam sends that argument and all subsequent arguments to ssh
unchanged. A bare `--` also ends the roam flags.

| Flag | Default | Purpose |
|---|---|---|
| `--debounce=<dur>` | 2s | Delay before roam reacts to a path change; prevents reactions to rapid interface changes |
| `--max-backoff=<dur>` | 30s | Maximum retry delay while the path is up but connections fail |
| `--quiet` | off | Do not show status lines |
| `--verbose` | off | Write a log entry for each state transition |
| `--no-defaults` | off | Do not add the default ssh options (see below) |
| `--ssh=<path>` | `ssh` from PATH | Path to the ssh binary |
| `--netmon-debug` | — | Show raw network path events (diagnostic); press `^C` to exit |
| `--version` | — | Show the version |

### Examples

    roam myhost                      # interactive session
    roam -t myhost zmx attach main   # persistent session with zmx
    roam --max-backoff=10s myhost    # limit the retry delay
    roam --netmon-debug              # show raw path events; press ^C to exit
    kill -USR1 $(pgrep roam)         # force an immediate reconnect

## Resumable remote sessions

This procedure comes from [zmx's ssh workflow](https://github.com/neurosnap/zmx#ssh-workflow).
A wildcard `Host` entry in `~/.ssh/config` connects each ssh alias to its
own persistent session:

    Host d.*
        HostName 192.168.1.xxx
        RemoteCommand zmx attach %k
        RequestTTY yes

ssh expands `%k` to the alias that you typed. Thus each alias identifies its
own session, in one native terminal window. The `-t` option is not necessary
because `RequestTTY` requests a tty:

    roam d.term
    roam d.irc
    roam d.dotfiles

`zmx attach` creates a session or attaches to an existing session. After a
network drop, roam reconnects to the same session. A detach or an `exit`
command stops roam. Only a connection error causes a reconnect.

zmx's README puts this configuration in an `autossh -M 0 -q` alias. roam
does not need an alias: `roam d.term` is the resumable command. The original
entry also sets `ControlMaster` and `ControlPersist` to share one TCP
connection. Do not set these two options with roam (see Behavior below).
Each window reconnects independently.

## Default ssh options

roam adds these ssh options before your arguments:

    -o ServerAliveInterval=5  -o ServerAliveCountMax=2  -o ConnectTimeout=5

roam does not add an option if you supply that option, or if you use
`--no-defaults`. Keepalives detect connection failures that the path monitor
cannot see. roam examines only the ssh options before the destination. roam
parses grouped short options the same way that ssh's getopt does. roam
matches option names without case sensitivity, in both `Name=value` and
`Name value` form.

Caution: a command-line `-o` option has priority over `ssh_config`. Thus a
default option from roam replaces a per-host configuration value. To prevent
this, use `--no-defaults` or supply the option yourself. roam does not add
`-t`. Control tty allocation in `ssh_config` or in your ssh arguments.

## Behavior

- ssh controls your terminal directly. roam does not proxy the PTY. Escape
  sequences, prompts, and raw mode operate the same as with bare ssh. At
  startup, roam records your terminal attributes. After each child exit,
  roam restores those attributes. Thus a killed ssh process cannot leave
  the terminal in raw mode.
- ssh exit status 255 indicates a connection error. roam then reconnects.
  Each other exit status stops roam with that status. Examples: a zmx
  detach, an `exit` command, a completed command. ssh gives status 255 both
  for a connection error and for a remote command that exits 255. Thus roam
  retries in both cases. Press `^C` to stop the loop.
- **To stop roam:** the `~.` sequence causes ssh to exit with status 255.
  Thus roam reconnects; roam does not stop. To stop roam fully, type `~.`.
  Then press `^C` while roam shows `[roam: reconnecting…]`. While the link
  is down, `^C` always stops roam. roam does not support `~^Z` (ssh
  suspends alone; roam continues).
- `kill -USR1 <roam pid>` forces an immediate reconnect.
- roam monitors the machine's default network path. When that path runs
  fully over a tunnel, roam also monitors the wifi and wired networks below
  it. Examples of full tunnels: a full-tunnel VPN, a Tailscale exit node.
  Thus roam can see physical migrations. The default keepalives detect, in
  10–15 s, changes that the path fingerprint cannot show. Examples: relay
  switches, ProxyJump hops, a hotspot handoff on the same wifi interface.
- **Do not use ControlPersist with roam.** roam does not manage mux
  masters. A dead persistent master can hold the control socket. This
  blocks reconnects until the master exits.

### Exit status

For each exit status other than 255, roam exits with the child ssh's
status. For a connection error, roam retries with backoff until you
interrupt it. `^C` (SIGINT) causes exit status 130. `SIGTERM` causes exit
status 143. A remote command that exits 255 causes a retry loop. Press
`^C` to stop the loop.

## Comparison with autossh

roam is a native macOS replacement for autossh for interactive sessions.
roam and autossh both supervise ssh and restart it. roam's trigger is a
path event, not a timer or a socket probe.

- `kill -USR1 <roam pid>` forces a reconnect. autossh has the same
  behavior.
- `~.` causes a reconnect; it does not stop roam. autossh has the same
  ambiguity. To stop roam, type `~.`. Then press `^C` in the reconnect gap.
- roam has no daemon mode, no forward health checks, and no
  `AUTOSSH_GATETIME`. roam's target is interactive sessions. Unattended
  tunnels usually operate correctly through the exit-255 rules, but they
  are not a design target. Use autossh for unattended tunnels.
- autossh can operate together with a mux master that you configure. roam
  does not manage masters. A stale master can block reconnects. Do not use
  ControlPersist with roam.
- roam operates only on macOS. On other platforms, use autossh.

## Diagnostics

- `roam --verbose` writes a log entry, with a reason, for each state
  transition.
- `roam --netmon-debug` shows raw path events (`satisfied=… fingerprint=…`).
  This output shows exactly what roam sees. Press `^C` to exit. Use this
  mode when roam does not detect a migration.
- `roam --quiet` removes status lines for non-interactive use.
