"""Sidecar driver adapter for the D1 manual-model HTTP boundary (GB02).

Matches the exact isolated D1 sidecar launch contract from
`scripts/coordinator-benchmark/smoke.py` and `application_environment.py`:
- Strips all ambient `LYCAON_*` environment variables before setting explicit D1 keys
- Binds `LYCAON_CONFIG_ROOT`, `LYCAON_ENGINE_ROOT`, and process `cwd` strictly to
  `--product-root` (never the D2 harness worktree; Decision 009 & 010)
- Launches `[engine_bin, "serve", "--db", store_db_path]` (never unknown flags like `--addr`;
  Decision 011) with `LYCAON_ADDR`, `LYCAON_API_TOKEN`, `LYCAON_DEV=1`, `LYCAON_HARNESS=1`,
  `LYCAON_LLM_MOCK=1`, `LYCAON_LLM_MANUAL=1`
- Verifies `/health`, disables `/harness/llm/auto`, confirms `/harness/llm/pending`,
  and records owned address, scratch DB path, binary provenance, and run manifest
  without ever logging credential/token values
- Extracts the independent D3 `DeliveryLog` strictly from what was actually delivered
  to the mock model in `/harness/llm/pending` (`Message.tool_result.tool_call_id`,
  `Message.evidence_handles`, actual delivered body & authenticated delivery tags,
  and true line ranges verified against fixture bytes — NEVER synthesized handles or
  copied requested arguments)
- Waits for turn/submission settlement (`/harness/execution/{session}/{submission}` or
  session `idle`) before reading durable product observations from `store.db`
"""
from __future__ import annotations

import contextlib
from dataclasses import dataclass
import hashlib
import json
import os
import pathlib
import platform as py_platform
import re
import secrets
import socket
import sqlite3
import subprocess
import time
from typing import Any, Mapping, Sequence
import urllib.error
import urllib.parse
import urllib.request
import uuid

from .checker import CheckResult, check_slot, compare_records
from .delivery_log import (
    GROUNDED_VERDICTS,
    DeliveryLog,
    DeliveryRecord,
    LineRange,
    ProductRecord,
    ProductVerdict,
    SurfaceOutcome,
)
from .friction_probe import (
    FrictionSnapshot,
    build_exact_binary_debugger_prefix,
    parse_friction_trace_log,
    probe_store_db,
)
from .reference_resolver import Citation, PermittedScope, resolve_citation


HARNESS_ROOT: pathlib.Path = pathlib.Path(__file__).resolve().parents[3]

_DELIVERY_TAG_RE = re.compile(
    r"\[(?P<handle>[a-z][a-z0-9_]*#[1-9]\d*)(?:\s+(?P<path>\"[^\"]+\"|[^\s:\]]+)(?::(?P<start>\d+)(?:-(?P<end>\d+))?)?)?\]"
)
_NUMBERED_LINE_RE = re.compile(r"^\s*(?P<num>\d+)(?:\s*[:|]\s?|\t)(?P<rest>.*)$")


def _git_head(repo_dir: pathlib.Path) -> str:
    """Return `git rev-parse HEAD` for `repo_dir`, or 'unavailable'."""
    try:
        proc = subprocess.run(
            ["git", "-C", str(repo_dir), "rev-parse", "HEAD"],
            capture_output=True,
            text=True,
            timeout=5,
            check=False,
        )
        if proc.returncode == 0 and proc.stdout.strip():
            return proc.stdout.strip()
    except (OSError, subprocess.SubprocessError):
        pass
    return "unavailable"


def _is_within(child: pathlib.Path, parent: pathlib.Path) -> bool:
    try:
        child.resolve().relative_to(parent.resolve())
        return True
    except ValueError:
        return False


@dataclass(frozen=True)
class EngineBinaryProvenance:
    """Exact provenance of the D1 engine binary executed by the sidecar lane."""

    path: str
    sha256: str
    go_buildid: str
    platform: str
    arch: str

    def to_dict(self) -> dict[str, str]:
        return {
            "path": self.path,
            "sha256": self.sha256,
            "go_buildid": self.go_buildid,
            "platform": self.platform,
            "arch": self.arch,
        }


@dataclass(frozen=True)
class SidecarRunManifest:
    """Decision 009/010 run manifest binding D1 executable, roots, commits, and harness head.

    Never contains API tokens or credentials.
    """

    product_root: str
    product_commit: str
    harness_head: str
    resolved_config_root: str
    resolved_engine_root: str
    engine_bin: str
    engine_sha256: str
    engine_buildid: str
    platform: str
    arch: str
    process_cwd: str
    listen_addr: str = ""
    scratch_db_path: str = ""
    manual_boundary_verified: bool = False

    def to_dict(self) -> dict[str, Any]:
        return {
            "product_root": self.product_root,
            "product_commit": self.product_commit,
            "harness_head": self.harness_head,
            "resolved_config_root": self.resolved_config_root,
            "resolved_engine_root": self.resolved_engine_root,
            "engine_bin": self.engine_bin,
            "engine_sha256": self.engine_sha256,
            "engine_buildid": self.engine_buildid,
            "platform": self.platform,
            "arch": self.arch,
            "process_cwd": self.process_cwd,
            "listen_addr": self.listen_addr,
            "scratch_db_path": self.scratch_db_path,
            "manual_boundary_verified": self.manual_boundary_verified,
        }


def inspect_engine_binary(
    engine_bin: str | pathlib.Path,
    *,
    expected_sha256: str = "",
) -> EngineBinaryProvenance:
    """Compute SHA-256, Go build ID (if available), and host platform for `--engine-bin`."""
    bin_path = pathlib.Path(engine_bin).resolve()
    if not bin_path.is_file():
        raise FileNotFoundError(f"Engine binary not found: {bin_path}")

    hasher = hashlib.sha256()
    with bin_path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(65536), b""):
            hasher.update(chunk)
    digest = hasher.hexdigest()

    if expected_sha256 and digest.lower() != expected_sha256.strip().lower():
        raise ValueError(
            f"Engine binary SHA-256 mismatch for {bin_path}: "
            f"got {digest}, expected {expected_sha256.strip().lower()}"
        )

    buildid = "unavailable"
    try:
        proc = subprocess.run(
            ["go", "tool", "buildid", str(bin_path)],
            capture_output=True,
            text=True,
            timeout=5,
            check=False,
        )
        if proc.returncode == 0 and proc.stdout.strip():
            buildid = proc.stdout.strip()
    except (OSError, subprocess.SubprocessError):
        buildid = "unavailable"

    return EngineBinaryProvenance(
        path=str(bin_path),
        sha256=digest,
        go_buildid=buildid,
        platform=py_platform.system().lower(),
        arch=py_platform.machine().lower(),
    )


def probe_free_port(host: str = "127.0.0.1") -> int:
    """Bind an ephemeral loopback socket to reserve a free TCP port."""
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind((host, 0))
        return int(sock.getsockname()[1])


@dataclass(frozen=True)
class ScriptedTurn:
    """One scripted manual-model turn: either tool calls, prose content, or both."""

    content: str = ""
    tool_calls: tuple[dict[str, Any], ...] = ()
    surface: str = "coordinator_closeout"

    def __post_init__(self) -> None:
        if not isinstance(self.tool_calls, tuple):
            object.__setattr__(self, "tool_calls", tuple(self.tool_calls))


