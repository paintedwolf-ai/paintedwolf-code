//go:build linux

package hostpower

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lycaon/lycaon/internal/linuxbus"
	"github.com/lycaon/lycaon/internal/version"
	"golang.org/x/sys/unix"
)

const busDeadline = 500 * time.Millisecond

// provider is one named inhibition protocol. Availability comes from the bus
// name's owner or activation contract; which program serves it is never inspected.
type provider interface {
	Name() string
	// Check is nil when the protocol is served, otherwise why it cannot be used.
	Check(ctx context.Context) error
	Acquire(ctx context.Context) (lease, error)
}

type busConnector func(context.Context) (*dbus.Conn, error)

// linuxProviders are tried in order; a desktop without a login manager keeps
// its session-level inhibition protocols.
var linuxProviders = []provider{
	login1Provider{connect: linuxbus.ConnectSystem},
	portalProvider{connect: linuxbus.ConnectSession, appID: version.BundleID, unitAppID: ownUnitAppID},
	powerManagementProvider{connect: linuxbus.ConnectSession},
}

type platformInhibitor struct{}

func (platformInhibitor) Availability() error { return availabilityOf(linuxProviders) }

func (platformInhibitor) Acquire() (lease, error) { return acquireFrom(linuxProviders) }

func availabilityOf(providers []provider) error {
	ctx, cancel := context.WithTimeout(context.Background(), busDeadline)
	defer cancel()
	unavailable := &UnavailableError{}
	for _, candidate := range providers {
		err := candidate.Check(ctx)
		if err == nil {
			return nil
		}
		unavailable.Reasons = append(unavailable.Reasons, reasonFor(candidate, err))
		unavailable.causes = append(unavailable.causes, err)
	}
	return unavailable
}

// unavailableProvider is a protocol that cannot be used, with its classified cause.
type unavailableProvider struct {
	code ProviderReasonCode
	err  error
}

func (u *unavailableProvider) Error() string { return u.err.Error() }

func (u *unavailableProvider) Unwrap() error { return u.err }

func unavailable(code ProviderReasonCode, format string, args ...any) error {
	return &unavailableProvider{code: code, err: fmt.Errorf(format, args...)}
}

func reasonFor(candidate provider, err error) ProviderReason {
	code := ReasonQueryFailed
	var classified *unavailableProvider
	if errors.As(err, &classified) {
		code = classified.code
	}
	var remote dbus.Error
	if errors.As(err, &remote) {
		switch remote.Name {
		case "org.freedesktop.DBus.Error.AccessDenied", "org.freedesktop.DBus.Error.AuthFailed", "org.freedesktop.DBus.Error.InteractiveAuthorizationRequired":
			code = ReasonPermissionDenied
		case "org.freedesktop.DBus.Error.UnknownMethod", "org.freedesktop.DBus.Error.UnknownInterface", "org.freedesktop.DBus.Error.NotSupported":
			code = ReasonUnsupportedOperation
		case "org.freedesktop.DBus.Error.Disconnected", "org.freedesktop.DBus.Error.NameHasNoOwner", "org.freedesktop.DBus.Error.ServiceUnknown":
			code = ReasonDisconnected
		}
	}
	if errors.Is(err, dbus.ErrClosed) {
		code = ReasonDisconnected
	} else if errors.Is(err, context.Canceled) {
		code = ReasonCanceled
	}
	return ProviderReason{Provider: candidate.Name(), Code: code, Reason: err.Error()}
}

// acquireFrom returns the first provider that grants a lease; refusals from
// earlier providers travel with the final error.
func acquireFrom(providers []provider) (lease, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*busDeadline)
	defer cancel()
	unavailable := &UnavailableError{}
	for _, candidate := range providers {
		if err := candidate.Check(ctx); err != nil {
			unavailable.Reasons = append(unavailable.Reasons, reasonFor(candidate, err))
			unavailable.causes = append(unavailable.causes, err)
			continue
		}
		held, err := candidate.Acquire(ctx)
		if err == nil {
			return held, nil
		}
		unavailable.Reasons = append(unavailable.Reasons, reasonFor(candidate, err))
		unavailable.causes = append(unavailable.causes, err)
	}
	return nil, unavailable
}

