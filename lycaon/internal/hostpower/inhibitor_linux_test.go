//go:build linux

package hostpower

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/lycaon/lycaon/internal/linuxbus"
	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

type fixtureLogind struct {
	mu     sync.Mutex
	writer *os.File
	// legacy is a login manager from before block-weak existed.
	legacy bool
	calls  [][]string
}

func (f *fixtureLogind) Inhibit(what, who, why, mode string) (dbus.UnixFD, *dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, []string{what, who, why, mode})
	if f.legacy && mode == "block-weak" {
		return 0, dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", []any{"Invalid mode specification block-weak"})
	}
	return dbus.UnixFD(f.writer.Fd()), nil
}

func (f *fixtureLogind) recorded() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// serveLogind exports a login manager on the fixture bus; the returned reader
// sees EOF once every inhibitor descriptor it handed out is closed.
func serveLogind(t *testing.T, path string, legacy bool) (*fixtureLogind, *os.File) {
	t.Helper()
	server, err := linuxbus.Connect(t.Context(), path)
	testutil.FailErr(t, "connect login manager fixture", err)
	t.Cleanup(func() { _ = server.Close() })
	reader, writer, err := os.Pipe()
	testutil.FailErr(t, "create inhibitor pipe", err)
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	fixture := &fixtureLogind{writer: writer, legacy: legacy}
	testutil.FailErr(t, "export login manager fixture", server.Export(fixture, linuxbus.Login1Path, linuxbus.Login1Name+".Manager"))
	ownName(t, server, linuxbus.Login1Name)
	return fixture, reader
}

