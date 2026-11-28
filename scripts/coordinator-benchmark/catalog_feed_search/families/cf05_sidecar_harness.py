"""Isolated D1 sidecar process manager and CF05 HTTP/SSE client helpers.

Never touches 127.0.0.1:8787 or ~/.config/paintedwolf-dev/; never runs pkill/killall.
Always allocates an ephemeral localhost port, isolated temp config/db dirs, and
terminates only its own spawned subprocess.
"""

from __future__ import annotations

import contextlib
from dataclasses import dataclass
from datetime import datetime, timezone
import http.client
import json
import os
from pathlib import Path
import socket
import subprocess
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

from ..driver_http import HTTPTransport
from ..driver_provenance import file_sha256
from ..driver_sidecar import SidecarDriver


def pick_ephemeral_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return int(s.getsockname()[1])


@dataclass
class IsolatedSidecar:
    """Owned D1 lycaon-dev sidecar process with isolated DB, config, and ephemeral port."""

    proc: subprocess.Popen[bytes]
    url: str
    token: str
    port: int
    db_path: Path
    config_dir: Path
    log_path: Path
    binary_path: Path
    binary_sha256: str
    d1_commit: str
    d1_tree: str
    harness_commit: str
    harness_tree: str
    utc_started: str

    def launch_receipt(self) -> dict[str, object]:
        return {
            "utc_started": self.utc_started,
            "pid": self.proc.pid,
            "url": self.url,
            "binary_path": str(self.binary_path),
            "binary_sha256": self.binary_sha256,
            "d1_commit": self.d1_commit,
            "d1_tree": self.d1_tree,
            "harness_commit": self.harness_commit,
            "harness_tree": self.harness_tree,
        }

    def close(self, timeout_s: float = 10.0) -> None:
        if self.proc.poll() is None:
            with contextlib.suppress(OSError):
                self.proc.terminate()
            try:
                self.proc.wait(timeout=timeout_s)
            except subprocess.TimeoutExpired:
                with contextlib.suppress(OSError):
                    self.proc.kill()
                with contextlib.suppress(subprocess.TimeoutExpired):
                    self.proc.wait(timeout=3.0)


def start_isolated_sidecar(
    *,
    binary_path: Path,
    d1_worktree: Path,
    state_dir: Path,
    d1_commit: str = "3bb1464d22dd0a38a149d37610810e7a0324e8cf",
    d1_tree: str = "a05c37487b1254b32be0640bcf47c0d52172f088",
    harness_commit: str = "",
    harness_tree: str = "",
    ready_timeout_s: float = 30.0,
) -> IsolatedSidecar:
    """Launch an isolated D1 lycaon-dev sidecar on an ephemeral port and wait for /health."""
    state_dir = Path(state_dir).resolve()
    state_dir.mkdir(parents=True, exist_ok=True)
    config_dir = state_dir / "config"
    config_dir.mkdir(parents=True, exist_ok=True)
    db_path = state_dir / "lycaon.sqlite"
    log_path = state_dir / "sidecar.log"

    port = pick_ephemeral_port()
    token = f"cfs-cf05-{uuid.uuid4().hex}"
    url = f"http://127.0.0.1:{port}"
    bin_resolved = Path(binary_path).resolve()
    bin_sha = file_sha256(bin_resolved)

    env = {k: v for k, v in os.environ.items() if not k.startswith("LYCAON_")}
    env.update(
        {
            "LYCAON_DEV": "1",
            "LYCAON_HARNESS": "1",
            "LYCAON_ADDR": f"127.0.0.1:{port}",
            "LYCAON_API_TOKEN": token,
            "LYCAON_CONFIG_DIR": str(config_dir),
            "LYCAON_CONFIG_ROOT": str(Path(d1_worktree).resolve() / "lycaon"),
            "LYCAON_LLM_MOCK": "1",
            "LYCAON_LLM_MANUAL": "1",
        }
    )

    utc_started = datetime.now(timezone.utc).isoformat()
    log_file = log_path.open("wb")
    try:
        proc = subprocess.Popen(
            [str(bin_resolved), "serve", "--db", str(db_path)],
            cwd=str(Path(d1_worktree).resolve()),
            env=env,
            stdout=log_file,
            stderr=subprocess.STDOUT,
        )
    except Exception:
        log_file.close()
        raise
    log_file.close()

    sidecar = IsolatedSidecar(
        proc=proc,
        url=url,
        token=token,
        port=port,
        db_path=db_path,
        config_dir=config_dir,
        log_path=log_path,
        binary_path=bin_resolved,
        binary_sha256=bin_sha,
        d1_commit=d1_commit,
        d1_tree=d1_tree,
        harness_commit=harness_commit,
        harness_tree=harness_tree,
        utc_started=utc_started,
    )

    deadline = time.monotonic() + ready_timeout_s
    health_url = f"{url}/health"
    while time.monotonic() < deadline:
        if proc.poll() is not None:
            tail = log_path.read_text(encoding="utf-8", errors="replace")[-2000:]
            raise RuntimeError(f"sidecar exited early with code {proc.returncode}:\n{tail}")
        try:
            req = urllib.request.Request(
                health_url,
                headers={"Authorization": f"Bearer {token}"},
                method="GET",
            )
            with urllib.request.urlopen(req, timeout=2.0) as resp:
                if resp.status == 200:
                    return sidecar
        except Exception:
            time.sleep(0.1)

    sidecar.close()
    tail = log_path.read_text(encoding="utf-8", errors="replace")[-2000:]
    raise TimeoutError(f"sidecar did not become healthy within {ready_timeout_s}s:\n{tail}")


