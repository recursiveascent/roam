// Command roam supervises ssh for interactive sessions, reconnecting on
// macOS network path events instead of waiting for timeouts.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/paulsmith/roam/internal/netmon"
	"github.com/paulsmith/roam/internal/status"
	"github.com/paulsmith/roam/internal/supervisor"
)

const version = "0.1.0"

// netmonStartTimeout bounds the wait for the monitor's first report; past
// it, roam fails open and lets ssh's own timeouts judge the network.
const netmonStartTimeout = 3 * time.Second

const usage = `usage: roam [--flags] <ssh args...>

roam flags come first and use --name or --name=value form; everything from
the first argument not starting with "--" onward passes to ssh verbatim.

  --debounce=<dur>     wait before acting on a path change (default 2s)
  --max-backoff=<dur>  longest retry delay while the path is up (default 30s)
  --quiet              suppress status lines
  --verbose            log state transitions
  --no-defaults        do not inject ssh keepalive defaults
  --ssh=<path>         ssh binary to run (default: ssh from PATH)
  --netmon-debug       dump raw network path events; ^C to exit
  --version            print version
`

type roamFlags struct {
	debounce    time.Duration
	maxBackoff  time.Duration
	quiet       bool
	verbose     bool
	noDefaults  bool
	sshPath     string
	netmonDebug bool
	version     bool
}

// partition splits argv into roam's own flags and ssh's arguments: leading
// "--"-prefixed arguments are roam's; a bare "--" ends roam's flags.
func partition(args []string) (roamArgs, sshArgs []string) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:]
		}
		if !strings.HasPrefix(a, "--") {
			return args[:i], args[i:]
		}
	}
	return args, nil
}

func parseFlags(args []string) (roamFlags, error) {
	var f roamFlags
	fs := flag.NewFlagSet("roam", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	fs.DurationVar(&f.debounce, "debounce", 2*time.Second, "")
	fs.DurationVar(&f.maxBackoff, "max-backoff", 30*time.Second, "")
	fs.BoolVar(&f.quiet, "quiet", false, "")
	fs.BoolVar(&f.verbose, "verbose", false, "")
	fs.BoolVar(&f.noDefaults, "no-defaults", false, "")
	fs.StringVar(&f.sshPath, "ssh", "ssh", "")
	fs.BoolVar(&f.netmonDebug, "netmon-debug", false, "")
	fs.BoolVar(&f.version, "version", false, "")
	return f, fs.Parse(args)
}

// sshValueFlags are the ssh flags that consume a separate value argument,
// from ssh's getopt string.
var sshValueFlags = map[byte]bool{
	'B': true, 'b': true, 'c': true, 'D': true, 'E': true, 'e': true,
	'F': true, 'I': true, 'i': true, 'J': true, 'L': true, 'l': true,
	'm': true, 'O': true, 'o': true, 'P': true, 'p': true, 'Q': true,
	'R': true, 'S': true, 'W': true, 'w': true,
}

// scanOptions walks the leading ssh option arguments and returns the index
// where the destination begins (len(args) if none). ssh accepts grouped
// short options (-tp 2222 is -t plus -p taking the next argument, -vo X is
// -v plus -o X), so each dash cluster is walked character by character: the
// first value-taking flag consumes the glued remainder or the next
// argument. visit, if non-nil, receives each value-taking flag and its
// value.
func scanOptions(args []string, visit func(flag byte, value string)) int {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "" || a == "-" || a[0] != '-' {
			return i
		}
		for j := 1; j < len(a); j++ {
			if !sshValueFlags[a[j]] {
				continue // boolean flag; the next char is another flag
			}
			value := ""
			if j == len(a)-1 {
				if i+1 < len(args) {
					i++
					value = args[i]
				}
			} else {
				value = a[j+1:]
			}
			if visit != nil {
				visit(a[j], value)
			}
			break
		}
	}
	return len(args)
}