@dataclass(frozen=True)
class SidecarRunArtifact:
    """Complete joined capture from one sidecar run, keeping delivery log separate."""

    session_id: str
    engine_provenance: EngineBinaryProvenance | None
    raw_pending_requests: tuple[dict[str, Any], ...]
    scripted_responses: tuple[dict[str, Any], ...]
    delivered_tool_messages: tuple[dict[str, Any], ...]
    delivery_log: DeliveryLog
    product_records: tuple[ProductRecord, ...]
    product_closeouts: tuple[dict[str, Any], ...]
    friction_before: FrictionSnapshot | None = None
    friction_after: FrictionSnapshot | None = None
    run_manifest: SidecarRunManifest | None = None

    def to_dict(self) -> dict[str, Any]:
        return {
            "version": 1,
            "session_id": self.session_id,
            "run_manifest": self.run_manifest.to_dict() if self.run_manifest else None,
            "engine_provenance": self.engine_provenance.to_dict() if self.engine_provenance else None,
            "raw_pending_requests": list(self.raw_pending_requests),
            "scripted_responses": list(self.scripted_responses),
            "delivered_tool_messages": list(self.delivered_tool_messages),
            "delivery_log": self.delivery_log.to_dict(),
            "product_records": [
                {
                    "handle": r.handle,
                    "kind": r.kind,
                    "session_id": r.session_id,
                    "worker_leg": r.worker_leg,
                    "source_revision": r.source_revision,
                    "delivered_ranges": [rng.to_dict() for rng in r.delivered_ranges],
                    "body_sha256": r.effective_body_sha256(),
                    "path": r.path,
                    "url": r.url,
                    "artifact_id": r.artifact_id,
                    "shape": r.shape,
                    "trust": r.trust,
                }
                for r in self.product_records
            ],
            "product_closeouts": list(self.product_closeouts),
            "friction_before": self.friction_before.to_dict() if self.friction_before else None,
            "friction_after": self.friction_after.to_dict() if self.friction_after else None,
        }


class SidecarSessionClient:
    """Pure-stdlib HTTP client for the D1 manual-model harness and session API."""

    def __init__(self, base_url: str, token: str = "", *, timeout: float = 15.0) -> None:
        self.base_url = base_url.rstrip("/")
        self.token = token
        self.timeout = timeout

    def _headers(self) -> dict[str, str]:
        headers = {"Accept": "application/json", "Content-Type": "application/json"}
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        return headers

    def request(
        self,
        path: str,
        body: Mapping[str, Any] | None = None,
        *,
        method: str | None = None,
        missing: bool = False,
    ) -> Any:
        url = f"{self.base_url}{path}"
        data = json.dumps(body).encode("utf-8") if body is not None else None
        req_method = method or ("POST" if data is not None else "GET")
        req = urllib.request.Request(url, data=data, headers=self._headers(), method=req_method)
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                raw = resp.read().decode("utf-8")
                return json.loads(raw) if raw.strip() else {}
        except urllib.error.HTTPError as exc:
            if missing and exc.code == 404:
                return None
            raise

    def health(self) -> Any:
        return self.request("/health")

    def disable_auto_llm(self) -> Any:
        return self.request("/harness/llm/auto", {"enabled": False}, missing=True)

    def probe_manual_boundary(self) -> dict[str, Any]:
        """Confirm `/harness/llm/pending` is reachable before submitting any prompt."""
        payload = self.request("/harness/llm/pending")
        if not isinstance(payload, dict):
            raise RuntimeError(f"Unexpected /harness/llm/pending response: {payload!r}")
        return payload

    def get_pending(self, session_id: str) -> dict[str, Any] | None:
        q = urllib.parse.urlencode({"session_id": session_id})
        payload = self.request(f"/harness/llm/pending?{q}")
        if isinstance(payload, dict) and payload.get("id"):
            return payload
        if isinstance(payload, dict) and isinstance(payload.get("pending"), list) and payload["pending"]:
            return payload["pending"][0]
        return None

    def wait_pending(
        self, session_id: str, *, timeout: float = 15.0, poll_interval: float = 0.05
    ) -> dict[str, Any]:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            pending = self.get_pending(session_id)
            if pending is not None:
                return pending
            time.sleep(poll_interval)
        raise TimeoutError(f"Timed out waiting for /harness/llm/pending for session {session_id}")

    def respond(
        self,
        request_id: str,
        *,
        content: str = "",
        tool_calls: Sequence[Mapping[str, Any]] = (),
    ) -> dict[str, Any]:
        payload: dict[str, Any] = {"id": request_id}
        if content:
            payload["content"] = content
        if tool_calls:
            payload["tool_calls"] = list(tool_calls)
        self.request("/harness/llm/respond", payload)
        return payload

    def create_project(self, project_dir: str | pathlib.Path) -> dict[str, Any]:
        return self.request("/v1/projects", {"roots": [{"path": str(project_dir)}]})

    def create_session(self, project_id: str, *, posture: str = "build") -> dict[str, Any]:
        session = self.request("/v1/sessions", {"project_id": project_id, "posture": posture})
        sid = session["id"]
        self.request("/harness/model-limit", {"session_id": sid, "limit": 32}, missing=True)
        return session

    def wait_session_ready(self, session_id: str, *, timeout: float = 30.0) -> dict[str, Any]:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            state = self.request(f"/v1/sessions/{session_id}")
            status = state.get("status")
            if status == "idle":
                return state
            if status != "preparing":
                raise RuntimeError(f"Session {session_id} preparation ended in status={status!r}")
            time.sleep(0.1)
        raise TimeoutError(f"Timed out waiting for session {session_id} to become idle")

    def send_prompt(self, session_id: str, text: str) -> dict[str, Any]:
        return self.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": str(uuid.uuid4()), "text": text},
        )

    def wait_settled(
        self,
        session_id: str,
        submission_id: str = "",
        *,
        timeout: float = 30.0,
        poll_interval: float = 0.05,
    ) -> dict[str, Any]:
        """Wait for exact turn/submission settlement before reading store.db/API."""
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if submission_id:
                obs = self.request(
                    f"/harness/execution/{session_id}/{submission_id}", missing=True
                )
                if isinstance(obs, dict) and obs.get("settled") is True:
                    return obs
            state = self.request(f"/v1/sessions/{session_id}", missing=True)
            if isinstance(state, dict) and state.get("status") == "idle":
                if not submission_id:
                    return state
            time.sleep(poll_interval)
        raise TimeoutError(
            f"Timed out waiting for session {session_id} (submission={submission_id!r}) to settle"
        )

    def get_messages(self, session_id: str) -> Any:
        return self.request(f"/v1/sessions/{session_id}/messages")


