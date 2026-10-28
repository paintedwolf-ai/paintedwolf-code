"""Observe installed Linux package, graphical session and platform identities."""
from __future__ import annotations

from dataclasses import dataclass
import json
import os
from pathlib import Path
import platform
import pwd
import shlex
import subprocess
import time

from . import installed_gnome
from .installed_cases.logind_context import CallFailure, LOGIN, MANAGER, PROPERTIES, Reader, SESSION_FIELD_BYTES
from .installed_cases.records import CaseFailed

OS_IDS = {"Ubuntu": {"ubuntu"}, "Fedora": {"fedora"}, "Debian": {"debian"}, "RHEL-compatible": {"rhel", "rocky", "almalinux", "centos"},
          "Arch Linux": {"arch"}, "Arch Linux ARM": {"archarm"}, "Artix": {"artix"},
          "Debian-compatible": {"debian", "ubuntu", "linuxmint", "pop"},
          "Arch-family": {"arch", "archarm"}, "NixOS": {"nixos"}}
DESKTOPS = {"GNOME": {"gnome", "ubuntu", "gnome-xorg"}, "Plasma": {"kde", "plasma", "plasmawayland", "plasma-x11"},
            "Xfce": {"xfce"}, "Cinnamon": {"cinnamon", "x-cinnamon"}, "MATE": {"mate"},
            "LXQt": {"lxqt"}, "COSMIC": {"cosmic"}, "Sway": {"sway"}, "Hyprland": {"hyprland"}, "i3": {"i3"}}


def query(arguments: list[str]) -> dict:
    try:
        result = subprocess.run(arguments, stdin=subprocess.DEVNULL, capture_output=True, timeout=30,
                                env={"PATH": "/run/current-system/sw/bin:/usr/bin:/bin", "LC_ALL": "C"})
    except (OSError, subprocess.TimeoutExpired) as error:
        return {"arguments": arguments, "exit_code": None, "error": type(error).__name__}
    if len(result.stdout) + len(result.stderr) > 16 << 20:
        raise ValueError("platform observation exceeded its output bound")
    return {"arguments": arguments, "exit_code": result.returncode,
            "stdout": result.stdout.decode("utf-8", "replace"), "stderr": result.stderr.decode("utf-8", "replace")}


def properties(text: str) -> dict:
    result = {}
    for line in text.splitlines():
        key, separator, value = line.partition("=")
        if not separator or key in result:
            raise ValueError("platform properties have an invalid or duplicate field")
        result[key] = value
    return result


def release(path=Path("/etc/os-release")) -> dict:
    result = {}
    for raw in path.read_text().splitlines():
        if not raw or raw.startswith("#"):
            continue
        key, separator, value = raw.partition("=")
        if not separator or key in result:
            raise ValueError("OS release has an invalid or duplicate field")
        words = shlex.split(value)
        if len(words) > 1:
            raise ValueError("OS release field is not a scalar")
        result[key] = words[0] if words else ""
    if not result.get("ID"):
        raise ValueError("OS release does not identify its distribution")
    return result


@dataclass(frozen=True)
class GraphicalSessionObservation:
    session: dict
    observations: dict


def _inventory(reader, observations, name):
    call = {'object': MANAGER, 'interface': LOGIN + '.Manager', 'method': 'ListSessions'}
    observations['call'] = call
    try:
        result = reader.sessions()
    except CallFailure as error:
        observations.setdefault('failed_calls', []).append({**call, 'remote_name': error.name})
        raise
    observations[name] = result
    return result


def _session_properties(reader, listed, observations):
    observations.update(session_id=listed['id'], session_object=listed['object'])
    values = {}
    fields = [('User', '(uo)', tuple), ('Seat', '(so)', tuple),
              ('Type', 's', str), ('Desktop', 's', str), ('Class', 's', str),
              ('Display', 's', str), ('Active', 'b', bool), ('Remote', 'b', bool)]
    for name, signature, expected in fields:
        observations['property'] = name
        observations['call'] = {'session_id': listed['id'], 'object': listed['object'],
                                'interface': PROPERTIES, 'method': 'Get',
                                'property_interface': LOGIN + '.Session', 'property': name}
        try:
            value = reader.property(listed['object'], LOGIN + '.Session', name, signature)
        except CallFailure as error:
            observations.setdefault('failed_calls', []).append({**observations['call'], 'remote_name': error.name})
            raise
        if type(value) is not expected or isinstance(value, str) and len(value.encode()) > SESSION_FIELD_BYTES:
            raise ValueError('login manager session property has an invalid type or bound')
        values[name] = value
    user, seat = values['User'], values['Seat']
    if (len(user) != 2 or type(user[0]) is not int or user[0] != listed['uid']
            or not isinstance(user[1], str) or len(user[1].encode()) > SESSION_FIELD_BYTES
            or not user[1].startswith(MANAGER + '/user/')
            or len(seat) != 2 or seat[0] != listed['seat'] or not isinstance(seat[1], str)
            or len(seat[1].encode()) > SESSION_FIELD_BYTES
            or seat[1] != '/' and not seat[1].startswith(MANAGER + '/seat/')):
        raise ValueError('login manager session owner or seat changed')
    return {'id': listed['id'], **values, 'User': str(user[0]), 'Seat': seat[0],
            'Active': 'yes' if values['Active'] else 'no',
            'Remote': 'yes' if values['Remote'] else 'no'}


