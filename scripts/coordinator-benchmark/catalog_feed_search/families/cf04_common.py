"""Shared D1 sidecar launcher, extended root/worker/workspace HTTP helpers, and evidence utilities for CF04."""

from __future__ import annotations

import contextlib
from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import platform
import secrets
import signal
import socket
import sqlite3
import subprocess
import threading
import time
from typing import Any
import urllib.error
import urllib.parse
import urllib.request

from ..driver_provenance import RunProvenance, file_sha256, write_evidence_manifest
from ..driver_sidecar import HTTPTransport, SidecarDriver


def free_tcp_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def branch_dir_for_root_id(root_id: str) -> str:
    """Match lycaon/internal/projectroot.BranchDirForID(id)."""
    cleaned = root_id.strip()
    if not cleaned:
        raise ValueError("root_id must be non-empty")
    return "root-" + hashlib.sha256(cleaned.encode("utf-8")).hexdigest()


@dataclass
class SidecarHandle:
    """Owned real D1 lycaon-dev sidecar process with isolated store, config, and port."""

    proc: subprocess.Popen[bytes]
    url: str
    token: str
    port: int
    config_dir: Path
    db_path: Path
    perf_file: Path
    stderr_log: Path
    binary_path: Path
    cwd: Path

    @property
    def pid(self) -> int:
        return int(self.proc.pid)

    def launch_receipt(self) -> dict[str, object]:
        return {
            "pid": self.pid,
            "url": self.url,
            "port": self.port,
            "binary_path": str(self.binary_path),
            "binary_sha256": file_sha256(self.binary_path) if self.binary_path.is_file() else "",
            "cwd": str(self.cwd),
            "config_dir": str(self.config_dir),
            "db_path": str(self.db_path),
            "perf_file": str(self.perf_file),
            "platform": platform.system(),
            "machine": platform.machine(),
        }

    def stop(self, timeout_s: float = 10.0) -> int | None:
        if self.proc.poll() is not None:
            return self.proc.returncode
        with contextlib.suppress(OSError):
            self.proc.send_signal(signal.SIGINT)
        try:
            return self.proc.wait(timeout=timeout_s)
        except subprocess.TimeoutExpired:
            with contextlib.suppress(OSError):
                self.proc.terminate()
            try:
                return self.proc.wait(timeout=3.0)
            except subprocess.TimeoutExpired:
                with contextlib.suppress(OSError):
                    self.proc.kill()
                return self.proc.wait(timeout=3.0)


def start_sidecar(
    binary_path: Path,
    work_dir: Path,
    *,
    cwd: Path | None = None,
    ready_timeout_s: float = 25.0,
    extra_env: dict[str, str] | None = None,
) -> SidecarHandle:
    """Launch an isolated real D1 lycaon-dev sidecar (never touches 8787 or ~/.config/paintedwolf-dev)."""
    binary = Path(binary_path).resolve()
    if not binary.is_file():
        raise FileNotFoundError(f"D1 binary not found: {binary}")
    work = Path(work_dir).resolve()
    config_dir = work / "config"
    logs_dir = work / "logs"
    config_dir.mkdir(parents=True, exist_ok=True)
    logs_dir.mkdir(parents=True, exist_ok=True)

    db_path = config_dir / "store.db"
    perf_file = logs_dir / "performance.jsonl"
    stderr_log = logs_dir / "engine.stderr.log"
    port = free_tcp_port()
    if port == 8787:
        port = free_tcp_port()
    token = secrets.token_hex(24)
    addr = f"127.0.0.1:{port}"
    url = f"http://{addr}"

    env = dict(os.environ)
    env.update(
        {
            "LYCAON_ADDR": addr,
            "LYCAON_API_TOKEN": token,
            "LYCAON_CONFIG_DIR": str(config_dir),
            "LYCAON_LLM_MOCK": "1",
            "LYCAON_PERF_DEBUG": "1",
            "LYCAON_PERF_DEBUG_FILE": str(perf_file),
            "LYCAON_LOG_LEVEL": "info",
        }
    )
    if extra_env:
        env.update(extra_env)

    launch_cwd = Path(cwd).resolve() if cwd else work
    stderr_fh = stderr_log.open("wb")
    try:
        proc = subprocess.Popen(
            [str(binary), "serve", "--db", str(db_path)],
            cwd=str(launch_cwd),
            env=env,
            stdout=stderr_fh,
            stderr=stderr_fh,
        )
    except Exception:
        stderr_fh.close()
        raise
    stderr_fh.close()

    handle = SidecarHandle(
        proc=proc,
        url=url,
        token=token,
        port=port,
        config_dir=config_dir,
        db_path=db_path,
        perf_file=perf_file,
        stderr_log=stderr_log,
        binary_path=binary,
        cwd=launch_cwd,
    )

    deadline = time.monotonic() + ready_timeout_s
    waiter = threading.Event()
    last_err = ""
    while time.monotonic() < deadline:
        if proc.poll() is not None:
            tail = stderr_log.read_text(encoding="utf-8", errors="replace")[-1200:]
            raise RuntimeError(f"lycaon-dev exited early (code={proc.returncode}): {tail}")
        try:
            req = urllib.request.Request(f"{url}/health", method="GET")
            with urllib.request.urlopen(req, timeout=1.0) as resp:
                if resp.status == 200:
                    data = json.loads(resp.read().decode("utf-8"))
                    if data.get("status") == "ok":
                        return handle
        except Exception as exc:
            last_err = str(exc)
        waiter.wait(0.1)

    handle.stop()
    tail = stderr_log.read_text(encoding="utf-8", errors="replace")[-1200:]
    raise TimeoutError(f"lycaon-dev did not become healthy within {ready_timeout_s}s (last_err={last_err}): {tail}")