class IsolatedSidecarProcess:
    """Launch an explicit D1 `--engine-bin` bound strictly to `--product-root`.

    Follows `smoke.py` + `application_environment.py` (Decisions 009, 010, 011):
    - Strips all ambient `LYCAON_*` environment variables
    - Uses exact shipped flags `[engine_bin, "serve", "--db", store_db_path]`
    - Sets `LYCAON_ADDR`, `LYCAON_API_TOKEN`, `LYCAON_CONFIG_DIR`, `LYCAON_CONFIG_ROOT`,
      `LYCAON_ENGINE_ROOT`, `LYCAON_DEV=1`, `LYCAON_HARNESS=1`, `LYCAON_LLM_MOCK=1`,
      `LYCAON_LLM_MANUAL=1`
    - Rejects any D2 harness root leak
    - Verifies `/health`, disables `/harness/llm/auto`, and probes `/harness/llm/pending`
    - Never logs credential/token values
    """

    def __init__(
        self,
        engine_bin: str | pathlib.Path,
        *,
        product_root: str | pathlib.Path,
        config_dir: str | pathlib.Path,
        workspace_dir: str | pathlib.Path,
        expected_sha256: str = "",
        config_root: str | pathlib.Path | None = None,
        engine_root: str | pathlib.Path | None = None,
        token: str | None = None,
        extra_env: Mapping[str, str] | None = None,
        harness_root: str | pathlib.Path = HARNESS_ROOT,
        trace_friction: bool = False,
    ) -> None:
        self.engine_bin = pathlib.Path(engine_bin).resolve()
        self.product_root = pathlib.Path(product_root).resolve()
        self.harness_root = pathlib.Path(harness_root).resolve()
        self.config_dir = pathlib.Path(config_dir).resolve()
        self.workspace_dir = pathlib.Path(workspace_dir).resolve()
        self.token = token or secrets.token_hex(24)
        self.trace_friction = bool(trace_friction)
        self.extra_env = dict(extra_env or {})
        self.port: int = 0
        self.pid: int | None = None
        self.process: subprocess.Popen[bytes] | None = None
        self.manual_boundary_verified: bool = False

        if not self.product_root.is_dir():
            raise FileNotFoundError(f"D1 product_root directory not found: {self.product_root}")

        self.resolved_config_root = (
            pathlib.Path(config_root).resolve()
            if config_root is not None
            else (self.product_root / "lycaon").resolve()
        )
        self.resolved_engine_root = (
            pathlib.Path(engine_root).resolve()
            if engine_root is not None
            else (self.product_root / "lycaon-den" / "src-tauri" / "engine-root").resolve()
        )

        for label, candidate in (
            ("product_root", self.product_root),
            ("resolved_config_root", self.resolved_config_root),
            ("resolved_engine_root", self.resolved_engine_root),
        ):
            if _is_within(candidate, self.harness_root):
                raise ValueError(
                    f"D2 harness root leak detected: {label}={candidate} lies inside "
                    f"harness_root={self.harness_root}"
                )

        if not _is_within(self.resolved_config_root, self.product_root):
            raise ValueError(
                f"resolved_config_root={self.resolved_config_root} must reside inside "
                f"product_root={self.product_root}"
            )
        if not (
            _is_within(self.resolved_engine_root, self.product_root)
            or _is_within(self.resolved_engine_root, self.engine_bin.parent)
        ):
            raise ValueError(
                f"resolved_engine_root={self.resolved_engine_root} must reside inside "
                f"product_root={self.product_root} or engine_bin directory={self.engine_bin.parent}"
            )

        self.provenance: EngineBinaryProvenance = inspect_engine_binary(
            self.engine_bin, expected_sha256=expected_sha256
        )

    @property
    def base_url(self) -> str:
        return f"http://127.0.0.1:{self.port}"

    @property
    def store_db_path(self) -> pathlib.Path:
        return self.config_dir / "store.db"

    def build_command(self) -> list[str]:
        """Return exact D1 `lycaon-dev serve --db <scratch_db>` invocation (Decision 011)."""
        return [str(self.engine_bin), "serve", "--db", str(self.store_db_path)]

    def build_process_env(self, port: int | None = None) -> dict[str, str]:
        """Build clean isolated environment stripping ambient `LYCAON_*` vars (Decision 010)."""
        listen_port = port if port is not None else self.port
        env = {k: v for k, v in os.environ.items() if not k.startswith("LYCAON_")}
        env.update(
            {
                "LYCAON_CONFIG_DIR": str(self.config_dir),
                "LYCAON_CONFIG_ROOT": str(self.resolved_config_root),
                "LYCAON_ENGINE_ROOT": str(self.resolved_engine_root),
                "LYCAON_ADDR": f"127.0.0.1:{listen_port}",
                "LYCAON_API_TOKEN": self.token,
                "LYCAON_DEV": "1",
                "LYCAON_HARNESS": "1",
                "LYCAON_LOG_LEVEL": "debug",
                "LYCAON_LLM_MOCK": "1",
                "LYCAON_LLM_MANUAL": "1",
                "LYCAON_COMMAND_PATH": env.get("PATH", ""),
            }
        )
        env.update(self.extra_env)
        return env

    def build_run_manifest(self) -> SidecarRunManifest:
        """Build the Decision 009/010 run manifest (never includes tokens)."""
        return SidecarRunManifest(
            product_root=str(self.product_root),
            product_commit=_git_head(self.product_root),
            harness_head=_git_head(self.harness_root),
            resolved_config_root=str(self.resolved_config_root),
            resolved_engine_root=str(self.resolved_engine_root),
            engine_bin=self.provenance.path,
            engine_sha256=self.provenance.sha256,
            engine_buildid=self.provenance.go_buildid,
            platform=self.provenance.platform,
            arch=self.provenance.arch,
            process_cwd=str(self.product_root),
            listen_addr=f"127.0.0.1:{self.port}" if self.port else "",
            scratch_db_path=str(self.store_db_path),
            manual_boundary_verified=self.manual_boundary_verified,
        )

    def start(self, *, ready_timeout: float | None = None) -> SidecarSessionClient:
        if ready_timeout is None:
            ready_timeout = 90.0 if self.trace_friction else 60.0
        self.config_dir.mkdir(parents=True, exist_ok=True)
        self.workspace_dir.mkdir(parents=True, exist_ok=True)
        self.port = probe_free_port()
        log_path = self.config_dir / "sidecar.log"
        self.log_path = log_path
        env = self.build_process_env(self.port)
        cmd = self.build_command()
        if self.trace_friction:
            cmd = [*build_exact_binary_debugger_prefix(self.config_dir), *cmd]
        with log_path.open("wb") as log_fh:
            self.process = subprocess.Popen(
                cmd,
                cwd=str(self.product_root),
                env=env,
                stdout=log_fh,
                stderr=subprocess.STDOUT,
            )
        self.pid = self.process.pid
        # Persist ownership metadata without any token/credential
        (self.config_dir / "ownership.json").write_text(
            json.dumps(
                {
                    "pid": self.pid,
                    "listen_addr": f"127.0.0.1:{self.port}",
                    "scratch_db_path": str(self.store_db_path),
                },
                indent=2,
                sort_keys=True,
            ),
            encoding="utf-8",
        )
        client = SidecarSessionClient(self.base_url, self.token)
        deadline = time.monotonic() + ready_timeout
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                raise RuntimeError(
                    f"Sidecar exited prematurely with code {self.process.returncode}; see {log_path}"
                )
            try:
                client.health()
                client.disable_auto_llm()
                client.probe_manual_boundary()
                self.manual_boundary_verified = True
                return client
            except urllib.error.HTTPError as exc:
                if exc.code in {408, 429, 500, 502, 503, 504}:
                    time.sleep(0.1)
                    continue
                self.stop()
                raise
            except (urllib.error.URLError, OSError):
                time.sleep(0.1)
        self.stop()
        raise TimeoutError(f"Sidecar failed to become ready on {self.base_url}/health within {ready_timeout}s")

    def stop(self) -> None:
        if self.trace_friction and getattr(self, "log_path", None) and self.log_path.is_file():
            try:
                owned_pid, _ = parse_friction_trace_log(self.log_path.read_text(encoding="utf-8", errors="replace"))
                if owned_pid:
                    chk = subprocess.run(["ps", "-p", str(owned_pid), "-o", "comm="], capture_output=True, text=True, check=False)
                    if chk.returncode == 0 and pathlib.Path(chk.stdout.strip()).name == "lycaon-dev":
                        os.kill(owned_pid, 15)
            except OSError:
                pass
        if self.process is None:
            return
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=5)
        self.process = None

    def __enter__(self) -> SidecarSessionClient:
        return self.start()

    def __exit__(self, *_exc: Any) -> None:
        self.stop()


