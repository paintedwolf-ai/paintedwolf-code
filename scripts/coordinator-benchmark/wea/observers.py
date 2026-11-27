"""Independent observers for Workers, Edits, and Authority tracks (TB02 / D4).

All SQLite reads use strict `?mode=ro` URI mode so observers never mutate product state.
All filesystem hashes, diffs, and process-table reads run outside the product boundary.
"""

from __future__ import annotations

import contextlib
import difflib
import hashlib
import json
import pathlib
import sqlite3
import subprocess
from typing import Any, Iterable

WORKERS_TABLES = (
    "workers",
    "worker_jobs",
    "worker_attempts",
    "worker_results",
    "worker_result_dispositions",
    "worker_outcome_deliveries",
    "worker_decisions",
    "worker_baselines",
    "worker_baseline_objects",
    "landed_changes",
    "source_worktrees",
    "project_promotions",
)

EDITS_TABLES = (
    "source_files",
    "source_versions",
    "source_effects",
    "source_line_attr",
    "source_text_contributions",
    "source_text_identity_ranges",
    "source_mutations",
    "source_command_windows",
    "editor_documents",
    "editor_replica_heads",
    "editor_agent_receipts",
    "rewind_operations",
)

AUTHORITY_TABLES = (
    "checkpoints",
    "checkpoint_decision_stamps",
    "approval_operations",
    "chat_grants",
    "authorization_contexts",
    "authz_events",
    "person_actions",
    "invocation_receipts",
    "invocation_program_executions",
    "command_invocations",
    "managed_secrets",
    "managed_secret_uses",
    "managed_secret_reveals",
)


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def hash_file(path: str | pathlib.Path) -> dict[str, Any]:
    p = pathlib.Path(path)
    if not p.exists():
        return {"exists": False, "path": str(p), "sha256": None, "bytes": 0}
    raw = p.read_bytes()
    return {
        "exists": True,
        "path": str(p),
        "sha256": sha256_bytes(raw),
        "bytes": len(raw),
    }


def hash_tree(root: str | pathlib.Path, *, ignore_dirs: set[str] | None = None) -> dict[str, str]:
    """Deterministically hash every regular file under root (relative POSIX path -> SHA-256)."""
    base = pathlib.Path(root).resolve()
    ignored = set(ignore_dirs or {".git", "__pycache__", ".pytest_cache", "node_modules"})
    hashes: dict[str, str] = {}
    if not base.is_dir():
        return hashes
    for p in sorted(base.rglob("*")):
        rel = p.relative_to(base)
        if any(part in ignored for part in rel.parts):
            continue
        if p.is_file() and not p.is_symlink():
            hashes[rel.as_posix()] = sha256_bytes(p.read_bytes())
    return hashes


def diff_tree_hashes(before: dict[str, str], after: dict[str, str]) -> dict[str, list[str]]:
    before_keys = set(before)
    after_keys = set(after)
    added = sorted(after_keys - before_keys)
    removed = sorted(before_keys - after_keys)
    modified = sorted(k for k in (before_keys & after_keys) if before[k] != after[k])
    return {"added": added, "removed": removed, "modified": modified}


def outside_diff_dirs(dir_a: str | pathlib.Path, dir_b: str | pathlib.Path) -> dict[str, Any]:
    """Run external `diff -ruN` between two directory trees outside the product."""
    proc = subprocess.run(
        ["diff", "-ruN", "-x", ".git", str(dir_a), str(dir_b)],
        capture_output=True,
        text=True,
        check=False,
    )
    return {
        "returncode": proc.returncode,
        "identical": proc.returncode == 0,
        "unified_diff": proc.stdout,
    }


def outside_unified_diff(before_text: str, after_text: str, rel_path: str) -> str:
    return "".join(
        difflib.unified_diff(
            before_text.splitlines(keepends=True),
            after_text.splitlines(keepends=True),
            fromfile=f"a/{rel_path}",
            tofile=f"b/{rel_path}",
        )
    )


def independent_git_status(root: str | pathlib.Path) -> dict[str, Any]:
    """Run independent `git status --porcelain=v1` against a directory."""
    proc = subprocess.run(
        ["git", "-C", str(root), "status", "--porcelain=v1"],
        capture_output=True,
        text=True,
        check=False,
    )
    return {
        "is_git_repo": proc.returncode == 0,
        "returncode": proc.returncode,
        "porcelain": [line for line in proc.stdout.splitlines() if line.strip()],
    }


