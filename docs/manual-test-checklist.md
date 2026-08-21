# roam manual end-to-end checklist

Run against a real host (over Tailscale and, if available, the open
internet). Use a zmx session: `roam <host>` with ssh_config supplying
`RequestTTY yes` and the `zmx attach` command, or pass them explicitly:
`roam -t <host> zmx attach main`.

Before the manual pass, run the automated sanitizer check for the C
network monitor: `make -C internal/netmon/testdata check`. It builds the
monitor under TSan and ASan+UBSan and verifies one combined report from a
double start.

For each step, note the observed reconnect time. Expected times: 2–3 s
after the network returns; 3–5 s for a live migration (debounce included);
10–15 s for drops only keepalives can see.

- [ ] Connect; type in the session. Toggle wifi off. Within ~2s (debounce)
      a `[roam: link down — waiting for network]` line appears.
- [ ] Toggle wifi on. One durable `[roam: reconnecting…]` line appears; the
      final OpenSSH timeout/reset diagnostic does not also appear; the zmx
      session repaints; total gap 2–3 s.
- [ ] Switch wifi → personal hotspot. One `[roam: reconnecting…]` cycle;
      session repaints in 3–5 s without waiting on a timeout.
- [ ] Plug in ethernet while on wifi (primary interface changes). Same
      3–5 s reconnect cycle.
- [ ] Close the lid ≥30s; reopen. Session reattaches shortly after wake.
- [ ] Kill the connection server-side (restart sshd or drop the TCP
      connection). Reconnect happens within ~10–15s (keepalive detection,
      no path event). Confirm only roam's reconnect status remains visible;
      there is no separate OpenSSH timeout, reset, broken-pipe, or
      `client_loop` disconnect line.
- [ ] After the forced reconnect above, confirm the terminal is not stuck
      in raw mode: typing at the reattached session behaves normally, and
      after quitting roam the shell echoes input.
- [ ] zmx detach. roam exits 0; no reconnect.
- [ ] `exit` in the remote shell. roam exits 0; no reconnect.
- [ ] Run `roam <host> 'exit 255'`. roam retries under backoff (the
      documented exit-255 policy); `^C` stops it with exit 130.
- [ ] Run `roam <host> 'exit 7'`. roam exits 7 immediately.
- [ ] `~.` — roam reconnects (expected). Then `~.` followed by `^C` during
      the reconnect gap — roam exits fully.
- [ ] `^C` while wifi is off — roam exits 130.
- [ ] `kill -USR1 <roam pid>` — immediate reconnect cycle.
- [ ] `roam --quiet <host>` — no roam status lines during a wifi toggle;
      OpenSSH's disconnect diagnostic remains visible.
- [ ] Run an initial connection failure, such as `roam does-not-resolve.invalid`.
      The original OpenSSH diagnostic remains visible before roam retries.
- [ ] Run a remote command that writes ordinary stderr and exits nonzero, for
      example `roam <host> 'printf "remote stderr\\n" >&2; exit 7'`. The
      stderr text remains byte-for-byte visible and roam exits 7.
- [ ] Run `roam <host> 'printf "Write failed: Broken pipe\\n" >&2'`. The
      disconnect-shaped final stderr record remains visible and roam exits 0
      without reconnecting.
- [ ] Repeat a forced reconnect with `roam --verbose <host>`. The disconnect
      diagnostic is consolidated, verbose records are newline-delimited, and
      neither remote stdout nor ordinary stderr is erased or overwritten.
- [ ] Redirect roam stderr to a file and force a reconnect. No interactive
      status is rendered; ssh fd 2 remains direct and its diagnostic is in the
      file. Repeat with explicit ssh `-E <file>` or `-y` logging and confirm
      roam preserves that destination.