def _decompress_zstd(data: bytes) -> bytes | None:
    """Decompress zstd content blob using Python 3.14+ stdlib or `zstd` CLI if available."""
    try:
        from compression import zstd as _std_zstd  # type: ignore[import-not-found]
        return _std_zstd.decompress(data)
    except Exception:
        pass
    try:
        proc = subprocess.run(
            ["zstd", "-dc"],
            input=data,
            capture_output=True,
            timeout=5,
            check=False,
        )
        if proc.returncode == 0:
            return proc.stdout
    except (OSError, subprocess.SubprocessError):
        pass
    return None


def _read_content_blob_plaintext(
    db_file: pathlib.Path, sha: str, *, project_id: str = ""
) -> bytes | None:
    """Locate and decompress a D1 `projects/<project_id>/model-content/<sha[:2]>/<sha[2:]>` blob."""
    if len(sha) != 64:
        return None
    base_dir = db_file.parent
    candidates: list[pathlib.Path] = []
    if project_id:
        direct = base_dir / "projects" / project_id / "model-content" / sha[:2] / sha[2:]
        if direct.is_file():
            candidates.append(direct)
    if not candidates:
        candidates = list(base_dir.glob(f"projects/*/model-content/{sha[:2]}/{sha[2:]}"))
    if not candidates:
        candidates = list(base_dir.glob(f"**/model-content/{sha[:2]}/{sha[2:]}"))
    if not candidates:
        return None
    raw = candidates[0].read_bytes()
    decompressed = _decompress_zstd(raw)
    return decompressed if decompressed is not None else raw


def _decode_evidence_body_bytes(blob_plaintext: bytes) -> bytes:
    """Convert D1 `json.Marshal(rec.Body)` (`[]string` or JSON string) to raw bytes."""
    try:
        parsed = json.loads(blob_plaintext.decode("utf-8"))
        if isinstance(parsed, list) and all(isinstance(x, str) for x in parsed):
            if not parsed:
                return b""
            if len(parsed) == 1:
                return parsed[0].encode("utf-8")
            return ("\n".join(parsed) + "\n").encode("utf-8")
        if isinstance(parsed, str):
            return parsed.encode("utf-8")
    except (UnicodeDecodeError, ValueError):
        pass
    return blob_plaintext


def _strip_leading_delivery_tags(text: str) -> str:
    """Strip leading host-issued delivery tag lines (e.g. `[read#1 ...]`, `[command#1]`)."""
    lines = text.splitlines(keepends=True)
    idx = 0
    while idx < len(lines) and _DELIVERY_TAG_RE.fullmatch(lines[idx].strip()):
        idx += 1
    return "".join(lines[idx:])


_CMD_METADATA_HDR = "Data from tool (command_metadata):"
_CMD_STDOUT_HDR = "Data from tool (command_stdout):"
_NEXT_SEGMENT_RE = re.compile(r"^Data from tool \([^)]+\):", re.MULTILINE)


def _parse_command_wire_projection(model_content: str) -> tuple[str, str] | None:
    """Parse D1's structured `Message.content` projection into (metadata_segment, stdout_payload).

    Matches `lycaon/internal/llm/transcript/command_result.go` as verified in
    `coordination/receipts/coordinator-gb02-command-wire-projection.md`:
    - Host-issued delivery tags are extracted ONLY from `command_metadata`.
    - `command_stdout` is payload text only (tag-lookalikes inside stdout are never metadata).
    """
    if _CMD_METADATA_HDR not in model_content and _CMD_STDOUT_HDR not in model_content:
        return None

    metadata_seg = ""
    meta_idx = model_content.find(_CMD_METADATA_HDR)
    if meta_idx != -1:
        after_meta = model_content[meta_idx + len(_CMD_METADATA_HDR) :]
        nxt = _NEXT_SEGMENT_RE.search(after_meta)
        metadata_seg = after_meta[: nxt.start()] if nxt else after_meta

    stdout_payload = ""
    out_idx = model_content.find(_CMD_STDOUT_HDR)
    if out_idx != -1:
        after_out = model_content[out_idx + len(_CMD_STDOUT_HDR) :]
        nxt = _NEXT_SEGMENT_RE.search(after_out)
        out_seg = after_out[: nxt.start()] if nxt else after_out
        # Extract fenced payload after `Command stdout:\n```\n` up to closing `\n``` `
        fence_start = out_seg.find("```\n")
        if fence_start != -1:
            inner = out_seg[fence_start + 4 :]
            fence_end = inner.rfind("\n```")
            if fence_end != -1:
                stdout_payload = inner[:fence_end]
                # D1's command_stdout fence adds one blank framing newline before closing ```
                if stdout_payload.endswith("\n\n"):
                    stdout_payload = stdout_payload[:-1]
            else:
                stdout_payload = inner
        else:
            stdout_payload = out_seg

    return (metadata_seg, stdout_payload)


def _strip_cat_n_prefixes(source_text: str) -> bytes:
    """Strip numbered-line prefixes while preserving blank lines and exact clipped ends."""
    if not source_text:
        return b""
    lines = source_text.splitlines(keepends=True)
    out_parts: list[str] = []
    for ln in lines:
        eol = ""
        if ln.endswith("\r\n"):
            core, eol = ln[:-2], "\r\n"
        elif ln.endswith("\n"):
            core, eol = ln[:-1], "\n"
        elif ln.endswith("\r"):
            core, eol = ln[:-1], "\r"
        else:
            core = ln
        nm = _NUMBERED_LINE_RE.match(core)
        out_parts.append((nm.group("rest") if nm else core) + eol)
    return "".join(out_parts).encode("utf-8")


def _extract_delivered_read_bytes(delivered_text: str) -> bytes:
    """Derive independent delivered source bytes strictly from model-facing delivery (Decision 016).

    NEVER reads or substitutes bytes from the fixture file on disk.
    Preserves blank lines and exact clipped ends (including absence of a trailing newline).
    """
    proj = _parse_command_wire_projection(delivered_text)
    if proj is not None:
        return _strip_cat_n_prefixes(proj[1])

    stripped = _strip_leading_delivery_tags(delivered_text)
    source_text = stripped
    try:
        envelope = json.loads(stripped)
        if isinstance(envelope, Mapping) and isinstance(envelope.get("stdout"), str):
            source_text = envelope["stdout"]
    except (TypeError, ValueError):
        pass
    return _strip_cat_n_prefixes(source_text)


def _safe_fixture_file(ws_path: pathlib.Path | None, rel_path: str) -> pathlib.Path | None:
    """Resolve `rel_path` strictly inside `ws_path`, rejecting any traversal outside fixture root."""
    if ws_path is None or not rel_path:
        return None
    candidate = (ws_path / rel_path).resolve()
    if not _is_within(candidate, ws_path):
        return None
    return candidate if candidate.is_file() else None


