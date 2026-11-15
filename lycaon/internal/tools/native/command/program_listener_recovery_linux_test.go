//go:build linux

package command_test

import "strconv"

// The launch's supervisor performs and reports every bind itself.
const listenerReportsAreLossless = true

// The listener binds inside the sandbox, so the program exits on its own.
func withheldListenerSources(python string) []func(int) string {
	return []func(int) string{
		func(port int) string {
			return python + ` -c "import socket, sys; s=socket.socket(); s.bind(('127.0.0.1', ` + strconv.Itoa(port) + `)); s.listen(); sys.exit(3)"`
		},
	}
}