func fixtureSocketDirectory(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp(os.TempDir(), "pwb-") //nolint:usetesting // Unix socket paths must fit sockaddr_un.
	testutil.FailErr(t, "create short socket directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

type fixtureDaemon struct {
	path string
	cmd  *exec.Cmd
}

func (d *fixtureDaemon) stop() { _ = d.cmd.Process.Kill(); _ = d.cmd.Wait() }

func startFixtureBus(t *testing.T) *fixtureDaemon {
	t.Helper()
	path := filepath.Join(fixtureSocketDirectory(t), "bus")
	cmd := exec.Command("dbus-daemon", "--session", "--nofork", "--address=unix:path="+path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	testutil.FailErr(t, "start isolated bus", cmd.Start())
	daemon := &fixtureDaemon{path: path, cmd: cmd}
	t.Cleanup(daemon.stop)
	deadline := time.Now().Add(2 * time.Second)
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	var lastErr error
	for ctx.Err() == nil {
		connection, err := linuxbus.Connect(ctx, path)
		if err == nil {
			_ = connection.Close()
			return daemon
		}
		lastErr = err
		time.Sleep(time.Millisecond)
	}
	daemon.stop()
	t.Fatalf("isolated bus did not start: %v; %s", lastErr, stderr.String())
	return nil
}

func fixtureBus(t *testing.T) string { return startFixtureBus(t).path }

func connectTo(path string) busConnector {
	return func(ctx context.Context) (*dbus.Conn, error) { return linuxbus.Connect(ctx, path) }
}

func ownName(t *testing.T, server *dbus.Conn, name string) {
	t.Helper()
	result, err := server.RequestName(name, dbus.NameFlagDoNotQueue)
	testutil.FailErr(t, "own fixture service name "+name, err)
	if result != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("fixture service name %s not owned", name)
	}
}

func TestLogindLeaseTransfersDescriptorAndBlocksAutomaticSuspend(t *testing.T) {
	path := fixtureBus(t)
	server, err := linuxbus.Connect(t.Context(), path)
	testutil.FailErr(t, "connect fixture service", err)
	defer func() { _ = server.Close() }()
	reader, writer, err := os.Pipe()
	testutil.FailErr(t, "create inhibitor pipe", err)
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	fixture := &fixtureLogind{writer: writer}
	testutil.FailErr(t, "export fixture service", server.Export(fixture, linuxbus.Login1Path, linuxbus.Login1Name+".Manager"))
	ownName(t, server, linuxbus.Login1Name)
	subject := login1Provider{connect: connectTo(path)}
	if err := subject.Check(t.Context()); err != nil {
		t.Fatalf("served login manager was not advertised: %v", err)
	}
	held, err := subject.Acquire(t.Context())
	testutil.FailErr(t, "acquire native descriptor lease", err)
	defer func() { _ = held.Release() }()
	if held.Provider() != "login1" {
		t.Fatalf("provider = %q", held.Provider())
	}
	// "idle" would also hold the desktop's display awake.
	if calls := fixture.recorded(); len(calls) != 1 || !slices.Equal(calls[0], []string{"sleep", "Painted Wolf Code", "Active work", "block"}) {
		t.Fatalf("inhibitor calls = %v", calls)
	}
	flags, err := unix.FcntlInt(held.(*logindLease).file.Fd(), unix.F_GETFD, 0)
	testutil.FailErr(t, "inspect descriptor inheritance", err)
	if flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("inhibitor descriptor could reach a child process")
	}
	testutil.FailErr(t, "close service descriptor copy", writer.Close())
	ended := make(chan error, 1)
	go func() { var data [1]byte; _, err := reader.Read(data[:]); ended <- err }()
	select {
	case err := <-ended:
		t.Fatalf("lease ended before release: %v", err)
	default:
	}
	testutil.FailErr(t, "release inhibitor", held.Release())
	testutil.FailErr(t, "repeat release", held.Release())
	select {
	case err := <-ended:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("released inhibitor pipe = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("release left an inhibitor descriptor open")
	}
}

func TestLogindLeaseReportsServiceLossAndClosesItsDescriptor(t *testing.T) {
	reader, writer, err := os.Pipe()
	testutil.FailErr(t, "create service lifetime", err)
	held := &logindLease{file: writer, done: make(chan error, 1), stop: make(chan struct{}), stopped: make(chan struct{})}
	go held.watch()
	testutil.FailErr(t, "end service lifetime", reader.Close())
	select {
	case err := <-held.Done():
		if err == nil {
			t.Fatal("lost inhibitor had no failure")
		}
	case <-time.After(time.Second):
		t.Fatal("service loss was not detected")
	}
	testutil.FailErr(t, "release lost inhibitor", held.Release())
	if _, err := writer.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("lost descriptor still open: %v", err)
	}
}

func TestLogindLeaseRejectsAClosedDescriptor(t *testing.T) {
	reader, writer, err := os.Pipe()
	testutil.FailErr(t, "create closed descriptor fixture", err)
	defer func() { _ = reader.Close() }()
	testutil.FailErr(t, "close fixture descriptor", writer.Close())
	held := &logindLease{file: writer, done: make(chan error, 1), stop: make(chan struct{}), stopped: make(chan struct{})}
	go held.watch()
	select {
	case err := <-held.Done():
		if err == nil {
			t.Fatal("closed descriptor had no failure")
		}
	case <-time.After(time.Second):
		t.Fatal("closed descriptor was not detected")
	}
	if err := held.Release(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed descriptor release = %v", err)
	}
}

func layersOn(path string) sleepLayers {
	return sleepLayers{
		system:  login1Provider{connect: connectTo(path), semantics: &lockSemantics{}},
		session: portalProvider{connect: connectTo(path), appID: testAppID, unitAppID: inOwnUnit},
	}
}

func TestMissingServicesAdvertiseNoProviderAndAcquireNothing(t *testing.T) {
	path := fixtureBus(t)
	layers := layersOn(path)
	var unavailable *UnavailableError
	if err := layers.availability(); !errors.As(err, &unavailable) {
		t.Fatalf("missing services availability = %v", err)
	}
	want := []ProviderReason{
		{Provider: "login1", Code: ReasonNotServed, Reason: "org.freedesktop.login1 is not served"},
		{Provider: "portal", Code: ReasonNotServed, Reason: "org.freedesktop.portal.Desktop is not served"},
	}
	if !slices.Equal(unavailable.Reasons, want) {
		t.Fatalf("missing service reasons = %+v, want %+v", unavailable.Reasons, want)
	}
	if held, err := layers.acquire(); held != nil || !errors.As(err, &unavailable) || !slices.Equal(unavailable.Reasons, want) {
		t.Fatalf("missing services lease=%v error=%v", held, err)
	}
	client, err := linuxbus.Connect(t.Context(), path)
	testutil.FailErr(t, "connect bus without logind", err)
	defer func() { _ = client.Close() }()
	if held, err := acquireLogindLease(t.Context(), client); held != nil || err == nil {
		t.Fatalf("missing login manager lease=%v error=%v", held, err)
	}
	unreachable := connectTo(filepath.Join(t.TempDir(), "missing"))
	for _, candidate := range []provider{login1Provider{connect: unreachable}, portalProvider{connect: unreachable, appID: testAppID}} {
		if reason := reasonFor(candidate, candidate.Check(t.Context())); reason.Code != ReasonBusUnreachable {
			t.Fatalf("%s without a bus = %+v", candidate.Name(), reason)
		}
		if held, err := candidate.Acquire(t.Context()); held != nil || err == nil {
			t.Fatalf("%s acquired without a bus: %v, %v", candidate.Name(), held, err)
		}
	}
}

func TestLogindAuthenticationCannotHangHostStartup(t *testing.T) {
	path := filepath.Join(fixtureSocketDirectory(t), "bus")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	testutil.FailErr(t, "listen as silent bus", err)
	defer func() { _ = listener.Close() }()
	accepted := make(chan *net.UnixConn, 1)
	go func() { connection, _ := listener.AcceptUnix(); accepted <- connection }()
	started := time.Now()
	if bus, err := linuxbus.Connect(t.Context(), path); bus != nil || err == nil {
		t.Fatal("silent bus authenticated")
	}
	if time.Since(started) > 2*busDeadline {
		t.Fatal("silent bus exceeded the authentication deadline")
	}
	connection := <-accepted
	if connection != nil {
		_ = connection.Close()
	}
}

type fixturePortal struct {
	mu     sync.Mutex
	server *dbus.Conn
	deny   bool
	// grant is the Response code the portal sends once the backend answers.
	grant    uint32
	flags    uint32
	options  map[string]dbus.Variant
	request  dbus.ObjectPath
	closed   chan struct{}
	exported bool
}

func (f *fixturePortal) Inhibit(caller dbus.Sender, window string, flags uint32, options map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deny {
		return "", dbus.NewError("org.freedesktop.portal.Error.NotAllowed", []any{"inhibit denied"})
	}
	f.flags, f.options = flags, options
	token, _ := options["handle_token"].Value().(string)
	sender := strings.TrimPrefix(strings.ReplaceAll(string(caller), ".", "_"), ":")
	f.request = dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + sender + "/" + token)
	request, grant := f.request, f.grant
	// Like the portal, answer as soon as the backend does, which can precede
	// the method reply.
	go func() { _ = f.server.Emit(request, portalRequest+".Response", grant, map[string]dbus.Variant{}) }()
	if !f.exported {
		f.exported = true
		if err := f.server.Export(f, f.request, portalRequest); err != nil {
			return "", dbus.MakeFailedError(err)
		}
	}
	return f.request, nil
}