def _format_independent_command_body(delivered_text: str) -> bytes:
    """Derive independent command#1 body from delivered tool_result.content.

    Strips leading delivery tags to obtain the JSON envelope and formats the
    canonical program observation body (`program_id`, `state`, optional
    `status_code`, blank line, JSON body).
    """
    stripped = _strip_leading_delivery_tags(delivered_text)
    try:
        envelope = json.loads(stripped)
    except (TypeError, ValueError):
        return delivered_text.encode("utf-8")
    if not isinstance(envelope, Mapping) or not envelope.get("program_id"):
        return delivered_text.encode("utf-8")
    prog_id = str(envelope["program_id"])
    state = str(envelope.get("state") or "")
    header = f"program_id: {prog_id}\nstate: {state}"
    status_code = envelope.get("status_code")
    if status_code is None:
        status_code = envelope.get("status")
    if isinstance(status_code, int) and not isinstance(status_code, bool):
        header += f"\nstatus_code: {status_code}"
    return f"{header}\n\n{stripped}".encode("utf-8")


def extract_product_observations(
    db_path: str | pathlib.Path,
    session_id: str,
) -> tuple[tuple[ProductRecord, ...], tuple[dict[str, Any], ...]]:
    """Read durable product `evidence_records` and closeout `grounding_json` from `store.db`.

    Strictly separate from the independent `DeliveryLog` (D3).
    """
    db_file = pathlib.Path(db_path)
    if not db_file.is_file():
        return ((), ())

    records: list[ProductRecord] = []
    closeouts: list[dict[str, Any]] = []

    with contextlib.closing(sqlite3.connect(f"file:{db_file}?mode=ro", uri=True)) as conn:
        conn.row_factory = sqlite3.Row
        tables = {
            row[0]
            for row in conn.execute("SELECT name FROM sqlite_master WHERE type='table'").fetchall()
        }

        if "evidence_records" in tables:
            cols = {row[1] for row in conn.execute("PRAGMA table_info(evidence_records)").fetchall()}
            rows = conn.execute(
                "SELECT * FROM evidence_records WHERE session_id = ? ORDER BY handle",
                (session_id,),
            ).fetchall()
            for row in rows:
                ranges: list[LineRange] = []
                source_rev = str(row["source_revision"]) if "source_revision" in cols else ""
                lr_raw = row["line_ranges"] if "line_ranges" in cols else ""
                if lr_raw:
                    try:
                        lr_obj = json.loads(lr_raw)
                        for rng in lr_obj.get("ranges", ()):
                            if isinstance(rng, Mapping) and "start" in rng and "end" in rng:
                                ranges.append(LineRange(int(rng["start"]), int(rng["end"])))
                        sd = lr_obj.get("source_delivery")
                        if isinstance(sd, Mapping):
                            if not ranges:
                                sl = sd.get("start_line")
                                el = sd.get("end_line")
                                if isinstance(sl, int) and isinstance(el, int) and sl >= 1 and el >= sl:
                                    ranges.append(LineRange(sl, el))
                            src = sd.get("source")
                            if not source_rev and isinstance(src, Mapping) and src.get("SHA256"):
                                source_rev = str(src["SHA256"])
                    except (TypeError, ValueError):
                        pass
                marks_untrusted = int(row["marks_untrusted"]) if "marks_untrusted" in cols else 0
                project_id = str(row["project_id"]) if "project_id" in cols else ""
                blob_sha = str(row["content_blob_sha256"]) if "content_blob_sha256" in cols else ""
                decoded_raw: bytes | None = None
                effective_sha = blob_sha
                if blob_sha:
                    blob_plain = _read_content_blob_plaintext(db_file, blob_sha, project_id=project_id)
                    if blob_plain is not None:
                        decoded_raw = _decode_evidence_body_bytes(blob_plain)
                        # For file_region `read` records whose blob stores numbered lines,
                        # verify unnumbered source lines match `source_delivery.source.SHA256`.
                        if str(row["kind"]) == "read" and source_rev:
                            raw_lines = decoded_raw.decode("utf-8", errors="replace").splitlines()
                            unnumbered = [
                                _NUMBERED_LINE_RE.match(ln).group("rest") if _NUMBERED_LINE_RE.match(ln) else ln  # type: ignore[union-attr]
                                for ln in raw_lines
                            ]
                            unnum_bytes = ("\n".join(unnumbered) + "\n").encode("utf-8")
                            if hashlib.sha256(unnum_bytes).hexdigest() == source_rev:
                                decoded_raw = unnum_bytes
                        effective_sha = hashlib.sha256(decoded_raw).hexdigest()
                records.append(
                    ProductRecord(
                        handle=str(row["handle"]),
                        kind=str(row["kind"]),
                        session_id=str(row["session_id"]),
                        worker_leg=str(row["worker_leg"]) if "worker_leg" in cols else "",
                        source_revision=source_rev,
                        delivered_ranges=tuple(ranges),
                        body_sha256=effective_sha,
                        raw_bytes=decoded_raw,
                        path=str(row["path"]) if "path" in cols else "",
                        url=str(row["url"]) if "url" in cols else "",
                        shape=str(row["shape"]) if "shape" in cols else "",
                        trust="untrusted" if marks_untrusted else "trusted",
                        superseded_by=str(row["superseded_by"] or "") if "superseded_by" in cols else "",
                    )
                )

        if "messages" in tables:
            cols = {row[1] for row in conn.execute("PRAGMA table_info(messages)").fetchall()}
            if "grounding_json" in cols:
                for row in conn.execute(
                    "SELECT id, role, content, grounding_json FROM messages WHERE session_id = ? AND grounding_json IS NOT NULL AND grounding_json != ''",
                    (session_id,),
                ).fetchall():
                    try:
                        parsed = json.loads(row["grounding_json"])
                    except (TypeError, ValueError):
                        parsed = {}
                    closeouts.append(
                        {
                            "message_id": str(row["id"]),
                            "role": str(row["role"]),
                            "content": str(row["content"]),
                            "grounding": parsed,
                        }
                    )

    return (tuple(records), tuple(closeouts))


def _parse_delivery_tags(text: str, authenticated_handles: Sequence[str]) -> tuple[str, ...]:
    """Extract authenticated host-issued delivery tags from the delivered tool envelope.

    Decision 012 para 7: When `authenticated_handles` is empty, NEVER accept
    tag-shaped text from tool output as a host-issued delivery tag.
    """
    auth_set = {h.strip() for h in authenticated_handles if h and h.strip()}
    if not auth_set:
        return ()
    tags: list[str] = []
    for m in _DELIVERY_TAG_RE.finditer(text):
        handle = m.group("handle")
        if handle not in auth_set:
            continue
        tags.append(m.group(0))
    return tuple(tags)


