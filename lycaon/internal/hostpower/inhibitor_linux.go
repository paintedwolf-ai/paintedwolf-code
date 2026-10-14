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

const logindDeadline = 500 * time.Millisecond
const logindName = "org.freedesktop.login1"
const logindPath = dbus.ObjectPath("/org/freedesktop/login1")

type platformInhibitor struct{}

func logindAvailable(bus *dbus.Conn) bool {
	ctx, cancel := context.WithTimeout(context.Background(), logindDeadline)
	defer cancel()
	var owned bool
	return bus.SupportsUnixFDs() && bus.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, logindName).Store(&owned) == nil && owned
}

func (platformInhibitor) Supported() bool {
	bus, err := linuxbus.Connect(context.Background(), "/run/dbus/system_bus_socket")
	if err != nil {
		return false
	}
	defer func() { _ = bus.Close() }()
	return logindAvailable(bus)
}

func (platformInhibitor) Acquire() (lease, error) {
	bus, err := linuxbus.Connect(context.Background(), "/run/dbus/system_bus_socket")
	if err != nil {
		return nil, fmt.Errorf("connect to sleep inhibitor service: %w", err)
	}
	defer func() { _ = bus.Close() }()
	return acquireLogindLease(bus)
}

func acquireLogindLease(bus *dbus.Conn) (lease, error) {
	if !logindAvailable(bus) {
		return nil, errors.New("idle-sleep inhibition is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), logindDeadline)
	defer cancel()
	var descriptor dbus.UnixFD
	// Desktop power managers request sleep directly.
	err := bus.Object(logindName, logindPath).CallWithContext(ctx, logindName+".Manager.Inhibit", 0,
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
