//go:build linux

package linuxbus

import (
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
