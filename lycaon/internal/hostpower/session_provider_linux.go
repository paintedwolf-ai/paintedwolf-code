//go:build linux

package hostpower

import (
	"context"
	"fmt"

	"github.com/godbus/dbus/v5"
	"github.com/lycaon/lycaon/internal/linuxbus"
)

// sessionService binds an inhibition request and its cleanup to one unique owner.
type sessionService struct {
	bus     *dbus.Conn
	name    string
	owner   string
	signals chan *dbus.Signal
}

func openSessionService(ctx context.Context, connect busConnector, name string) (*sessionService, error) {
	bus, err := connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", name, err)
	}
	service := &sessionService{bus: bus, name: name, signals: make(chan *dbus.Signal, 16)}
	bus.Signal(service.signals)
	err = bus.AddMatchSignalContext(ctx, dbus.WithMatchSender("org.freedesktop.DBus"), dbus.WithMatchInterface("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, name))
	if err == nil {
		err = service.resolveOwner(ctx)
	}
	if err != nil {
		_ = bus.Close()
		return nil, err
	}
	return service, nil
}

func (s *sessionService) resolveOwner(ctx context.Context) error {
	state, err := linuxbus.DiscoverName(ctx, s.bus, s.name)
	if err != nil {
		return err
	}
	if state.Owner == "" && state.Activatable {
		// Activation precedes the effect so the effect itself can target a unique
		// owner. A later crash or replacement still fails the actual call.
		if err := s.bus.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.StartServiceByName", 0, s.name, uint32(0)).Err; err != nil {
			return err
		}
		state, err = linuxbus.DiscoverName(ctx, s.bus, s.name)
		if err != nil {
			return err
		}
	}
	if state.Owner == "" {
		return unavailable(ReasonNotServed, "%s is not served", s.name)
	}
	s.owner = state.Owner
	return nil
}

func (s *sessionService) lost(signal *dbus.Signal) bool {
	if signal == nil || signal.Sender != "org.freedesktop.DBus" || signal.Name != "org.freedesktop.DBus.NameOwnerChanged" || len(signal.Body) != 3 {
		return false
	}
	name, _ := signal.Body[0].(string)
	previous, _ := signal.Body[1].(string)
	current, _ := signal.Body[2].(string)
	return name == s.name && previous == s.owner && current != s.owner
}
