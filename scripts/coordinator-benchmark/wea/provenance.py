"""Product vs. harness provenance split for WEA receipts (TB02 / D3).

Unlike portable_command/provenance.py (which ties manifest HEAD to the evaluator checkout),
wea/provenance.py strictly separates:
  - product_source: verified from --engine binary SHA-256, `go tool buildid`, `go version -m`,
    and optional --engine-manifest JSON (supports both flat manifest JSON and TB01 nested
    `build-manifest.json`).
  - harness_source: git HEAD, tree SHA, branch, and dirty state of the harness checkout.
"""

from __future__ import annotations

import hashlib
import json
import os
import pathlib
import platform
import socket
import subprocess
from datetime import datetime, timezone
from typing import Any

WEA_DIR = pathlib.Path(__file__).resolve().parent
BENCHMARK_DIR = WEA_DIR.parent
REPO_ROOT = BENCHMARK_DIR.parent.parent


def file_sha256(path: str | pathlib.Path) -> str:
    h = hashlib.sha256()
    with pathlib.Path(path).open("rb") as fh:
        for chunk in iter(lambda: fh.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def _run_cmd(args: list[str], cwd: pathlib.Path | None = None) -> tuple[int, str]:
    try:
        proc = subprocess.run(
            args,
            cwd=str(cwd) if cwd is not None else None,
            capture_output=True,
            text=True,
            check=False,
        )
        return proc.returncode, proc.stdout.strip()
    except OSError:
        return -1, ""


def _git(root: pathlib.Path, *args: str) -> str:
    code, out = _run_cmd(["git", "-C", str(root), *args])
    return out if code == 0 else ""


def resolve_engine_binary(
    root: pathlib.Path = REPO_ROOT,
    engine_bin: str | pathlib.Path | None = None,
) -> tuple[pathlib.Path, str]:
    if engine_bin:
        return pathlib.Path(engine_bin).resolve(), "cli:--engine"
    env_bin = os.environ.get("WEA_ENGINE_BIN", "").strip() or os.environ.get("PW_ENGINE_BIN", "").strip()
    if env_bin:
        return pathlib.Path(env_bin).resolve(), "env:engine_bin"
    candidates = [
        root / "lycaon" / "lycaon-dev",
        root / "lycaon-den" / "src-tauri" / "binaries" / f"lycaon-dev-{platform.machine()}",
    ]
    for cand in candidates:
        if cand.is_file():
            return cand.resolve(), "checkout-build"
    return (root / "lycaon" / "lycaon-dev").resolve(), "checkout-build"


def resolve_engine_manifest(
    engine_manifest: str | pathlib.Path | None = None,
) -> pathlib.Path | None:
    if engine_manifest:
        return pathlib.Path(engine_manifest).resolve()
    env_m = os.environ.get("WEA_ENGINE_MANIFEST", "").strip() or os.environ.get("PW_ENGINE_MANIFEST", "").strip()
    return pathlib.Path(env_m).resolve() if env_m else None


def _extract_manifest_entry(manifest_data: dict[str, Any], actual_sha: str, binary_name: str) -> dict[str, str]:
    """Extract normalized manifest fields from either a flat manifest or TB01 nested build-manifest.json."""
    builds = manifest_data.get("builds")
    if isinstance(builds, dict) and builds:
        # Prefer the build entry whose binary SHA-256 matches actual_sha; fall back to product_baseline.
        chosen: dict[str, Any] | None = None
        chosen_bin: dict[str, Any] | None = None
        for entry in builds.values():
            if not isinstance(entry, dict):
                continue
            bins = entry.get("binaries")
            if not isinstance(bins, dict):
                continue
            cand = bins.get(binary_name) or bins.get("lycaon-dev")
            if isinstance(cand, dict) and actual_sha and cand.get("sha256") == actual_sha:
                chosen = entry
                chosen_bin = cand
                break
        if chosen is None:
            fallback = builds.get("product_baseline") or next(iter(builds.values()), None)
            if isinstance(fallback, dict):
                chosen = fallback
                bins = fallback.get("binaries")
                if isinstance(bins, dict):
                    chosen_bin = bins.get(binary_name) or bins.get("lycaon-dev")
        return {
            "executable_sha256": str((chosen_bin or {}).get("sha256") or ""),
            "go_build_id": str((chosen_bin or {}).get("go_build_id") or ""),
            "git_head": str((chosen or {}).get("head") or (chosen or {}).get("git_head") or ""),
            "git_tree": str((chosen or {}).get("tree") or (chosen or {}).get("git_tree") or ""),
            "build_label": str((chosen or {}).get("label") or ""),
        }
    return {
        "executable_sha256": str(manifest_data.get("executable_sha256") or manifest_data.get("sha256") or ""),
        "go_build_id": str(manifest_data.get("go_build_id") or manifest_data.get("build_id") or ""),
        "git_head": str(manifest_data.get("git_head") or manifest_data.get("product_git_head") or manifest_data.get("head") or ""),
        "git_tree": str(manifest_data.get("git_tree") or manifest_data.get("product_git_tree") or manifest_data.get("tree") or ""),
        "build_label": str(manifest_data.get("label") or ""),
    }


def inspect_product_source(
    root: pathlib.Path = REPO_ROOT,
    *,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
) -> dict[str, Any]:
    """Inspect the product engine binary independently of the harness checkout."""
    binary, origin = resolve_engine_binary(root, engine_bin)
    exists = binary.is_file()
    sha = file_sha256(binary) if exists else ""
    build_id = ""
    go_version = ""
    product_version = ""
    vcs: dict[str, str] = {}

    if exists:
        code, out = _run_cmd(["go", "tool", "buildid", str(binary)])
        if code == 0:
            build_id = out

        code, out = _run_cmd(["go", "version", "-m", str(binary)])
        if code == 0:
            for line in out.splitlines():
                parts = line.strip().split("\t")
                if line.startswith(str(binary)) and ": " in line:
                    go_version = line.split(": ", 1)[1].strip()
                elif len(parts) >= 2 and parts[0] == "build" and "=" in parts[1]:
                    k, v = parts[1].split("=", 1)
                    if k.startswith("vcs."):
                        vcs[k] = v

        code, out = _run_cmd([str(binary), "--version"])
        if code == 0:
            product_version = out

    manifest_path = resolve_engine_manifest(engine_manifest)
    manifest_data: dict[str, Any] = {}
    manifest_sha = ""
    manifest_sha_match: bool | None = None
    manifest_build_id_match: bool | None = None
    extracted = {"executable_sha256": "", "go_build_id": "", "git_head": "", "git_tree": "", "build_label": ""}
    if manifest_path is not None and manifest_path.is_file():
        manifest_sha = file_sha256(manifest_path)
        try:
            parsed = json.loads(manifest_path.read_text(encoding="utf-8"))
            if isinstance(parsed, dict):
                manifest_data = parsed
        except (OSError, ValueError):
            manifest_data = {}
        extracted = _extract_manifest_entry(manifest_data, sha, binary.name)
        expected_sha = extracted["executable_sha256"]
        manifest_sha_match = bool(exists and sha and expected_sha and expected_sha == sha)
        expected_bid = extracted["go_build_id"]
        if expected_bid:
            manifest_build_id_match = bool(build_id and expected_bid == build_id)

    m_head = extracted["git_head"]
    m_tree = extracted["git_tree"]
    vcs_rev = vcs.get("vcs.revision", "")
    vcs_modified = vcs.get("vcs.modified", "")

    if manifest_path is not None:
        verified = bool(
            exists
            and sha
            and manifest_sha_match is True
            and (manifest_build_id_match is None or manifest_build_id_match is True)
            and bool(m_head)
        )
    else:
        verified = bool(exists and sha and vcs_rev and vcs_modified in {"", "false"})

    return {
        "executable_path": str(binary),
        "executable_exists": exists,
        "executable_sha256": sha,
        "binary_origin": origin,
        "go_build_id": build_id,
        "go_version": go_version,
        "product_version": product_version,
        "vcs_revision": vcs_rev,
        "vcs_time": vcs.get("vcs.time", ""),
        "vcs_modified": vcs_modified,
        "manifest_path": str(manifest_path) if manifest_path is not None else "",
        "manifest_sha256": manifest_sha,
        "manifest_build_label": extracted["build_label"],
        "manifest_git_head": m_head,
        "manifest_git_tree": m_tree,
        "manifest_sha256_match": manifest_sha_match,
        "manifest_build_id_match": manifest_build_id_match,
        "effective_git_head": m_head or vcs_rev,
        "effective_git_tree": m_tree,
        "verified": verified,
    }


def inspect_harness_source(root: pathlib.Path = REPO_ROOT) -> dict[str, Any]:
    """Inspect the harness repository checkout separately from the product binary."""
    git_head = _git(root, "rev-parse", "HEAD")
    git_tree = _git(root, "rev-parse", "HEAD^{tree}")
    git_branch = _git(root, "rev-parse", "--abbrev-ref", "HEAD")
    dirty = bool(_git(root, "status", "--porcelain"))
    return {
        "repo_root": str(root.resolve()),
        "git_head": git_head,
        "git_tree": git_tree,
        "git_branch": git_branch,
        "dirty": dirty,
    }


def host_identity() -> dict[str, Any]:
    return {
        "platform": f"{platform.system().lower()}-{platform.machine().lower()}",
        "system": platform.system(),
        "release": platform.release(),
        "machine": platform.machine(),
        "hostname": socket.gethostname(),
        "python_version": platform.python_version(),
        "captured_at": datetime.now(timezone.utc).isoformat(),
    }


def collect_provenance(
    root: pathlib.Path = REPO_ROOT,
    *,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    lane: str = "sidecar",
    test_overlay_digest: str = "",
) -> dict[str, Any]:
    """Build a complete provenance block with product_source and harness_source kept strictly separate."""
    product = inspect_product_source(root, engine_bin=engine_bin, engine_manifest=engine_manifest)
    harness = inspect_harness_source(root)
    result: dict[str, Any] = {
        "schema": "wea-provenance-v1",
        "lane": lane,
        "product_source": product,
        "harness_source": harness,
        "host": host_identity(),
        "product_harness_distinct": bool(
            product.get("effective_git_head")
            and harness.get("git_head")
            and product.get("effective_git_head") != harness.get("git_head")
        ),
    }
    if test_overlay_digest:
        result["test_overlay_digest"] = test_overlay_digest
    return result
