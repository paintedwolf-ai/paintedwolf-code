"""Sidecar driver adapter for the D1 manual-model HTTP boundary (GB02).

Reuses the manual-model HTTP protocol (`/harness/llm/pending?session_id=...` and
`/harness/llm/respond`) from `portable_command/harness.py`, launches an explicit
`--engine-bin` (never assuming D2) in an isolated scratch config dir + free port
+ fixture workspace, and captures:
- exact engine binary provenance (`path`, `sha256`, `go_buildid`, `platform`)
- raw pending request JSON and scripted response JSON per turn
- independent delivered tool-message sequence joined by `call_id` & `fixture_provenance`
- strictly separate durable product records & closeouts from `store.db`
- D10 friction before/after snapshots via `friction_probe.py`
"""
from __future__ import annotations

import contextlib
from dataclasses import dataclass, field
import hashlib
import json
import os
import pathlib
import platform as py_platform
import socket
import sqlite3
import subprocess
import time
from typing import Any, Mapping, Sequence
import urllib.error
import urllib.parse
import urllib.request
import uuid

from .delivery_log import DeliveryLog, DeliveryRecord, LineRange, ProductRecord
from .friction_probe import FrictionSnapshot, probe_store_db


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


def inspect_engine_binary(
    engine_bin: str | pathlib.Path,
    *,
    expected_sha256: str = "",
) -> EngineBinaryProvenance:
    """Compute SHA-256, Go build ID (if available), and host platform for `--engine-bin`.

    Decision 009: When `expected_sha256` is provided, verify the exact D1 binary
    digest immediately and raise ValueError on mismatch.
    """
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

    def to_dict(self) -> dict[str, Any]:
        return {
            "version": 1,
            "session_id": self.session_id,
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

    def request(self, path: str, body: Mapping[str, Any] | None = None, *, method: str | None = None) -> Any:
        url = f"{self.base_url}{path}"
        data = json.dumps(body).encode("utf-8") if body is not None else None
        req_method = method or ("POST" if data is not None else "GET")
        req = urllib.request.Request(url, data=data, headers=self._headers(), method=req_method)
        with urllib.request.urlopen(req, timeout=self.timeout) as resp:
            raw = resp.read().decode("utf-8")
            return json.loads(raw) if raw.strip() else {}

    def get_pending(self, session_id: str) -> dict[str, Any] | None:
        q = urllib.parse.urlencode({"session_id": session_id})
        payload = self.request(f"/harness/llm/pending?{q}")
        if isinstance(payload, dict) and payload.get("id"):
            return payload
        if isinstance(payload, dict) and isinstance(payload.get("pending"), list) and payload["pending"]:
            return payload["pending"][0]
        return None

    def wait_pending(self, session_id: str, *, timeout: float = 15.0, poll_interval: float = 0.05) -> dict[str, Any]:
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
        return self.request("/v1/sessions", {"project_id": project_id, "posture": posture})

    def send_prompt(self, session_id: str, text: str) -> dict[str, Any]:
        return self.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": str(uuid.uuid4()), "text": text},
        )

    def get_messages(self, session_id: str) -> Any:
        return self.request(f"/v1/sessions/{session_id}/messages")


