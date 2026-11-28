"""Sanitized fixture export and fresh-profile replay for WEA cases (TB02)."""

from __future__ import annotations

import hashlib
import json
import pathlib
import re
from typing import Any, Callable

from wea.observers import hash_tree, sha256_bytes

SCHEMA = "wea-replay-v1"
VERSION = 1

_BEARER_RE = re.compile(r"(Bearer\s+)[A-Za-z0-9._\-]{8,}", re.IGNORECASE)
_UUID_RE = re.compile(r"\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b")


def sanitize_value(
    obj: Any,
    *,
    host_paths: dict[str, str] | None = None,
    secrets: list[str] | None = None,
    session_ids: list[str] | None = None,
) -> Any:
    """Recursively strip session IDs, tokens, and host-specific absolute paths."""
    path_replacements = sorted(
        ((str(k), v) for k, v in (host_paths or {}).items() if k),
        key=lambda kv: len(kv[0]),
        reverse=True,
    )
    secret_list = [s for s in (secrets or ()) if s]
    sid_list = [s for s in (session_ids or ()) if s]

    def _clean_str(text: str) -> str:
        out = text
        for raw, placeholder in path_replacements:
            out = out.replace(raw, placeholder)
        for sid in sid_list:
            out = out.replace(sid, "${SESSION_ID}")
        for tok in secret_list:
            out = out.replace(tok, "<redacted-token>")
        out = _BEARER_RE.sub(r"\1<redacted-token>", out)
        return out

    if isinstance(obj, str):
        return _clean_str(obj)
    if isinstance(obj, dict):
        return {_clean_str(str(k)): sanitize_value(v, host_paths=host_paths, secrets=secrets, session_ids=session_ids) for k, v in obj.items()}
    if isinstance(obj, list):
        return [sanitize_value(v, host_paths=host_paths, secrets=secrets, session_ids=session_ids) for v in obj]
    if isinstance(obj, tuple):
        return [sanitize_value(v, host_paths=host_paths, secrets=secrets, session_ids=session_ids) for v in obj]
    return obj


def _canonical_digest(payload: dict[str, Any]) -> str:
    body = {k: v for k, v in payload.items() if k != "bundle_sha256"}
    encoded = json.dumps(body, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def export_sanitized_bundle(
    export_dir: str | pathlib.Path,
    *,
    track: str,
    case_id: str,
    fixture_files: dict[str, str],
    steps: list[dict[str, Any]],
    expected: dict[str, Any],
    baseline_hashes: dict[str, str],
    receipt: dict[str, Any] | None = None,
    host_paths: dict[str, str] | None = None,
    secrets: list[str] | None = None,
    session_ids: list[str] | None = None,
) -> pathlib.Path:
    """Export a portable, sanitized WEA replay bundle free of session IDs, tokens, and host paths."""
    dest = pathlib.Path(export_dir).resolve()
    dest.mkdir(parents=True, exist_ok=True)

    sanitized_steps = sanitize_value(steps, host_paths=host_paths, secrets=secrets, session_ids=session_ids)
    sanitized_expected = sanitize_value(expected, host_paths=host_paths, secrets=secrets, session_ids=session_ids)

    bundle: dict[str, Any] = {
        "schema": SCHEMA,
        "version": VERSION,
        "track": track,
        "case_id": case_id,
        "fixture_files": dict(sorted(fixture_files.items())),
        "baseline_hashes": dict(sorted(baseline_hashes.items())),
        "steps": sanitized_steps,
        "expected": sanitized_expected,
    }
    bundle["bundle_sha256"] = _canonical_digest(bundle)

    bundle_path = dest / "sanitized-fixture.json"
    bundle_path.write_text(json.dumps(bundle, indent=2, sort_keys=True) + "\n", encoding="utf-8")

    if receipt is not None:
        clean_receipt = sanitize_value(receipt, host_paths=host_paths, secrets=secrets, session_ids=session_ids)
        (dest / "sanitized-receipt.json").write_text(
            json.dumps(clean_receipt, indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
        )
    return bundle_path


def load_and_verify_bundle(bundle_path: str | pathlib.Path) -> dict[str, Any]:
    """Load a sanitized replay bundle and verify schema, digest, and baseline hashes."""
    p = pathlib.Path(bundle_path).resolve()
    data = json.loads(p.read_text(encoding="utf-8"))
    if not isinstance(data, dict) or data.get("schema") != SCHEMA or data.get("version") != VERSION:
        raise ValueError(f"unsupported WEA replay bundle schema/version in {p}")
    expected_sha = data.get("bundle_sha256")
    actual_sha = _canonical_digest(data)
    if not expected_sha or expected_sha != actual_sha:
        raise ValueError(f"replay bundle digest mismatch: expected {expected_sha}, got {actual_sha}")

    fixture_files = data.get("fixture_files")
    baseline_hashes = data.get("baseline_hashes")
    if not isinstance(fixture_files, dict) or not isinstance(baseline_hashes, dict):
        raise ValueError("replay bundle requires fixture_files and baseline_hashes maps")
    for rel, content in fixture_files.items():
        computed = sha256_bytes(str(content).encode("utf-8"))
        if baseline_hashes.get(rel) != computed:
            raise ValueError(f"baseline hash mismatch for fixture file '{rel}'")
    return data


def materialize_fixture(bundle: dict[str, Any], project_dir: pathlib.Path) -> dict[str, str]:
    """Materialize fixture files into a fresh directory and verify baseline SHA-256 hashes."""
    project_dir = pathlib.Path(project_dir).resolve()
    project_dir.mkdir(parents=True, exist_ok=True)
    for rel, content in bundle["fixture_files"].items():
        target = (project_dir / rel).resolve()
        if not str(target).startswith(str(project_dir)):
            raise ValueError(f"fixture path escapes project root: {rel}")
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(str(content), encoding="utf-8")
    actual_hashes = hash_tree(project_dir)
    if actual_hashes != bundle["baseline_hashes"]:
        raise ValueError("materialized fixture hashes do not match bundle baseline_hashes")
    return actual_hashes


def replay_on_fresh_profile(
    bundle_path: str | pathlib.Path,
    out_dir: str | pathlib.Path,
    *,
    runner_fn: Callable[..., dict[str, Any]] | None = None,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
) -> dict[str, Any]:
    """Replay a sanitized WEA bundle on a brand-new isolated profile."""
    bundle = load_and_verify_bundle(bundle_path)
    out = pathlib.Path(out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    project_dir = out / "project"
    materialized = materialize_fixture(bundle, project_dir)

    if runner_fn is None:
        from wea.runner import run_track_control

        runner_fn = run_track_control

    result = runner_fn(
        bundle["track"],
        out / "run",
        engine_bin=engine_bin,
        engine_manifest=engine_manifest,
        project_dir=project_dir,
        replay_bundle=bundle,
    )
    replay_receipt = {
        "schema": "wea-replay-receipt-v1",
        "bundle_path": str(pathlib.Path(bundle_path).resolve()),
        "bundle_sha256": bundle["bundle_sha256"],
        "track": bundle["track"],
        "case_id": bundle["case_id"],
        "fresh_profile_dir": str(out / "run" / "app"),
        "baseline_hashes_verified": materialized == bundle["baseline_hashes"],
        "result": result,
    }
    (out / "replay-receipt.json").write_text(
        json.dumps(replay_receipt, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    return replay_receipt
