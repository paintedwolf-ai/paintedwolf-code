"""Pinned-D1 sidecar launcher and manual-LLM tool-calling bridge for CF07."""

from __future__ import annotations

import contextlib
from dataclasses import dataclass
import json
import os
from pathlib import Path
import secrets
import socket
import sqlite3
import subprocess
import time
import urllib.error
import uuid

from ..driver_http import HTTPTransport
from ..driver_provenance import RunProvenance, collect_run_provenance
from ..driver_sidecar import SidecarDriver


@dataclass(frozen=True)
class CapturedToolCall:
    """Exact model-facing and host-persisted result of one agent tool call."""

    tool_name: str
    args: dict[str, object]
    call_id: str
    outcome: str
    rejection_code: str | None
    stdout: str
    stderr: str
    status_code: int | None
    stop_code: str | None
    generation_id: str | None
    fresh: bool | None
    declared_gaps: tuple[str, ...]
    tool_result_json: dict[str, object]
    model_facing_message: str

    def to_dict(self) -> dict[str, object]:
        return {
            "tool_name": self.tool_name,
            "args": self.args,
            "call_id": self.call_id,
            "outcome": self.outcome,
            "rejection_code": self.rejection_code,
            "stdout": self.stdout,
            "stderr": self.stderr,
            "status_code": self.status_code,
            "stop_code": self.stop_code,
            "generation_id": self.generation_id,
            "fresh": self.fresh,
            "declared_gaps": list(self.declared_gaps),
            "tool_result_json": self.tool_result_json,
            "model_facing_message": self.model_facing_message,
        }


def _extract_json_object(text: str) -> dict[str, object] | None:
    stripped = text.strip()
    if not stripped or not stripped.startswith("{"):
        return None
    try:
        parsed = json.loads(stripped)
        return parsed if isinstance(parsed, dict) else None
    except ValueError:
        return None


