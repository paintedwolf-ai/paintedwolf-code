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

// sessionLease holds an inhibition while its unique service owner stays present.
type sessionLease struct {
	service      *sessionService
	name         string
	release      func(context.Context) error
	request      dbus.ObjectPath
	done         chan error
	stop         chan struct{}
	stopped      chan struct{}
	once         sync.Once
	releaseError error
}

func newSessionLease(service *sessionService, name string, release func(context.Context) error, request dbus.ObjectPath) *sessionLease {
	held := &sessionLease{service: service, name: name, release: release, request: request, done: make(chan error, 1), stop: make(chan struct{}), stopped: make(chan struct{})}
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
		case <-l.service.bus.Context().Done():
			l.done <- unavailable(ReasonDisconnected, "sleep inhibitor bus disconnected")
			return
		case response, ok := <-l.service.signals:
			if !ok || response == nil || l.service.lost(response) {
				l.done <- unavailable(ReasonDisconnected, "sleep inhibitor service owner disconnected")
				return
			}
			// A different client's request cannot settle this lease.
			if response.Sender != l.service.owner || response.Path != l.request || response.Name != portalRequest+".Response" {
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
		if l.service.bus.Context().Err() == nil {
			ctx, cancel := context.WithTimeout(context.Background(), busDeadline)
			l.releaseError = l.release(ctx)
			cancel()
			var remote dbus.Error
			if errors.As(l.releaseError, &remote) && (remote.Name == "org.freedesktop.DBus.Error.NameHasNoOwner" || remote.Name == "org.freedesktop.DBus.Error.ServiceUnknown" || remote.Name == "org.freedesktop.DBus.Error.Disconnected") {
				l.releaseError = nil
			}
		}
		_ = l.service.bus.Close()
	})
	return l.releaseError
}

// portalProvider inhibits through the desktop portal; the request object the
// portal returns is closed to release. The portal must attribute the caller to
// appID before inhibiting (see portalAttribution).
type portalProvider struct {
	connect busConnector
	appID   string
	// unitAppID is the application id this process's systemd unit names.
	unitAppID func() string
}

func (portalProvider) Name() string { return "portal" }

func (p portalProvider) Check(ctx context.Context) error {
	bus, err := p.connect(ctx)
	if err != nil {
		return unavailable(ReasonBusUnreachable, "session bus unreachable: %w", err)
	}
	defer func() { _ = bus.Close() }()
	if err := nameAvailableOn(ctx, bus, portalName); err != nil {
		return err
	}
	return p.attribute(ctx, bus, portalName)
}

func (p portalProvider) Acquire(ctx context.Context) (lease, error) {
	service, err := openSessionService(ctx, p.connect, portalName)
	if err != nil {
		return nil, fmt.Errorf("connect to desktop portal: %w", err)
	}
	bus := service.bus
	if err := p.attribute(ctx, bus, service.owner); err != nil {
		_ = bus.Close()
		return nil, err
	}
	token, err := requestToken()
	if err != nil {
		_ = bus.Close()
		return nil, err
	}
	if err := bus.AddMatchSignalContext(ctx, dbus.WithMatchSender(service.owner), dbus.WithMatchInterface(portalRequest), dbus.WithMatchMember("Response")); err != nil {
		_ = bus.Close()
		return nil, fmt.Errorf("observe portal inhibitor: %w", err)
	}
	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(token),
		"reason":       dbus.MakeVariant("Painted Wolf Code: active work"),
	}
	var request dbus.ObjectPath
	err = bus.Object(service.owner, portalPath).CallWithContext(ctx, portalInhibit+".Inhibit", 0, "", portalFlagSuspend|portalFlagIdle, options).Store(&request)
	if err != nil {
		_ = bus.Close()
		return nil, fmt.Errorf("acquire portal inhibitor: %w", err)
	}
	release := func(ctx context.Context) error {
		if err := bus.Object(service.owner, request).CallWithContext(ctx, portalRequest+".Close", 0).Err; err != nil {
			return fmt.Errorf("release portal inhibitor: %w", err)
		}
		return nil
	}
	return newSessionLease(service, p.Name(), release, request), nil
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
	service, err := openSessionService(ctx, p.connect, powerManagementName)
	if err != nil {
		return nil, fmt.Errorf("connect to power management service: %w", err)
	}
	bus := service.bus
	var cookie uint32
	err = bus.Object(service.owner, powerManagementPath).CallWithContext(ctx, powerManagementInhibit+".Inhibit", 0, "Painted Wolf Code", "Active work").Store(&cookie)
	if err != nil {
		_ = bus.Close()
		return nil, fmt.Errorf("acquire power management inhibitor: %w", err)
	}
	release := func(ctx context.Context) error {
		if err := bus.Object(service.owner, powerManagementPath).CallWithContext(ctx, powerManagementInhibit+".UnInhibit", 0, cookie).Err; err != nil {
			return fmt.Errorf("release power management inhibitor: %w", err)
		}
		return nil
	}
	return newSessionLease(service, p.Name(), release, ""), nil
}

func nameServed(ctx context.Context, connect busConnector, name string) error {
	bus, err := connect(ctx)
	if err != nil {
		return unavailable(ReasonBusUnreachable, "session bus unreachable: %w", err)
	}
	defer func() { _ = bus.Close() }()
	return nameAvailableOn(ctx, bus, name)
}
