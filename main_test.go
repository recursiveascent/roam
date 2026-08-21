package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/recursiveascent/roam/internal/netmon"
	"github.com/recursiveascent/roam/internal/supervisor"
)

// When ROAM_TEST_RUN is set, TestMain runs the roam program instead of the
// tests. Signal dispositions are per-process, so a test of them must start
// a real child process that runs the real program.
func TestMain(m *testing.M) {
	if args, ok := os.LookupEnv("ROAM_TEST_RUN"); ok {
		os.Exit(run(strings.Fields(args)))
	}
	os.Exit(m.Run())
}

// A Ctrl-\ in a cooked tty sends SIGQUIT to all processes in the foreground
// group. The supervisor must drop the signal and must not die in the Go
// runtime's stack-dump exit. The tty is cooked during connects and
// reconnects, and that is when the user is most likely to press the detach
// key.
func TestRunSurvivesSIGQUIT(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "ROAM_TEST_RUN=--verbose --no-defaults --ssh=/bin/sleep 30")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	lines := make(chan string, 64)
	go func() {
		r := bufio.NewReader(stderr)
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				lines <- line
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()

	// The first verbose event line shows that the event loop runs. Signal
	// registration comes before the loop in run, so it is complete at this
	// point.
	var seen []string
	deadline := time.After(10 * time.Second)
ready:
	for {
		select {
		case ln, ok := <-lines:
			if !ok {
				t.Fatalf("roam exited before first event; stderr:\n%s", strings.Join(seen, "\n"))
			}
			seen = append(seen, ln)
			if strings.Contains(ln, "[roam:") {
				break ready
			}
		case <-deadline:
			t.Fatalf("no event within 10s; stderr:\n%s", strings.Join(seen, "\n"))
		}
	}

	cmd.Process.Signal(syscall.SIGQUIT)
	time.Sleep(100 * time.Millisecond)
	cmd.Process.Signal(syscall.SIGTERM)

	for ln := range lines {
		seen = append(seen, ln)
	}
	err = cmd.Wait()
	all := strings.Join(seen, "\n")
	if strings.Contains(all, "\r") {
		t.Fatalf("non-terminal verbose output contains carriage returns: %q", all)
	}
	if strings.Contains(all, "SIGQUIT: quit") {
		t.Fatalf("supervisor died with a runtime stack dump:\n%s", all)
	}
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	}
	if code != 143 {
		t.Fatalf("exit code = %d, want 143 (clean SIGTERM shutdown); stderr:\n%s", code, all)
	}
}

type tweeCell struct {
	Text string
	Dim  bool
}

type tweeLine struct {
	Cells []tweeCell
}

type tweeSnapshot struct {
	Lines []tweeLine
}

