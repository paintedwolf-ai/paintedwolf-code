//go:build linux

package linuxbus

import (
	"context"
	"errors"
	"github.com/godbus/dbus/v5"
	"os"
	"time"
)

// Connect opens the first reachable address within one bounded deadline.
func (s Session) Connect(ctx context.Context) (*dbus.Conn, error) {
	if len(s.Addresses) == 0 {
		return nil, errors.New("session bus has no address")
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	var err error
	for _, address := range s.Addresses {
		var bus *dbus.Conn
		bus, err = Connect(ctx, address.dialAddress())
		if err == nil {
			return bus, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, err
}

// ConnectSession uses the inherited session selection without launching a bus.
func ConnectSession(ctx context.Context) (*dbus.Conn, error) {
	session, err := SelectSession(os.LookupEnv, os.Getuid())
	if err != nil {
		return nil, err
	}
	return session.Connect(ctx)
}