func (f *fixturePortal) Close() *dbus.Error {
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-f.closed:
	default:
		close(f.closed)
	}
	return nil
}

func servePortal(t *testing.T, path string, deny bool) *fixturePortal {
	t.Helper()
	return servePortalWith(t, path, deny, nil)
}

// servePortalWith advertises implemented interfaces through introspection.
func servePortalWith(t *testing.T, path string, deny bool, registry *fixtureRegistry) *fixturePortal {
	t.Helper()
	server, err := linuxbus.Connect(t.Context(), path)
	testutil.FailErr(t, "connect portal fixture", err)
	t.Cleanup(func() { _ = server.Close() })
	fixture := &fixturePortal{server: server, deny: deny, closed: make(chan struct{})}
	testutil.FailErr(t, "export portal fixture", server.Export(fixture, portalPath, portalInhibit))
	interfaces := []introspect.Interface{{Name: portalInhibit}}
	if registry != nil {
		testutil.FailErr(t, "export registry fixture", server.Export(registry, portalPath, portalRegistry))
		interfaces = append(interfaces, introspect.Interface{Name: portalRegistry})
	}
	node := &introspect.Node{Name: string(portalPath), Interfaces: append(interfaces, introspect.IntrospectData)}
	testutil.FailErr(t, "export introspection", server.Export(introspect.NewIntrospectable(node), portalPath, "org.freedesktop.DBus.Introspectable"))
	ownName(t, server, portalName)
	return fixture
}

