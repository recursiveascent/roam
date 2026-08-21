# SSH Disconnect Status Consolidation Design

## Problem

During an interactive session, OpenSSH writes terminal disconnect diagnostics such as:

- `Read from remote host oberon: Connection reset by peer`
- `Read from remote host oberon: Operation timed out`
- `Connection to oberon closed by remote host.`
- `Write failed: Broken pipe`
- `client_loop: send disconnect: Broken pipe`

The child currently inherits `os.Stderr` directly. These diagnostics therefore scroll into the terminal immediately before roam renders `[roam: reconnecting…]`, producing two competing descriptions of one state transition.

Reconnect attempts already receive `-q`, but OpenSSH has terminal disconnect paths that still write to stderr. The existing twee fake suppresses its diagnostic when it sees `-q`, so it does not reproduce the reported behavior.

## Goal

When interactive roam status is enabled and the supervisor classifies an SSH exit as reconnectable, suppress the redundant OpenSSH terminal disconnect record and let roam own the reconnect/link status shown while no SSH child owns the terminal.

## Non-goals

- Hiding initial connection, authentication, configuration, or host-key errors.
- Deliberately hiding remote command stderr.
- Changing `--quiet` or non-terminal behavior.
- Replacing SSH's stdin/stdout terminal path or proxying its remote PTY.
- Preserving exact cross-stream ordering between direct stdout and filtered stderr for non-PTY remote commands while consolidation is enabled.
- Keeping a roam transient visible while an SSH child owns the terminal.
- Changing the existing roam status wording.
- Resolving SSH's existing exit-status-255 ambiguity between connection failure and a remote command that exits 255.

## Child stderr interception

Keep direct stdin/stdout and reconnect-time `-q`. When stderr is a terminal and roam status is enabled, assign `exec.Cmd.Stderr` a narrow per-child streaming filter whose downstream writer is the status printer.

Because that writer is not an `*os.File`, Go connects child fd 2 through a pipe. This is a deliberate, mode-scoped semantic change: fd 0 and fd 1 remain attached directly to the user's terminal, while fd 2 is streamed by Go's copy goroutine. SSH sees a non-terminal fd 2, and exact ordering between direct stdout and filtered stderr is not guaranteed. Ordinary stderr remains byte-preserving and ordered within its own stream; only one final matching candidate can be delayed. `--quiet` and non-terminal stderr retain direct inherited fd 2 behavior.

The filter recognizes only complete terminal disconnect records emitted by supported OpenSSH client paths:

- `Read from remote host <host>: <reason>`
- `Connection to <host> closed by remote host.`
- `Connection to <host> closed.`
- `Write failed: <reason>`
- `client_loop: send disconnect: <reason>`

It retains a matching record only while it is the final stderr record from that child. Any subsequent stderr byte proves it was not the terminal record and causes it to be forwarded before the new data.

## Consolidation eligibility

After the child exits, the supervisor applies its existing state and exit-255 policy. A pending record is discarded only when all of these are true:

1. Interactive status consolidation is enabled.
2. The record exactly matches a supported disconnect form.
3. The exit classification emits a visible replacement status before retrying or waiting rather than terminating roam.
4. The child was an established session, a reconnect attempt, or was killed by roam for a network transition.

An initial child that exits before establishment without a roam-initiated network transition flushes its diagnostic, even if it exits 255. This preserves initial connection failures. An established initial child that later loses its connection is consolidated, covering the reported network-change case.

A reconnectable exit always chooses one replacement form before its diagnostic may be discarded:

- an immediate retry emits a newline-terminated durable `[roam: reconnecting…]` record before spawning, so the notice remains visible while the child owns the terminal;
- retry backoff or path-settle waiting uses the existing in-place reconnecting transient; and
- an unavailable path uses the existing in-place link-down transient.

The durable immediate-retry record is emitted once for that exit and leaves no active transient row. Subsequent failed attempts use the transient during their wait states, which is cleared before the next spawn.

For an eligible exit, the runner discards the retained record. For every terminal or ineligible exit, it flushes the record unchanged. The output finish occurs before verbose exit logging, reports, timers, respawns, or program exit.

SSH provides no provenance marker that distinguishes its own stderr records from remote-command stderr after both enter local fd 2. A remote command that emits one exact disconnect-shaped final record and exits 255 in reconnect context is therefore indistinguishable from a connection failure: roam already reconnects under that policy, and the matching final record will also be consolidated. This narrow collision is documented and tested.

## Terminal-row ownership

Roam owns a transient row only while no SSH child owns the terminal.

Before every `Cmd.Start`, the status printer relinquishes its transient row: it clears an active transient and marks the child as terminal owner before the process can emit direct stdout. This removes the race where `Last login`, a prompt, or other child stdout advances the cursor and a later roam clear erases the wrong row.

