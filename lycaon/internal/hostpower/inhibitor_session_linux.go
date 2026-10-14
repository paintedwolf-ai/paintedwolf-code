//go:build linux

package hostpower

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

const (
	portalName        = "org.freedesktop.portal.Desktop"
	portalPath        = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	portalInhibit     = "org.freedesktop.portal.Inhibit"
	portalRequest     = "org.freedesktop.portal.Request"
	portalRegistry    = "org.freedesktop.host.portal.Registry"
	portalFlagSuspend = uint32(4)
	portalFlagIdle    = uint32(8)

	powerManagementName    = "org.freedesktop.PowerManagement"
	powerManagementPath    = dbus.ObjectPath("/org/freedesktop/PowerManagement/Inhibit")
	powerManagementInhibit = "org.freedesktop.PowerManagement.Inhibit"
)

// sessionLease holds an inhibition that lives as long as its bus connection.
type sessionLease struct {
	bus          *dbus.Conn
	name         string
	release      func(context.Context) error
	responses    chan *dbus.Signal
	request      dbus.ObjectPath
	done         chan error
	stop         chan struct{}
	stopped      chan struct{}
	once         sync.Once
	releaseError error
}

func newSessionLease(bus *dbus.Conn, name string, release func(context.Context) error, responses chan *dbus.Signal, request dbus.ObjectPath) *sessionLease {
	held := &sessionLease{bus: bus, name: name, release: release, responses: responses, request: request, done: make(chan error, 1), stop: make(chan struct{}), stopped: make(chan struct{})}
	go held.watch()
	return held
}

func (l *sessionLease) Done() <-chan error { return l.done }

func (l *sessionLease) Provider() string { return l.name }

func (l *sessionLease) watch() {
	defer close(l.stopped)
	defer close(l.done)
	for {
		select {
		case <-l.stop:
			return
		case <-l.bus.Context().Done():
			l.done <- errors.New("sleep inhibitor service disconnected")
			return
		case response, ok := <-l.responses:
			if !ok {
				l.done <- errors.New("sleep inhibitor service disconnected")
				return
			}
			// Every signal on the connection arrives here; only the request's
			// own Response ends the lease.
			if response.Path != l.request || response.Name != portalRequest+".Response" {
				continue
			}
			code := uint32(0)
			if len(response.Body) > 0 {
				code, _ = response.Body[0].(uint32)
			}
			l.done <- fmt.Errorf("sleep inhibitor ended by the desktop portal (response %d)", code)
			return
		}
	}
}

func (l *sessionLease) Release() error {
	l.once.Do(func() {
		close(l.stop)
		<-l.stopped
		if l.bus.Context().Err() == nil {
			ctx, cancel := context.WithTimeout(context.Background(), busDeadline)
			l.releaseError = l.release(ctx)
			cancel()
		}
		_ = l.bus.Close()
	})
	return l.releaseError
}

// portalProvider inhibits through the desktop portal; the request object the
// portal returns is closed to release. Portal backends refuse callers they
// cannot attribute to an application: an unsandboxed process is attributed by
// its systemd application unit, which a desktop-entry launch provides and the
// engine inherits, or by registering its application id on the connection.
type portalProvider struct {
	connect busConnector
	appID   string
	// scoped reports whether this process runs in a systemd application unit.
	scoped func() bool
}

func (portalProvider) Name() string { return "portal" }

func (p portalProvider) Check(ctx context.Context) error {
	bus, err := p.connect(ctx)
	if err != nil {
		return unavailable(ReasonBusUnreachable, "session bus unreachable: %w", err)
	}
	defer func() { _ = bus.Close() }()
	if err := nameOwnedOn(ctx, bus, portalName); err != nil {
		return err
	}
	if p.applicationScoped() {
		return nil
	}
	registry, err := portalHasRegistry(ctx, bus)
	if err != nil {
		return unavailable(ReasonQueryFailed, "inspect the desktop portal: %w", err)
	}
	if !registry || p.appID == "" {
		return unavailable(ReasonNoApplicationID, "the desktop portal cannot attribute this process to an application: it runs outside an application unit and the portal offers no host registry")
	}
	return nil
}

func (p portalProvider) applicationScoped() bool { return p.scoped != nil && p.scoped() }