class CF05SidecarClient:
    """Thin CF05-specific wrapper around SidecarDriver + HTTPTransport for feed/mutation/doc/walk APIs."""

    def __init__(self, base_url: str, token: str) -> None:
        self.base_url = base_url.rstrip("/")
        self.token = token
        self.transport = HTTPTransport(self.base_url, self.token)
        self.driver = SidecarDriver(self.transport)

    def create_source_entry(
        self,
        project_id: str,
        root_id: str,
        rel_path: str,
        *,
        kind: str = "file",
        operation_id: str | None = None,
    ) -> dict[str, object]:
        return self.transport.request(
            f"/v1/projects/{project_id}/source",
            {
                "operation_id": operation_id or str(uuid.uuid4()),
                "root_id": root_id,
                "path": rel_path,
                "kind": kind,
            },
            method="POST",
        )

    def write_source_file(
        self,
        project_id: str,
        root_id: str,
        rel_path: str,
        content: str,
        base_sha256: str,
        *,
        encoding: str = "utf-8",
        operation_id: str | None = None,
    ) -> dict[str, object]:
        return self.transport.request(
            f"/v1/projects/{project_id}/source",
            {
                "operation_id": operation_id or str(uuid.uuid4()),
                "root_id": root_id,
                "path": rel_path,
                "content": content,
                "encoding": encoding,
                "base_sha256": base_sha256,
            },
            method="PUT",
        )

    def rename_source_entry(
        self,
        project_id: str,
        root_id: str,
        src_path: str,
        dst_path: str,
        *,
        operation_id: str | None = None,
    ) -> dict[str, object]:
        return self.transport.request(
            f"/v1/projects/{project_id}/source/rename",
            {
                "operation_id": operation_id or str(uuid.uuid4()),
                "root_id": root_id,
                "from": src_path,
                "to": dst_path,
            },
            method="POST",
        )

    def copy_source_entry(
        self,
        project_id: str,
        root_id: str,
        src_path: str,
        dst_path: str,
        *,
        operation_id: str | None = None,
    ) -> dict[str, object]:
        return self.transport.request(
            f"/v1/projects/{project_id}/source/copy",
            {
                "operation_id": operation_id or str(uuid.uuid4()),
                "root_id": root_id,
                "from": src_path,
                "to": dst_path,
            },
            method="POST",
        )

    def delete_source_entry(
        self,
        project_id: str,
        root_id: str,
        rel_path: str,
        *,
        recursive: bool = False,
        operation_id: str | None = None,
    ) -> None:
        qs = urllib.parse.urlencode(
            {
                "root_id": root_id,
                "path": rel_path,
                "recursive": "true" if recursive else "false",
                "operation_id": operation_id or str(uuid.uuid4()),
            }
        )
        self.transport.request_raw(f"/v1/projects/{project_id}/source?{qs}", method="DELETE")

    def open_editor_document(
        self,
        project_id: str,
        root_id: str,
        rel_path: str,
        *,
        client_id: str = "cfs-cf05-editor",
    ) -> dict[str, object]:
        return self.transport.request(
            f"/v1/projects/{project_id}/editor-documents",
            {"path": rel_path, "root_id": root_id, "client_id": client_id},
            method="POST",
        )

    def observe_editor_document(
        self,
        project_id: str,
        document_id: str,
    ) -> dict[str, object]:
        return self.transport.request(
            f"/v1/projects/{project_id}/editor-documents/{document_id}/observe",
            {},
            method="POST",
        )

    def get_source_walk_all(
        self,
        project_id: str,
        *,
        baseline: str = "presentation",
        limit: int = 200,
    ) -> dict[str, object]:
        """Fetch all pages of GET /v1/projects/{id}/source/walk."""
        files: list[dict[str, object]] = []
        git_changes: list[dict[str, object]] = []
        seen_git: set[str] = set()
        cursor: str | None = None
        last_resp: dict[str, object] = {}
        for _ in range(50):
            resp = self.driver.list_source_walk(
                project_id,
                baseline=baseline,
                limit=limit,
                cursor=cursor,
            )
            last_resp = resp
            for f in resp.get("files") or []:
                if isinstance(f, dict):
                    files.append(f)
            for gc in resp.get("git_changes") or []:
                if isinstance(gc, dict):
                    gid = str(gc.get("id") or "")
                    if gid and gid not in seen_git:
                        seen_git.add(gid)
                        git_changes.append(gc)
            nxt = str(resp.get("next_cursor") or "")
            if not nxt or nxt == cursor:
                break
            cursor = nxt
        return {
            "baseline": last_resp.get("baseline", baseline),
            "files": files,
            "git_changes": git_changes,
            "commit_available": last_resp.get("commit_available", False),
        }

    def delete_project(self, project_id: str, *, timeout_s: float = 15.0) -> None:
        """Remove a project via POST /v1/projects/{id}/removals (202), wait for deleted + 404."""
        op_id = str(uuid.uuid4())
        req = urllib.request.Request(
            f"{self.base_url}/v1/projects/{project_id}/removals",
            data=json.dumps({"operation_id": op_id, "force": True}).encode("utf-8"),
            headers={
                "Authorization": f"Bearer {self.token}",
                "Content-Type": "application/json",
            },
            method="POST",
        )
        try:
            with urllib.request.urlopen(req, timeout=timeout_s) as resp:
                status = resp.status
        except urllib.error.HTTPError as err:
            if err.code == 404:
                return
            raise RuntimeError(f"project removal POST returned HTTP {err.code} for {project_id}") from err
        if status not in (200, 202, 204):
            raise RuntimeError(f"project removal POST returned unexpected HTTP {status} for {project_id}")

        deadline = time.monotonic() + timeout_s
        deleted = False
        while time.monotonic() < deadline:
            try:
                st_req = urllib.request.Request(
                    f"{self.base_url}/v1/projects/{project_id}/removals/{op_id}",
                    headers={"Authorization": f"Bearer {self.token}"},
                    method="GET",
                )
                with urllib.request.urlopen(st_req, timeout=5.0) as st_resp:
                    body = json.loads(st_resp.read().decode("utf-8"))
                    if isinstance(body, dict) and body.get("project_state") == "deleted":
                        deleted = True
                        break
                    if isinstance(body, dict) and body.get("project_state") == "failed":
                        raise RuntimeError(f"project removal failed for {project_id}: {body}")
            except urllib.error.HTTPError as err:
                if err.code == 404:
                    deleted = True
                    break
            time.sleep(0.1)
        if not deleted:
            raise RuntimeError(f"project removal did not reach deleted within {timeout_s}s for {project_id}")

        # Verify 404 on GET /v1/projects/{id}
        g_req = urllib.request.Request(
            f"{self.base_url}/v1/projects/{project_id}",
            headers={"Authorization": f"Bearer {self.token}"},
            method="GET",
        )
        try:
            with urllib.request.urlopen(g_req, timeout=5.0) as g_resp:
                raise RuntimeError(f"project {project_id} still returned HTTP {g_resp.status} after removal")
        except urllib.error.HTTPError as err:
            if err.code != 404:
                raise RuntimeError(f"project {project_id} post-removal GET returned HTTP {err.code}") from err

    def probe_sse_cursor(
        self,
        project_id: str,
        *,
        after: str,
        client_id: str = "cfs-probe",
    ) -> tuple[int, dict[str, object]]:
        """Issue GET /v1/events with an `after` cursor and return (status, json_body_if_error)."""
        qs = urllib.parse.urlencode(
            {"project_id": project_id, "client_id": client_id, "after": after}
        )
        req = urllib.request.Request(
            f"{self.base_url}/v1/events?{qs}",
            headers={
                "Authorization": f"Bearer {self.token}",
                "Accept": "text/event-stream",
            },
            method="GET",
        )
        try:
            with urllib.request.urlopen(req, timeout=5.0) as resp:
                return resp.status, {}
        except urllib.error.HTTPError as err:
            raw = err.read().decode("utf-8", errors="replace")
            try:
                body = json.loads(raw)
            except ValueError:
                body = {"raw": raw}
            return err.code, body if isinstance(body, dict) else {"raw": raw}


