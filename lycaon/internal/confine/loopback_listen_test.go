package confine_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
)

// The direct-IP boundary permits local listeners.
func TestSeatbeltDirectIPAllowsLocalListener(t *testing.T) {
	self := requireSeatbelt(t)
	python := requirePython(t)
	c := confine.Confinement{Roots: []string{t.TempDir()}, Network: confine.NetworkDirectIP}

	const bindLoopback = `
import socket, sys
s = socket.socket()
try:
    s.bind(('127.0.0.1', 0))
    s.listen(1)
    print('BOUND', s.getsockname()[1])
except OSError as e:
    print('DENIED', e); sys.exit(1)
`
	code, out := confinedRun(t, self, c, python, "-c", bindLoopback)
	if code != 0 || !strings.Contains(out, "BOUND") {
		t.Fatalf("confined command must bind a loopback listener: exit=%d out=%q", code, out)
	}
}

// The local bind rule covers wildcard addresses.
func TestSeatbeltBindAddressIsNotBounded(t *testing.T) {
	self := requireSeatbelt(t)
	python := requirePython(t)
	c := confine.Confinement{Roots: []string{t.TempDir()}, Network: confine.NetworkDirectIP}

	const bindWildcard = `
import socket, sys
s = socket.socket()
try:
    s.bind(('0.0.0.0', 0))
    s.listen(1)
    print('BOUND')
except OSError as e:
    print('DENIED', e); sys.exit(1)
`
	code, out := confinedRun(t, self, c, python, "-c", bindWildcard)
	if code != 0 || !strings.Contains(out, "BOUND") {
		t.Fatalf("wildcard bind failed: exit=%d out=%q", code, out)
	}
}

// Terminal allocation requires a writable slave device.
func TestSeatbeltAllowsPTYAllocation(t *testing.T) {
	self := requireSeatbelt(t)
	python := requirePython(t)
	c := confine.Confinement{Roots: []string{t.TempDir()}}

	const openPTY = `
import pty, sys
try:
    m, s = pty.openpty()
    print('PTY OK')
except OSError as e:
    print('DENIED', e); sys.exit(1)
`
	code, out := confinedRun(t, self, c, python, "-c", openPTY)
	if code != 0 || !strings.Contains(out, "PTY OK") {
		t.Fatalf("confined command must allocate a pty: exit=%d out=%q", code, out)
	}
}

func requirePython(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required for syscall-level sandbox probes")
	}
	return path
}