func (p portalProvider) Acquire(ctx context.Context) (lease, error) {
	bus, err := p.connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to desktop portal: %w", err)
	}
	if !p.applicationScoped() {
		// Registration must precede every other portal call on the connection.
		if err := bus.Object(portalName, portalPath).CallWithContext(ctx, portalRegistry+".Register", 0, p.appID, map[string]dbus.Variant{}).Err; err != nil {
			_ = bus.Close()
			return nil, unavailable(ReasonNoApplicationID, "register %s with the desktop portal: %w", p.appID, err)
		}
	}
	token, err := requestToken()
	if err != nil {
		_ = bus.Close()
		return nil, err
	}
	responses := make(chan *dbus.Signal, 4)
	bus.Signal(responses)
	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(token),
		"reason":       dbus.MakeVariant("Painted Wolf Code: active work"),
	}
	var request dbus.ObjectPath
	err = bus.Object(portalName, portalPath).CallWithContext(ctx, portalInhibit+".Inhibit", 0, "", portalFlagSuspend|portalFlagIdle, options).Store(&request)
	if err != nil {
		_ = bus.Close()
		return nil, fmt.Errorf("acquire portal inhibitor: %w", err)
	}
	if err := bus.AddMatchSignalContext(ctx, dbus.WithMatchObjectPath(request), dbus.WithMatchInterface(portalRequest), dbus.WithMatchMember("Response")); err != nil {
		_ = bus.Object(portalName, request).CallWithContext(ctx, portalRequest+".Close", 0).Err
		_ = bus.Close()
		return nil, fmt.Errorf("observe portal inhibitor: %w", err)
	}
	release := func(ctx context.Context) error {
		if err := bus.Object(portalName, request).CallWithContext(ctx, portalRequest+".Close", 0).Err; err != nil {
			return fmt.Errorf("release portal inhibitor: %w", err)
		}
		return nil
	}
	return newSessionLease(bus, p.Name(), release, responses, request), nil
}

func requestToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("portal request token: %w", err)
	}
	return "pw" + hex.EncodeToString(raw[:]), nil
}

// powerManagementProvider uses the session power-management inhibit protocol
// that standalone desktops serve; the cookie is released explicitly and the
// service drops it when the connection ends.
type powerManagementProvider struct{ connect busConnector }

func (powerManagementProvider) Name() string { return "power-management" }

func (p powerManagementProvider) Check(ctx context.Context) error {
	return nameServed(ctx, p.connect, powerManagementName)
}

func (p powerManagementProvider) Acquire(ctx context.Context) (lease, error) {
	bus, err := p.connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to power management service: %w", err)
	}
	var cookie uint32
	err = bus.Object(powerManagementName, powerManagementPath).CallWithContext(ctx, powerManagementInhibit+".Inhibit", 0, "Painted Wolf Code", "Active work").Store(&cookie)
	if err != nil {
		_ = bus.Close()
		return nil, fmt.Errorf("acquire power management inhibitor: %w", err)
	}
	release := func(ctx context.Context) error {
		if err := bus.Object(powerManagementName, powerManagementPath).CallWithContext(ctx, powerManagementInhibit+".UnInhibit", 0, cookie).Err; err != nil {
			return fmt.Errorf("release power management inhibitor: %w", err)
		}
		return nil
	}
	return newSessionLease(bus, p.Name(), release, nil, ""), nil
}

// portalHasRegistry reports whether the portal object implements the host registry.
func portalHasRegistry(ctx context.Context, bus *dbus.Conn) (bool, error) {
	var document string
	if err := bus.Object(portalName, portalPath).CallWithContext(ctx, "org.freedesktop.DBus.Introspectable.Introspect", 0).Store(&document); err != nil {
		return false, err
	}
	var node introspect.Node
	if err := xml.Unmarshal([]byte(document), &node); err != nil {
		return false, err
	}
	for _, iface := range node.Interfaces {
		if iface.Name == portalRegistry {
			return true, nil
		}
	}
	return false, nil
}

// inApplicationUnit reports whether this process's cgroup is a systemd
// application unit (`app-*.scope` or `app-*.service`), from which portals
// derive the application id.
func inApplicationUnit() bool {
	raw, err := os.ReadFile("/proc/self/cgroup")
	return err == nil && applicationUnit(string(raw))
}

func applicationUnit(cgroup string) bool {
	for _, line := range strings.Split(strings.TrimSpace(cgroup), "\n") {
		// The unified hierarchy is the `0::` entry.
		path, ok := strings.CutPrefix(line, "0::")
		if !ok {
			continue
		}
		unit := path[strings.LastIndexByte(path, '/')+1:]
		return strings.HasPrefix(unit, "app-") && (strings.HasSuffix(unit, ".scope") || strings.HasSuffix(unit, ".service"))
	}
	return false
}

func nameServed(ctx context.Context, connect busConnector, name string) error {
	bus, err := connect(ctx)
	if err != nil {
		return unavailable(ReasonBusUnreachable, "session bus unreachable: %w", err)
	}
	defer func() { _ = bus.Close() }()
	return nameOwnedOn(ctx, bus, name)
}
