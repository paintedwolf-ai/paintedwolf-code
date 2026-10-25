//go:build linux

package linuxbus

import (
	"context"
	"errors"
	"slices"

	"github.com/godbus/dbus/v5"
)

// NameState records current ownership or an installed activation contract.
// Neither fact authorizes an operation or guarantees the next call succeeds.
type NameState struct {
	Owner       string
	Activatable bool
}

// DiscoverName queries the selected bus without starting a service. Feature
// owners retain protocol negotiation, invocation checks, and connection lifetime.
func DiscoverName(ctx context.Context, bus *dbus.Conn, name string) (NameState, error) {
	if bus == nil {
		return NameState{}, errors.New("bus connection required")
	}
	var owner string
	err := bus.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetNameOwner", 0, name).Store(&owner)
	if err == nil {
		return NameState{Owner: owner}, nil
	}
	var remote dbus.Error
	if !errors.As(err, &remote) || remote.Name != "org.freedesktop.DBus.Error.NameHasNoOwner" {
		return NameState{}, err
	}
	var activatable []string
	if err := bus.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.ListActivatableNames", 0).Store(&activatable); err != nil {
		return NameState{}, err
	}
	return NameState{Activatable: slices.Contains(activatable, name)}, nil
}
