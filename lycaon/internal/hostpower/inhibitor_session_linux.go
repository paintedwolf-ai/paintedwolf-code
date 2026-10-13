//go:build linux

package hostpower

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/lycaon/lycaon/internal/linuxbus"
)

const (
	portalName        = "org.freedesktop.portal.Desktop"
	portalPath        = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	portalInhibit     = "org.freedesktop.portal.Inhibit"
	portalRequest     = "org.freedesktop.portal.Request"
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
	done         chan error
	stop         chan struct{}
	stopped      chan struct{}
	once         sync.Once
	releaseError error
}

func newSessionLease(bus *dbus.Conn, name string, release func(context.Context) error, responses chan *dbus.Signal) *sessionLease {
	held := &sessionLease{bus: bus, name: name, release: release, responses: responses, done: make(chan error, 1), stop: make(chan struct{}), stopped: make(chan struct{})}
	go held.watch()
	return held
}

func (l *sessionLease) Done() <-chan error { return l.done }

func (l *sessionLease) Provider() string { return l.name }

func (l *sessionLease) watch() {
	defer close(l.stopped)
	defer close(l.done)
	select {
	case <-l.stop:
	case <-l.bus.Context().Done():
		l.done <- errors.New("sleep inhibitor service disconnected")
	case response, ok := <-l.responses:
		if !ok {
			l.done <- errors.New("sleep inhibitor service disconnected")
			return
		}
		code := uint32(0)
		if len(response.Body) > 0 {
			code, _ = response.Body[0].(uint32)
		}
		l.done <- fmt.Errorf("sleep inhibitor ended by the desktop portal (response %d)", code)
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
// portal returns is closed to release.
type portalProvider struct{ connect busConnector }

func (portalProvider) Name() string { return "portal" }

func (p portalProvider) Available(ctx context.Context) bool {
	return nameServed(ctx, p.connect, portalName)
}

func (p portalProvider) Acquire(ctx context.Context) (lease, error) {
	bus, err := p.connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to desktop portal: %w", err)
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
	return newSessionLease(bus, p.Name(), release, responses), nil
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

func (p powerManagementProvider) Available(ctx context.Context) bool {
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
	return newSessionLease(bus, p.Name(), release, nil), nil
}

func nameServed(ctx context.Context, connect busConnector, name string) bool {
	bus, err := connect(ctx)
	if err != nil {
		return false
	}
	defer func() { _ = bus.Close() }()
	owned, err := linuxbus.NameOwned(ctx, bus, name)
	return err == nil && owned
}