def parse_captured_tool_call(
    tool_name: str,
    args: dict[str, object],
    call_id: str,
    tool_result_json: dict[str, object],
    model_facing_message: str,
) -> CapturedToolCall:
    """Parse structured host tool_result_json and serialized model-facing message."""
    outcome = str(tool_result_json.get("outcome") or "unknown")
    rejection_code: str | None = None
    for key in ("rejection_code", "code", "error_code"):
        val = tool_result_json.get(key)
        if val:
            rejection_code = str(val)
            break
    if rejection_code is None:
        rej = tool_result_json.get("rejection")
        if isinstance(rej, dict) and rej.get("code"):
            rejection_code = str(rej["code"])
    if rejection_code is None:
        err = tool_result_json.get("error")
        if isinstance(err, dict) and err.get("code"):
            rejection_code = str(err["code"])

    stdout = str(tool_result_json.get("stdout") or "")
    stderr = str(tool_result_json.get("stderr") or "")
    if not stdout and isinstance(tool_result_json.get("output"), str):
        stdout = str(tool_result_json["output"])
    if not stdout and isinstance(tool_result_json.get("result"), str):
        stdout = str(tool_result_json["result"])

    raw_status = tool_result_json.get("status_code")
    if raw_status is None:
        raw_status = tool_result_json.get("exit_code")
    status_code = int(raw_status) if isinstance(raw_status, (int, float)) else None

    raw_stop = tool_result_json.get("stop_code")
    stop_code = str(raw_stop) if raw_stop else None

    # Inspect structured JSON inside stdout or model_facing_message for inventory/coverage facts.
    embedded = _extract_json_object(stdout) or _extract_json_object(model_facing_message) or {}
    if rejection_code is None and embedded.get("code"):
        rejection_code = str(embedded["code"])
    if rejection_code is None and "SURVEY_INVENTORY_WARMING" in model_facing_message:
        rejection_code = "SURVEY_INVENTORY_WARMING"
    if rejection_code is None and "GREP_MATCH_BUDGET" in model_facing_message:
        rejection_code = "GREP_MATCH_BUDGET"
    if rejection_code is None and "GREP_REGEX_UNSUPPORTED" in model_facing_message:
        rejection_code = "GREP_REGEX_UNSUPPORTED"

    gaps: list[str] = []
    fresh: bool | None = None
    generation_id: str | None = None

    inv = embedded.get("inventory")
    if isinstance(inv, dict):
        if "fresh" in inv:
            fresh = bool(inv["fresh"])
            if fresh is False:
                gaps.append("stale_inventory")
        if inv.get("revision") is not None:
            generation_id = str(inv["revision"])

    cov = embedded.get("coverage")
    if isinstance(cov, dict):
        cat_state = str(cov.get("catalog_state") or "").lower()
        if cat_state == "warming":
            gaps.append("catalog_warming")
        elif cat_state == "failed":
            gaps.append("catalog_failed")
        if cov.get("catalog_refreshing") is True:
            gaps.append("catalog_refreshing")
            if fresh is None:
                fresh = False
        if cov.get("catalog_revision") is not None:
            generation_id = str(cov["catalog_revision"])
        if cov.get("truncated") is True or cov.get("budget_exhausted") is True:
            gaps.append("catalog_bounded")

    inv_state = str(embedded.get("inventory_state") or "").lower()
    if inv_state == "warming":
        gaps.append("catalog_warming")
    elif inv_state in ("stale", "refreshing"):
        gaps.append("stale_inventory")
        if fresh is None:
            fresh = False
    if embedded.get("truncated") is True:
        gaps.append("catalog_bounded")

    if rejection_code in ("SURVEY_INVENTORY_WARMING", "GREP_MATCH_BUDGET", "GREP_REGEX_UNSUPPORTED"):
        gaps.append(rejection_code)

    return CapturedToolCall(
        tool_name=tool_name,
        args=dict(args),
        call_id=call_id,
        outcome=outcome,
        rejection_code=rejection_code,
        stdout=stdout,
        stderr=stderr,
        status_code=status_code,
        stop_code=stop_code,
        generation_id=generation_id,
        fresh=fresh,
        declared_gaps=tuple(dict.fromkeys(gaps)),
        tool_result_json=dict(tool_result_json),
        model_facing_message=model_facing_message,
    )


def _probe_free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


