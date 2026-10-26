"""Observe actual Linux process births, bounded ancestry and installed executable images."""
from __future__ import annotations

from pathlib import Path
import re
import subprocess

from .installed_cases.records import CaseUnavailable


def process_identity(pid: int) -> dict:
    row = Path(f"/proc/{pid}/stat").read_text().rpartition(") ")[2].split()
    if len(row) < 20: raise ValueError("installed process identity is unavailable")
    return {"pid": pid, "parent_pid": int(row[1]), "start_time_ticks": int(row[19])}


def descends(identity: dict, ancestor: int) -> list[dict]:
    """Return identity's live parent chain before ancestor within a bounded depth, or []."""
    chain, current = [identity], identity
    for _ in range(16):
        if current["parent_pid"] == ancestor: return chain
        if current["parent_pid"] <= 1: return []
        current = process_identity(current["parent_pid"]); chain.append(current)
    return []


def engine_image(pid: int) -> tuple:
    before = process_identity(pid)
    try:
        observed = Path(f"/proc/{pid}/exe").stat()
        identity = observed.st_dev, observed.st_ino, observed.st_size
    except PermissionError:
        # Nondumpable engines can hide proc-exe even from the actual launcher UID.
        try:
            result = subprocess.run(["/usr/bin/sudo", "-n", "/usr/bin/stat", "-L", "-c", "%d:%i:%s", f"/proc/{pid}/exe"],
                                    capture_output=True, timeout=10, check=False, env={"PATH": "/usr/bin:/bin", "LC_ALL": "C"})
        except (OSError, subprocess.SubprocessError) as error:
            raise CaseUnavailable("selected engine image observer is unavailable") from error
        if result.returncode: raise CaseUnavailable("selected engine image observer is unavailable")
        if not re.fullmatch(rb"[0-9]+:[0-9]+:[0-9]+\n", result.stdout):
            raise ValueError("selected engine image observer returned malformed identity")
        identity = tuple(int(value) for value in result.stdout.strip().split(b":"))
    if process_identity(pid) != before: raise ValueError("selected engine changed during image observation")
    return identity
