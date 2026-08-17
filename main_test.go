package main

import (
	"slices"
	"testing"
	"time"

	"github.com/paulsmith/roam/internal/netmon"
	"github.com/paulsmith/roam/internal/supervisor"
)

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