def graphical_session(*, account_name=None, deadline=None) -> GraphicalSessionObservation:
    actual_uid = os.getuid()
    observations = {'process_uid': actual_uid, 'phase': 'account-lookup'}
    try:
        name = pwd.getpwuid(actual_uid).pw_name if account_name is None else account_name
        if not isinstance(name, str) or not name or len(name.encode()) > SESSION_FIELD_BYTES:
            raise ValueError('graphical session selection requires a bounded account name')
        observations['account_name'] = name
        matches = [row for row in pwd.getpwall() if row.pw_name == name or row.pw_uid == actual_uid]
        observations['account_match_count'] = len(matches)
        if len(matches) > 32 or any(not isinstance(row.pw_name, str)
                or len(row.pw_name.encode()) > SESSION_FIELD_BYTES for row in matches):
            raise ValueError('passwd account facts exceed their observation bound')
        observations['account_matches'] = [{'name': row.pw_name, 'uid': row.pw_uid} for row in matches]
        if (len(matches) != 1 or matches[0].pw_name != name or type(matches[0].pw_uid) is not int
                or matches[0].pw_uid != actual_uid or actual_uid <= 0):
            raise ValueError('graphical session selection requires one unambiguous nonroot passwd account')
        uid = matches[0].pw_uid
        observations.update(account={'name': name, 'uid': uid}, phase='inventory-before')
        observation_deadline = time.monotonic() + 30 if deadline is None else deadline
        with Reader(deadline=observation_deadline) as reader:
            before = _inventory(reader, observations, 'inventory_before')
            observations['phase'] = 'properties-before'
            first = [_session_properties(reader, row, observations) for row in before if row['uid'] == uid]
            matches = [row for row in first if row is not None and row['User'] == str(uid)
                       and row['Active'] == 'yes' and row['Remote'] == 'no'
                       and row['Type'] in {'wayland', 'x11'}]
            observations['graphical_candidates_before'] = [row['id'] for row in matches]
            if len(matches) != 1:
                observations['code'] = ('GRAPHICAL_SESSION_NOT_READY' if not matches
                                        else 'GRAPHICAL_SESSION_AMBIGUOUS')
                raise ValueError('qualification requires one active local graphical session for its nonroot account')
            initial = matches[0]
            selected = next(row for row in before if row['id'] == initial['id'])
            observations['selected_identity'] = selected
            observations['selected_properties_before'] = initial
            observations['phase'] = 'inventory-after'
            after = _inventory(reader, observations, 'inventory_after')
            if selected not in after:
                raise ValueError('selected account session identity changed during observation')
            observations['phase'] = 'properties-after'
            second = [_session_properties(reader, row, observations) for row in after if row['uid'] == uid]
            sessions = [row for row in second if row is not None and row['User'] == str(uid)
                        and row['Active'] == 'yes' and row['Remote'] == 'no'
                        and row['Type'] in {'wayland', 'x11'}]
            observations['graphical_candidates_after'] = [row['id'] for row in sessions]
            if len(sessions) != 1 or initial != sessions[0]:
                raise ValueError('selected graphical session changed during observation')
            session = sessions[0]
            observations['selected_properties_after'] = dict(session)
            if not session.get('Desktop'):
                observations['phase'] = 'compositor-owner'
                session['DesktopOwner'] = installed_gnome.owner(reader, expected_uid=uid, deadline=observation_deadline)
                session['Desktop'] = 'GNOME'
    except (CallFailure, OSError, ValueError, TypeError, KeyError) as error:
        failure = CaseFailed('graphical session observation failed')
        observations['error_type'] = type(error).__name__
        if isinstance(error, CaseFailed) and isinstance(getattr(error, 'observations', None), dict):
            observations['cause_observations'] = error.observations
        if isinstance(error, CallFailure):
            observations['remote_name'] = error.name
        else:
            observations['detail'] = str(error)
        failure.observations = {'graphical_session': observations}
        raise failure from None
    return GraphicalSessionObservation(session, {'graphical_session': observations})