class D1SidecarProcess:
    """Isolated pinned-D1 `lycaon-dev` process with manual LLM harness control."""

    def __init__(
        self,
        binary_path: Path,
        repo_root: Path,
        work_dir: Path,
        *,
        d1_commit: str = "3bb1464d22dd0a38a149d37610810e7a0324e8cf",
        d1_tree: str = "",
        rg_bin: str = "rg",
    ) -> None:
        self.binary_path = Path(binary_path).resolve()
        self.repo_root = Path(repo_root).resolve()
        self.work_dir = Path(work_dir).resolve()
        self.d1_commit = d1_commit
        self.d1_tree = d1_tree
        self.rg_bin = rg_bin
        self.token = secrets.token_hex(24)
        self.port = _probe_free_port()
        self.base_url = f"http://127.0.0.1:{self.port}"
        self.db_path = self.work_dir / "store.db"
        self.config_dir = self.work_dir / "config"
        self.log_path = self.work_dir / "sidecar.log"
        self._proc: subprocess.Popen[str] | None = None
        self._log_file = None
        self.transport: HTTPTransport | None = None
        self.driver: SidecarDriver | None = None
        self.provenance: RunProvenance | None = None
        self.launch_receipt: dict[str, object] = {}

    def __enter__(self) -> "D1SidecarProcess":
        self.work_dir.mkdir(parents=True, exist_ok=True)
        self.config_dir.mkdir(parents=True, exist_ok=True)
        self.provenance = collect_run_provenance(
            harness_repo=self.repo_root,
            work_dir=self.work_dir,
            binary_path=self.binary_path,
            d1_commit=self.d1_commit,
            d1_tree=self.d1_tree,
            rg_bin=self.rg_bin,
        )
        env = dict(os.environ)
        env.update(
            {
                "LYCAON_CONFIG_DIR": str(self.config_dir),
                "LYCAON_CONFIG_ROOT": str(self.repo_root / "lycaon"),
                "LYCAON_ADDR": f"127.0.0.1:{self.port}",
                "LYCAON_API_TOKEN": self.token,
                "LYCAON_DEV": "1",
                "LYCAON_HARNESS": "1",
                "LYCAON_LOG_LEVEL": "info",
                "LYCAON_LLM_MOCK": "1",
                "LYCAON_LLM_MANUAL": "1",
                "LYCAON_DISABLE_KEYCHAIN": "1",
            }
        )
        self._log_file = self.log_path.open("w", encoding="utf-8")
        argv = [str(self.binary_path), "serve", "--db", str(self.db_path)]
        self._proc = subprocess.Popen(
            argv,
            cwd=str(self.repo_root),
            env=env,
            stdout=self._log_file,
            stderr=subprocess.STDOUT,
            text=True,
        )
        self.transport = HTTPTransport(self.base_url, self.token)
        self.driver = SidecarDriver(self.transport)
        self._wait_healthy(timeout_s=30.0)
        # NewManualProvider starts with autoEnabled=true; disable auto-reply so
        # coordinator turns pause at /harness/llm/pending for deterministic tool calls.
        self.transport.request("/harness/llm/auto", {"enabled": False}, method="POST")
        self.launch_receipt = {
            "pid": self._proc.pid,
            "port": self.port,
            "argv": argv,
            "binary_path": str(self.binary_path),
            "binary_sha256": self.provenance.product.binary_sha256,
            "go_buildid": self.provenance.product.go_buildid,
            "d1_commit": self.d1_commit,
            "harness_commit": self.provenance.harness_commit,
            "host": self.provenance.host,
            "filesystem": self.provenance.filesystem,
            "case_sensitive": self.provenance.case_sensitive,
            "rg_version": self.provenance.rg_version,
        }
        return self

    def _wait_healthy(self, timeout_s: float = 30.0) -> None:
        assert self.transport is not None
        deadline = time.monotonic() + timeout_s
        while time.monotonic() < deadline:
            if self._proc is not None and self._proc.poll() is not None:
                raise RuntimeError(f"lycaon-dev exited prematurely with {self._proc.returncode}")
            try:
                self.transport.request("/health", method="GET")
                return
            except (urllib.error.URLError, OSError):
                time.sleep(0.1)
        raise RuntimeError(f"lycaon-dev did not become healthy within {timeout_s}s")

    def __exit__(self, exc_type, exc, tb) -> None:
        if self._proc is not None and self._proc.poll() is None:
            self._proc.terminate()
            try:
                self._proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                self._proc.kill()
                self._proc.wait(timeout=5)
        if self._log_file is not None:
            self._log_file.close()


