//go:build linux

package linuxbus

import (
	"context"
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