def _derive_actual_range_and_numbering(
    delivered_text: str,
    *,
    delivery_tags: Sequence[str],
    fixture_file: pathlib.Path | None = None,
) -> tuple[tuple[LineRange, ...], bool, bool]:
    """Determine actual delivered line range and numbering strictly from authenticated tags + fixture bytes.

    Decision 010 & Decision 012 para 7:
    - NEVER copies requested tool arguments as delivered ranges.
    - NEVER promotes bare printed-looking lines into a range without an
      independently verified fixture mapping or an authenticated delivery tag.
    """
    tag_ranges: list[LineRange] = []
    for tag in delivery_tags:
        m = _DELIVERY_TAG_RE.match(tag)
        if m and m.group("start"):
            s = int(m.group("start"))
            e = int(m.group("end") or s)
            if s >= 1 and e >= s:
                tag_ranges.append(LineRange(s, e))

    # Extract candidate source lines (excluding standalone delivery tag lines)
    body_lines = [
        ln
        for ln in delivered_text.splitlines()
        if ln.strip() and not _DELIVERY_TAG_RE.fullmatch(ln.strip())
    ]
    printed_nums: list[int] = []
    stripped_lines: list[str] = []
    for ln in body_lines:
        nm = _NUMBERED_LINE_RE.match(ln)
        if nm:
            printed_nums.append(int(nm.group("num")))
            stripped_lines.append(nm.group("rest"))

    has_printed_nums = bool(body_lines) and len(printed_nums) == len(body_lines)

    # 1. Independently verify delivered lines against fixture file bytes when available
    if fixture_file is not None and fixture_file.is_file() and body_lines:
        fixture_lines = fixture_file.read_text(encoding="utf-8", errors="replace").splitlines()
        probe_lines = stripped_lines if has_printed_nums else body_lines
        n = len(probe_lines)
        for idx in range(0, len(fixture_lines) - n + 1):
            if fixture_lines[idx : idx + n] == probe_lines:
                true_start = idx + 1
                true_end = idx + n
                true_range = (LineRange(true_start, true_end),)
                if has_printed_nums:
                    expected_nums = list(range(true_start, true_end + 1))
                    if printed_nums == expected_nums:
                        return (true_range, True, False)
                    return (true_range, False, True)
                return (true_range, False, False)

    # 2. Otherwise require an authenticated host-issued delivery tag carrying a range
    if tag_ranges:
        if has_printed_nums and len(tag_ranges) == 1:
            expected_nums = list(range(tag_ranges[0].start, tag_ranges[0].end + 1))
            if printed_nums == expected_nums:
                return (tuple(tag_ranges), True, False)
            return (tuple(tag_ranges), False, True)
        return (tuple(tag_ranges), has_printed_nums, False)

    # 3. No authenticated tag and no verified fixture mapping -> do NOT promote bare printed numbers
    return ((), False, False)


def extract_tool_messages_from_pending(
    pending: Mapping[str, Any],
    seen_call_ids: set[str],
    call_meta: Mapping[str, Mapping[str, Any]],
) -> list[dict[str, Any]]:
    """Extract newly delivered tool messages from a raw D1 `/harness/llm/pending` payload.

    Handles D1's real nested `Message.tool_result.tool_call_id` and
    `Message.evidence_handles` shape (Decision 010 para 9) as well as flat
    mock/test shapes.
    """
    new_msgs: list[dict[str, Any]] = []
    for msg in pending.get("messages") or ():
        if not isinstance(msg, Mapping):
            continue
        if msg.get("role") != "tool":
            continue
        tool_result = msg.get("tool_result")
        tr_map = tool_result if isinstance(tool_result, Mapping) else {}
        call_id = str(
            tr_map.get("tool_call_id")
            or msg.get("tool_call_id")
            or msg.get("call_id")
            or ""
        )
        if not call_id or call_id in seen_call_ids:
            continue
        seen_call_ids.add(call_id)
        meta = call_meta.get(call_id, {})

        # Decision 016 para 7 & coordinator-gb02-command-wire-projection.md:
        # Top-level `Message.content` is the real model-facing projection (matching
        # `openai_compat_wire.go`, which uses Message.Content first and falls back to
        # nested `tool_result.content` only when top-level content is empty).
        # Retain both projections separately so host metadata tags come strictly from
        # `command_metadata` (never stdout payload) and receipt details come from `tool_result`.
        model_content = str(msg.get("content") or "")
        nested_receipt = ""
        if tr_map:
            nested_receipt = str(tr_map.get("content") or tr_map.get("output") or "")
        delivered_text = model_content if model_content else nested_receipt

        raw_handles = msg.get("evidence_handles")
        if not raw_handles and tr_map:
            raw_handles = tr_map.get("evidence_handles")
        evidence_handles = (
            tuple(str(h) for h in raw_handles if isinstance(h, str) and h.strip())
            if isinstance(raw_handles, Sequence) and not isinstance(raw_handles, (str, bytes))
            else ()
        )

        proj = _parse_command_wire_projection(model_content) if model_content else None
        if proj is not None:
            meta_segment, stdout_payload = proj
            # Trust ONLY `command_metadata` for host-issued tags; stdout is payload text!
            delivery_tags = _parse_delivery_tags(meta_segment, evidence_handles)
        else:
            stdout_payload = ""
            delivery_tags = _parse_delivery_tags(delivered_text, evidence_handles)

        new_msgs.append(
            {
                "message_id": str(msg.get("id") or ""),
                "call_id": call_id,
                "tool": str(meta.get("name") or tr_map.get("tool_name") or msg.get("name") or ""),
                "arguments": dict(meta.get("args") or {}),
                "delivered_body": delivered_text,
                "delivered_body_sha256": hashlib.sha256(delivered_text.encode("utf-8")).hexdigest(),
                "delivered_stdout": stdout_payload,
                "nested_receipt_content": nested_receipt,
                "evidence_handles": list(evidence_handles),
                "delivery_tags": list(delivery_tags),
                "request_id": str(pending.get("id") or ""),
            }
        )
    return new_msgs


