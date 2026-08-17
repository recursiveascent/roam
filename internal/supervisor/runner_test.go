package supervisor

import (
	"syscall"
	"testing"
	"time"
)

func TestProcRunnerReportsExitCode(t *testing.T) {
	r := &procRunner{sshPath: "/bin/sh", args: []string{"-c", "exit 7"}}
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	got := r.wait()
	want := childExited{code: 7}
	if got != want {
		t.Fatalf("wait() = %+v, want %+v", got, want)
	}
}

func TestProcRunnerReportsSignalDeath(t *testing.T) {
	r := &procRunner{sshPath: "/bin/sh", args: []string{"-c", "kill -TERM $$"}}
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	got := r.wait()
	want := childExited{signaled: true, signum: int(syscall.SIGTERM)}
	if got != want {
		t.Fatalf("wait() = %+v, want %+v", got, want)
	}
}

func TestProcRunnerKillTerminatesChild(t *testing.T) {
	r := &procRunner{sshPath: "/bin/sh", args: []string{"-c", "sleep 30"}}
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan childExited, 1)
	go func() { done <- r.wait() }()
	r.kill()
	select {
	case got := <-done:
		if !got.signaled || got.signum != int(syscall.SIGTERM) {
			t.Fatalf("wait() = %+v, want SIGTERM death", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child did not die within 5s of kill()")
	}
}

func TestProcRunnerKillEscalatesToSIGKILL(t *testing.T) {
	r := &procRunner{
		sshPath:    "/bin/sh",
		args:       []string{"-c", `trap "" TERM; while :; do :; done`},
		escalation: 100 * time.Millisecond,
	}
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	done := make(chan childExited, 1)
	go func() { done <- r.wait() }()
	r.kill()
	select {
	case got := <-done:
		if !got.signaled || got.signum != int(syscall.SIGKILL) {
			t.Fatalf("wait() = %+v, want SIGKILL death after escalation", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child survived SIGKILL escalation window")
	}
}

func TestProcRunnerStartErrorOnMissingBinary(t *testing.T) {
	r := &procRunner{sshPath: "/nonexistent/ssh"}
	if err := r.start(); err == nil {
		t.Fatal("start() = nil error, want failure")
	}
}

func TestProcRunnerDefensiveWithoutChild(t *testing.T) {
	r := &procRunner{sshPath: "/bin/sh"}
	r.kill()
	got := r.wait()
	if got.code != 255 {
		t.Fatalf("wait() without child = %+v, want code 255", got)
	}
}