@dataclass(frozen=True)
class HTTPResponse:
    """Raw status + decoded JSON (if any) + headers from a D1 sidecar endpoint."""

    status: int
    body_text: str
    json_body: dict[str, object] | list[object] | None
    headers: dict[str, str]


class CF04SidecarClient:
    """CF04-specific sidecar client wrapping CF02 SidecarDriver + extra D1 root/workspace/source routes."""

    def __init__(self, base_url: str, token: str) -> None:
        self.base_url = base_url.rstrip("/")
        self.token = token
        self.transport = HTTPTransport(self.base_url, self.token)
        self.driver = SidecarDriver(self.transport)

    def raw_call(
        self,
        method: str,
        path: str,
        body: dict[str, object] | None = None,
        *,
        timeout_s: float = 30.0,
    ) -> HTTPResponse:
        data = json.dumps(body).encode("utf-8") if body is not None else None
        headers = {
            "Authorization": f"Bearer {self.token}",
            "Accept": "application/json",
        }
        if data is not None:
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(
            f"{self.base_url}{path}",
            data=data,
            headers=headers,
            method=method,
        )
        try:
            with urllib.request.urlopen(req, timeout=timeout_s) as resp:
                raw = resp.read()
                resp_headers = {k.lower(): v for k, v in resp.headers.items()}
                text = raw.decode("utf-8", errors="replace")
                parsed: dict[str, object] | list[object] | None = None
                if text.strip():
                    with contextlib.suppress(ValueError):
                        parsed = json.loads(text)
                return HTTPResponse(status=int(resp.status), body_text=text, json_body=parsed, headers=resp_headers)
        except urllib.error.HTTPError as err:
            raw = err.read()
            resp_headers = {k.lower(): v for k, v in err.headers.items()} if err.headers else {}
            text = raw.decode("utf-8", errors="replace")
            parsed = None
            if text.strip():
                with contextlib.suppress(ValueError):
                    parsed = json.loads(text)
            return HTTPResponse(status=int(err.code), body_text=text, json_body=parsed, headers=resp_headers)

    def create_project(self, roots: list[dict[str, object]]) -> dict[str, object]:
        resp = self.raw_call("POST", "/v1/projects", {"roots": roots})
        if resp.status not in (200, 201) or not isinstance(resp.json_body, dict):
            raise RuntimeError(f"POST /v1/projects failed: status={resp.status} body={resp.body_text}")
        return resp.json_body

    def get_project(self, project_id: str) -> dict[str, object]:
        resp = self.raw_call("GET", f"/v1/projects/{project_id}")
        if resp.status != 200 or not isinstance(resp.json_body, dict):
            raise RuntimeError(f"GET /v1/projects/{project_id} failed: status={resp.status} body={resp.body_text}")
        return resp.json_body

    def attach_root(
        self,
        project_id: str,
        path: Path,
        *,
        label: str | None = None,
        is_primary: bool | None = None,
    ) -> HTTPResponse:
        payload: dict[str, object] = {"path": str(Path(path).resolve())}
        if label is not None:
            payload["label"] = label
        if is_primary is not None:
            payload["is_primary"] = is_primary
        return self.raw_call("POST", f"/v1/projects/{project_id}/roots", payload)

    def update_root(
        self,
        project_id: str,
        root_id: str,
        *,
        label: str | None = None,
        is_primary: bool | None = None,
    ) -> HTTPResponse:
        payload: dict[str, object] = {}
        if label is not None:
            payload["label"] = label
        if is_primary is not None:
            payload["is_primary"] = is_primary
        return self.raw_call("PATCH", f"/v1/projects/{project_id}/roots/{root_id}", payload)

    def detach_root(self, project_id: str, root_id: str, *, force: bool = True) -> HTTPResponse:
        qs = "?force=true" if force else ""
        return self.raw_call("DELETE", f"/v1/projects/{project_id}/roots/{root_id}{qs}")

    def get_source_workspace(self, project_id: str) -> HTTPResponse:
        return self.raw_call("GET", f"/v1/projects/{project_id}/source/workspace")

    def get_source_index(self, project_id: str) -> HTTPResponse:
        return self.raw_call("GET", f"/v1/projects/{project_id}/source/index")

    def get_source_storage(self, project_id: str) -> HTTPResponse:
        return self.raw_call("GET", f"/v1/projects/{project_id}/source/storage")

    def browse_dir(self, project_id: str, root_id: str, dir_path: str = ".") -> HTTPResponse:
        qs = urllib.parse.urlencode({"root_id": root_id, "dir": dir_path})
        return self.raw_call("GET", f"/v1/projects/{project_id}/source/browse?{qs}")

    def get_source_file(
        self,
        project_id: str,
        root_id: str,
        rel_path: str,
        *,
        worker_id: str | None = None,
        include_deleted: bool = False,
    ) -> HTTPResponse:
        params: dict[str, str] = {"root_id": root_id, "path": rel_path}
        if worker_id:
            params["worker_id"] = worker_id
        if include_deleted:
            params["include_deleted"] = "true"
        qs = urllib.parse.urlencode(params)
        return self.raw_call("GET", f"/v1/projects/{project_id}/source?{qs}")

    def delete_project_async(self, project_id: str, *, operation_id: str) -> HTTPResponse:
        return self.raw_call(
            "POST",
            f"/v1/projects/{project_id}/removals",
            {"operation_id": operation_id, "force": True},
        )