// splitAtDestination divides ssh args into the options before the
// destination and everything from the destination on (the destination and
// the remote command). With no destination found, rest is nil.
func splitAtDestination(args []string) (opts, rest []string) {
	i := scanOptions(args, nil)
	if i == len(args) {
		return args, nil
	}
	return args[:i], args[i:]
}

// sshDefaults are injected unless the user passes the same option among the
// ssh options (never matched against the remote command). SSH option names
// are case-insensitive.
var sshDefaults = []string{
	"ServerAliveInterval=5",
	"ServerAliveCountMax=2",
	"ConnectTimeout=5",
}

// optionName extracts the lowercase option keyword from an -o argument,
// accepting both "Name=value" and ssh_config's "Name value" form.
func optionName(opt string) string {
	name, _, _ := strings.Cut(opt, "=")
	if fields := strings.Fields(name); len(fields) > 0 {
		name = fields[0]
	}
	return strings.ToLower(name)
}

func injectDefaults(args []string) []string {
	supplied := map[string]bool{}
	scanOptions(args, func(flag byte, value string) {
		if flag == 'o' {
			supplied[optionName(value)] = true
		}
	})
	var out []string
	for _, d := range sshDefaults {
		if !supplied[optionName(d)] {
			out = append(out, "-o", d)
		}
	}
	return append(out, args...)
}

// forwardPathEvents relays netmon reports to the supervisor. If the first
// report does not arrive within timeout, it synthesizes a satisfied event:
// fail open and let ssh's ConnectTimeout and keepalives judge the network.
func forwardPathEvents(in <-chan netmon.Event, out chan<- supervisor.PathEvent, timeout time.Duration) {
	select {
	case e := <-in:
		out <- supervisor.PathEvent{Satisfied: e.Satisfied, Fingerprint: e.Fingerprint}
	case <-time.After(timeout):
		// An empty fingerprint means "baseline unknown": the core adopts
		// the first real report instead of treating it as a migration.
		out <- supervisor.PathEvent{Satisfied: true}
	}
	for e := range in {
		out <- supervisor.PathEvent{Satisfied: e.Satisfied, Fingerprint: e.Fingerprint}
	}
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	roamPart, sshArgs := partition(args)
	f, err := parseFlags(roamPart)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if f.version {
		fmt.Println("roam " + version)
		return 0
	}
	if f.netmonDebug {
		return netmonDebug()
	}
	if len(sshArgs) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}

	sshPath, err := exec.LookPath(f.sshPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "roam: %v\n", err)
		return 1
	}
	if !f.noDefaults {
		sshArgs = injectDefaults(sshArgs)
	}

	fi, statErr := os.Stderr.Stat()
	isTTY := statErr == nil && fi.Mode()&os.ModeCharDevice != 0
	rep := status.New(os.Stderr, isTTY, f.quiet, f.verbose)

	pathc := make(chan supervisor.PathEvent, 16)
	go forwardPathEvents(netmon.Start(), pathc, netmonStartTimeout)

	sigc := make(chan os.Signal, 8)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR1)

	var debugf func(string, ...any)
	if f.verbose {
		debugf = func(format string, a ...any) {
			fmt.Fprintf(os.Stderr, "[roam: "+format+"]\n", a...)
		}
	}

	return supervisor.Run(supervisor.Options{
		SSHPath:    sshPath,
		SSHArgs:    sshArgs,
		Debounce:   f.debounce,
		MaxBackoff: f.maxBackoff,
		Report:     rep,
		PathEvents: pathc,
		Signals:    sigc,
		Debugf:     debugf,
	})
}

func netmonDebug() int {
	fmt.Println("roam: dumping network path events; ^C to exit")
	for e := range netmon.Start() {
		fmt.Printf("satisfied=%v fingerprint=%q\n", e.Satisfied, e.Fingerprint)
	}
	return 0
}