func runTwee(t *testing.T, fakeScript, roamFlags string, ops []map[string]any, env ...string) tweeSnapshot {
	t.Helper()
	fakeSSH := filepath.Join(t.TempDir(), "fake-ssh")
	if err := os.WriteFile(fakeSSH, []byte(fakeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	script, err := json.Marshal(ops)
	if err != nil {
		t.Fatal(err)
	}

	roamArgs := strings.TrimSpace("--no-defaults " + roamFlags + " --ssh=" + fakeSSH + " host")
	cmd := exec.Command("twee", "run", "--emit", "results", "--cols", "80", "--rows", "16", "--script", "-", "--", os.Args[0])
	cmd.Stdin = bytes.NewReader(script)
	cmd.Env = append(os.Environ(), "ROAM_TEST_RUN="+roamArgs)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("twee run failed: %v\n%s", err, out)
	}

	var result struct{ Data json.RawMessage }
	dec := json.NewDecoder(bytes.NewReader(out))
	for range ops {
		if err := dec.Decode(&result); err != nil {
			t.Fatalf("decode twee result: %v\n%s", err, out)
		}
	}
	var snapshot tweeSnapshot
	if err := json.Unmarshal(result.Data, &snapshot); err != nil {
		t.Fatalf("decode twee snapshot: %v\n%s", err, out)
	}
	return snapshot
}

func snapshotText(snapshot tweeSnapshot) string {
	lines := make([]string, len(snapshot.Lines))
	for i, line := range snapshot.Lines {
		var text strings.Builder
		for _, cell := range line.Cells {
			text.WriteString(cell.Text)
		}
		lines[i] = strings.TrimRight(text.String(), " ")
	}
	return strings.Join(lines, "\n")
}

func snapshotLine(snapshot tweeSnapshot, match string) (tweeLine, bool) {
	for _, line := range snapshot.Lines {
		var text strings.Builder
		for _, cell := range line.Cells {
			text.WriteString(cell.Text)
		}
		if strings.Contains(text.String(), match) {
			return line, true
		}
	}
	return tweeLine{}, false
}

// TestReconnectStatusRendering drives the real roam binary and fake ssh
// clients under a twee PTY. It covers terminal ownership, disconnect
// consolidation and preservation, wait rendering, and fd inheritance.
func TestReconnectStatusRendering(t *testing.T) {
	if os.Getenv("ROAM_TEST_TWEE") != "1" {
		t.Skip("set ROAM_TEST_TWEE=1 (and have twee on PATH) to run")
	}
	if _, err := exec.LookPath("twee"); err != nil {
		t.Skip("twee not on PATH")
	}

	t.Run("established reconnect", func(t *testing.T) {
		fakeScript := `#!/bin/sh
marker="$0-$PPID"
if test ! -e "$marker"; then
	touch "$marker"
	trap 'printf "Read from remote host example.invalid: Operation timed out\r\n" >&2; exit 255' TERM
	printf "session ready\r\n"
	while :; do read line || :; done
fi
trap 'exit 0' TERM
printf "Last login: Fri Aug 21 07:04:02 2026\r\n"
while :; do read line || :; done
`
		ops := []map[string]any{
			{"op": "wait_text", "args": map[string]any{"text": "session ready", "timeout": "8s"}},
			{"op": "signal", "args": map[string]any{"name": "SIGUSR1"}},
			{"op": "wait_text", "args": map[string]any{"text": "[roam: reconnecting", "timeout": "8s"}},
			{"op": "wait_text", "args": map[string]any{"text": "Last login:", "timeout": "8s"}},
			{"op": "wait_stable", "args": map[string]any{"quiet": "100ms", "timeout": "1s"}},
			{"op": "snapshot", "args": map[string]any{}},
		}
		for _, tt := range []struct {
			name    string
			wantDim bool
			env     []string
		}{
			{name: "color", wantDim: true},
			{name: "no color", env: []string{"NO_COLOR=1"}},
		} {
			t.Run(tt.name, func(t *testing.T) {
				snapshot := runTwee(t, fakeScript, "", ops, tt.env...)
				text := snapshotText(snapshot)
				if strings.Contains(text, "Read from remote host") {
					t.Fatalf("raw ssh disconnect remained visible:\n%s", text)
				}
				line, statusFound := snapshotLine(snapshot, "[roam: reconnecting")
				_, loginFound := snapshotLine(snapshot, "Last login:")
				if !statusFound || !loginFound {
					t.Fatalf("status found=%v login found=%v:\n%s", statusFound, loginFound, text)
				}
				for _, cell := range line.Cells {
					if cell.Text == "[" && cell.Dim != tt.wantDim {
						t.Fatalf("status dim = %v, want %v", cell.Dim, tt.wantDim)
					}
				}
			})
		}
	})

	t.Run("initial failure remains visible", func(t *testing.T) {
		fakeScript := `#!/bin/sh
marker="$0-$PPID"
if test ! -e "$marker"; then
	touch "$marker"
	printf "Read from remote host example.invalid: Operation timed out\r\n" >&2
	exit 255
fi
printf "reconnect child\r\n"
trap 'exit 0' TERM
while :; do read line || :; done
`
		ops := []map[string]any{
			{"op": "wait_text", "args": map[string]any{"text": "reconnect child", "timeout": "8s"}},
			{"op": "wait_stable", "args": map[string]any{"quiet": "100ms", "timeout": "1s"}},
			{"op": "snapshot", "args": map[string]any{}},
		}
		text := snapshotText(runTwee(t, fakeScript, "", ops))
		if !strings.Contains(text, "Read from remote host example.invalid: Operation timed out") ||
			!strings.Contains(text, "[roam: reconnecting") {
			t.Fatalf("initial diagnostic or replacement status missing:\n%s", text)
		}
	})

	t.Run("clean exit flushes final record", func(t *testing.T) {
		fakeScript := `#!/bin/sh
printf "Connection to example.invalid closed.\r\n" >&2
read line
exit 0
`
		ops := []map[string]any{
			{"op": "key", "args": map[string]any{"key": "Enter"}},
			{"op": "wait_text", "args": map[string]any{"text": "Connection to example.invalid closed.", "timeout": "8s"}},
			{"op": "snapshot", "args": map[string]any{}},
		}
		text := snapshotText(runTwee(t, fakeScript, "", ops))
		if !strings.Contains(text, "Connection to example.invalid closed.") || strings.Contains(text, "[roam:") {
			t.Fatalf("clean exit output:\n%s", text)
		}
	})

	t.Run("stdout stderr and verbose output remain visible", func(t *testing.T) {
		fakeScript := `#!/bin/sh
marker="$0-$PPID"
if test ! -e "$marker"; then
	touch "$marker"
	printf "child stdout\r\n"
	printf "remote stderr\r\n" >&2
	trap 'printf "Read from remote host example.invalid: Operation timed out\r\n" >&2; exit 255' TERM
	while :; do read line || :; done
fi
printf "reconnected child\r\n"
trap 'exit 0' TERM
while :; do read line || :; done
`
		ops := []map[string]any{
			{"op": "wait_text", "args": map[string]any{"text": "remote stderr", "timeout": "8s"}},
			{"op": "signal", "args": map[string]any{"name": "SIGUSR1"}},
			{"op": "wait_text", "args": map[string]any{"text": "reconnected child", "timeout": "8s"}},
			{"op": "wait_stable", "args": map[string]any{"quiet": "100ms", "timeout": "1s"}},
			{"op": "snapshot", "args": map[string]any{}},
		}
		text := snapshotText(runTwee(t, fakeScript, "--verbose", ops))
		for _, want := range []string{"child stdout", "remote stderr", "[roam: event", "reconnected child"} {
			if !strings.Contains(text, want) {
				t.Fatalf("%q missing:\n%s", want, text)
			}
		}
		if strings.Contains(text, "Read from remote host") {
			t.Fatalf("raw disconnect remained visible:\n%s", text)
		}
	})

	t.Run("failed reconnect uses transient backoff status", func(t *testing.T) {
		fakeScript := `#!/bin/sh
marker="$0-$PPID"
if test ! -e "$marker"; then
	touch "$marker"
	printf "session ready\r\n"
	trap 'printf "Read from remote host example.invalid: Operation timed out\r\n" >&2; exit 255' TERM
	while :; do read line || :; done
fi
printf "Read from remote host example.invalid: Operation timed out\r\n" >&2
exit 255
`
		ops := []map[string]any{
			{"op": "wait_text", "args": map[string]any{"text": "session ready", "timeout": "8s"}},
			{"op": "signal", "args": map[string]any{"name": "SIGUSR1"}},
			{"op": "wait_text", "args": map[string]any{"text": "[roam: reconnecting", "timeout": "8s"}},
			{"op": "wait_stable", "args": map[string]any{"quiet": "100ms", "timeout": "500ms"}},
			{"op": "snapshot", "args": map[string]any{}},
		}
		text := snapshotText(runTwee(t, fakeScript, "--max-backoff=2s", ops))
		if strings.Contains(text, "Read from remote host") || !strings.Contains(text, "[roam: reconnecting") {
			t.Fatalf("backoff output:\n%s", text)
		}
	})

	t.Run("fd ownership", func(t *testing.T) {
		fakeScript := `#!/bin/sh
if test -t 0; then echo fd0=tty; else echo fd0=pipe; fi
if test -t 1; then echo fd1=tty; else echo fd1=pipe; fi
if test -t 2; then echo fd2=tty; else echo fd2=pipe; fi
`
		ops := []map[string]any{
			{"op": "wait_text", "args": map[string]any{"text": "fd2=", "timeout": "8s"}},
			{"op": "snapshot", "args": map[string]any{}},
		}
		interactive := snapshotText(runTwee(t, fakeScript, "", ops))
		for _, want := range []string{"fd0=tty", "fd1=tty", "fd2=pipe"} {
			if !strings.Contains(interactive, want) {
				t.Fatalf("interactive %q missing:\n%s", want, interactive)
			}
		}
		quiet := snapshotText(runTwee(t, fakeScript, "--quiet --verbose", ops))
		for _, want := range []string{"fd0=tty", "fd1=tty", "fd2=tty"} {
			if !strings.Contains(quiet, want) {
				t.Fatalf("quiet %q missing:\n%s", want, quiet)
			}
		}
		if strings.Contains(quiet, "reconnecting") {
			t.Fatalf("quiet status remained visible:\n%s", quiet)
		}
	})
}

func TestVersionResolution(t *testing.T) {
	previous := versionOverride
	t.Cleanup(func() { versionOverride = previous })

	versionOverride = ""
	if got := version(); got != "0.1.0" {
		t.Errorf("version() = %q, want 0.1.0", got)
	}

	versionOverride = "v9.9.9"
	if got := version(); got != "v9.9.9" {
		t.Errorf("version() with override = %q, want v9.9.9", got)
	}
}

func TestPartition(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantRoam []string
		wantSSH  []string
	}{
		{"plain destination", []string{"d.term"}, nil, []string{"d.term"}},
		{"roam flags then ssh", []string{"--quiet", "--debounce=1s", "d.term"},
			[]string{"--quiet", "--debounce=1s"}, []string{"d.term"}},
		{"ssh single-dash flags pass through", []string{"-p", "2222", "host"},
			nil, []string{"-p", "2222", "host"}},
		{"bare dash-dash ends roam flags", []string{"--quiet", "--", "--weird", "host"},
			[]string{"--quiet"}, []string{"--weird", "host"}},
		{"all roam flags", []string{"--version"}, []string{"--version"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roam, ssh := partition(tt.args)
			if !slices.Equal(roam, tt.wantRoam) || !slices.Equal(ssh, tt.wantSSH) {
				t.Fatalf("partition = %v, %v; want %v, %v",
					roam, ssh, tt.wantRoam, tt.wantSSH)
			}
		})
	}
}

func TestQuietSSHArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"plain destination", []string{"host"}, []string{"-q", "host"}},
		{"after options", []string{"-v", "-p", "2222", "host"}, []string{"-v", "-p", "2222", "-q", "host"}},
		{"before option terminator", []string{"--", "host"}, []string{"-q", "--", "host"}},
		{"option value named terminator", []string{"-F", "--", "host"}, []string{"-F", "--", "-q", "host"}},
		{"before remote command", []string{"host", "-v"}, []string{"-q", "host", "-v"}},
		{"log file preserves diagnostics", []string{"-vv", "-E", "ssh.log", "host"}, []string{"-vv", "-E", "ssh.log", "host"}},
		{"glued log file preserves diagnostics", []string{"-Essh.log", "host"}, []string{"-Essh.log", "host"}},
		{"syslog preserves diagnostics", []string{"-vy", "host"}, []string{"-vy", "host"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := quietSSHArgs(tt.args); !slices.Equal(got, tt.want) {
				t.Fatalf("quietSSHArgs = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSplitAtDestination(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantOpts []string
		wantRest []string
	}{
		{"bare", []string{"d.term"}, nil, []string{"d.term"}},
		{"separate value flag", []string{"-p", "2222", "d.term"},
			[]string{"-p", "2222"}, []string{"d.term"}},
		{"glued value", []string{"-p2222", "d.term"},
			[]string{"-p2222"}, []string{"d.term"}},
		{"boolean flags", []string{"-4", "-tt", "d.term", "zmx", "attach"},
			[]string{"-4", "-tt"}, []string{"d.term", "zmx", "attach"}},
		{"grouped boolean+value flag", []string{"-tp", "2222", "d.term"},
			[]string{"-tp", "2222"}, []string{"d.term"}},
		{"grouped -vo takes next arg", []string{"-vo", "ConnectTimeout=17", "d.term"},
			[]string{"-vo", "ConnectTimeout=17"}, []string{"d.term"}},
		{"option terminator", []string{"--", "-host"},
			[]string{"--"}, []string{"-host"}},
		{"no destination", []string{"-p", "2222"},
			[]string{"-p", "2222"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, rest := splitAtDestination(tt.args)
			if !slices.Equal(opts, tt.wantOpts) || !slices.Equal(rest, tt.wantRest) {
				t.Fatalf("splitAtDestination = %v, %v; want %v, %v",
					opts, rest, tt.wantOpts, tt.wantRest)
			}
		})
	}
}

func TestInjectDefaults(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"bare destination gets all three", []string{"d.term"}, []string{
			"-o", "ServerAliveInterval=5",
			"-o", "ServerAliveCountMax=2",
			"-o", "ConnectTimeout=5",
			"d.term",
		}},
		{"user option wins, case-insensitive", []string{"-o", "serveraliveinterval=30", "d.term"}, []string{
			"-o", "ServerAliveCountMax=2",
			"-o", "ConnectTimeout=5",
			"-o", "serveraliveinterval=30", "d.term",
		}},
		{"glued -o form is recognized", []string{"-oConnectTimeout=1", "d.term"}, []string{
			"-o", "ServerAliveInterval=5",
			"-o", "ServerAliveCountMax=2",
			"-oConnectTimeout=1", "d.term",
		}},
		{"remote command is not scanned", []string{"d.term", "some-tool", "-oConnectTimeout=1"}, []string{
			"-o", "ServerAliveInterval=5",
			"-o", "ServerAliveCountMax=2",
			"-o", "ConnectTimeout=5",
			"d.term", "some-tool", "-oConnectTimeout=1",
		}},
		{"whitespace option form wins", []string{"-o", "ConnectTimeout 17", "d.term"}, []string{
			"-o", "ServerAliveInterval=5",
			"-o", "ServerAliveCountMax=2",
			"-o", "ConnectTimeout 17", "d.term",
		}},
		{"grouped -vo option wins", []string{"-vo", "ConnectTimeout=17", "d.term"}, []string{
			"-o", "ServerAliveInterval=5",
			"-o", "ServerAliveCountMax=2",
			"-vo", "ConnectTimeout=17", "d.term",
		}},
		{"value after grouped -tp is not the destination", []string{"-tp", "2222", "-o", "ServerAliveInterval=99", "d.term"}, []string{
			"-o", "ServerAliveCountMax=2",
			"-o", "ConnectTimeout=5",
			"-tp", "2222", "-o", "ServerAliveInterval=99", "d.term",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := injectDefaults(tt.args); !slices.Equal(got, tt.want) {
				t.Fatalf("injectDefaults = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestForwardPathEventsFailsOpen(t *testing.T) {
	in := make(chan netmon.Event)
	out := make(chan supervisor.PathEvent, 1)
	go forwardPathEvents(in, out, 10*time.Millisecond)
	select {
	case e := <-out:
		if !e.Satisfied || e.Fingerprint != "" {
			t.Fatalf("synthesized event = %+v, want Satisfied with empty fingerprint", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no synthesized event within 1s")
	}
}

func TestForwardPathEventsRelaysRealEvents(t *testing.T) {
	in := make(chan netmon.Event, 1)
	in <- netmon.Event{Satisfied: true, Fingerprint: "en0/1"}
	out := make(chan supervisor.PathEvent, 1)
	go forwardPathEvents(in, out, time.Minute)
	select {
	case e := <-out:
		if e.Fingerprint != "en0/1" {
			t.Fatalf("relayed event = %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no relayed event within 1s")
	}
}
