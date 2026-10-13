//go:build linux

package gitexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lycaon/lycaon/internal/linuxbus"
)

func agentFixtureBus(t *testing.T) string {
	t.Helper()
	path := filepath.Join(agentFixtureDirectory(t), "bus")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+path)
	command := exec.Command("dbus-daemon", "--session", "--nofork", "--address=unix:path="+path)
	if err := command.Start(); err != nil {
		t.Fatalf("start private bus: %v", err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("private bus socket did not appear")
	return ""
}

type agentEnvironmentFixture struct {
	mu      sync.Mutex
	value   dbus.Variant
	queried chan []string
}

func (f *agentEnvironmentFixture) Get(iface, property string) (dbus.Variant, *dbus.Error) {
	f.mu.Lock()
	value := f.value
	f.mu.Unlock()
	f.queried <- []string{iface, property}
	return value, nil
}

func TestLinuxAgentDiscoveryReadsTheNativeUserManagerProperty(t *testing.T) {
	path := agentFixtureBus(t)
	server, err := linuxbus.Connect(t.Context(), path)
	if err != nil {
		t.Fatalf("connect fixture service: %v", err)
	}
	defer func() { _ = server.Close() }()
	socket := agentFixtureSocket(t)
	fixture := &agentEnvironmentFixture{value: dbus.MakeVariant([]string{"UNRELATED=value", "SSH_AUTH_SOCK=" + socket}), queried: make(chan []string, 2)}
	if err := server.Export(fixture, "/org/freedesktop/systemd1", "org.freedesktop.DBus.Properties"); err != nil {
		t.Fatalf("export property fixture: %v", err)
	}
	result, err := server.RequestName("org.freedesktop.systemd1", dbus.NameFlagDoNotQueue)
	if err != nil || result != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("own native service: %v, %v", result, err)
	}
	if got := sessionAgentSocket(t.Context()); got != socket {
		t.Fatalf("discovered socket = %q, want %q", got, socket)
	}
	query := <-fixture.queried
	if query[0] != "org.freedesktop.systemd1.Manager" || query[1] != "Environment" {
		t.Fatalf("native property query = %v", query)
	}
	fixture.mu.Lock()
	fixture.value = dbus.MakeVariant(true)
	fixture.mu.Unlock()
	if got := sessionAgentSocket(t.Context()); got != "" {
		t.Fatalf("accepted mistyped environment %q", got)
	}
}

func TestLinuxAgentDiscoveryRejectsMissingServiceAndPreservesExplicitSocket(t *testing.T) {
	agentFixtureBus(t)
	if got := sessionAgentSocket(t.Context()); got != "" {
		t.Fatalf("missing user manager supplied %q", got)
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "missing"))
	if got := sessionAgentSocket(t.Context()); got != "" {
		t.Fatalf("missing user bus supplied %q", got)
	}
	socket := agentFixtureSocket(t)
	t.Setenv("SSH_AUTH_SOCK", socket)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := defaultAgentSocket(ctx); got != socket {
		t.Fatalf("explicit socket = %q, want %q", got, socket)
	}
}

func TestLinuxAgentEnvironmentUsesAnExactBoundedSocketField(t *testing.T) {
	socket := agentFixtureSocket(t)
	if got := agentSocketFromEnvironment([]string{"SSH_AUTH_SOCK=" + socket}); got != socket {
		t.Fatalf("environment socket = %q", got)
	}
	for _, entries := range [][]string{
		nil, {"SSH_AUTH_SOCK=relative"}, {"OTHER_SSH_AUTH_SOCK=" + socket},
		{"SSH_AUTH_SOCK=" + socket, "SSH_AUTH_SOCK=" + socket},
		{"SSH_AUTH_SOCK=" + socket, strings.Repeat("x", 64<<10)},
		make([]string, 1025),
	} {
		if got := agentSocketFromEnvironment(entries); got != "" {
			t.Fatalf("invalid environment supplied %q", got)
		}
	}
}
