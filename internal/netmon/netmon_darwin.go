//go:build darwin

// Package netmon reports macOS network path changes. It is a thin layer
// over nw_path_monitor and holds no policy: no debouncing, no filtering.
package netmon

/*
#cgo LDFLAGS: -framework Network
#include "netmon_darwin.h"
*/
import "C"

// Event is one path report. Fingerprint is the ordered interface list
// (name/type); a change in fingerprint means the default path migrated.
type Event struct {
	Satisfied   bool
	Fingerprint string
}

var events = make(chan Event, 16)

// Start begins monitoring and returns the event channel. Call Start once per
// process; the first report is the current path state.
func Start() <-chan Event {
	C.roam_netmon_start()
	return events
}

//export netmonPathUpdate
func netmonPathUpdate(satisfied C.int, fingerprint *C.char) {
	e := Event{Satisfied: satisfied != 0, Fingerprint: C.GoString(fingerprint)}
	for {
		select {
		case events <- e:
			return
		default:
			// The channel is full: drop the oldest report so the send never
			// blocks the dispatch queue; only the latest path state matters.
			select {
			case <-events:
			default:
			}
		}
	}
}