def count_linux_inotify_watches(pid: int) -> int | None:
    """Count exact kernel inotify watch descriptors held by pid via /proc/<pid>/fdinfo/*."""
    if platform.system().lower() != "linux":
        return None
    fdinfo_dir = Path(f"/proc/{pid}/fdinfo")
    if not fdinfo_dir.is_dir():
        return None
    total = 0
    for entry in fdinfo_dir.iterdir():
        try:
            text = entry.read_text(encoding="utf-8", errors="replace")
        except OSError:
            continue
        for line in text.splitlines():
            if line.startswith("inotify wd:"):
                total += 1
    return total


def count_workspace_live_watchers(workspace_payload: dict[str, object]) -> dict[str, object]:
    """Summarize per-root watch states from GET /v1/projects/{id}/source/workspace."""
    roots = workspace_payload.get("roots") or []
    by_root: dict[str, str] = {}
    live_or_partial = 0
    faulted = 0
    unwatched = 0
    if isinstance(roots, list):
        for r in roots:
            if not isinstance(r, dict):
                continue
            rid = str(r.get("id") or "")
            watch = r.get("watch")
            state = str(watch.get("state") or "unknown").lower() if isinstance(watch, dict) else "unknown"
            by_root[rid] = state
            if state in ("live", "partial"):
                live_or_partial += 1
            elif state == "faulted":
                faulted += 1
            elif state == "unwatched":
                unwatched += 1
    return {
        "root_count": len(by_root),
        "live_or_partial_count": live_or_partial,
        "faulted_count": faulted,
        "unwatched_count": unwatched,
        "by_root": by_root,
    }


def wait_root_watch_state(
    client: CF04SidecarClient,
    project_id: str,
    root_id: str,
    target_states: set[str],
    *,
    max_wait_s: float = 15.0,
    poll_s: float = 0.15,
) -> tuple[bool, dict[str, object]]:
    """Poll /source/workspace until root_id watch.state enters target_states."""
    deadline = time.monotonic() + max_wait_s
    waiter = threading.Event()
    last_ws: dict[str, object] = {}
    while True:
        resp = client.get_source_workspace(project_id)
        if resp.status == 200 and isinstance(resp.json_body, dict):
            last_ws = resp.json_body
            summary = count_workspace_live_watchers(last_ws)
            by_root = summary["by_root"]
            if isinstance(by_root, dict) and by_root.get(root_id) in target_states:
                return True, last_ws
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            return False, last_ws
        waiter.wait(min(poll_s, remaining))