// login1Provider holds a login manager inhibitor descriptor; systemd-logind
// and elogind both serve the protocol.
type login1Provider struct{ connect busConnector }

func (login1Provider) Name() string { return "login1" }

func (p login1Provider) Check(ctx context.Context) error {
	bus, err := p.connect(ctx)
	if err != nil {
		return unavailable(ReasonBusUnreachable, "system bus unreachable: %w", err)
	}
	defer func() { _ = bus.Close() }()
	return logindAvailability(ctx, bus)
}

func (p login1Provider) Acquire(ctx context.Context) (lease, error) {
	bus, err := p.connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to login manager: %w", err)
	}
	defer func() { _ = bus.Close() }()
	return acquireLogindLease(ctx, bus)
}

func logindAvailability(ctx context.Context, bus *dbus.Conn) error {
	if !bus.SupportsUnixFDs() {
		return unavailable(ReasonNoDescriptorPassing, "the system bus connection cannot pass file descriptors")
	}
	return nameAvailableOn(ctx, bus, linuxbus.Login1Name)
}

func nameAvailableOn(ctx context.Context, bus *dbus.Conn, name string) error {
	state, err := linuxbus.DiscoverName(ctx, bus, name)
	if err != nil {
		return unavailable(ReasonQueryFailed, "query %s: %w", name, err)
	}
	if state.Owner == "" && !state.Activatable {
		return unavailable(ReasonNotServed, "%s is not served", name)
	}
	return nil
}

func acquireLogindLease(ctx context.Context, bus *dbus.Conn) (lease, error) {
	if err := logindAvailability(ctx, bus); err != nil {
		return nil, fmt.Errorf("login manager is unavailable: %w", err)
	}
	var descriptor dbus.UnixFD
	// Desktop power managers request sleep directly, so idle alone is not enough.
	err := bus.Object(linuxbus.Login1Name, linuxbus.Login1Path).CallWithContext(ctx, linuxbus.Login1Name+".Manager.Inhibit", 0,
		"idle:sleep", "Painted Wolf Code", "Active work", "block").Store(&descriptor)
	if err != nil {
		return nil, fmt.Errorf("acquire idle-sleep inhibitor: %w", err)
	}
	fd := int(descriptor)
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		return nil, fmt.Errorf("inspect sleep inhibitor descriptor: %w", err)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags|unix.FD_CLOEXEC); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("protect sleep inhibitor descriptor: %w", err)
	}
	held := &logindLease{file: os.NewFile(uintptr(fd), "sleep-inhibitor"), done: make(chan error, 1), stop: make(chan struct{}), stopped: make(chan struct{})}
	go held.watch()
	return held, nil
}

type logindLease struct {
	file         *os.File
	done         chan error
	stop         chan struct{}
	stopped      chan struct{}
	once         sync.Once
	releaseError error
}

func (l *logindLease) Done() <-chan error { return l.done }

func (*logindLease) Provider() string { return "login1" }

func (l *logindLease) watch() {
	defer close(l.stopped)
	defer close(l.done)
	defer func() { l.releaseError = l.file.Close() }()
	fd := l.file.Fd()
	if fd > math.MaxInt32 {
		l.done <- errors.New("sleep inhibitor descriptor is unavailable")
		return
	}
	descriptors := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLERR | unix.POLLHUP}}
	for {
		select {
		case <-l.stop:
			return
		default:
		}
		_, err := unix.Poll(descriptors, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || descriptors[0].Revents != 0 {
			if err == nil {
				err = errors.New("sleep inhibitor service disconnected")
			}
			l.done <- err
			return
		}
	}
}

func (l *logindLease) Release() error {
	l.once.Do(func() {
		close(l.stop)
		<-l.stopped
	})
	return l.releaseError
}