def run_scripted_session(
    client: SidecarSessionClient,
    *,
    session_id: str,
    turns: Sequence[ScriptedTurn],
    db_path: str | pathlib.Path | None = None,
    workspace_dir: str | pathlib.Path | None = None,
    submission_id: str = "",
    wait_for_settlement: bool = False,
    max_repair_replies: int = 0,
    repair_reply_content: str = "",
    fixture_provenance: str = "host",
    source_revision: str = "",
    round_id: str = "round-1",
    intent_window: str = "intent-1",
    engine_provenance: EngineBinaryProvenance | None = None,
    run_manifest: SidecarRunManifest | None = None,
    sse_events: Sequence[Mapping[str, Any]] = (),
    poll_timeout: float = 30.0,
) -> SidecarRunArtifact:
    """Drive scripted multi-turn completions against `/harness/llm/pending` & `/respond`.

    Builds the independent D3 `DeliveryLog` strictly from the actual tool messages
    delivered to the mock model (real `evidence_handles`, authenticated `delivery_tags`,
    actual delivered bytes, and fixture-verified line ranges — NEVER synthesized
    handles or copied requested arguments).

    When `wait_for_settlement=True` and `max_repair_replies > 0`, answers any
    host-issued citation-repair pending turns until the submission settles cleanly.
    """
    friction_before = probe_store_db(db_path, session_id) if db_path else None
    raw_pending: list[dict[str, Any]] = []
    scripted_resps: list[dict[str, Any]] = []
    delivered_tools: list[dict[str, Any]] = []
    delivery_records: list[DeliveryRecord] = []

    seen_call_ids: set[str] = set()
    call_meta: dict[str, dict[str, Any]] = {}
    ws_path = pathlib.Path(workspace_dir).resolve() if workspace_dir else None

    for turn in turns:
        pending = client.wait_pending(session_id, timeout=poll_timeout)
        raw_pending.append(dict(pending))

        # Auto-load tools via `request_tools` if a scripted tool is not yet offered
        offered = set(pending.get("tools") or ())
        if offered and turn.tool_calls:
            missing_tools = sorted({tc["name"] for tc in turn.tool_calls if tc["name"] not in offered})
            if missing_tools and "request_tools" in offered:
                load_id = f"load-{uuid.uuid4().hex[:8]}"
                client.respond(
                    str(pending["id"]),
                    tool_calls=({"id": load_id, "name": "request_tools", "args": {"need": ", ".join(missing_tools)}},),
                )
                pending = client.wait_pending(session_id, timeout=poll_timeout)
                raw_pending.append(dict(pending))

        newly_delivered = extract_tool_messages_from_pending(pending, seen_call_ids, call_meta)
        for item in newly_delivered:
            delivered_tools.append(item)
            handles = item["evidence_handles"]
            all_tags = tuple(item["delivery_tags"])
            delivered_text = item["delivered_body"]
            args = item["arguments"]
            arg_path = str(args.get("path") or "")
            url = str(args.get("url") or "")

            # Decision 012 para 7: Preserve raw message once, then derive a separately
            # attributed DeliveryRecord for each authenticated host-issued handle.
            for handle in handles:
                kind = handle.split("#", 1)[0] if "#" in handle else ""
                handle_tags = tuple(
                    t for t in all_tags
                    if (_DELIVERY_TAG_RE.match(t) and _DELIVERY_TAG_RE.match(t).group("handle") == handle)  # type: ignore[union-attr]
                )
                handle_path = ""
                if handle_tags:
                    m = _DELIVERY_TAG_RE.match(handle_tags[0])
                    if m and m.group("path"):
                        handle_path = m.group("path").strip('"')
                elif kind != "command":
                    handle_path = arg_path

                if kind == "command":
                    rec_shape = "command"
                    ranges, line_numbered, printed_other = ((), False, False)
                    receipt_src = item.get("nested_receipt_content") or delivered_text
                    rec_bytes = _format_independent_command_body(receipt_src)
                    rec_source_rev = source_revision
                else:
                    rec_shape = "file_region" if handle_path else ""
                    fixture_file = _safe_fixture_file(ws_path, handle_path)
                    # Decision 016: Derive delivered bytes strictly from model-facing projection,
                    # NEVER substituting bytes from the fixture file on disk!
                    rec_bytes = _extract_delivered_read_bytes(delivered_text)
                    if item.get("delivered_stdout"):
                        range_probe_text = str(item["delivered_stdout"])
                    else:
                        stripped_env = _strip_leading_delivery_tags(delivered_text)
                        try:
                            env_obj = json.loads(stripped_env)
                            range_probe_text = (
                                env_obj["stdout"]
                                if isinstance(env_obj, Mapping) and isinstance(env_obj.get("stdout"), str)
                                else stripped_env
                            )
                        except (TypeError, ValueError):
                            range_probe_text = stripped_env

                    ranges, line_numbered, printed_other = _derive_actual_range_and_numbering(
                        range_probe_text,
                        delivery_tags=handle_tags,
                        fixture_file=fixture_file,
                    )
                    # Only attribute fixture source_revision when delivered bytes actually match
                    # the admitted fixture file (never mask a divergence!).
                    rec_source_rev = source_revision
                    if fixture_file is not None and ranges:
                        file_bytes = fixture_file.read_bytes()
                        f_lines = file_bytes.decode("utf-8", errors="replace").splitlines(keepends=True)
                        expected_slice = "".join(
                            f_lines[ln_no - 1]
                            for rng in ranges
                            for ln_no in range(rng.start, min(rng.end, len(f_lines)) + 1)
                        ).encode("utf-8")
                        if rec_bytes == expected_slice:
                            rec_source_rev = hashlib.sha256(file_bytes).hexdigest()

                delivery_records.append(
                    DeliveryRecord(
                        tool=item["tool"],
                        arguments=args,
                        raw_bytes=rec_bytes,
                        delivered_ranges=ranges,
                        delivery_tags=handle_tags,
                        line_numbered=line_numbered,
                        printed_other_numbers=printed_other,
                        session_id=session_id,
                        worker_leg="",
                        round_id=round_id,
                        intent_window=intent_window,
                        source_revision=rec_source_rev,
                        handle=handle,
                        kind=kind,
                        path=handle_path,
                        url=url,
                        artifact_id="",
                        call_id=item["call_id"],
                        fixture_provenance=fixture_provenance,
                        shape=rec_shape,
                    )
                )

        outgoing_calls: list[dict[str, Any]] = []
        for idx, tc in enumerate(turn.tool_calls):
            cid = str(tc.get("id") or f"call-{len(scripted_resps) + 1}-{idx + 1}")
            call_entry = {"id": cid, "name": tc["name"], "args": dict(tc.get("args") or {})}
            call_meta[cid] = call_entry
            outgoing_calls.append(call_entry)

        resp_payload = client.respond(
            str(pending["id"]),
            content=turn.content,
            tool_calls=outgoing_calls,
        )
        scripted_resps.append(resp_payload)

    if wait_for_settlement:
        deadline = time.monotonic() + poll_timeout
        repair_replies_sent = 0
        settled = False
        while time.monotonic() < deadline:
            if submission_id:
                obs = client.request(
                    f"/harness/execution/{session_id}/{submission_id}", missing=True
                )
                if isinstance(obs, dict) and obs.get("settled") is True:
                    settled = True
                    break
            else:
                st = client.request(f"/v1/sessions/{session_id}", missing=True)
                if isinstance(st, dict) and st.get("status") == "idle":
                    settled = True
                    break

            if repair_replies_sent < max_repair_replies:
                repair_pending = client.get_pending(session_id)
                if repair_pending is not None:
                    raw_pending.append(dict(repair_pending))
                    reply_text = repair_reply_content or (turns[-1].content if turns else "")
                    resp_payload = client.respond(str(repair_pending["id"]), content=reply_text)
                    scripted_resps.append(resp_payload)
                    repair_replies_sent += 1
                    continue
            time.sleep(0.05)

        if not settled:
            raise TimeoutError(
                f"Timed out waiting for session {session_id} (submission={submission_id!r}, "
                f"repair_replies_sent={repair_replies_sent}) to settle"
            )

    product_records, product_closeouts = (
        extract_product_observations(db_path, session_id) if db_path else ((), ())
    )
    friction_after = probe_store_db(db_path, session_id, sse_events=sse_events) if db_path else None

    return SidecarRunArtifact(
        session_id=session_id,
        engine_provenance=engine_provenance,
        raw_pending_requests=tuple(raw_pending),
        scripted_responses=tuple(scripted_resps),
        delivered_tool_messages=tuple(delivered_tools),
        delivery_log=DeliveryLog(records=tuple(delivery_records)),
        product_records=product_records,
        product_closeouts=product_closeouts,
        friction_before=friction_before,
        friction_after=friction_after,
        run_manifest=run_manifest,
    )


CITATION_REFUSAL_CODES: frozenset[str] = frozenset(
    {
        "INVEST_HANDLE_NOT_OBSERVED",
        "INVEST_CITATION_UNVERIFIABLE",
        "INVEST_URL_NOT_OBSERVED",
        "SYNTH_HANDLE_NOT_IN_LEGS",
        "SYNTH_CITATION_UNVERIFIABLE",
        "SYNTH_URL_NOT_OBSERVED",
    }
)


def _match_cited_evidence(
    cited_evidence: Sequence[Mapping[str, Any]],
    *,
    allowed_handles: set[str],
    expected_path: str = "",
    expected_line: int | None = None,
) -> Mapping[str, Any] | None:
    """Find a grounded `cited_evidence` readback matching the exact handle, path, and line."""
    for entry in cited_evidence:
        if not isinstance(entry, Mapping):
            continue
        handle = str(entry.get("handle") or "")
        verdict = str(entry.get("verdict") or "")
        if handle not in allowed_handles or verdict not in GROUNDED_VERDICTS:
            continue
        if expected_line is not None and entry.get("line") != expected_line:
            continue
        if expected_path and str(entry.get("path") or "") != expected_path:
            continue
        return entry
    return None


