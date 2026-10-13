//go:build linux

package gitexec

import (
	"context"
	"os"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/lycaon/lycaon/internal/linuxbus"
)

func defaultAgentSocket(ctx context.Context) string {
	if socket := validAgentSocket(os.Getenv("SSH_AUTH_SOCK")); socket != "" {
		return socket
	}
	return sessionAgentSocket(ctx)
}

func sessionAgentSocket(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, agentSocketLookupTimeout)
	defer cancel()
	bus, err := linuxbus.ConnectSession(ctx)
	if err != nil {
		return ""
	}
	defer func() { _ = bus.Close() }()
	var environment dbus.Variant
	err = bus.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1").CallWithContext(ctx,
		"org.freedesktop.DBus.Properties.Get", 0, "org.freedesktop.systemd1.Manager", "Environment").Store(&environment)
	if err != nil {
		return ""
	}
	entries, ok := environment.Value().([]string)
	if !ok {
		return ""
	}
	return agentSocketFromEnvironment(entries)
}

func agentSocketFromEnvironment(entries []string) string {
	if len(entries) > 1024 {
		return ""
	}
	var socket string
	found := false
	bytes := 0
	for _, entry := range entries {
		bytes += len(entry)
		if bytes > 64<<10 {
			return ""
		}
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key != "SSH_AUTH_SOCK" {
			continue
		}
		if found {
			return ""
		}
		found = true
		socket = value
	}
	return validAgentSocket(socket)
}
