"""Observe existing session portal owners and versions without activating services."""

from __future__ import annotations

import json
import os
import sys

SERVICES = {
    "org.freedesktop.portal.Desktop": ("/org/freedesktop/portal/desktop", (
        "OpenURI", "FileChooser", "Settings", "Inhibit", "Notification", "Screenshot", "ScreenCast", "RemoteDesktop")),
    "org.freedesktop.portal.Documents": ("/org/freedesktop/portal/documents", ("Documents",)),
}


class ObservationUnavailable(Exception):
    def __init__(self, code: str):
        self.code = code
        super().__init__(code)


def capture(bus) -> dict:
    """Require one stable bus generation and unique owner per observed service."""
    result = {"format_version": 1, "status": "unverified", "services": [],
              "qualification_status": "unverified", "errors": []}
    try:
        generation = bus.bus_id()
        if not isinstance(generation, str) or not generation:
            raise ObservationUnavailable("bus-identity-unavailable")
        owners = {name: bus.owner(name) for name in SERVICES}
        result["bus_id"] = generation
        for name, (path, interfaces) in SERVICES.items():
            owner = owners[name]
            if owner is not None and (not isinstance(owner, str) or not owner.startswith(":")):
                raise ObservationUnavailable("unique-owner-unavailable")
            service = {"name": name, "owner": owner, "interfaces": []}
            result["services"].append(service)
            if owner is None:
                result["errors"].append("service-owner-unavailable")
                continue
            for suffix in interfaces:
                interface = "org.freedesktop.portal." + suffix
                properties = bus.properties(owner, path, interface)
                version = properties.get("version")
                observed = type(version) is int and 0 < version <= 0xFFFFFFFF
                service["interfaces"].append({"name": interface,
                    "status": "observed" if observed else "version-unavailable",
                    "version": version if observed else None})
        if any(bus.owner(name) != owner for name, owner in owners.items()):
            raise ObservationUnavailable("portal-owner-changed")
        if bus.bus_id() != generation:
            raise ObservationUnavailable("bus-generation-changed")
        if not result["errors"]:
            result["status"] = "observed"
    except ObservationUnavailable as exc:
        result["errors"].append(exc.code)
        result["status"] = "unverified"
    return result


class SessionBus:
    def __init__(self):
        address = os.environ.get("DBUS_SESSION_BUS_ADDRESS")
        if not address:
            raise ObservationUnavailable("session-bus-address-unavailable")
        try:
            from gi.repository import Gio, GLib
        except ImportError as exc:
            raise ObservationUnavailable("gio-unavailable") from exc
        self.Gio, self.GLib = Gio, GLib
        try:
            self.connection = Gio.DBusConnection.new_for_address_sync(address,
                Gio.DBusConnectionFlags.AUTHENTICATION_CLIENT | Gio.DBusConnectionFlags.MESSAGE_BUS_CONNECTION,
                None, None)
        except GLib.Error as exc:
            raise ObservationUnavailable("session-bus-connect-failed") from exc

    def call(self, destination, path, interface, method, parameters, reply_type):
        try:
            return self.connection.call_sync(destination, path, interface, method,
                parameters, self.GLib.VariantType.new(reply_type),
                self.Gio.DBusCallFlags.NO_AUTO_START, 1500, None).unpack()
        except self.GLib.Error as exc:
            remote = self.Gio.DBusError.get_remote_error(exc)
            if remote == "org.freedesktop.DBus.Error.NameHasNoOwner":
                raise ObservationUnavailable("name-has-no-owner") from exc
            if remote in ("org.freedesktop.DBus.Error.UnknownInterface", "org.freedesktop.DBus.Error.UnknownProperty"):
                raise ObservationUnavailable("interface-unavailable") from exc
            raise ObservationUnavailable("portal-bus-call-failed") from exc

    def bus_id(self):
        return self.call("org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus",
                         "GetId", None, "(s)")[0]

    def owner(self, name):
        try:
            return self.call("org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus",
                             "GetNameOwner", self.GLib.Variant("(s)", (name,)), "(s)")[0]
        except ObservationUnavailable as exc:
            if exc.code == "name-has-no-owner":
                return None
            raise

    def properties(self, owner, path, interface):
        try:
            return self.call(owner, path, "org.freedesktop.DBus.Properties", "GetAll",
                             self.GLib.Variant("(s)", (interface,)), "(a{sv})")[0]
        except ObservationUnavailable as exc:
            if exc.code == "interface-unavailable":
                return {}
            raise

    def close(self):
        try:
            self.connection.close_sync(None)
        except self.GLib.Error:
            pass


def main() -> int:
    bus = None
    try:
        bus = SessionBus()
        result = capture(bus)
    except ObservationUnavailable as exc:
        result = {"format_version": 1, "status": "unverified", "qualification_status": "unverified",
                  "services": [], "errors": [exc.code]}
    finally:
        if bus is not None:
            bus.close()
    json.dump(result, sys.stdout, indent=2)
    sys.stdout.write("\n")
    return 0 if result["status"] == "observed" else 2


if __name__ == "__main__":
    raise SystemExit(main())
