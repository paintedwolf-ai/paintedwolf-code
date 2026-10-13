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
	"golang.org/x/sys/unix"
)

const busDeadline = 500 * time.Millisecond

// provider is one named inhibition protocol. Availability comes from the bus
// name being served; which program serves it is never inspected.
type provider interface {
	Name() string
	Available(ctx context.Context) bool
	Acquire(ctx context.Context) (lease, error)
}

type busConnector func(context.Context) (*dbus.Conn, error)

// linuxProviders are tried in order; a desktop without a login manager keeps
// its session-level inhibition protocols.
var linuxProviders = []provider{
	login1Provider{connect: linuxbus.ConnectSystem},
	portalProvider{connect: linuxbus.ConnectSession},
	powerManagementProvider{connect: linuxbus.ConnectSession},
}

type platformInhibitor struct{}

func (platformInhibitor) Supported() bool { return supportedBy(linuxProviders) }

func (platformInhibitor) Acquire() (lease, error) { return acquireFrom(linuxProviders) }

func supportedBy(providers []provider) bool {
	ctx, cancel := context.WithTimeout(context.Background(), busDeadline)
	defer cancel()
	for _, candidate := range providers {
		if candidate.Available(ctx) {
			return true
		}
	}
	return false
}

// acquireFrom returns the first provider that grants a lease; refusals from
// earlier providers travel with the final error.
func acquireFrom(providers []provider) (lease, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*busDeadline)
	defer cancel()
	var failures []error
	for _, candidate := range providers {
		if !candidate.Available(ctx) {
			continue
		}
		held, err := candidate.Acquire(ctx)
		if err == nil {
			return held, nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", candidate.Name(), err))
	}
	if len(failures) == 0 {
		return nil, errors.New("idle-sleep inhibition is unavailable")
	}
	return nil, errors.Join(failures...)
}

// login1Provider holds a login manager inhibitor descriptor; systemd-logind
// and elogind both serve the protocol.
type login1Provider struct{ connect busConnector }

func (login1Provider) Name() string { return "login1" }

func (p login1Provider) Available(ctx context.Context) bool {
	bus, err := p.connect(ctx)
	if err != nil {
		return false
	}
	defer func() { _ = bus.Close() }()
	return logindAvailable(ctx, bus)
}

func (p login1Provider) Acquire(ctx context.Context) (lease, error) {
	bus, err := p.connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to login manager: %w", err)
	}
	defer func() { _ = bus.Close() }()
	return acquireLogindLease(ctx, bus)
}

func logindAvailable(ctx context.Context, bus *dbus.Conn) bool {
	owned, err := linuxbus.NameOwned(ctx, bus, linuxbus.Login1Name)
	return bus.SupportsUnixFDs() && err == nil && owned
}

func acquireLogindLease(ctx context.Context, bus *dbus.Conn) (lease, error) {
	if !logindAvailable(ctx, bus) {
		return nil, errors.New("login manager is unavailable")
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