def seed_worker_branch_in_db(
    db_path: Path,
    *,
    worker_id: str,
    project_id: str,
    primary_root_id: str,
    primary_root_path: Path,
    branch_files_by_root: dict[str, dict[str, bytes]] | None = None,
    roots_meta: list[dict[str, object]] | None = None,
    status: str = "complete",
    merge_status: str = "pending",
    retired: bool = False,
) -> Path | None:
    """Seed a real worker_jobs row in store.db (and optional worker-branches/<id> tree) for K06."""
    config_dir = Path(db_path).resolve().parent
    relpath = ""
    branch_root: Path | None = None
    if not retired:
        relpath = f"worker-branches/{worker_id}"
        branch_root = config_dir / "worker-branches" / worker_id
        branch_root.mkdir(parents=True, exist_ok=True)
        meta_dir = Path(str(branch_root) + ".meta")
        meta_dir.mkdir(parents=True, exist_ok=True)
        effective_roots = roots_meta or [
            {
                "id": primary_root_id,
                "path": str(primary_root_path.resolve()),
                "label": primary_root_path.name,
                "is_primary": True,
            }
        ]
        meta_payload = {
            "format_version": 1,
            "roots": effective_roots,
            "snapshot_complete": True,
        }
        (meta_dir / "state.json").write_text(json.dumps(meta_payload, indent=2) + "\n", encoding="utf-8")
        single_root = len(effective_roots) == 1
        # Ensure every recorded root directory exists for multi-root layouts (matching workspace.branchRootPaths).
        if not single_root:
            for rmeta in effective_roots:
                rid_str = str(rmeta["id"])
                (branch_root / branch_dir_for_root_id(rid_str)).mkdir(parents=True, exist_ok=True)
        for rid, files in (branch_files_by_root or {}).items():
            root_sub = branch_root if single_root else (branch_root / branch_dir_for_root_id(rid))
            root_sub.mkdir(parents=True, exist_ok=True)
            for rel_file, data in files.items():
                dest = root_sub / rel_file
                dest.parent.mkdir(parents=True, exist_ok=True)
                dest.write_bytes(data)

    now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    scope_json = json.dumps({"mode": "write", "paths": ["."]})
    waiter = threading.Event()
    last_exc: Exception | None = None
    for attempt in range(8):
        conn = sqlite3.connect(str(db_path), timeout=30.0)
        try:
            conn.execute("PRAGMA busy_timeout = 30000")
            conn.execute(
                """
                INSERT INTO worker_jobs (
                    id, project_id, workspace_root_id, workspace_path, workspace_key,
                    agent_type, status, prompt, brief, scope_json, created_at,
                    workspace_relpath, merge_status
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    worker_id,
                    project_id,
                    primary_root_id,
                    str(primary_root_path.resolve()),
                    "",
                    "implementer",
                    status,
                    "cf04-k06 worker branch verification",
                    "cf04-k06 worker branch verification",
                    scope_json,
                    now,
                    relpath,
                    merge_status,
                ),
            )
            conn.commit()
            last_exc = None
            break
        except sqlite3.OperationalError as exc:
            last_exc = exc
            if "locked" not in str(exc).lower() and "busy" not in str(exc).lower():
                raise
            waiter.wait(0.25 * (attempt + 1))
        finally:
            conn.close()
    if last_exc is not None:
        raise last_exc
    return branch_root


def write_sha256sums(directory: Path) -> Path:
    """Generate SHA256SUMS.txt covering every file under directory (excluding SHA256SUMS.txt itself)."""
    root = Path(directory).resolve()
    lines: list[str] = []
    for path in sorted(p for p in root.rglob("*") if p.is_file() and p.name != "SHA256SUMS.txt"):
        rel = path.relative_to(root).as_posix()
        lines.append(f"{file_sha256(path)}  {rel}")
    out = root / "SHA256SUMS.txt"
    out.write_text("\n".join(lines) + ("\n" if lines else ""), encoding="utf-8")
    return out


def finalize_family_evidence(
    out_dir: Path,
    provenance: RunProvenance,
    artifact_files: dict[str, Path],
    summary: dict[str, object],
    *,
    replay_command: list[str],
    launch_receipt: dict[str, object] | None = None,
) -> Path:
    """Write manifest.json and SHA256SUMS.txt for a CF04 family run."""
    manifest = write_evidence_manifest(
        out_dir,
        provenance,
        artifact_files,
        extra=summary,
        replay_command=replay_command,
        launch_receipt=launch_receipt,
    )
    write_sha256sums(out_dir)
    return manifest