def package_inventory_arguments(installation: str) -> list[str]:
    if installation == "deb":
        command = ["dpkg-query", "-W", "-f=${binary:Package}\t${Version}\n"]
    elif installation == "rpm":
        command = ["rpm", "-qa", "--qf", "%{NAME}\t%{VERSION}-%{RELEASE}.%{ARCH}\n"]
    elif installation == "pacman":
        command = ["pacman", "-Q"]
    else:
        command = ["nix-store", "--query", "--requisites", "/run/current-system"]
    return command


def package_inventory(installation: str) -> dict:
    return query(package_inventory_arguments(installation))


def collect(environment: dict, binding: dict, guest: dict, display_protocol: str) -> tuple[dict, dict]:
    actual = release()
    if actual["ID"] not in OS_IDS.get(environment["distribution"], set()):
        raise ValueError("installed distribution differs from its selected support row")
    session_observation = graphical_session()
    session = session_observation.session
    desktop = environment["desktop"]
    reported = set(session.get("Desktop", "").casefold().split(":"))
    marker_desktop = {"GNOME": "gnome", "Plasma": "plasma"}.get(desktop, desktop.casefold())
    if (not reported & DESKTOPS.get(desktop, set()) or guest["desktop"] != marker_desktop
            or session["Type"] != display_protocol):
        raise ValueError("actual desktop/display differs from its selected support row")
    family = next((family for family, ids in {"deb": {"debian", "ubuntu", "linuxmint", "pop"},
                   "rpm": {"fedora", "rhel", "rocky", "almalinux", "centos"},
                   "pacman": {"arch", "archarm", "artix"}}.items() if actual["ID"] in ids), "nix")
    inventory = package_inventory(family)
    if inventory["exit_code"] != 0:
        raise ValueError("installed package inventory could not be recorded")
    observations = {**session_observation.observations, "os_release": actual, "kernel": platform.uname()._asdict(), "session": session,
                    "packages": inventory, "libc": query(["getconf", "GNU_LIBC_VERSION"]),
                    "login_manager": query(["loginctl", "--version"]),
                    "security": {"apparmor": read_optional(Path("/sys/module/apparmor/parameters/enabled")),
                                 "selinux": read_optional(Path("/sys/fs/selinux/enforce"))},
                    "repositories": repository_files(binding["artifact"]["installation"])}
    import hashlib
    inventory_raw = json.dumps(inventory, sort_keys=True).encode()
    selected = [line for line in inventory["stdout"].splitlines() if any(
        token in line.split("\t")[0].casefold() for token in ("gtk", "webkit", "mesa", "vulkan", "portal", "gnome", "plasma", "mutter", "kwin", "glib", "wayland", "xorg", "xfce", "cosmic", "cinnamon", "lxqt", "hyprland", "sway", "elogind", "systemd"))]
    if len(selected) > 2000:
        raise ValueError("runtime package observation exceeds its row bound")
    inventory_summary = {"sha256": hashlib.sha256(inventory_raw).hexdigest(), "bytes": len(inventory_raw), "runtime_packages": selected}
    encoded = lambda value: json.dumps(value, sort_keys=True)
    facts = {"distribution-and-release": encoded(actual),
             "repository-snapshots": encoded(observations["repositories"]),
             "kernel-and-libc": encoded({"kernel": observations["kernel"], "libc": observations["libc"]}),
             "gtk-webkitgtk-and-graphics-driver": encoded(inventory_summary),
             "desktop-compositor-display-protocol": encoded(session),
             "session-and-portal-provider-versions": encoded({"session": session, "packages": inventory_summary}),
             "security-policy": encoded(observations["security"]),
             "nix-version-and-locks-when-applicable": encoded(binding["payload"]) if binding["artifact"]["identity"]["kind"] == "nix-closure" else "not-applicable",
             "source-commit-and-final-package-or-closure-identity": encoded({"source_commit": binding["source_commit"], "artifact": binding["artifact"]})}
    return facts, observations


def read_optional(path: Path) -> str | None:
    return path.read_text().strip() if path.is_file() else None


def repository_files(installation: str) -> dict:
    import hashlib
    roots = {"deb": [Path("/etc/apt/sources.list"), Path("/etc/apt/sources.list.d")],
             "rpm": [Path("/etc/yum.repos.d")], "pacman": [Path("/etc/pacman.conf"), Path("/etc/pacman.d/mirrorlist")]}
    records = {}
    for root in roots.get(installation, [Path("/etc/nix/nix.conf"), Path("/run/current-system")]):
        if root.is_symlink():
            records[str(root)] = {"link_target": str(root.resolve(strict=True))}
            continue
        paths = root.rglob("*") if root.is_dir() else [root]
        for path in paths:
            if path.is_file():
                raw = path.read_bytes()
                if len(raw) > 1 << 20:
                    raise ValueError("repository configuration exceeds its observation bound")
                records[str(path)] = {"sha256": hashlib.sha256(raw).hexdigest(), "bytes": len(raw)}
    return records
