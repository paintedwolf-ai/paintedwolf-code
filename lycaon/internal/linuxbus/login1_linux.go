//go:build linux

package linuxbus

import (
	"context"
	"errors"
	"fmt"

	"github.com/godbus/dbus/v5"
)

// Login1Name is the login manager protocol; systemd-logind and elogind both serve it.
const Login1Name = "org.freedesktop.login1"

// Login1Path is the login manager object.
const Login1Path = dbus.ObjectPath("/org/freedesktop/login1")

// LoginSession is the login manager's record of the session this process belongs to.
type LoginSession struct {
	ID     string
	Path   dbus.ObjectPath
	Type   string
	Class  string
	Seat   string
	Active bool
}

// CurrentLoginSession asks the login manager which session owns this process,
// so a user with several sessions is attributed by process, not by guess.
func CurrentLoginSession(ctx context.Context, bus *dbus.Conn) (LoginSession, error) {
	if bus == nil {
		return LoginSession{}, errors.New("bus connection required")
	}
	var path dbus.ObjectPath
	err := bus.Object(Login1Name, Login1Path).CallWithContext(ctx, Login1Name+".Manager.GetSessionByPID", 0, uint32(0)).Store(&path)
	if err != nil {
		return LoginSession{}, fmt.Errorf("resolve login session: %w", err)
	}
	var properties map[string]dbus.Variant
	err = bus.Object(Login1Name, path).CallWithContext(ctx, "org.freedesktop.DBus.Properties.GetAll", 0, Login1Name+".Session").Store(&properties)
	if err != nil {
		return LoginSession{}, fmt.Errorf("read login session: %w", err)
	}
	session := LoginSession{Path: path}
	session.ID, _ = properties["Id"].Value().(string)
	session.Type, _ = properties["Type"].Value().(string)
	session.Class, _ = properties["Class"].Value().(string)
	session.Active, _ = properties["Active"].Value().(bool)
	if seat, ok := properties["Seat"].Value().([]any); ok && len(seat) == 2 {
		session.Seat, _ = seat[0].(string)
	}
	if session.ID == "" {
		return LoginSession{}, errors.New("login session has no identifier")
	}
	return session, nil
}