func TestPortalInhibitorRequestsOnlySuspendAndClosesOnRelease(t *testing.T) {
	path := fixtureBus(t)
	fixture := servePortal(t, path, false)
	subject := portalProvider{connect: connectTo(path), appID: testAppID, unitAppID: inOwnUnit}
	if err := subject.Check(t.Context()); err != nil {
		t.Fatalf("served portal was not advertised: %v", err)
	}
	held, err := subject.Acquire(t.Context())
	testutil.FailErr(t, "acquire portal inhibitor", err)
	if held.Provider() != "portal" {
		t.Fatalf("provider = %q", held.Provider())
	}
	fixture.mu.Lock()
	flags, reason := fixture.flags, fixture.options["reason"].Value()
	fixture.mu.Unlock()
	// The Idle flag would suppress the desktop's screen lock.
	if flags != portalFlagSuspend || reason != "Painted Wolf Code: active work" {
		t.Fatalf("portal request flags=%d reason=%v", flags, reason)
	}
	select {
	case err := <-held.Done():
		t.Fatalf("lease ended before release: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	testutil.FailErr(t, "release portal inhibitor", held.Release())
	testutil.FailErr(t, "repeat release", held.Release())
	select {
	case <-fixture.closed:
	case <-time.After(time.Second):
		t.Fatal("release did not close the portal request")
	}
	if _, open := <-held.Done(); open {
		t.Fatal("released lease still reports completion values")
	}
}

func TestPortalDenialAndResponseEndTheLease(t *testing.T) {
	path := fixtureBus(t)
	fixture := servePortal(t, path, true)
	subject := portalProvider{connect: connectTo(path), appID: testAppID, unitAppID: inOwnUnit}
	if held, err := subject.Acquire(t.Context()); held != nil || err == nil || !strings.Contains(err.Error(), "inhibit denied") {
		t.Fatalf("denied portal lease=%v error=%v", held, err)
	}
	fixture.mu.Lock()
	fixture.deny = false
	fixture.mu.Unlock()
	held, err := subject.Acquire(t.Context())
	testutil.FailErr(t, "acquire portal inhibitor", err)
	defer func() { _ = held.Release() }()
	fixture.mu.Lock()
	request := fixture.request
	fixture.mu.Unlock()
	testutil.FailErr(t, "emit portal response", fixture.server.Emit(request, portalRequest+".Response", uint32(2), map[string]dbus.Variant{}))
	select {
	case err := <-held.Done():
		if err == nil || !strings.Contains(err.Error(), "response 2") {
			t.Fatalf("portal response = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("portal response did not end the lease")
	}
}

func TestPortalGrantDecidesTheInhibition(t *testing.T) {
	path := fixtureBus(t)
	fixture := servePortal(t, path, false)
	fixture.mu.Lock()
	fixture.grant = 2
	fixture.mu.Unlock()
	subject := portalProvider{connect: connectTo(path), appID: testAppID, unitAppID: inOwnUnit}
	if held, err := subject.Acquire(t.Context()); held != nil || err == nil || !strings.Contains(err.Error(), "refused the inhibitor (response 2)") {
		t.Fatalf("refused grant lease=%v error=%v", held, err)
	}
	select {
	case <-fixture.closed:
	case <-time.After(time.Second):
		t.Fatal("refused request was not closed")
	}
}

func TestSessionLeaseReportsBusDisconnection(t *testing.T) {
	daemon := startFixtureBus(t)
	servePortal(t, daemon.path, false)
	held, err := portalProvider{connect: connectTo(daemon.path), appID: testAppID, unitAppID: inOwnUnit}.Acquire(t.Context())
	testutil.FailErr(t, "acquire portal inhibitor", err)
	daemon.stop()
	select {
	case err := <-held.Done():
		if err == nil {
			t.Fatal("bus loss had no failure")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bus loss was not detected")
	}
	testutil.FailErr(t, "release after bus loss", held.Release())
}

const testAppID = "dev.paintedwolf.code"

func inOwnUnit() string { return testAppID }

func outsideAnyUnit() string { return "" }

func TestUnitAppIDFollowsTheDesktopEnvironmentUnitConventions(t *testing.T) {
	const manager = "0::/user.slice/user-1000.slice/user@1000.service/"
	for cgroup, want := range map[string]string{
		manager + "app.slice/app-gnome-dev.paintedwolf.code-4242.scope\n":        "dev.paintedwolf.code",
		manager + "app.slice/app-dev.paintedwolf.code-4242.scope":                "dev.paintedwolf.code",
		manager + "app.slice/app-dev.paintedwolf.code@a1b2.service":              "dev.paintedwolf.code",
		manager + "app.slice/app-gnome-dev.paintedwolf.code-4242.scope/subgroup": "dev.paintedwolf.code",
		manager + "app.slice/app-gnome-org.gnome.Evolution\\x2dalarm-7.scope":    "org.gnome.Evolution-alarm",
		manager + "app.slice/app-gnome-org.gnome.Evolution\\x2-7.scope":          "",
		manager + "app.slice/app-gnome-com.visualstudio.code-99.scope":           "com.visualstudio.code",
		manager + "app.slice/app-org.gnome.Terminal.slice/vte-spawn-1234.scope":  "",
		"0::/user.slice/user-1000.slice/session-3.scope":                         "",
		"1:name=systemd:/app-gnome-x-1.scope\n0::/user.slice/session-3.scope":    "",
		"12:cpu:/app-legacy-1.scope":                                             "",
		"":                                                                       "",
	} {
		if got := unitAppID(userUnit(cgroup)); got != want {
			t.Fatalf("unit app id of %q = %q, want %q", cgroup, got, want)
		}
	}
}

type fixtureRegistry struct {
	mu     sync.Mutex
	deny   bool
	events *[]string
}

func (f *fixtureRegistry) Register(appID string, _ map[string]dbus.Variant) *dbus.Error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deny {
		return dbus.NewError("org.freedesktop.portal.Error.Failed", []any{"connection already associated"})
	}
	*f.events = append(*f.events, "register "+appID)
	return nil
}

func TestPortalWithoutRegistryAttributesOnlyThroughTheApplicationsOwnUnit(t *testing.T) {
	path := fixtureBus(t)
	servePortal(t, path, false)
	for name, unit := range map[string]func() string{"outside any unit": outsideAnyUnit, "another application's unit": func() string { return "com.visualstudio.code" }} {
		subject := portalProvider{connect: connectTo(path), appID: testAppID, unitAppID: unit}
		if reason := reasonFor(subject, subject.Check(t.Context())); reason.Code != ReasonNoApplicationID {
			t.Fatalf("%s: availability = %+v", name, reason)
		}
		if held, err := subject.Acquire(t.Context()); held != nil || reasonFor(subject, err).Code != ReasonNoApplicationID {
			t.Fatalf("%s: acquired %v, %v", name, held, err)
		}
	}
	if err := (portalProvider{connect: connectTo(path), appID: testAppID, unitAppID: inOwnUnit}).Check(t.Context()); err != nil {
		t.Fatalf("a caller in its own application unit was refused: %v", err)
	}
	if err := (portalProvider{connect: connectTo(path), unitAppID: inOwnUnit}).Check(t.Context()); reasonFor(portalProvider{}, err).Code != ReasonNoApplicationID {
		t.Fatalf("a provider without an application id = %v", err)
	}
}

func TestPortalRegistersTheApplicationOnEveryConnection(t *testing.T) {
	path := fixtureBus(t)
	var events []string
	registry := &fixtureRegistry{events: &events}
	fixture := servePortalWith(t, path, false, registry)
	// Registration decides even inside another application's unit.
	subject := portalProvider{connect: connectTo(path), appID: testAppID, unitAppID: func() string { return "com.visualstudio.code" }}
	testutil.FailErr(t, "check registered portal", subject.Check(t.Context()))
	held, err := subject.Acquire(t.Context())
	testutil.FailErr(t, "acquire registered portal inhibitor", err)
	fixture.mu.Lock()
	inhibited := fixture.request != ""
	fixture.mu.Unlock()
	registry.mu.Lock()
	registered := slices.Clone(events)
	registry.mu.Unlock()
	if !inhibited || !slices.Equal(registered, []string{"register " + testAppID, "register " + testAppID}) {
		t.Fatalf("registration %q, inhibited %t", registered, inhibited)
	}
	testutil.FailErr(t, "release", held.Release())

	registry.mu.Lock()
	registry.deny = true
	registry.mu.Unlock()
	if reason := reasonFor(subject, subject.Check(t.Context())); reason.Code != ReasonNoApplicationID {
		t.Fatalf("refused registration availability = %+v", reason)
	}
	held, err = subject.Acquire(t.Context())
	if held != nil || reasonFor(subject, err).Code != ReasonNoApplicationID {
		t.Fatalf("refused registration = %v, %v", held, err)
	}
}
