//go:build darwin

package confine_test

import (
	"os/exec"
	"testing"
)

// Where the Homebrew bind's EPERM comes from: no confinement, and the system
// sandbox-exec with a profile that denies only network-bind.
func TestListenerRefusalOrigin(t *testing.T) {
	bind := "import socket; s=socket.socket(); s.bind(('127.0.0.1', 18786)); print('bound')"
	for _, args := range [][]string{
		{"/opt/homebrew/bin/python3", "-c", bind},
		{"/usr/bin/python3", "-c", bind},
		{"/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)(deny network-bind)", "/opt/homebrew/bin/python3", "-c", bind},
		{"/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)", "/opt/homebrew/bin/python3", "-c", bind},
		{"/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)(deny network-bind)", "/usr/bin/python3", "-c", bind},
	} {
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		t.Logf("%v => err=%v\n%s", args, err, out)
	}
}
