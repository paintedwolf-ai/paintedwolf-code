"""AT-SPI client setup shared by the WebKitGTK accessibility harness and the
installed desktop cases: which session bus to talk to, the client library with
bounded call timeouts, and asking toolkits to expose their trees."""

from contextlib import contextmanager
import os
import time

SESSION_BUS_VARIABLES = ("DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR")


@contextmanager
def session_bus(environment):
    """Points this process's AT-SPI client at the given graphical session for the block."""
    previous = {name: os.environ.get(name) for name in SESSION_BUS_VARIABLES}
    try:
        os.environ.update({name: environment[name] for name in SESSION_BUS_VARIABLES})
        yield
    finally:
        for name, value in previous.items():
            if value is None:
                os.environ.pop(name, None)
            else:
                os.environ[name] = value


def load_atspi(call_timeout_ms: int, startup_timeout_ms: int):
    """The AT-SPI client library with bounded calls; ImportError when it is not installed."""
    import gi

    try:
        gi.require_version("Atspi", "2.0")
    except ValueError as error:
        raise ImportError(str(error)) from None
    from gi.repository import Atspi

    Atspi.set_timeout(call_timeout_ms, startup_timeout_ms)
    return Atspi


def enable_accessibility_bus() -> None:
    """Asks toolkits on this session to expose their accessibility trees, as a running screen reader does."""
    from gi.repository import Gio, GLib

    bus = Gio.bus_get_sync(Gio.BusType.SESSION, None)
    for _ in range(50):
        try:
            bus.call_sync("org.a11y.Bus", "/org/a11y/bus", "org.freedesktop.DBus.Properties", "Set",
                          GLib.Variant("(ssv)", ("org.a11y.Status", "IsEnabled", GLib.Variant("b", True))),
                          None, Gio.DBusCallFlags.NONE, 2000, None)
            return
        except GLib.Error:
            time.sleep(0.2)
    raise RuntimeError("org.a11y.Bus did not answer")
