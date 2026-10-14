//go:build linux

// Package linuxbus owns bounded local D-Bus connection setup.
package linuxbus

import (
	"context"
	"net"
	"time"

	"github.com/godbus/dbus/v5"
)

// Connect returns an owned connection; callers admit services and capabilities.
func Connect(ctx context.Context, path string) (*dbus.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	socket, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	conn := socket.(*net.UnixConn)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return nil, err
	}
	bus, err := dbus.ConnectUnix(conn)
	if err != nil {
		_ = conn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	stop()
	if ctx.Err() != nil {
		_ = bus.Close()
		return nil, ctx.Err()
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = bus.Close()
		return nil, err
	}
	return bus, nil
}