def read_sqlite_tables_ro(
    db_path: str | pathlib.Path,
    tables: Iterable[str],
    *,
    session_id: str | None = None,
    row_limit: int = 2000,
) -> dict[str, Any]:
    """Read selected tables from SQLite strictly via `?mode=ro` URI."""
    path = pathlib.Path(db_path).resolve()
    if not path.is_file():
        return {"available": False, "db_path": str(path), "present_tables": [], "rows": {}}
    uri = path.as_uri() + "?mode=ro"
    present: list[str] = []
    rows_by_table: dict[str, list[dict[str, Any]]] = {}
    with contextlib.closing(sqlite3.connect(uri, uri=True)) as conn:
        conn.row_factory = sqlite3.Row
        existing = {
            r[0]
            for r in conn.execute(
                "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
            ).fetchall()
        }
        for table in tables:
            if table not in existing:
                continue
            present.append(table)
            quoted = '"' + table.replace('"', '""') + '"'
            cols = [r[1] for r in conn.execute(f"PRAGMA table_info({quoted})").fetchall()]
            if session_id and "session_id" in cols:
                cur = conn.execute(f"SELECT * FROM {quoted} WHERE session_id = ? LIMIT ?", (session_id, row_limit))
            else:
                cur = conn.execute(f"SELECT * FROM {quoted} LIMIT ?", (row_limit,))
            rows_by_table[table] = [dict(r) for r in cur.fetchall()]
    return {
        "available": True,
        "db_path": str(path),
        "present_tables": present,
        "rows": rows_by_table,
    }


def read_host_process_table(pids: Iterable[int]) -> dict[str, Any]:
    """Read live host process table entries keyed by launched PIDs (D4)."""
    clean_pids = sorted({int(p) for p in pids if int(p) > 0})
    if not clean_pids:
        return {"available": True, "queried_pids": [], "alive": {}, "missing": []}
    pid_arg = ",".join(str(p) for p in clean_pids)
    proc = subprocess.run(
        ["ps", "-o", "pid=,ppid=,stat=,comm=,args=", "-p", pid_arg],
        capture_output=True,
        text=True,
        check=False,
    )
    alive: dict[str, dict[str, Any]] = {}
    for line in proc.stdout.splitlines():
        parts = line.strip().split(None, 4)
        if len(parts) >= 4 and parts[0].isdigit():
            pid_str = parts[0]
            alive[pid_str] = {
                "pid": int(pid_str),
                "ppid": int(parts[1]) if parts[1].isdigit() else None,
                "stat": parts[2],
                "comm": parts[3],
                "args": parts[4] if len(parts) > 4 else "",
            }
    missing = [p for p in clean_pids if str(p) not in alive]
    return {
        "available": True,
        "queried_pids": clean_pids,
        "alive": alive,
        "missing": missing,
    }


def read_tool_results_ro(db_path: str | pathlib.Path, session_id: str) -> list[dict[str, Any]]:
    """Read captured `tool_result_json` rows from messages via `?mode=ro`."""
    path = pathlib.Path(db_path).resolve()
    if not path.is_file():
        return []
    uri = path.as_uri() + "?mode=ro"
    results: list[dict[str, Any]] = []
    with contextlib.closing(sqlite3.connect(uri, uri=True)) as conn:
        existing = {r[0] for r in conn.execute("SELECT name FROM sqlite_master WHERE type='table'").fetchall()}
        if "messages" not in existing:
            return []
        rows = conn.execute(
            "SELECT tool_result_call_id, tool_result_json FROM messages "
            "WHERE session_id = ? AND tool_result_json IS NOT NULL AND tool_result_json != '' ORDER BY ord ASC",
            (session_id,),
        ).fetchall()
        for call_id, raw in rows:
            try:
                parsed = json.loads(raw)
            except ValueError:
                parsed = {"raw": raw}
            results.append({"call_id": call_id, "result": parsed})
    return results