class AgentToolSession:
    """Drives real coordinator tool calls on D1 via /harness/llm/pending & /respond."""

    def __init__(self, sidecar: D1SidecarProcess, project_id: str) -> None:
        assert sidecar.transport is not None
        self._sidecar = sidecar
        self._transport = sidecar.transport
        self.project_id = project_id
        sess = self._transport.request(
            "/v1/sessions",
            {"project_id": project_id, "posture": "build"},
            method="POST",
        )
        self.session_id = str(sess["id"])
        try:
            self._transport.request(
                "/harness/model-limit",
                {"session_id": self.session_id, "limit": 500},
                method="POST",
            )
        except Exception:
            pass
        self._wait_session_idle(timeout_s=30.0)
        self._transport.request(
            f"/v1/sessions/{self.session_id}/prompts",
            {"operation_id": str(uuid.uuid4()), "text": "CF07 agent discovery probe"},
            method="POST",
        )
        self._pending = self._wait_pending(timeout_s=30.0)

    def _wait_session_idle(self, timeout_s: float = 30.0) -> None:
        deadline = time.monotonic() + timeout_s
        while time.monotonic() < deadline:
            state = self._transport.request(f"/v1/sessions/{self.session_id}", method="GET")
            status = str(state.get("status") or "")
            if status == "idle":
                return
            if status != "preparing":
                raise RuntimeError(f"session {self.session_id} preparation ended in {status!r}")
            time.sleep(0.05)
        raise RuntimeError(f"session {self.session_id} stayed in preparing past {timeout_s}s")

    def _wait_pending(self, timeout_s: float = 30.0) -> dict[str, object]:
        deadline = time.monotonic() + timeout_s
        while time.monotonic() < deadline:
            remaining_ms = max(100, min(1000, int((deadline - time.monotonic()) * 1000)))
            val = self._transport.request(
                f"/harness/llm/pending?session_id={self.session_id}&wait={remaining_ms}",
                method="GET",
            )
            if val.get("pending") and val.get("session_id") == self.session_id:
                return val
        raise RuntimeError(f"session {self.session_id} did not reach /harness/llm/pending within {timeout_s}s")

    def _read_tool_result_row(self, call_id: str, timeout_s: float = 30.0) -> dict[str, object]:
        deadline = time.monotonic() + timeout_s
        while time.monotonic() < deadline:
            with contextlib.closing(sqlite3.connect(self._sidecar.db_path)) as db:
                row = db.execute(
                    "SELECT tool_result_json FROM messages WHERE session_id=? AND tool_result_call_id=?",
                    (self.session_id, call_id),
                ).fetchone()
            if row and row[0]:
                parsed = json.loads(row[0])
                if isinstance(parsed, dict):
                    return parsed
            time.sleep(0.05)
        raise RuntimeError(f"tool_result_json not recorded for call_id={call_id}")

    def _extract_model_message(self, pending_req: dict[str, object], call_id: str) -> str:
        messages = pending_req.get("messages") or []
        if isinstance(messages, list):
            for msg in reversed(messages):
                if not isinstance(msg, dict):
                    continue
                if msg.get("tool_call_id") == call_id or msg.get("role") == "tool":
                    content = msg.get("content")
                    if isinstance(content, str):
                        return content
                    return json.dumps(content, ensure_ascii=False)
        return ""

    def invoke_tool(self, tool_name: str, args: dict[str, object], *, timeout_s: float = 45.0) -> CapturedToolCall:
        req = self._pending
        tools_avail = req.get("tools") or []
        if isinstance(tools_avail, list) and tool_name not in tools_avail and "request_tools" in tools_avail:
            load_id = str(uuid.uuid4())
            self._transport.request(
                "/harness/llm/respond",
                {
                    "id": req["id"],
                    "tool_calls": [{"id": load_id, "name": "request_tools", "args": {"need": tool_name}}],
                },
                method="POST",
            )
            self._read_tool_result_row(load_id, timeout_s=timeout_s)
            req = self._wait_pending(timeout_s=timeout_s)
            self._pending = req

        call_id = str(uuid.uuid4())
        self._transport.request(
            "/harness/llm/respond",
            {
                "id": req["id"],
                "tool_calls": [{"id": call_id, "name": tool_name, "args": args}],
            },
            method="POST",
        )
        raw_result = self._read_tool_result_row(call_id, timeout_s=timeout_s)
        next_pending = self._wait_pending(timeout_s=timeout_s)
        self._pending = next_pending
        model_msg = self._extract_model_message(next_pending, call_id)
        return parse_captured_tool_call(tool_name, args, call_id, raw_result, model_msg)

    def close(self) -> None:
        try:
            self._transport.request(f"/v1/sessions/{self.session_id}/abort", {}, method="POST")
        except Exception:
            pass