def _refusal_names_rejected_occurrence(
    artifact: SidecarRunArtifact,
    grounding: Mapping[str, Any],
    unsupported_citation: Citation,
) -> bool:
    """Verify the refusal before recovery or grounding payload names the rejected occurrence."""
    needles = [
        n
        for n in (
            unsupported_citation.handle,
            unsupported_citation.raw.strip("[]"),
            f":{unsupported_citation.start_line}" if unsupported_citation.start_line is not None else "",
        )
        if n
    ]
    if not needles:
        return False
    # Check retained raw pending repair prompts (after initial turn) and durable grounding payload
    blobs: list[str] = [json.dumps(grounding)]
    for req in artifact.raw_pending_requests[2:]:
        blobs.append(json.dumps(req))
    combined = "\n".join(blobs)
    return any(needle in combined for needle in needles)


def evaluate_three_ledger_controls(
    supported_artifact: SidecarRunArtifact,
    unsupported_artifact: SidecarRunArtifact,
    *,
    supported_citation: Citation,
    unsupported_citation: Citation,
    adjacent_supported_citation: Citation | None = None,
    platform: str = "linux",
) -> dict[str, Any]:
    """Assert all 3 ledger controls with strict typed readback verification (Decision 017).

    - Control 1 (Supported): joins `cited_evidence` to the exact supporting handle, path,
      line, and grounded verdict; rejects exempt prose (`evidence_optional: true`) or
      empty `cited_evidence`.
    - Control 2 (Unsupported / Negative): requires a typed citation refusal code
      (`CITATION_REFUSAL_CODES`), verifies the rejected occurrence was named before
      recovery, verifies the invalid reference never became grounded in `cited_evidence`,
      and (when `adjacent_supported_citation` is supplied) verifies the adjacent supported
      citation in the same reply passed.
    - Control 3 (Minting parity): exact `compare_records` parity between independent
      `DeliveryLog` and durable `evidence_records`.
    """
    # Control 1: Supported citation grounded by BOTH reference resolver and product readback
    sup_scope = PermittedScope(
        session_id=supported_artifact.session_id,
        round_id="round-1",
        intent_window="intent-1",
        platform=platform,
    )
    ref_sup = resolve_citation(supported_artifact.delivery_log, supported_citation, sup_scope)
    sup_closeout = supported_artifact.product_closeouts[-1] if supported_artifact.product_closeouts else None
    sup_grounding = (sup_closeout or {}).get("grounding") or {}
    sup_cited = sup_grounding.get("cited_evidence") or []

    expected_sup_path = supported_citation.path
    if not expected_sup_path and ref_sup.supporting_handles:
        for rec in supported_artifact.delivery_log.records:
            if rec.handle in ref_sup.supporting_handles and rec.path:
                expected_sup_path = rec.path
                break

    matched_sup_entry = (
        _match_cited_evidence(
            sup_cited,
            allowed_handles=set(ref_sup.supporting_handles),
            expected_path=expected_sup_path,
            expected_line=supported_citation.start_line,
        )
        if isinstance(sup_cited, Sequence)
        else None
    )
    prod_sup_grounded = bool(
        sup_closeout is not None
        and sup_grounding.get("traced") is True
        and sup_grounding.get("evidence_optional") is not True
        and sup_grounding.get("host_assembled") is not True
        and matched_sup_entry is not None
    )
    c1_passed = ref_sup.verdict_class == "grounded" and prod_sup_grounded

    # Control 2: Unsupported citation refused with typed citation code, never grounded,
    # and adjacent supported citation passes
    unsup_scope = PermittedScope(
        session_id=unsupported_artifact.session_id,
        round_id="round-1",
        intent_window="intent-1",
        platform=platform,
    )
    ref_unsup = resolve_citation(unsupported_artifact.delivery_log, unsupported_citation, unsup_scope)
    unsup_closeout = unsupported_artifact.product_closeouts[-1] if unsupported_artifact.product_closeouts else None
    unsup_grounding = (unsup_closeout or {}).get("grounding") or {}
    unsup_cited = unsup_grounding.get("cited_evidence") or []
    hint_code = str(unsup_grounding.get("hint_code") or "")

    typed_citation_refusal = hint_code in CITATION_REFUSAL_CODES
    named_rejected = _refusal_names_rejected_occurrence(
        unsupported_artifact, unsup_grounding, unsupported_citation
    )
    invalid_became_grounded = bool(
        isinstance(unsup_cited, Sequence)
        and unsupported_citation.handle
        and any(
            isinstance(e, Mapping)
            and str(e.get("handle") or "") == unsupported_citation.handle
            and str(e.get("verdict") or "") in GROUNDED_VERDICTS
            for e in unsup_cited
        )
    )

    adjacent_ok = True
    adjacent_entry: Mapping[str, Any] | None = None
    if adjacent_supported_citation is not None:
        ref_adj = resolve_citation(
            unsupported_artifact.delivery_log, adjacent_supported_citation, unsup_scope
        )
        adj_path = adjacent_supported_citation.path
        if not adj_path and ref_adj.supporting_handles:
            for rec in unsupported_artifact.delivery_log.records:
                if rec.handle in ref_adj.supporting_handles and rec.path:
                    adj_path = rec.path
                    break
        adjacent_entry = (
            _match_cited_evidence(
                unsup_cited,
                allowed_handles=set(ref_adj.supporting_handles),
                expected_path=adj_path,
                expected_line=adjacent_supported_citation.start_line,
            )
            if isinstance(unsup_cited, Sequence)
            else None
        )
        adjacent_ok = ref_adj.verdict_class == "grounded" and adjacent_entry is not None

    prod_unsup_refused = bool(
        unsup_closeout is not None
        and typed_citation_refusal
        and named_rejected
        and not invalid_became_grounded
        and adjacent_ok
    )
    c2_passed = ref_unsup.verdict_class == "ungrounded" and prod_unsup_refused

    # Control 3: Minted evidence record equals independent delivery log
    mint_defects = compare_records(
        supported_artifact.delivery_log,
        supported_artifact.product_records,
    )
    c3_passed = bool(supported_artifact.delivery_log.records) and len(mint_defects) == 0

    all_passed = c1_passed and c2_passed and c3_passed
    return {
        "passed": all_passed,
        "control_1_supported": {
            "passed": c1_passed,
            "reference_verdict": ref_sup.verdict_class,
            "reference_handles": list(ref_sup.supporting_handles),
            "matched_cited_evidence": dict(matched_sup_entry) if matched_sup_entry else None,
            "product_grounded": prod_sup_grounded,
            "product_grounding": sup_grounding,
        },
        "control_2_unsupported": {
            "passed": c2_passed,
            "reference_verdict": ref_unsup.verdict_class,
            "reference_reason": ref_unsup.reason,
            "hint_code": hint_code,
            "typed_citation_refusal": typed_citation_refusal,
            "named_rejected_occurrence": named_rejected,
            "invalid_became_grounded": invalid_became_grounded,
            "adjacent_supported_passed": adjacent_ok,
            "adjacent_cited_evidence": dict(adjacent_entry) if adjacent_entry else None,
            "product_refused": prod_unsup_refused,
            "product_grounding": unsup_grounding,
        },
        "control_3_minting_parity": {
            "passed": c3_passed,
            "delivery_log_count": len(supported_artifact.delivery_log.records),
            "product_record_count": len(supported_artifact.product_records),
            "defects": [{"kind": d.kind, "handle": d.handle, "detail": d.detail} for d in mint_defects],
        },
    }