class CursorSSESubscriber:
    """Project-scoped SSE subscriber supporting `after=<cursor>` resumption and wake counting."""

    def __init__(
        self,
        base_url: str,
        token: str,
        *,
        project_id: str,
        client_id: str = "cfs-cf05-sub",
        after: str = "",
        connect_timeout_s: float = 10.0,
    ) -> None:
        self._parsed = urllib.parse.urlsplit(base_url.rstrip("/"))
        params: dict[str, str] = {"project_id": project_id, "client_id": client_id}
        if after:
            params["after"] = after
        qs = urllib.parse.urlencode(params)
        base_path = self._parsed.path.rstrip("/")
        self._request_target = f"{base_path}/v1/events?{qs}"
        self._token = token
        self._connect_timeout_s = connect_timeout_s

        self._ready = threading.Event()
        self._stop = threading.Event()
        self._cond = threading.Condition()
        self._frames: list[dict[str, object]] = []
        self._http_status: int | None = None
        self._error_body: dict[str, object] | None = None
        self._conn: http.client.HTTPConnection | None = None
        self._error: Exception | None = None
        self._thread = threading.Thread(target=self._run, daemon=True)

    @property
    def http_status(self) -> int | None:
        return self._http_status

    @property
    def error_body(self) -> dict[str, object] | None:
        return self._error_body

    def start(self) -> None:
        self._thread.start()

    def wait_ready(self, max_wait_s: float = 10.0) -> bool:
        if not self._ready.wait(timeout=max_wait_s):
            return False
        return self._http_status == 200 and self._error is None

    def frames(self) -> list[dict[str, object]]:
        with self._cond:
            return list(self._frames)

    def last_cursor(self) -> str:
        with self._cond:
            for f in reversed(self._frames):
                c = f.get("cursor")
                if isinstance(c, str) and c:
                    return c
        return ""

    def wait_for_frames(
        self,
        predicate,
        max_wait_s: float = 10.0,
    ) -> tuple[list[dict[str, object]], bool]:
        deadline = time.monotonic() + max_wait_s
        with self._cond:
            while not predicate(self._frames):
                if self._stop.is_set():
                    return list(self._frames), False
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    return list(self._frames), False
                self._cond.wait(timeout=remaining)
            return list(self._frames), True

    def _run(self) -> None:
        conn: http.client.HTTPConnection | None = None
        try:
            host = self._parsed.hostname or "127.0.0.1"
            port = self._parsed.port
            if self._parsed.scheme == "https":
                conn = http.client.HTTPSConnection(host, port, timeout=self._connect_timeout_s)
            else:
                conn = http.client.HTTPConnection(host, port, timeout=self._connect_timeout_s)
            self._conn = conn
            conn.request(
                "GET",
                self._request_target,
                headers={
                    "Authorization": f"Bearer {self._token}",
                    "Accept": "text/event-stream",
                },
            )
            resp = conn.getresponse()
            self._http_status = resp.status
            if resp.status != 200:
                raw = resp.read().decode("utf-8", errors="replace")
                try:
                    parsed = json.loads(raw)
                    self._error_body = parsed if isinstance(parsed, dict) else {"raw": raw}
                except ValueError:
                    self._error_body = {"raw": raw}
                self._error = RuntimeError(f"HTTP {resp.status}")
                return
            if conn.sock is not None:
                conn.sock.settimeout(None)
            self._ready.set()

            while not self._stop.is_set():
                raw = resp.readline()
                if not raw:
                    break
                line = raw.decode("utf-8", errors="replace").rstrip("\r\n")
                if not line.startswith("data:"):
                    continue
                payload = line[5:].strip()
                if not payload:
                    continue
                try:
                    parsed = json.loads(payload)
                except ValueError:
                    continue
                if isinstance(parsed, dict):
                    with self._cond:
                        self._frames.append(parsed)
                        self._cond.notify_all()
        except Exception as exc:
            if not self._stop.is_set():
                self._error = exc
        finally:
            self._ready.set()
            self._stop.set()
            with self._cond:
                self._cond.notify_all()
            if conn is not None:
                with contextlib.suppress(OSError):
                    conn.close()

    def close(self, max_wait_s: float = 3.0) -> None:
        self._stop.set()
        conn = self._conn
        if conn is not None:
            sock = conn.sock
            if sock is not None:
                with contextlib.suppress(OSError):
                    sock.shutdown(socket.SHUT_RDWR)
                with contextlib.suppress(OSError):
                    sock.close()
            with contextlib.suppress(OSError):
                conn.close()
        with self._cond:
            self._cond.notify_all()
        if self._thread.is_alive():
            self._thread.join(timeout=max_wait_s)
