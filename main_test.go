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
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
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

// TestReconnectStatusRendering drives the real roam binary under a twee
// pty through a loop of failing reconnects (a fake ssh that exits 255) and
// asserts the transient status renders in place on row 0 with dim ANSI
// color, that rows below stay blank, and that NO_COLOR strips the color
// while leaving the in-place layout intact.
//
// Gated on ROAM_TEST_TWEE=1 and twee on PATH so it never runs in CI without
// an explicit opt-in: it needs a real PTY and a live network monitor.
func TestReconnectStatusRendering(t *testing.T) {
	if os.Getenv("ROAM_TEST_TWEE") != "1" {
		t.Skip("set ROAM_TEST_TWEE=1 (and have twee on PATH) to run")
	}
	if _, err := exec.LookPath("twee"); err != nil {
		t.Skip("twee not on PATH")
	}

	fakeSSH := filepath.Join(t.TempDir(), "fake-ssh")
	fakeScript := `#!/bin/sh
marker="` + fakeSSH + `-$PPID"
quiet=false
for arg do
	if test "$arg" = -q; then quiet=true; fi
done
if test -e "$marker" && ! $quiet; then
	echo "ssh: connect to host example.invalid port 22: Network is unreachable" >&2
fi
touch "$marker"
exit 255
`
	if err := os.WriteFile(fakeSSH, []byte(fakeScript), 0o755); err != nil {
		t.Fatal(err)
	}

	// roam args reused across both color and no-color runs. The backoff leaves
	// a deterministic quiet window after each failed attempt for inspection.
	roamArgs := "--no-defaults --max-backoff=500ms --ssh=" + fakeSSH + " host"

	runCase := func(t *testing.T, wantDim bool) {
		t.Helper()
		// Wait for the first transient, then inspect the rendered cells: the
		// status must remain on row 0, keep its dim attribute when enabled,
		// and leave every row below blank despite failed ssh attempts.
		ops := []map[string]any{
			{"op": "wait_text", "args": map[string]any{"text": "[roam: reconnecting", "timeout": "8s"}},
			{"op": "wait_stable", "args": map[string]any{"quiet": "100ms", "timeout": "1s"}},
			{"op": "snapshot", "args": map[string]any{}},
		}
		script, err := json.Marshal(ops)
		if err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command("twee", "run", "--emit", "results", "--cols", "80", "--rows", "10", "--script", "-", "--", os.Args[0])
		cmd.Stdin = bytes.NewReader(script)
		cmd.Env = append(os.Environ(), "ROAM_TEST_RUN="+roamArgs)
		if !wantDim {
			cmd.Env = append(cmd.Env, "NO_COLOR=1")
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("twee run failed: %v\n%s", err, out)
		}

		var waitResult, stableResult, snapshotResult struct {
			Data json.RawMessage
		}
		dec := json.NewDecoder(bytes.NewReader(out))
		if err := dec.Decode(&waitResult); err != nil {
			t.Fatalf("decode twee wait result: %v\n%s", err, out)
		}
		if err := dec.Decode(&stableResult); err != nil {
			t.Fatalf("decode twee stable result: %v\n%s", err, out)
		}
		if err := dec.Decode(&snapshotResult); err != nil {
			t.Fatalf("decode twee snapshot result: %v\n%s", err, out)
		}
		var snapshot struct {
			Lines []struct {
				Cells []struct {
					Text string
					Dim  bool
				}
			}
		}
		if err := json.Unmarshal(snapshotResult.Data, &snapshot); err != nil {
			t.Fatalf("decode twee snapshot: %v\n%s", err, out)
		}
		if got := snapshot.Lines[0].Cells[0]; got.Text != "[" || got.Dim != wantDim {
			t.Fatalf("status first cell = %+v, want text '[' and dim %v", got, wantDim)
		}
		for y, line := range snapshot.Lines[1:] {
			for _, cell := range line.Cells {
				if cell.Text != "" {
					t.Fatalf("row %d is not blank; first text cell %q", y+1, cell.Text)
				}
			}
		}
	}

	t.Run("color", func(t *testing.T) { runCase(t, true) })
	t.Run("no_color", func(t *testing.T) { runCase(t, false) })
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
