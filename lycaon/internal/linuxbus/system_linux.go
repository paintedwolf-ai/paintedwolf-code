//go:build linux

package linuxbus

import (
	"context"
	"errors"
	"os"

	"github.com/godbus/dbus/v5"
)

const defaultSystemBusPath = "/run/dbus/system_bus_socket"

// SelectSystem resolves the system bus from DBUS_SYSTEM_BUS_ADDRESS, else the
// conventional socket.
func SelectSystem(lookupEnv func(string) (string, bool)) ([]SocketAddress, error) {
	if address, ok := lookupEnv("DBUS_SYSTEM_BUS_ADDRESS"); ok && address != "" {
		return parseAddresses(address)
	}
	return []SocketAddress{{Path: defaultSystemBusPath}}, nil
}

// ConnectSystem opens the system bus; callers admit services and capabilities.
func ConnectSystem(ctx context.Context) (*dbus.Conn, error) {
	addresses, err := SelectSystem(os.LookupEnv)
	if err != nil {
		return nil, err
	}
	return Session{Addresses: addresses}.Connect(ctx)
}

// NameOwned reports whether any process currently owns a well-known name.
// Protocol providers are admitted by the name they serve, never by identity.
func NameOwned(ctx context.Context, bus *dbus.Conn, name string) (bool, error) {
	if bus == nil {
		return false, errors.New("bus connection required")
	}
	var owned bool
	err := bus.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, name).Store(&owned)
	return owned && err == nil, err
}
