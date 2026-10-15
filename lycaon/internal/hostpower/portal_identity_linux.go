//go:build linux

package hostpower

import (
	"context"
	"encoding/xml"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

const portalRegistry = "org.freedesktop.host.portal.Registry"

// attribute makes the portal attribute calls on bus to p.appID. Portal backends
// refuse an unsandboxed caller without an application id. A portal with the
// host registry takes the id from Register, which must precede every other
// portal call on the connection and succeeds only for an installed desktop
// entry. An older portal derives it from the caller's systemd application
// unit, which a desktop-entry launch creates and the engine inherits.
func (p portalProvider) attribute(ctx context.Context, bus *dbus.Conn) error {
	if p.appID == "" {
		return unavailable(ReasonNoApplicationID, "no application id to present to the desktop portal")
	}
	registry, err := portalHasRegistry(ctx, bus)
	if err != nil {
		return unavailable(ReasonQueryFailed, "inspect the desktop portal: %w", err)
	}
	if registry {
		if err := bus.Object(portalName, portalPath).CallWithContext(ctx, portalRegistry+".Register", 0, p.appID, map[string]dbus.Variant{}).Err; err != nil {
			return unavailable(ReasonNoApplicationID, "the desktop portal refused application id %s: %w", p.appID, err)
		}
		return nil
	}
	unit := ""
	if p.unitAppID != nil {
		unit = p.unitAppID()
	}
	if unit == p.appID {
		return nil
	}
	if unit == "" {
		return unavailable(ReasonNoApplicationID, "the desktop portal cannot attribute this process to %s: it runs outside an application unit and the portal offers no host registry", p.appID)
	}
	return unavailable(ReasonNoApplicationID, "the desktop portal would attribute this process to %s, not %s, and offers no host registry", unit, p.appID)
}

// portalHasRegistry reports whether the portal object implements the host registry.
func portalHasRegistry(ctx context.Context, bus *dbus.Conn) (bool, error) {
	var document string
	if err := bus.Object(portalName, portalPath).CallWithContext(ctx, "org.freedesktop.DBus.Introspectable.Introspect", 0).Store(&document); err != nil {
		return false, err
	}
	var node introspect.Node
	if err := xml.Unmarshal([]byte(document), &node); err != nil {
		return false, err
	}
	for _, iface := range node.Interfaces {
		if iface.Name == portalRegistry {
			return true, nil
		}
	}
	return false, nil
}

// ownUnitAppID is the application id the portal derives from this process's
// systemd user unit, or empty.
func ownUnitAppID() string {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	return unitAppID(userUnit(string(raw)))
}

// userUnit is the first non-slice unit beneath the user manager in the
// unified cgroup path, as systemd reports a process's user unit.
func userUnit(cgroup string) string {
	for _, line := range strings.Split(strings.TrimSpace(cgroup), "\n") {
		path, ok := strings.CutPrefix(line, "0::")
		if !ok {
			continue
		}
		managed := false
		for _, component := range strings.Split(path, "/") {
			switch {
			case strings.HasPrefix(component, "user@") && strings.HasSuffix(component, ".service"):
				managed = true
			case managed && component != "" && !strings.HasSuffix(component, ".slice"):
				return component
			}
		}
		return ""
	}
	return ""
}

// The unit name forms of systemd's desktop environment conventions:
// app[-<launcher>]-<ApplicationID>-<RANDOM>.scope and
// app[-<launcher>]-<ApplicationID>[@<RANDOM>].service.
var (
	scopeUnitPattern   = regexp.MustCompile(`^app-(?:[[:alnum:]]+-)?(.+?)(?:-[[:alnum:]]*)(?:\.scope|\.slice)$`)
	serviceUnitPattern = regexp.MustCompile(`^app-(?:[[:alnum:]]+-)?(.+?)(?:@[[:alnum:]]*|-autostart)?\.service$`)
)

// unitAppID extracts the application id from an application unit name.
func unitAppID(unit string) string {
	if !strings.HasPrefix(unit, "app-") {
		return ""
	}
	match := scopeUnitPattern.FindStringSubmatch(unit)
	if match == nil {
		match = serviceUnitPattern.FindStringSubmatch(unit)
	}
	if match == nil {
		return ""
	}
	id, ok := unescapeUnit(match[1])
	if !ok {
		return ""
	}
	return id
}

// unescapeUnit decodes the `\xNN` escapes systemd writes into unit names.
func unescapeUnit(escaped string) (string, bool) {
	var out strings.Builder
	for i := 0; i < len(escaped); i++ {
		if escaped[i] != '\\' {
			out.WriteByte(escaped[i])
			continue
		}
		if i+3 >= len(escaped) || escaped[i+1] != 'x' {
			return "", false
		}
		value, err := strconv.ParseUint(escaped[i+2:i+4], 16, 8)
		if err != nil {
			return "", false
		}
		out.WriteByte(byte(value))
		i += 3
	}
	return out.String(), true
}
