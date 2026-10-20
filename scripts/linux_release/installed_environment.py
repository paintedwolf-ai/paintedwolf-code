"""Observe installed Linux package, graphical session and platform identities."""
from __future__ import annotations

import json
import os
from pathlib import Path
import platform
import shlex
import subprocess

OS_IDS = {"Ubuntu": {"ubuntu"}, "Fedora": {"fedora"}, "Debian": {"debian"}, "RHEL-compatible": {"rhel", "rocky", "almalinux", "centos"},
          "Arch Linux": {"arch"}, "Arch Linux ARM": {"archarm"}, "Artix": {"artix"},
          "Debian-compatible": {"debian", "ubuntu", "linuxmint", "pop"},
          "Arch-family": {"arch", "archarm"}, "NixOS": {"nixos"}}
DESKTOPS = {"GNOME": {"gnome", "ubuntu", "gnome-xorg"}, "Plasma": {"kde", "plasma", "plasmawayland", "plasma-x11"},
            "Xfce": {"xfce"}, "Cinnamon": {"cinnamon", "x-cinnamon"}, "MATE": {"mate"},
            "LXQt": {"lxqt"}, "COSMIC": {"cosmic"}, "Sway": {"sway"}, "Hyprland": {"hyprland"}, "i3": {"i3"}}
FLOORS = {"ubuntu-floor-gnome-wayland": "24.04", "ubuntu-floor-gnome-x11": "24.04", "debian-floor-gnome": "13", "rhel-floor-gnome": "10"}


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


def graphical_session() -> dict:
    listing = query(["loginctl", "list-sessions", "--no-legend", "--no-pager"])
    if listing["exit_code"] != 0:
        raise ValueError("graphical qualification requires actual logind or elogind session facts")
    sessions = []
    for line in listing["stdout"].splitlines():
        fields = line.split()
        if not fields:
            continue
        session_id = fields[0]
        if not session_id.isalnum():
            raise ValueError("login manager returned an invalid session identifier")
        result = query(["loginctl", "show-session", session_id, "--no-pager",
                        "--property=User", "--property=Type", "--property=Desktop", "--property=Active",
                        "--property=Remote", "--property=Class", "--property=Display", "--property=Seat"])
        if result["exit_code"] != 0:
            raise ValueError("login manager could not describe a listed session")
        row = properties(result["stdout"])
        if (row.get("User") == str(os.getuid()) and row.get("Active") == "yes"
                and row.get("Remote") == "no" and row.get("Type") in {"wayland", "x11"}):
            sessions.append({"id": session_id, **row})
    if len(sessions) != 1:
        raise ValueError("qualification requires one active local graphical session for its nonroot account")
    return sessions[0]


def package_inventory(installation: str) -> dict:
    if installation == "deb":
        command = ["dpkg-query", "-W", "-f=${binary:Package}\t${Version}\n"]
    elif installation == "rpm":
        command = ["rpm", "-qa", "--qf", "%{NAME}\t%{VERSION}-%{RELEASE}.%{ARCH}\n"]
    elif installation == "pacman":
        command = ["pacman", "-Q"]
    else:
        command = ["nix-store", "--query", "--requisites", "/run/current-system"]
    return query(command)


def collect(environment: dict, binding: dict, guest: dict) -> tuple[dict, dict]:
    actual = release()
    if actual["ID"] not in OS_IDS.get(environment["distribution"], set()):
        raise ValueError("installed distribution differs from its selected support row")
    floor = FLOORS.get(environment["id"])
    version = actual.get("VERSION_ID", "")
    if actual["ID"] in {"rhel", "rocky", "almalinux", "centos"}:
        version = version.split(".")[0]
    if floor and version != floor:
        raise ValueError("installed distribution version differs from the selected floor row")
    if environment["id"].startswith("nixos-stable-") and actual.get("VERSION_ID") != "26.05":
        raise ValueError("NixOS stable row requires its declared generation series")
    session = graphical_session()
    desktop = environment["desktop"]
    reported = set(session.get("Desktop", "").casefold().split(":"))
    marker_desktop = {"GNOME": "gnome", "Plasma": "plasma"}.get(desktop, desktop.casefold())
    if (not reported & DESKTOPS.get(desktop, set()) or guest["desktop"] != marker_desktop
            or session["Type"] != environment["display"].casefold()):
        raise ValueError("actual desktop/display differs from its selected support row")
    family = next((family for family, ids in {"deb": {"debian", "ubuntu", "linuxmint", "pop"},
                   "rpm": {"fedora", "rhel", "rocky", "almalinux", "centos"},
                   "pacman": {"arch", "archarm", "artix"}}.items() if actual["ID"] in ids), "nix")
    inventory = package_inventory(family)
    if inventory["exit_code"] != 0:
        raise ValueError("installed package inventory could not be recorded")
    observations = {"os_release": actual, "kernel": platform.uname()._asdict(), "session": session,
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
