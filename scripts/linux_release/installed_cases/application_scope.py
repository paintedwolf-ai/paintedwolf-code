"""Observe Linux application attribution without confusing controlled and public launches."""
from pathlib import Path
import os
import re
import subprocess
import time

from ..installed_runtime import file_identity
from ..installed_storage import Storage
from .records import CaseUnavailable
from ..installed_process import process_identity

APP_SCOPE_RE = re.compile(r"^app-(?:[0-9A-Za-z]+-)?(.+?)(?:-[0-9A-Za-z]*)(?:\.scope|\.slice)$")


def enter_desktop_application_scope(path: Path, storage: Storage,
                                    cli_probe: Path = Path("/opt/painted-wolf-code/bin/pw")) -> dict:
    from gi.repository import Gio, GLib
    desktop_path = Path(f"/usr/share/applications/{storage.application_identifier}.desktop")
    desktop_identity = file_identity(desktop_path); desktop_identity.pop("header", None)
    lines = [line.strip() for line in desktop_path.read_text(encoding="utf-8").splitlines()]
    exec_lines = [line.removeprefix("Exec=").strip() for line in lines if line.startswith("Exec=")]
    if (f"StartupWMClass={storage.application_identifier}" not in lines or len(exec_lines) != 1
            or exec_lines[0].split()[0] not in {str(path), "/usr/libexec/painted-wolf-code/pw-launch",
                                                "/opt/painted-wolf-code/bin/painted-wolf-code"}):
        raise ValueError("installed desktop entry does not match the selected application launcher")
    cgroup_before = Path("/proc/self/cgroup").read_text(encoding="utf-8").strip()
    probe = subprocess.Popen([str(cli_probe), "--version"], stdin=subprocess.DEVNULL,
                             stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
    direct_identity = process_identity(probe.pid)
    direct_cgroup = Path(f"/proc/{probe.pid}/cgroup").read_text(encoding="utf-8").strip()
    probe_exit, direct_leaf = probe.wait(timeout=10), direct_cgroup.rsplit("/", 1)[-1]
    direct_match = APP_SCOPE_RE.match(direct_leaf)
    unit = f"app-gnome-{storage.application_identifier}-{os.getpid()}{int(time.monotonic() * 1000)}.scope"
    os.environ.setdefault("XDG_RUNTIME_DIR", f"/run/user/{os.getuid()}")
    os.environ.setdefault("DBUS_SESSION_BUS_ADDRESS", f"unix:path=/run/user/{os.getuid()}/bus")
    bus, props = Gio.bus_get_sync(Gio.BusType.SESSION, None), [("PIDs", GLib.Variant("au", [os.getpid()]))]
    bus.call_sync("org.freedesktop.systemd1", "/org/freedesktop/systemd1", "org.freedesktop.systemd1.Manager",
                  "StartTransientUnit", GLib.Variant("(ssa(sv)a(sa(sv)))", (unit, "fail", props, [])),
                  GLib.VariantType.new("(o)"), Gio.DBusCallFlags.NONE, 5000, None)
    deadline, cgroup_after = time.monotonic() + 5.0, ""
    while time.monotonic() < deadline:
        cgroup_after = Path("/proc/self/cgroup").read_text(encoding="utf-8").strip()
        if cgroup_after.endswith("/" + unit): break
        time.sleep(0.02)
    else: raise ValueError("installed desktop application scope unit did not attach to the launcher cgroup")
    scoped_match = APP_SCOPE_RE.match(unit)
    return {"desktop_entry_path": str(desktop_path), "desktop_entry_identity": desktop_identity,
            "exec_target": str(path), "application_id": storage.application_identifier, "unit": unit,
            "caller_cgroup_before": cgroup_before, "caller_cgroup_after": cgroup_after,
            "launch_mode": "controlled-systemd-application-scope", "desktop_entry_launch_proven": False,
            "cli_launch_comparison": {"direct_cli_executable": str(cli_probe), "direct_cli_identity": direct_identity,
                "direct_cli_exit_code": probe_exit, "direct_cli_cgroup": direct_cgroup, "direct_cli_leaf_unit": direct_leaf,
                "direct_cli_attributed_app_id": direct_match.group(1) if direct_match else None,
                "controlled_scope_unit": unit,
                "controlled_scope_attributed_app_id": scoped_match.group(1) if scoped_match else None}}



def observe_engine_scope(info, pid):
    cgroup = Path(f'/proc/{pid}/cgroup').read_text(encoding='utf-8').strip()
    if info['launch_mode'] == 'controlled-systemd-application-scope':
        if cgroup != info['launch_cgroup'] or not cgroup.endswith('/' + info['unit']):
            raise ValueError('installed engine escaped its desktop application unit scope')
    elif cgroup != info['launch_cgroup']:
        match = APP_SCOPE_RE.fullmatch(cgroup.rsplit('/', 1)[-1])
        if (not match or match.group(1) != info['application_id']
                or f'/user@{os.getuid()}.service/' not in cgroup or '\n' in cgroup):
            raise ValueError('installed engine migrated outside its own application scope')
    return {**info, 'engine_cgroup': cgroup}
