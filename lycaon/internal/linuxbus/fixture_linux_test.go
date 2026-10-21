//go:build linux

package linuxbus

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
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
	cmd := exec.Command("dbus-daemon", "--session", "--nofork", "--address=unix:path="+path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start isolated bus: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("isolated bus did not start: %s", stderr.String())
	return ""
}

type fixtureLoginManager struct {
	sessionPath dbus.ObjectPath
	fail        bool
}

func (f *fixtureLoginManager) GetSessionByPID(uint32) (dbus.ObjectPath, *dbus.Error) {
	if f.fail {
		return "", dbus.NewError("org.freedesktop.login1.NoSessionForPID", nil)
	}
	return f.sessionPath, nil
}

// seatRecord marshals as the login manager's (so) seat property.
type seatRecord struct {
	ID   string
	Path dbus.ObjectPath
}

type fixtureLoginSession struct {
	properties map[string]dbus.Variant
}

func (f *fixtureLoginSession) GetAll(string) (map[string]dbus.Variant, *dbus.Error) {
	return f.properties, nil
}

func TestCurrentLoginSessionReadsTheSessionOwningThisProcess(t *testing.T) {
	path := fixtureBus(t)
	server, err := Connect(t.Context(), path)
	if err != nil {
		t.Fatalf("connect fixture service: %v", err)
	}
	defer func() { _ = server.Close() }()
	manager := &fixtureLoginManager{sessionPath: "/org/freedesktop/login1/session/_32"}
	if err := server.Export(manager, Login1Path, Login1Name+".Manager"); err != nil {
		t.Fatalf("export manager: %v", err)
	}
	session := &fixtureLoginSession{properties: map[string]dbus.Variant{
		"Id":     dbus.MakeVariant("2"),
		"Type":   dbus.MakeVariant("wayland"),
		"Class":  dbus.MakeVariant("user"),
		"Active": dbus.MakeVariant(true),
		"Seat":   dbus.MakeVariant(seatRecord{ID: "seat0", Path: "/org/freedesktop/login1/seat/seat0"}),
	}}
	if err := server.Export(session, manager.sessionPath, "org.freedesktop.DBus.Properties"); err != nil {
		t.Fatalf("export session: %v", err)
	}
	if reply, err := server.RequestName(Login1Name, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("own login manager name: %v, %v", reply, err)
	}
	client, err := Connect(t.Context(), path)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	defer func() { _ = client.Close() }()
	state, err := DiscoverName(t.Context(), client, Login1Name)
	if err != nil || state.Owner != server.Names()[0] {
		t.Fatalf("login manager ownership = %+v, %v", state, err)
	}
	if state, err := DiscoverName(t.Context(), client, "org.freedesktop.absent"); err != nil || state != (NameState{}) {
		t.Fatalf("absent name ownership = %+v, %v", state, err)
	}
	got, err := CurrentLoginSession(t.Context(), client)
	if err != nil {
		t.Fatalf("current login session: %v", err)
	}
	want := LoginSession{ID: "2", Path: manager.sessionPath, Type: "wayland", Class: "user", Seat: "seat0", Active: true}
	if got != want {
		t.Fatalf("login session = %+v, want %+v", got, want)
	}
	manager.fail = true
	if _, err := CurrentLoginSession(t.Context(), client); err == nil {
		t.Fatal("a process outside every session resolved one")
	}
	delete(session.properties, "Id")
	manager.fail = false
	if _, err := CurrentLoginSession(t.Context(), client); err == nil {
		t.Fatal("a session without an identifier was accepted")
	}
	if _, err := DiscoverName(t.Context(), nil, Login1Name); err == nil {
		t.Fatal("nil bus was accepted")
	}
	if _, err := CurrentLoginSession(t.Context(), nil); err == nil {
		t.Fatal("nil bus was accepted for session lookup")
	}
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