class IsolatedSidecarProcess:
    """Launch an explicit D1 `--engine-bin` in an isolated scratch dir + free port."""

    def __init__(
        self,
        engine_bin: str | pathlib.Path,
        *,
        config_dir: str | pathlib.Path,
        workspace_dir: str | pathlib.Path,
        expected_sha256: str = "",
        token: str | None = None,
        extra_env: Mapping[str, str] | None = None,
    ) -> None:
        self.engine_bin = pathlib.Path(engine_bin).resolve()
        self.config_dir = pathlib.Path(config_dir).resolve()
        self.workspace_dir = pathlib.Path(workspace_dir).resolve()
        self.token = token or f"gb02-{uuid.uuid4().hex}"
        self.extra_env = dict(extra_env or {})
        self.port: int = 0
        self.pid: int | None = None
        self.process: subprocess.Popen[bytes] | None = None
        self.provenance: EngineBinaryProvenance = inspect_engine_binary(
            self.engine_bin, expected_sha256=expected_sha256
        )

    @property
    def base_url(self) -> str:
        return f"http://127.0.0.1:{self.port}"

    @property
    def store_db_path(self) -> pathlib.Path:
        return self.config_dir / "store.db"

    def start(self, *, ready_timeout: float = 20.0) -> SidecarSessionClient:
        self.config_dir.mkdir(parents=True, exist_ok=True)
        self.workspace_dir.mkdir(parents=True, exist_ok=True)
        self.port = probe_free_port()
        log_path = self.config_dir / "sidecar.log"
        env = {
            **os.environ,
            "LYCAON_CONFIG_DIR": str(self.config_dir),
            "LYCAON_AUTH_TOKEN": self.token,
            "LYCAON_LLM_PROVIDER": "manual",
            "LYCAON_LISTEN_ADDR": f"127.0.0.1:{self.port}",
            **self.extra_env,
        }
        with log_path.open("wb") as log_fh:
            self.process = subprocess.Popen(
                [str(self.engine_bin), "serve", "--addr", f"127.0.0.1:{self.port}"],
                cwd=str(self.workspace_dir),
                env=env,
                stdout=log_fh,
                stderr=subprocess.STDOUT,
            )
        self.pid = self.process.pid
        client = SidecarSessionClient(self.base_url, self.token)
        deadline = time.monotonic() + ready_timeout
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                raise RuntimeError(
                    f"Sidecar exited prematurely with code {self.process.returncode}; see {log_path}"
                )
            try:
                client.request("/healthz")
                return client
            except (urllib.error.URLError, OSError):
                time.sleep(0.05)
        self.stop()
        raise TimeoutError(f"Sidecar failed to become ready on {self.base_url} within {ready_timeout}s")

    def stop(self) -> None:
        if self.process is None:
            return
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=5)
        self.process = None

    def __enter__(self) -> SidecarSessionClient:
        return self.start()

    def __exit__(self, *_exc: Any) -> None:
        self.stop()


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
                lr_raw = row["line_ranges"] if "line_ranges" in cols else ""
                if lr_raw:
                    try:
                        lr_obj = json.loads(lr_raw)
                        for rng in lr_obj.get("ranges", ()):
                            if isinstance(rng, Mapping) and "start" in rng and "end" in rng:
                                ranges.append(LineRange(int(rng["start"]), int(rng["end"])))
                    except (TypeError, ValueError):
                        pass
                marks_untrusted = int(row["marks_untrusted"]) if "marks_untrusted" in cols else 0
                records.append(
                    ProductRecord(
                        handle=str(row["handle"]),
                        kind=str(row["kind"]),
                        session_id=str(row["session_id"]),
                        worker_leg=str(row["worker_leg"]) if "worker_leg" in cols else "",
                        source_revision=str(row["source_revision"]) if "source_revision" in cols else "",
                        delivered_ranges=tuple(ranges),
                        body_sha256=str(row["content_blob_sha256"]) if "content_blob_sha256" in cols else "",
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


def _extract_tool_messages_from_pending(
    pending: Mapping[str, Any],
    seen_call_ids: set[str],
    call_meta: Mapping[str, Mapping[str, Any]],
) -> list[dict[str, Any]]:
    """Extract newly delivered tool messages from a raw pending LLM request."""
    new_msgs: list[dict[str, Any]] = []
    for msg in pending.get("messages") or ():
        if not isinstance(msg, Mapping):
            continue
        if msg.get("role") != "tool":
            continue
        call_id = str(msg.get("tool_call_id") or msg.get("call_id") or "")
        if not call_id or call_id in seen_call_ids:
            continue
        seen_call_ids.add(call_id)
        meta = call_meta.get(call_id, {})
        new_msgs.append(
            {
                "call_id": call_id,
                "tool": str(meta.get("name") or msg.get("name") or ""),
                "arguments": dict(meta.get("args") or {}),
                "content": str(msg.get("content") or ""),
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
    fixture_provenance: str = "host",
    source_revision: str = "rev-1",
    round_id: str = "round-1",
    intent_window: str = "intent-1",
    engine_provenance: EngineBinaryProvenance | None = None,
    sse_events: Sequence[Mapping[str, Any]] = (),
    poll_timeout: float = 15.0,
) -> SidecarRunArtifact:
    """Drive scripted multi-turn completions against `/harness/llm/pending` & `/respond`.

    Captures raw pending requests, scripted responses, and delivered tool messages
    joined by `call_id` & `fixture_provenance`, then reads durable product records
    and D10 friction snapshots separately from `store.db`.
    """
    friction_before = probe_store_db(db_path, session_id) if db_path else None
    raw_pending: list[dict[str, Any]] = []
    scripted_resps: list[dict[str, Any]] = []
    delivered_tools: list[dict[str, Any]] = []
    delivery_records: list[DeliveryRecord] = []

    seen_call_ids: set[str] = set()
    call_meta: dict[str, dict[str, Any]] = {}
    ordinal_by_kind: dict[str, int] = {}

    for turn in turns:
        pending = client.wait_pending(session_id, timeout=poll_timeout)
        raw_pending.append(dict(pending))

        # Capture any tool messages delivered to the model in this pending request
        newly_delivered = _extract_tool_messages_from_pending(pending, seen_call_ids, call_meta)
        for item in newly_delivered:
            delivered_tools.append(item)
            tool_name = item["tool"] or "tool"
            kind = "read" if "read" in tool_name else ("web" if "web" in tool_name else tool_name)
            ordinal_by_kind[kind] = ordinal_by_kind.get(kind, 0) + 1
            handle = f"{kind}#{ordinal_by_kind[kind]}"
            raw_content = item["content"].encode("utf-8")
            args = item["arguments"]
            path = str(args.get("path") or "")
            url = str(args.get("url") or "")
            ranges: tuple[LineRange, ...] = ()
            if "start_line" in args and "end_line" in args:
                ranges = (LineRange(int(args["start_line"]), int(args["end_line"])),)
            delivery_records.append(
                DeliveryRecord(
                    tool=tool_name,
                    arguments=args,
                    raw_bytes=raw_content,
                    delivered_ranges=ranges,
                    delivery_tags=(),
                    line_numbered=False,
                    printed_other_numbers=False,
                    session_id=session_id,
                    worker_leg="",
                    round_id=round_id,
                    intent_window=intent_window,
                    source_revision=source_revision,
                    handle=handle,
                    kind=kind,
                    path=path,
                    url=url,
                    artifact_id="",
                    call_id=item["call_id"],
                    fixture_provenance=fixture_provenance,
                )
            )

        # Assign deterministic/explicit call_ids for scripted tool calls
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
    )
