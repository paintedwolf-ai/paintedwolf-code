//go:build linux

package linuxbus

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// fixtureBus starts a private session bus daemon and returns its socket path.
func fixtureBus(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp(os.TempDir(), "pwb-") //nolint:usetesting // Unix socket paths must fit sockaddr_un.
	if err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	path := filepath.Join(directory, "bus")
	cmd := exec.CommandContext(t.Context(), "dbus-daemon", "--session", "--nofork", "--address=unix:path="+path, "--print-address=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	ready, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("create isolated bus readiness pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start isolated bus: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("isolated bus stderr: %s", stderr.String())
		}
	})
	// The daemon prints its address after initialization, when it can accept clients.
	if _, err := bufio.NewReader(ready).ReadString('\n'); err != nil {
		t.Fatalf("wait for isolated bus readiness: %v", err)
	}
	return path
}

func TestSessionConnectTriesAlternativesAndReportsStaleSockets(t *testing.T) {
	path := fixtureBus(t)
	stale := filepath.Join(filepath.Dir(path), "stale")
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatalf("write stale socket placeholder: %v", err)
	}
	session := Session{Source: SessionSourceAddress, Addresses: []SocketAddress{{Path: stale}, {Path: path}}}
	bus, err := session.Connect(t.Context())
	if err != nil {
		t.Fatalf("connect past stale alternative: %v", err)
	}
	_ = bus.Close()
	if bus, err := (Session{Addresses: []SocketAddress{{Path: stale}}}).Connect(t.Context()); err == nil || bus != nil {
		t.Fatalf("stale socket connected: %v, %v", bus, err)
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+path)
	bus, err = ConnectSession(t.Context())
	if err != nil {
		t.Fatalf("connect inherited session: %v", err)
	}
	_ = bus.Close()
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "tcp:host=remote")
	if _, err := ConnectSession(t.Context()); err == nil {
		t.Fatal("non-local inherited session connected")
	}
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+path)
	bus, err = ConnectSystem(t.Context())
	if err != nil {
		t.Fatalf("connect explicit system bus: %v", err)
	}
	_ = bus.Close()
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "tcp:host=remote")
	if _, err := ConnectSystem(t.Context()); err == nil {
		t.Fatal("non-local system bus connected")
	}
}
