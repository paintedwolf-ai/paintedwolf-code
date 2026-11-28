"""D6 run-scoped synthetic canary generator and recursive leak scanner."""

from __future__ import annotations

import contextlib
import hashlib
import hmac
import json
import pathlib
import secrets
import sqlite3
from typing import Any, Iterable


class CanaryGenerator:
    """Generates run-scoped synthetic canary secrets for isolated profiles only (D6).

    Canaries have format `WEA_CANARY_<run_id>_<label>_<hex>` and carry no value outside
    the isolated test profile (`source_config=None`).
    """

    def __init__(self, run_id: str, *, seed: str | None = None) -> None:
        if not run_id or any(ch.isspace() for ch in run_id):
            raise ValueError("run_id must be a non-empty token without whitespace")
        self.run_id = run_id
        self._seed = seed
        self._counter = 0
        self.issued: list[dict[str, str]] = []

    def mint(self, label: str = "secret") -> str:
        clean_label = "".join(ch if ch.isalnum() or ch in "-_" else "_" for ch in label).strip("_") or "secret"
        self._counter += 1
        if self._seed is not None:
            digest = hmac.new(
                self._seed.encode("utf-8"),
                f"{self.run_id}:{clean_label}:{self._counter}".encode("utf-8"),
                hashlib.sha256,
            ).hexdigest()[:24]
        else:
            digest = secrets.token_hex(12)
        token = f"WEA_CANARY_{self.run_id}_{clean_label}_{digest}"
        self.issued.append({"label": clean_label, "token": token})
        return token

    @property
    def tokens(self) -> list[str]:
        return [entry["token"] for entry in self.issued]

    def materialize_env_file(self, profile_dir: pathlib.Path, entries: dict[str, str]) -> pathlib.Path:
        """Write canaries strictly inside an isolated profile directory with 0600 permissions."""
        profile_dir = pathlib.Path(profile_dir).resolve()
        profile_dir.mkdir(parents=True, exist_ok=True)
        out = profile_dir / "canaries.env"
        lines = [f"{k}={v}\n" for k, v in sorted(entries.items())]
        out.write_text("".join(lines), encoding="utf-8")
        out.chmod(0o600)
        return out


def _scan_text(text: str, canaries: list[str], locator: str, findings: list[dict[str, Any]]) -> None:
    for token in canaries:
        if token and token in text:
            findings.append({
                "canary_sha256": hashlib.sha256(token.encode("utf-8")).hexdigest()[:16],
                "locator": locator,
                "kind": "text",
            })


def _scan_obj(obj: Any, canaries: list[str], locator: str, findings: list[dict[str, Any]]) -> None:
    if isinstance(obj, str):
        _scan_text(obj, canaries, locator, findings)
    elif isinstance(obj, bytes):
        _scan_text(obj.decode("utf-8", errors="replace"), canaries, locator, findings)
    elif isinstance(obj, dict):
        for k, v in obj.items():
            _scan_text(str(k), canaries, f"{locator}.<key:{k}>", findings)
            _scan_obj(v, canaries, f"{locator}.{k}", findings)
    elif isinstance(obj, (list, tuple, set)):
        for idx, item in enumerate(obj):
            _scan_obj(item, canaries, f"{locator}[{idx}]", findings)


def scan_sqlite_ro(db_path: pathlib.Path, canaries: list[str]) -> list[dict[str, Any]]:
    """Read-only scan across every user table in a SQLite database for canary leaks."""
    findings: list[dict[str, Any]] = []
    path = pathlib.Path(db_path).resolve()
    if not path.is_file() or not canaries:
        return findings
    uri = path.as_uri() + "?mode=ro"
    with contextlib.closing(sqlite3.connect(uri, uri=True)) as conn:
        tables = [
            row[0]
            for row in conn.execute(
                "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name"
            ).fetchall()
        ]
        for table in tables:
            quoted = '"' + table.replace('"', '""') + '"'
            try:
                cur = conn.execute(f"SELECT * FROM {quoted}")
            except sqlite3.Error:
                continue
            cols = [desc[0] for desc in (cur.description or ())]
            for row_idx, row in enumerate(cur.fetchall()):
                for col_idx, val in enumerate(row):
                    if val is None:
                        continue
                    text = val.decode("utf-8", errors="replace") if isinstance(val, (bytes, bytearray)) else str(val)
                    col_name = cols[col_idx] if col_idx < len(cols) else str(col_idx)
                    for token in canaries:
                        if token and token in text:
                            findings.append({
                                "canary_sha256": hashlib.sha256(token.encode("utf-8")).hexdigest()[:16],
                                "locator": f"sqlite://{path.name}/{table}[{row_idx}].{col_name}",
                                "table": table,
                                "column": col_name,
                                "row_index": row_idx,
                                "kind": "sqlite",
                            })
    return findings


def scan_for_canaries(
    canaries: Iterable[str],
    *,
    paths: Iterable[str | pathlib.Path] = (),
    sqlite_dbs: Iterable[str | pathlib.Path] = (),
    objects: dict[str, Any] | None = None,
    ignore_names: set[str] | None = None,
) -> dict[str, Any]:
    """Recursively scan files, directories, SQLite databases, and in-memory objects for canary leaks.

    Returns a screened report that NEVER echoes raw canary values (only truncated SHA-256 digests).
    """
    tokens = [t for t in canaries if isinstance(t, str) and t]
    ignored = set(ignore_names or {"canaries.env"})
    findings: list[dict[str, Any]] = []
    scanned_files = 0
    scanned_dbs = 0
    seen_dbs: set[pathlib.Path] = set()

    for db in sqlite_dbs:
        p = pathlib.Path(db).resolve()
        if p.is_file() and p not in seen_dbs:
            seen_dbs.add(p)
            scanned_dbs += 1
            findings.extend(scan_sqlite_ro(p, tokens))

    for raw_path in paths:
        p = pathlib.Path(raw_path)
        if not p.exists():
            continue
        targets = [p] if p.is_file() else sorted(f for f in p.rglob("*") if f.is_file())
        for f in targets:
            if f.name in ignored:
                continue
            if f.suffix.lower() in {".db", ".sqlite", ".sqlite3"}:
                resolved = f.resolve()
                if resolved not in seen_dbs:
                    seen_dbs.add(resolved)
                    scanned_dbs += 1
                    findings.extend(scan_sqlite_ro(resolved, tokens))
                continue
            scanned_files += 1
            try:
                raw = f.read_bytes()
            except OSError:
                continue
            text = raw.decode("utf-8", errors="replace")
            if f.suffix.lower() == ".json":
                try:
                    parsed = json.loads(text)
                    _scan_obj(parsed, tokens, str(f), findings)
                    continue
                except ValueError:
                    pass
            _scan_text(text, tokens, str(f), findings)

    if objects:
        for name, obj in objects.items():
            _scan_obj(obj, tokens, f"object://{name}", findings)

    return {
        "clean": len(findings) == 0,
        "canary_count": len(tokens),
        "scanned_files": scanned_files,
        "scanned_dbs": scanned_dbs,
        "leak_count": len(findings),
        "findings": findings,
    }