class WorkersObserver:
    """Independent Workers track observer."""

    def __init__(self, primary_root: pathlib.Path, db_path: pathlib.Path) -> None:
        self.primary_root = pathlib.Path(primary_root).resolve()
        self.db_path = pathlib.Path(db_path).resolve()
        self.pre_primary: dict[str, str] = {}
        self.post_primary: dict[str, str] = {}
        self.worker_trees: dict[str, dict[str, dict[str, str]]] = {}

    def capture_pre(self, worker_workspaces: dict[str, pathlib.Path] | None = None) -> dict[str, Any]:
        self.pre_primary = hash_tree(self.primary_root)
        for wid, wpath in (worker_workspaces or {}).items():
            self.worker_trees.setdefault(wid, {})["pre"] = hash_tree(wpath)
        return {
            "primary_hashes": dict(self.pre_primary),
            "git_status": independent_git_status(self.primary_root),
        }

    def capture_post(
        self,
        *,
        session_id: str,
        worker_workspaces: dict[str, pathlib.Path] | None = None,
        next_coordinator_input: dict[str, Any] | None = None,
        den_worker_card: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        self.post_primary = hash_tree(self.primary_root)
        for wid, wpath in (worker_workspaces or {}).items():
            self.worker_trees.setdefault(wid, {})["post"] = hash_tree(wpath)
        tree_diff = diff_tree_hashes(self.pre_primary, self.post_primary)
        db_records = read_sqlite_tables_ro(self.db_path, WORKERS_TABLES, session_id=session_id)
        return {
            "track": "workers",
            "primary_pre_hashes": dict(self.pre_primary),
            "primary_post_hashes": dict(self.post_primary),
            "primary_tree_diff": tree_diff,
            "worker_trees": dict(self.worker_trees),
            "git_status": independent_git_status(self.primary_root),
            "sqlite_records": db_records,
            "next_coordinator_model_input": next_coordinator_input,
            "den_worker_card": den_worker_card,
        }


class EditsObserver:
    """Independent Edits track observer."""

    def __init__(self, project_root: pathlib.Path, db_path: pathlib.Path) -> None:
        self.project_root = pathlib.Path(project_root).resolve()
        self.db_path = pathlib.Path(db_path).resolve()
        self._pre_bytes: dict[str, bytes] = {}
        self._pre_hashes: dict[str, dict[str, Any]] = {}

    def capture_pre(self, rel_paths: Iterable[str]) -> dict[str, Any]:
        for rel in rel_paths:
            full = self.project_root / rel
            self._pre_hashes[rel] = hash_file(full)
            self._pre_bytes[rel] = full.read_bytes() if full.is_file() else b""
        return {"pre_files": dict(self._pre_hashes)}

    def capture_post(
        self,
        rel_paths: Iterable[str],
        *,
        session_id: str,
        open_documents: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        post_files: dict[str, dict[str, Any]] = {}
        diffs: dict[str, str] = {}
        for rel in rel_paths:
            full = self.project_root / rel
            post_files[rel] = hash_file(full)
            after_bytes = full.read_bytes() if full.is_file() else b""
            before_text = self._pre_bytes.get(rel, b"").decode("utf-8", errors="replace")
            after_text = after_bytes.decode("utf-8", errors="replace")
            diffs[rel] = outside_unified_diff(before_text, after_text, rel)
        db_records = read_sqlite_tables_ro(self.db_path, EDITS_TABLES, session_id=session_id)
        tool_results = read_tool_results_ro(self.db_path, session_id)
        return {
            "track": "edits",
            "pre_files": dict(self._pre_hashes),
            "post_files": post_files,
            "outside_unified_diffs": diffs,
            "sqlite_records": db_records,
            "open_documents": open_documents or {},
            "tool_results": tool_results,
        }


class AuthorityObserver:
    """Independent Authority track observer (D4)."""

    def __init__(self, db_path: pathlib.Path) -> None:
        self.db_path = pathlib.Path(db_path).resolve()
        self._pre_fs: dict[str, dict[str, Any]] = {}

    def capture_pre_fs(self, paths: dict[str, pathlib.Path]) -> dict[str, dict[str, Any]]:
        self._pre_fs = {label: hash_file(p) for label, p in paths.items()}
        return dict(self._pre_fs)

    def capture_post(
        self,
        *,
        session_id: str,
        fs_paths: dict[str, pathlib.Path],
        launched_pids: Iterable[int] = (),
        network_fixtures: list[dict[str, Any]] | None = None,
        approval_cards: list[dict[str, Any]] | None = None,
    ) -> dict[str, Any]:
        post_fs = {label: hash_file(p) for label, p in fs_paths.items()}
        fs_changes = {
            label: {
                "pre": self._pre_fs.get(label),
                "post": post_fs[label],
                "unchanged": (
                    self._pre_fs.get(label, {}).get("exists") == post_fs[label].get("exists")
                    and self._pre_fs.get(label, {}).get("sha256") == post_fs[label].get("sha256")
                ),
            }
            for label in post_fs
        }
        db_records = read_sqlite_tables_ro(self.db_path, AUTHORITY_TABLES, session_id=session_id)
        tool_results = read_tool_results_ro(self.db_path, session_id)
        return {
            "track": "authority",
            "host_fs": fs_changes,
            "host_processes": read_host_process_table(launched_pids),
            "network_fixtures": list(network_fixtures or []),
            "approval_cards": list(approval_cards or []),
            "sqlite_records": db_records,
            "tool_results": tool_results,
        }
