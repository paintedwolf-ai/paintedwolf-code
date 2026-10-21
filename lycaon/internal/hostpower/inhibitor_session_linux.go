//go:build linux

package hostpower

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

const (
	portalName    = "org.freedesktop.portal.Desktop"
	portalPath    = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	portalInhibit = "org.freedesktop.portal.Inhibit"
	portalRequest = "org.freedesktop.portal.Request"
	// portalFlagSuspend is the only flag requested; the Idle flag (8) is the
	// desktop's screen-saver and lock inhibitor.
	portalFlagSuspend = uint32(4)
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
			// Every signal on the connection arrives here; only a later
			// Response on the request ends the granted inhibition.
			code, isResponse := responseCode(response, l.request)
			if !isResponse || code == 0 {
				continue
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

// portalProvider inhibits automatic suspend through the desktop portal, whose
// backend forwards it to the session's power policy; a backend that cannot
// inhibit suspend alone refuses the request. The request object the portal
// returns is closed to release. The portal must attribute the caller to
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
	if err := nameOwnedOn(ctx, bus, portalName); err != nil {
		return err
	}
	return p.attribute(ctx, bus)
}

func (p portalProvider) Acquire(ctx context.Context) (lease, error) {
	bus, err := p.connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to desktop portal: %w", err)
	}
	if err := p.attribute(ctx, bus); err != nil {
		_ = bus.Close()
		return nil, err
	}
	token, err := requestToken()
	if err != nil {
		_ = bus.Close()
		return nil, err
	}
	responses := make(chan *dbus.Signal, 4)
	bus.Signal(responses)
	// The portal answers on the request object it is about to create, often
	// before Inhibit returns, so observe that path before asking.
	predicted := requestPath(bus, token)
	if err := bus.AddMatchSignalContext(ctx, dbus.WithMatchObjectPath(predicted), dbus.WithMatchInterface(portalRequest), dbus.WithMatchMember("Response")); err != nil {
		_ = bus.Close()
		return nil, fmt.Errorf("observe portal inhibitor: %w", err)
	}
	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(token),
		"reason":       dbus.MakeVariant("Painted Wolf Code: active work"),
	}
	var request dbus.ObjectPath
	err = bus.Object(portalName, portalPath).CallWithContext(ctx, portalInhibit+".Inhibit", 0, "", portalFlagSuspend, options).Store(&request)
	if err != nil {
		_ = bus.Close()
		return nil, fmt.Errorf("acquire portal inhibitor: %w", err)
	}
	if err := awaitGrant(ctx, responses, request); err != nil {
		_ = bus.Object(portalName, request).CallWithContext(ctx, portalRequest+".Close", 0).Err
		_ = bus.Close()
		return nil, err
	}
	release := func(ctx context.Context) error {
		if err := bus.Object(portalName, request).CallWithContext(ctx, portalRequest+".Close", 0).Err; err != nil {
			return fmt.Errorf("release portal inhibitor: %w", err)
		}
		return nil
	}
	return newSessionLease(bus, p.Name(), release, responses, request), nil
}

// requestPath is the request object the portal creates for token on bus.
func requestPath(bus *dbus.Conn, token string) dbus.ObjectPath {
	sender := strings.ReplaceAll(strings.TrimPrefix(bus.Names()[0], ":"), ".", "_")
	return dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + sender + "/" + token)
}

// awaitGrant waits for the request's first Response: 0 grants the inhibition,
// which then lasts until the request is closed; any other code refuses it.
func awaitGrant(ctx context.Context, responses <-chan *dbus.Signal, request dbus.ObjectPath) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("desktop portal did not answer the inhibitor request: %w", ctx.Err())
		case response, ok := <-responses:
			if !ok {
				return errors.New("desktop portal disconnected before answering the inhibitor request")
			}
			code, isResponse := responseCode(response, request)
			if !isResponse {
				continue
			}
			if code != 0 {
				return fmt.Errorf("desktop portal refused the inhibitor (response %d)", code)
			}
			return nil
		}
	}
}

// responseCode is the code of request's Response signal.
func responseCode(signal *dbus.Signal, request dbus.ObjectPath) (uint32, bool) {
	if signal.Path != request || signal.Name != portalRequest+".Response" {
		return 0, false
	}
	code := uint32(2)
	if len(signal.Body) > 0 {
		if value, ok := signal.Body[0].(uint32); ok {
			code = value
		}
	}
	return code, true
}

func requestToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("portal request token: %w", err)
	}
	return "pw" + hex.EncodeToString(raw[:]), nil
}
