//go:build darwin

package command_test

import "strconv"

// The kernel's live report stream can drop reports without saying so.
const listenerReportsAreLossless = false

// The kernel refuses the bind, so each program exits on the refusal; the
// server form also probes interface state on the way.
func withheldListenerSources(python string) []func(int) string {
	return []func(int) string{
		func(port int) string {
			return python + ` -c "import socket; s=socket.socket(); s.bind(('127.0.0.1', ` + strconv.Itoa(port) + `)); s.listen()"`
		},
		func(port int) string {
			return python + " -m http.server " + strconv.Itoa(port) + " --bind 127.0.0.1"
		},
	}
}
