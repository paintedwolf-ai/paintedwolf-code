"""Fresh native installation with selected bytes, owned effects and retained recovery."""
from __future__ import annotations

from contextlib import contextmanager
import fcntl
import os
from pathlib import Path
import platform
import re
import stat

from ..installed_evidence import artifacts
from ..installed_runtime import bind
from .package_native import command, current_package, operations, owner_commands
from .package_payload import inspect
from .products import require_owned_guest
from .records import CaseUnavailable, retain

PACKAGE = "painted-wolf-code"


@contextmanager
def native_lock():
    directory = Path.home() / ".local/state/paintedwolf-qualification"
    directory.mkdir(parents=True, mode=0o700, exist_ok=True)
    info = directory.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
        raise ValueError("native transaction lock directory is not private and owned")
    descriptor = os.open(directory / "native-package.lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
    try:
        info = os.fstat(descriptor)
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
            raise ValueError("native transaction lock is not private and owned")
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise CaseUnavailable("another owned package transaction is active") from error
        yield
    finally:
        os.close(descriptor)


def query(family):
    return {"deb": ["dpkg-query", "--show", "--showformat=${db:Status-Status}", PACKAGE],
            "rpm": ["rpm", "--query", "--queryformat", "%{NAME}", PACKAGE],
            "pacman": ["pacman", "--query", PACKAGE]}[family]


def prepare_install(final, source, artifact_id, file_root, install_root, case_inputs, output):
    """Only an explicitly requested absent-to-installed transaction proves freshness."""
    native = (case_inputs or {}).get("native_packages", {})
    if not isinstance(native, dict):
        raise ValueError("native package inputs must be an object")
    operation = native.get("installation")
    if operation is None:
        return None
    if operation != {"operation": "fresh-install", "expected_absent": True} or type(operation.get("expected_absent")) is not bool:
        raise ValueError("native installation requires its exact fresh absent-package operation")
    selected = artifacts(final, source)
    if artifact_id not in selected or selected[artifact_id]["installation"] not in {"deb", "rpm", "pacman"}:
        raise ValueError("fresh native installation requires its selected native artifact")
    host_architecture = {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine())
    if platform.system() != "Linux" or host_architecture != selected[artifact_id]["architecture"]:
        raise ValueError("fresh native installation requires its actual native Linux CPU")
    binding = {"source_commit": source, "artifact": selected[artifact_id], "install_root": str(install_root),
               "case_inputs": case_inputs}
    require_owned_guest(binding)
    family = binding["artifact"]["installation"]
    observations = {"operation": "fresh-install", "expected_absent": True, "installed": False,
                    "restored_absence": False, "recovery_required": False, "output_limit": False}
    streams, snapshot, snapshot_identity = {}, None, None
    status = "failed"

    def run(label, arguments, *, capture=True):
        code, stdout, stderr = command(arguments)
        observations[label] = {"arguments": arguments, "exit_code": code}
        for stream, raw in (("stdout", stdout), ("stderr", stderr)):
            name = label + "-" + stream if capture else "transaction-operations-" + stream
            offset = len(streams.get(name, b""))
            remaining = max(0, min((8 << 20) - offset, (24 << 20) - sum(map(len, streams.values()))))
            if len(raw) > remaining:
                observations["output_limit"] = True
            streams[name] = streams.get(name, b"") + raw[:remaining]
            observations[label].setdefault("streams", {})[stream] = {
                "name": name, "offset": offset, "bytes": min(len(raw), remaining),
                "truncated": len(raw) > remaining}
        return code, stdout.decode("utf-8", errors="strict").strip()

    def absent(entries, label, *, capture=True):
        controls = {"deb": (["dpkg-query", "--show", "--showformat=${Package}", "dpkg"], "dpkg"),
                    "rpm": (["rpm", "--query", "--queryformat", "%{NAME}", "rpm"], "rpm"),
                    "pacman": (["pacman", "--query", "--quiet", "pacman"], "pacman")}
        arguments, expected = controls[family]
        health_code, name = run(label + "-database-control", arguments, capture=capture)
        if health_code or name != expected:
            raise CaseUnavailable("native package database failed its positive installed-manager control")
        code, _ = run(label, query(family), capture=capture)
        if code not in (0, 1):
            raise CaseUnavailable("native package database could not establish package absence")
        remaining = [entry.name for entry in entries.values()
                     if entry.kind != "directory" and os.path.lexists(Path("/") / entry.name)]
        observations[label]["remaining_payload_paths"] = remaining[:128]
        return code == 1 and not remaining

    with native_lock():
        try:
            package = current_package(binding)
            if package.parent != Path(file_root).resolve(strict=True):
                raise ValueError("fresh installation package differs from the selected file root")
            entries = inspect(package, family, binding["artifact"]["identity"]["sha256"])
            _, header = owner_commands(family, package, str(Path(install_root) / "bin/painted-wolf-code"))
            code, owner = run("selected-owner", header)
            if code or owner != PACKAGE:
                raise ValueError("fresh installation package is not the selected product")
            profile = Path.home() / ".config/paintedwolf"
            webview_profile = Path.home() / ".local/share/dev.paintedwolf.code"
            observations["webview_profile_absent"] = not os.path.lexists(webview_profile)
            observations["profile_absent"] = not os.path.lexists(profile)
            if not observations["profile_absent"] or not observations["webview_profile_absent"]:
                raise CaseUnavailable("fresh installation requires an uninitialized application profile")
            if not absent(entries, "absence-before"):
                raise CaseUnavailable("fresh installation requires an absent native package and payload")
            code, directory = run("snapshot-directory", ["sudo", "-n", "mktemp", "--directory", "/var/tmp/paintedwolf-native-XXXXXXXXXX"], capture=False)
            if code or not re.fullmatch(r"/var/tmp/paintedwolf-native-[A-Za-z0-9]{10}", directory):
                raise ValueError("native installation could not create its owned root snapshot")
            snapshot = Path(directory)
            info = snapshot.lstat()
            if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or stat.S_IMODE(info.st_mode) != 0o700:
                raise ValueError("native installation snapshot is not private and root owned")
            snapshot_identity = (info.st_dev, info.st_ino)
            staged = snapshot / {"deb": "package.deb", "rpm": "package.rpm", "pacman": "package.pkg.tar.zst"}[family]
            code, _ = run("snapshot-copy", ["sudo", "-n", "install", "--mode=0600", "--owner=0", "--group=0", "--", str(package), str(staged)], capture=False)
            if code:
                raise ValueError("native installation could not retain selected bytes")
            code, checksum = run("snapshot-digest", ["sudo", "-n", "sha256sum", "--", str(staged)], capture=False)
            if code or checksum != binding["artifact"]["identity"]["sha256"] + "  " + str(staged):
                raise ValueError("native installation snapshot differs from selected bytes")
            observations["snapshot_sha256"] = binding["artifact"]["identity"]["sha256"]
            if family == "pacman":
                observations["system_sync_attempted"] = True
                code, _ = run("system-sync", ["sudo", "-n", "pacman", "--sync", "--refresh", "--sysupgrade", "--noconfirm"], capture=False)
                if code:
                    observations["recovery_required"] = True
                    raise ValueError("native pacman preparation did not complete a full system upgrade")
                if not absent(entries, "absence-after-system-sync", capture=False):
                    raise CaseUnavailable("native system preparation changed package absence")
            install = {
                "deb": ["sudo", "-n", "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "--assume-yes", "--no-install-recommends", "install", "--", str(staged)],
                "rpm": ["sudo", "-n", "dnf", "--assumeyes", "--setopt=install_weak_deps=False", "install", "--", str(staged)],
                "pacman": operations(family, staged, PACKAGE)[1]}[family]
            observations["installation_attempted"] = True
            code, _ = run("install", install)
            if code:
                raise ValueError("native package installation failed")
            current_package(binding)
            rebound = bind(final, source, artifact_id, Path(file_root), Path(install_root))
            observations["installed"] = True
            observations["payload"] = rebound["payload"]
            status = "passed" if not observations["output_limit"] else "failed"
        except (OSError, ValueError) as error:
            observations["error"] = str(error)
            status = "unverified" if isinstance(error, CaseUnavailable) else "failed"
        finally:
            if status != "passed" and observations.get("installation_attempted"):
                try:
                    remove, _ = operations(family, Path("/unused"), PACKAGE)
                    if family == "deb":
                        remove = ["sudo", "-n", "dpkg", "--purge", PACKAGE]
                    code, _ = run("rollback", remove)
                    observations["restored_absence"] = code == 0 and absent(entries, "absence-after-rollback") and not os.path.lexists(profile) and not os.path.lexists(webview_profile)
                    observations["recovery_required"] = not observations["restored_absence"]
                except (OSError, ValueError) as error:
                    observations["recovery_required"] = True
                    observations["rollback_error"] = str(error)
            if snapshot is not None and snapshot_identity is None:
                observations["snapshot_cleanup_error"] = "snapshot identity was not established; cleanup refused"
                observations["recovery_required"], status = True, "failed"
            if snapshot is not None and snapshot_identity is not None:
                try:
                    info = snapshot.lstat()
                    if (info.st_dev, info.st_ino) != snapshot_identity or info.st_uid != 0:
                        raise ValueError("owned native snapshot changed identity before cleanup")
                    removed, _ = run("snapshot-remove", ["sudo", "-n", "rm", "--force", "--", str(staged)], capture=False)
                    cleared, _ = run("snapshot-clear", ["sudo", "-n", "rmdir", "--", str(snapshot)], capture=False)
                    if removed or cleared:
                        raise ValueError("owned native snapshot cleanup failed")
                except (OSError, ValueError) as error:
                    observations["snapshot_cleanup_error"] = str(error)
                    observations["recovery_required"], status = True, "failed"
    result = retain(binding, "installation", "preinstallation-transaction", output, status, observations, streams)
    return {"format_version": 1, "status": status, "source_commit": source, "artifact_id": artifact_id,
            "evidence": result["evidence"], "observations": observations}
