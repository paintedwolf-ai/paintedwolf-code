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
// name being served; which program serves it is never inspected.
type provider interface {
	Name() string
	// Check is nil when the protocol is served, otherwise why it cannot be used.
	Check(ctx context.Context) error
	Acquire(ctx context.Context) (lease, error)
}

type busConnector func(context.Context) (*dbus.Conn, error)

var linuxLayers = sleepLayers{
	system:  login1Provider{connect: linuxbus.ConnectSystem, semantics: &lockSemantics{}},
	session: portalProvider{connect: linuxbus.ConnectSession, appID: version.BundleID, unitAppID: ownUnitAppID},
}

type platformInhibitor struct{}

func (platformInhibitor) Availability() error { return linuxLayers.availability() }

func (platformInhibitor) Acquire() (lease, error) { return linuxLayers.acquire() }

// sleepLayers are the two places Linux decides automatic suspend. The login
// manager runs its own idle action and carries out every suspend request.
// Desktop power managers decide idle suspend inside the session, honour session
// suspend inhibitors before asking, and ask the login manager as the person's
// own user. A login manager without block-weak lets a caller override block
// locks its own user holds, so there only the session layer stops a desktop's
// idle suspend. Neither layer requests idle or screen-saver inhibition: screen
// locking and display sleep stay with the desktop.
type sleepLayers struct {
	system  login1Provider
	session provider
}

func (l sleepLayers) availability() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*busDeadline)
	defer cancel()
	unavailable := &UnavailableError{}
	systemErr := l.system.Check(ctx)
	if systemErr != nil {
		unavailable.Reasons = append(unavailable.Reasons, reasonFor(l.system, systemErr))
	}
	sessionErr := l.session.Check(ctx)
	if sessionErr == nil {
		return nil
	}
	unavailable.Reasons = append(unavailable.Reasons, reasonFor(l.session, sessionErr))
	if systemErr == nil {
		if err := l.system.blocksOwnUser(ctx); err != nil {
			unavailable.Reasons = append(unavailable.Reasons, reasonFor(l.system, err))
			return unavailable
		}
		return nil
	}
	return unavailable
}

// acquire holds every served layer. The session layer may be absent only when
// the login manager's lock also binds the person's own desktop.
func (l sleepLayers) acquire() (lease, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*busDeadline)
	defer cancel()
	unavailable := &UnavailableError{}
	var parts []lease
	systemErr := l.system.Check(ctx)
	if systemErr == nil {
		held, err := l.system.Acquire(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", l.system.Name(), err)
		}
		parts = append(parts, held)
	} else {
		unavailable.Reasons = append(unavailable.Reasons, reasonFor(l.system, systemErr))
	}
	sessionErr := l.session.Check(ctx)
	if sessionErr == nil {
		held, err := l.session.Acquire(ctx)
		if err == nil {
			return layered(append(parts, held)), nil
		}
		sessionErr = fmt.Errorf("%s: %w", l.session.Name(), err)
	} else {
		unavailable.Reasons = append(unavailable.Reasons, reasonFor(l.session, sessionErr))
	}
	if systemErr != nil {
		return nil, unavailable
	}
	if err := l.system.blocksOwnUser(ctx); err != nil {
		releaseAll(parts)
		return nil, errors.Join(sessionErr, fmt.Errorf("%s: %w", l.system.Name(), err))
	}
	return layered(parts), nil
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
	return ProviderReason{Provider: candidate.Name(), Code: code, Reason: err.Error()}
}

// login1Provider holds a login manager sleep inhibitor descriptor;
// systemd-logind and elogind both serve the protocol.
type login1Provider struct {
	connect   busConnector
	semantics *lockSemantics
}

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
	return nameOwnedOn(ctx, bus, linuxbus.Login1Name)
}

// nameOwnedOn is nil when name has an owner on bus; an activatable name that
// is not running is not served.
func nameOwnedOn(ctx context.Context, bus *dbus.Conn, name string) error {
	owned, err := linuxbus.NameOwned(ctx, bus, name)
	if err != nil {
		return unavailable(ReasonQueryFailed, "query %s: %w", name, err)
	}
	if !owned {
		return unavailable(ReasonNotServed, "%s is not served", name)
	}
	return nil
}

func acquireLogindLease(ctx context.Context, bus *dbus.Conn) (lease, error) {
	if err := logindAvailability(ctx, bus); err != nil {
		return nil, fmt.Errorf("login manager is unavailable: %w", err)
	}
	// "idle" is not requested: desktops read it as a display inhibitor.
	fd, err := inhibitSleep(ctx, bus, "Active work", "block")
	if err != nil {
		return nil, fmt.Errorf("acquire sleep inhibitor: %w", err)
	}
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

func inhibitSleep(ctx context.Context, bus *dbus.Conn, why, mode string) (int, error) {
	var descriptor dbus.UnixFD
	err := bus.Object(linuxbus.Login1Name, linuxbus.Login1Path).CallWithContext(ctx, linuxbus.Login1Name+".Manager.Inhibit", 0,
		"sleep", "Painted Wolf Code", why, mode).Store(&descriptor)
	return int(descriptor), err
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