While a child owns the terminal:

- the printer never clears or redraws a transient;
- filtered ordinary stderr is forwarded unchanged;
- interactive verbose logs are serialized with filtered stderr and are emitted as newline-delimited durable records prefixed with `\r\n`, so they may interrupt output but never erase it; and
- verbose `reconnected` output is also durable rather than transient.

After `Cmd.Wait` and stderr-copy completion, the runner first flushes or discards the retained candidate. The printer then marks the child finished. The next roam status begins with `\r\n` to establish a fresh row without assuming where direct stdout left the cursor; later no-child status updates overwrite that known transient row in place.

Reconnect reporting has two forms, both used only when no child currently owns the terminal:

- a durable reconnecting record immediately before an immediate retry; and
- transient status during retry backoff, path-settle waits, link-down waits, or persistent-failure waits.

The durable record ends at a known line boundary before `Cmd.Start`. A transient is cleared before `Cmd.Start`. Thus neither form remains active while the child can produce stdout.

## Streaming behavior

The filter handles records split across writes and multiple records in one write. It buffers only while bytes at the start of a line remain candidates for known prefixes. Non-candidate bytes are forwarded immediately through the synchronized printer. Candidate records are bounded; if a candidate exceeds a small fixed limit, it is forwarded. A trailing incomplete record is always forwarded when the child finishes.

At most one complete matching record remains pending after `Cmd.Wait`. The runner exposes a finish operation that writes or discards it. Connection context and supervisor exit classification, not text matching alone, choose suppression.

## Verbose ordering

`runShell` currently logs an event immediately after `decide`, before dispatching commands. For a child-exit event, it will instead:

1. dispatch the leading child-output finish command;
2. mark the child finished in the printer;
3. emit the verbose transition log through the printer; and
4. dispatch the remaining report, timer, spawn, or exit commands.

Other event types retain their existing debug ordering. Thus a retained diagnostic is flushed before the verbose line describing its exit, while an eligible diagnostic is discarded before any replacement status.

## Code changes

- `main.go`: pass the interactive child-stderr sink into the supervisor, route interactive `--verbose` logging through the printer, and retain reconnect-time `-q` construction.
- `internal/supervisor/core.go`: include pending-record presence in child-exit events; emit ordered flush/discard commands using establishment/reconnect/kill context; pair every discarded diagnostic with either a durable immediate-retry notice or a no-child transient.
- `internal/supervisor/shell.go`: relinquish transient status before `Cmd.Start`; track successful child ownership; finish child output and ownership before child-exit verbose logging and remaining commands.
- `internal/supervisor/runner.go`: add the bounded filter, retain at most one final matching record after `Cmd.Wait`, and flush or discard it when directed.
- `internal/status/status.go`: synchronize status, durable reconnect notices, verbose logs, and filtered stderr; track child ownership; prohibit transient clears while a child owns the terminal; and establish a fresh row after child exit.
- `internal/supervisor/core_test.go`: cover established initial failures, pre-establishment initial failures, immediate-retry replacement notices, reconnect attempts, roam-initiated kills, clean exits, requested termination, no-child transient reporting, persistent failures, and command ordering.
- `internal/supervisor/runner_test.go`: cover fragmented/coalesced writes, every recognized form including `client_loop`, pass-through, overflow, incomplete records, high-volume no-deadlock behavior, per-stream byte ordering, and the exact-record-plus-255 ambiguity.
- `internal/supervisor/shell_test.go`: verify pre-spawn relinquishment and child-exit ordering around output finalization, ownership, verbose logging, and remaining commands.
- `internal/status/status_test.go`: cover durable reconnect notices, relinquishment before child output, stdout-then-stderr safety, durable verbose output while child-owned, fresh-row acquisition after exit, and in-place updates while childless.
- `main_test.go`: make the twee fake emit diagnostics regardless of `-q`; cover the durable immediate-retry notice, child stdout followed by stderr, reconnect backoff, initial failures, clean exits, fd 0/1 remaining terminal-backed, fd 2 being piped during consolidation, and `--quiet` retaining direct fd 2.
- `README.md`: document consolidation eligibility, status visibility only while childless, initial-error preservation, supported forms, the piped-fd-2/cross-stream-ordering tradeoff, and the narrow remote-stderr/exit-255 ambiguity.

## Verification

1. Run focused supervisor, runner, and status tests with the race detector.
2. Run `make check`.
3. Run `go test -race ./...`.
4. With `twee`, run the reconnect rendering tests and inspect child stdout followed by stderr/verbose output, initial failure, backoff status, clean close, and successful reconnect cases.
