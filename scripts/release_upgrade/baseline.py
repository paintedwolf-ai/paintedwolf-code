"""Retain the first stable automatic updater after mutable feeds advance."""
from __future__ import annotations

import json
import os
from pathlib import Path
import subprocess

from release_semver import compare, parse
from release_upgrade.feeds import digest, read_bytes
from update_keys import validate_binding

FIRST_AUTOMATIC_VERSION = "1.0.2"
ROOT = Path(__file__).resolve().parents[2]


def capture(base: str, candidate: str, registry: dict, feeds: list[dict], destination: Path) -> list[dict]:
    if compare(parse(candidate), parse(FIRST_AUTOMATIC_VERSION)) <= 0:
        return []
    if any(row["manifest"]["version"] == FIRST_AUTOMATIC_VERSION for row in feeds):
        return []
    relative = f"anchors/{FIRST_AUTOMATIC_VERSION}.json"
    raw = read_bytes(f"{base}/updates/releases/{FIRST_AUTOMATIC_VERSION}.json", 1024 * 1024)
    if raw is None:
        raise ValueError("the retained automatic updater baseline manifest is missing")
    manifest = json.loads(raw)
    if manifest.get("version") != FIRST_AUTOMATIC_VERSION:
        raise ValueError("automatic updater baseline has a different release version")
    validate_binding(registry, manifest)
    path = destination / relative
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(raw)
    subprocess.run(["bash", str(ROOT / "scripts/release-validate-updater-manifest.sh"), "--file", str(path), "--existing"],
                   env={**os.environ, "DOWNLOAD_BASE_URL": base}, check=True, timeout=60)
    return [{"path": relative, "manifest_sha256": digest(raw), "manifest": manifest,
             "verification": "artifact_signature_required"}]
