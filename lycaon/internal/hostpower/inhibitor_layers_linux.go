//go:build linux

package hostpower

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/lycaon/lycaon/internal/linuxbus"
	"golang.org/x/sys/unix"
)

// lockSemantics remembers, per login manager instance, whether its block locks
// bind requests from the user who holds them.
type lockSemantics struct {
	mu     sync.Mutex
	owner  string
	strong bool
}

// blocksOwnUser is nil when the login manager's block locks also refuse the
// holder's own user. Login managers that introduced block-weak made block
// binding for every caller; earlier ones reject the mode as invalid, and there
// the person's own desktop overrides the lock.
func (p login1Provider) blocksOwnUser(ctx context.Context) error {
	bus, err := p.connect(ctx)
	if err != nil {
		return unavailable(ReasonBusUnreachable, "system bus unreachable: %w", err)
	}
	defer func() { _ = bus.Close() }()
	var owner string
	if err := bus.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetNameOwner", 0, linuxbus.Login1Name).Store(&owner); err != nil {
		return unavailable(ReasonQueryFailed, "query %s owner: %w", linuxbus.Login1Name, err)
	}
	if p.semantics != nil {
		p.semantics.mu.Lock()
		known, strong := p.semantics.owner == owner, p.semantics.strong
		p.semantics.mu.Unlock()
		if known {
			return ownUserVerdict(strong)
		}
	}
	strong, err := probeBlockWeak(ctx, bus)
	if err != nil {
		return err
	}
	if p.semantics != nil {
		p.semantics.mu.Lock()
		p.semantics.owner, p.semantics.strong = owner, strong
		p.semantics.mu.Unlock()
	}
	return ownUserVerdict(strong)
}

func ownUserVerdict(strong bool) error {
	if strong {
		return nil
	}
	return unavailable(ReasonOwnUserOverride, "the login manager lets the desktop's own user override sleep locks, and no session suspend inhibitor is available")
}

// probeBlockWeak asks for a block-weak lock and releases it at once. Mode
// validation precedes authorization, so only InvalidArgs means the mode is
// unknown.
func probeBlockWeak(ctx context.Context, bus *dbus.Conn) (bool, error) {
	fd, err := inhibitSleep(ctx, bus, "Inhibitor mode check", "block-weak")
	if err == nil {
		_ = unix.Close(fd)
		return true, nil
	}
	switch callErrorName(err) {
	case "org.freedesktop.DBus.Error.InvalidArgs":
		return false, nil
	case "org.freedesktop.DBus.Error.AccessDenied", "org.freedesktop.DBus.Error.InteractiveAuthorizationRequired":
		return true, nil
	default:
		return false, unavailable(ReasonQueryFailed, "query login manager lock semantics: %w", err)
	}
}

func callErrorName(err error) string {
	var value dbus.Error
	if errors.As(err, &value) {
		return value.Name
	}
	var pointer *dbus.Error
	if errors.As(err, &pointer) {
		return pointer.Name
	}
	return ""
}

// layeredLease holds every inhibitor layer as one assertion: when any layer
// ends, the rest are released so the controller reacquires the whole set.
type layeredLease struct {
	parts        []lease
	done         chan error
	stop         chan struct{}
	stopped      chan struct{}
	once         sync.Once
	releaseError error
}

func layered(parts []lease) lease {
	if len(parts) == 1 {
		return parts[0]
	}
	held := &layeredLease{parts: parts, done: make(chan error, 1), stop: make(chan struct{}), stopped: make(chan struct{})}
	go held.watch()
	return held
}

func (l *layeredLease) Done() <-chan error { return l.done }

func (l *layeredLease) Provider() string {
	names := make([]string, 0, len(l.parts))
	for _, part := range l.parts {
		names = append(names, part.Provider())
	}
	return strings.Join(names, "+")
}

func (l *layeredLease) watch() {
	defer close(l.stopped)
	defer close(l.done)
	ended := make(chan error, len(l.parts))
	for _, part := range l.parts {
		go func() {
			err, ok := <-part.Done()
			if !ok || err == nil {
				err = errors.New("ended")
			}
			ended <- fmt.Errorf("%s: %w", part.Provider(), err)
		}()
	}
	select {
	case <-l.stop:
	case err := <-ended:
		l.releaseError = releaseAll(l.parts)
		l.done <- err
	}
}

func (l *layeredLease) Release() error {
	l.once.Do(func() {
		close(l.stop)
		<-l.stopped
		if err := releaseAll(l.parts); err != nil {
			l.releaseError = err
		}
	})
	return l.releaseError
}

func releaseAll(parts []lease) error {
	var failures []error
	for _, part := range parts {
		if err := part.Release(); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
