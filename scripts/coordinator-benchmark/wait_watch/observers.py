"""Five independent observers for wait/watch bug-bash evidence correlation."""
from __future__ import annotations

import base64
import contextlib
import hashlib
import json
import pathlib
import re
import sqlite3
import time
from datetime import datetime, timezone
from typing import Any

CORRELATION_FIELDS = (
    "session_id",
    "job_id",
    "handle",
    "invocation_id",
    "workspace_id",
    "occurrence_id",
    "claim_id",
    "turn",
    "revision",
    "event_seq",
    "clock_domain",
)

# Canonical SQLite tables audited by the storage observer (matching lycaon/internal/db/schema.sql).
STORAGE_TABLES = (
    "wait_leases",
    "watch_registrations",
    "watch_matches",
    "watch_deliveries",
    "watch_wait_selections",
    "watch_session_state",
    "watch_presentation_claims",
    "watch_runs",
    "prompt_submissions",
    "messages",
    "invocation_receipts",
    "invocation_programs",
    "invocation_program_outputs",
)


def canonical_sha256(obj: Any) -> str:
    raw = json.dumps(obj, sort_keys=True, separators=(",", ":"), default=str).encode("utf-8")
    return hashlib.sha256(raw).hexdigest()


def _table_exists(db: sqlite3.Connection, name: str) -> bool:
    return db.execute(
        "SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", (name,)
    ).fetchone() is not None


def _table_columns(db: sqlite3.Connection, name: str) -> set[str]:
    return {row[1] for row in db.execute(f"PRAGMA table_info({name})").fetchall()}


def _json_safe(value: Any) -> Any:
    if isinstance(value, memoryview):
        value = value.tobytes()
    if isinstance(value, (bytes, bytearray)):
        raw = bytes(value)
        try:
            return raw.decode("utf-8")
        except UnicodeDecodeError:
            return {
                "encoding": "base64",
                "bytes": len(raw),
                "data": base64.b64encode(raw).decode("ascii"),
            }
    if isinstance(value, dict):
        return {str(k): _json_safe(v) for k, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [_json_safe(v) for v in value]
    return value


def _query_table(db: sqlite3.Connection, table: str, session_id: str | None) -> list[dict[str, Any]]:
    cols = _table_columns(db, table)
    order = "ORDER BY rowid"
    # Tables created WITHOUT ROWID cannot be ordered by rowid.
    try:
        if session_id and "session_id" in cols:
            rows = db.execute(f"SELECT * FROM {table} WHERE session_id=? {order}", (session_id,)).fetchall()
        else:
            rows = db.execute(f"SELECT * FROM {table} {order}").fetchall()
    except sqlite3.OperationalError:
        if session_id and "session_id" in cols:
            rows = db.execute(f"SELECT * FROM {table} WHERE session_id=?", (session_id,)).fetchall()
        else:
            rows = db.execute(f"SELECT * FROM {table}").fetchall()
    return [_json_safe(dict(r)) for r in rows]


class SourceObserver:
    """Observer 1: fixture sentinel timestamps, exact child exit status, stdout/report digest."""

    def __init__(self, *, clock_domain: str = "system") -> None:
        self.clock_domain = clock_domain
        self.events: list[dict[str, Any]] = []
        self._seq = 0
        self.facts: dict[str, Any] = {}

    def record_event(self, kind: str, **fields: Any) -> dict[str, Any]:
        self._seq += 1
        rec = {
            "event_seq": self._seq,
            "clock_domain": self.clock_domain,
            "kind": kind,
            "ts_ns": time.time_ns(),
            **fields,
        }
        self.events.append(rec)
        return rec

    def set_facts(self, facts: dict[str, Any]) -> None:
        self.facts = dict(facts)

    def snapshot(self) -> dict[str, Any]:
        return {
            "observer": "source",
            "clock_domain": self.clock_domain,
            "facts": self.facts,
            "events": list(self.events),
            "sha256": canonical_sha256({"facts": self.facts, "events": self.events}),
        }


class StorageObserver:
    """Observer 2: direct read-only SQLite audit of wait/watch, prompt, message, and invocation tables."""

    def __init__(self, db_path: pathlib.Path | str, *, clock_domain: str = "system") -> None:
        self.db_path = pathlib.Path(db_path).resolve()
        self.clock_domain = clock_domain

    def snapshot(self, session_id: str | None = None) -> dict[str, Any]:
        if not self.db_path.is_file():
            return {
                "observer": "storage",
                "clock_domain": self.clock_domain,
                "available": False,
                "tables_present": [],
                "tables_missing": list(STORAGE_TABLES),
                "tables": {},
                "counts": {},
            }
        uri = self.db_path.as_uri() + "?mode=ro"
        tables_present: list[str] = []
        tables_missing: list[str] = []
        tables: dict[str, list[dict[str, Any]]] = {}
        with contextlib.closing(sqlite3.connect(uri, uri=True)) as db:
            db.row_factory = sqlite3.Row
            for name in STORAGE_TABLES:
                if _table_exists(db, name):
                    tables_present.append(name)
                    tables[name] = _query_table(db, name, session_id)
                else:
                    tables_missing.append(name)
            # Also support legacy/alias table names if present in an older or test DB.
            for alias in ("await_leases", "watch_occurrences"):
                if alias not in tables and _table_exists(db, alias):
                    tables_present.append(alias)
                    tables[alias] = _query_table(db, alias, session_id)
            # Narrow invocation_programs and invocation_program_outputs to session receipts if session_id given.
            if session_id and "invocation_receipts" in tables:
                receipt_ids = {r.get("id") for r in tables["invocation_receipts"] if r.get("id")}
                if "invocation_programs" in tables:
                    tables["invocation_programs"] = [
                        p for p in tables["invocation_programs"]
                        if p.get("parent_receipt_id") in receipt_ids or p.get("session_id") == session_id
                    ]
                prog_ids = {p.get("id") for p in tables.get("invocation_programs", []) if p.get("id")}
                if "invocation_program_outputs" in tables:
                    tables["invocation_program_outputs"] = [
                        o for o in tables["invocation_program_outputs"]
                        if o.get("program_id") in prog_ids
                    ]
            # Narrow watch_matches and watch_wait_selections by session watch_ids / lease_ids.
            if session_id:
                watch_ids = {w.get("watch_id") for w in tables.get("watch_registrations", []) if w.get("watch_id")}
                if "watch_matches" in tables:
                    tables["watch_matches"] = [
                        m for m in tables["watch_matches"] if m.get("watch_id") in watch_ids
                    ]
                lease_ids = {l.get("id") for l in tables.get("wait_leases", []) if l.get("id")}
                if "watch_wait_selections" in tables:
                    tables["watch_wait_selections"] = [
                        s for s in tables["watch_wait_selections"] if s.get("lease_id") in lease_ids
                    ]

        counts = {k: len(v) for k, v in tables.items()}
        armed_leases = [l for l in tables.get("wait_leases", []) if l.get("status") == "armed"]
        return {
            "observer": "storage",
            "clock_domain": self.clock_domain,
            "available": True,
            "session_id": session_id or "",
            "tables_present": tables_present,
            "tables_missing": tables_missing,
            "counts": counts,
            "armed_lease_count": len(armed_leases),
            "tables": tables,
            "sha256": canonical_sha256(tables),
        }


_SLOG_KV_RE = re.compile(r'([a-zA-Z0-9_.-]+)=("(?:\\.|[^"\\])*"|\S+)')


def parse_rfc3339_ns(val: Any) -> int | None:
    if isinstance(val, int):
        return val
    if not isinstance(val, str) or not val.strip():
        return None
    text = val.strip()
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    try:
        dt = datetime.fromisoformat(text)
        if dt.tzinfo is None:
            dt = dt.replace(tzinfo=timezone.utc)
        return int(dt.timestamp() * 1_000_000_000)
    except ValueError:
        return None


def parse_sidecar_phase_records(
    sidecar_log: pathlib.Path | str,
    *,
    session_id: str | None = None,
    handle: str | None = None,
) -> list[dict[str, Any]]:
    """Parse authoritative `await.EmitPhaseRecord` ('wait delivery phase') records from sidecar.log."""
    path = pathlib.Path(sidecar_log)
    if not path.is_file():
        return []
    records: list[dict[str, Any]] = []
    for line_no, raw_line in enumerate(path.read_text(encoding="utf-8", errors="replace").splitlines(), start=1):
        line = raw_line.strip()
        if not line or "wait delivery phase" not in line:
            continue
        fields: dict[str, Any] = {}
        if line.startswith("{"):
            try:
                parsed = json.loads(line)
                if isinstance(parsed, dict) and parsed.get("msg") == "wait delivery phase":
                    fields = parsed
            except ValueError:
                continue
        else:
            for k, v in _SLOG_KV_RE.findall(line):
                if len(v) >= 2 and v[0] == '"' and v[-1] == '"':
                    try:
                        fields[k] = json.loads(v)
                    except ValueError:
                        fields[k] = v[1:-1]
                else:
                    fields[k] = v
            if fields.get("msg") != "wait delivery phase":
                continue
        if not fields.get("phase"):
            continue
        rec_sid = str(fields.get("session_id") or "")
        rec_handle = str(fields.get("handle") or "")
        if session_id and rec_sid != session_id:
            continue
        if handle and rec_handle != handle:
            continue
        rec_at = str(fields.get("recorded_at") or fields.get("time") or "")
        rec_at_ns = parse_rfc3339_ns(rec_at)
        records.append({
            "line_no": line_no,
            "phase": str(fields.get("phase") or ""),
            "session_id": rec_sid,
            "handle": rec_handle,
            "tool_call_id": str(fields.get("tool_call_id") or ""),
            "lease_id": str(fields.get("lease_id") or ""),
            "tool": str(fields.get("tool") or ""),
            "outcome": str(fields.get("outcome") or ""),
            "status": str(fields.get("status") or ""),
            "recorded_at": rec_at,
            "recorded_at_ns": rec_at_ns,
            "evidence_ref": f"sidecar.log:L{line_no}:phase={fields.get('phase')}",
        })
    return records


class AdmissionObserver:
    """Observer 3: execution admission, watch REST endpoints, /v1/events SSE frames, and scoped phase records."""

    def __init__(self, *, clock_domain: str = "system") -> None:
        self.clock_domain = clock_domain
        self.executions: list[dict[str, Any]] = []
        self.project_watches: list[dict[str, Any]] = []
        self.session_watches: list[dict[str, Any]] = []
        self.sse_frames: list[dict[str, Any]] = []
        self.phase_records: list[dict[str, Any]] = []

    def load_phase_records(
        self,
        sidecar_log: pathlib.Path | str,
        *,
        session_id: str | None = None,
        handle: str | None = None,
    ) -> list[dict[str, Any]]:
        self.phase_records = parse_sidecar_phase_records(sidecar_log, session_id=session_id, handle=handle)
        return self.phase_records

    def capture_http(
        self,
        application: Any,
        *,
        session_id: str,
        project_id: str,
        submission_ids: list[str] | tuple[str, ...] = (),
    ) -> None:
        for sub_id in submission_ids:
            obs = application.request(f"/harness/execution/{session_id}/{sub_id}", missing=True)
            if obs is not None:
                self.executions.append({"submission_id": sub_id, "observation": obs})
        pw = application.request(f"/v1/projects/{project_id}/watches", missing=True)
        if pw is not None:
            self.project_watches.append({"project_id": project_id, "response": pw, "ts_ns": time.time_ns()})
        sw = application.request(f"/v1/sessions/{session_id}/watches", missing=True)
        if sw is not None:
            self.session_watches.append({"session_id": session_id, "response": sw, "ts_ns": time.time_ns()})

    def load_sse_jsonl(self, path: pathlib.Path | str) -> list[dict[str, Any]]:
        p = pathlib.Path(path)
        frames: list[dict[str, Any]] = []
        if not p.is_file():
            self.sse_frames = frames
            return frames
        for idx, line in enumerate(p.read_text(encoding="utf-8", errors="replace").splitlines(), start=1):
            text = line.strip()
            if not text:
                continue
            try:
                payload = json.loads(text)
            except ValueError:
                payload = {"raw": text}
            frames.append({
                "event_seq": idx,
                "clock_domain": self.clock_domain,
                "event_type": payload.get("type") or payload.get("event") or "",
                "session_id": payload.get("session_id") or (payload.get("data") or {}).get("session_id") or "",
                "payload": payload,
            })
        self.sse_frames = frames
        return frames

    def snapshot(self) -> dict[str, Any]:
        payload = {
            "executions": self.executions,
            "project_watches": self.project_watches,
            "session_watches": self.session_watches,
            "sse_frame_count": len(self.sse_frames),
            "sse_frames": self.sse_frames,
            "phase_record_count": len(self.phase_records),
            "phase_records": self.phase_records,
        }
        return {
            "observer": "admission",
            "clock_domain": self.clock_domain,
            **payload,
            "sha256": canonical_sha256(payload),
        }


class RenderedInputObserver:
    """Observer 4: exact serialized /harness/llm/pending payloads captured BEFORE posting /harness/llm/respond."""

    def __init__(self, *, clock_domain: str = "system") -> None:
        self.clock_domain = clock_domain
        self.presentations: list[dict[str, Any]] = []
        self._turn_counter = 0

    def record_pending(
        self,
        pending_payload: dict[str, Any],
        *,
        presentation_kind: str = "new_model_presentation",
        session_id: str | None = None,
        job_id: str = "",
        handle: str = "",
        invocation_id: str = "",
        occurrence_id: str = "",
        claim_id: str = "",
        attempt: int | None = None,
        revision: int | None = None,
        event_seq: int | None = None,
        exit_code: int | None = None,
        report: str = "",
        report_readable: bool | None = None,
        evidence_refs: dict[str, str] | None = None,
    ) -> dict[str, Any]:
        if presentation_kind not in {"new_model_presentation", "history_reread", "sse_reconnect_repaint"}:
            raise ValueError(f"unsupported presentation_kind: {presentation_kind}")
        if presentation_kind == "new_model_presentation":
            self._turn_counter += 1
        serialized = json.dumps(pending_payload, sort_keys=True, ensure_ascii=False)
        digest = hashlib.sha256(serialized.encode("utf-8")).hexdigest()
        observed_session_id = pending_payload.get("session_id") or ""
        if session_id is not None:
            observed_session_id = session_id
        rec = {
            "event_seq": event_seq,
            "turn": self._turn_counter,
            "clock_domain": self.clock_domain,
            "presentation_kind": presentation_kind,
            "request_id": pending_payload.get("id") or "",
            "session_id": observed_session_id,
            "job_id": job_id,
            "handle": handle,
            "invocation_id": invocation_id,
            "occurrence_id": occurrence_id,
            "claim_id": claim_id,
            "attempt": attempt,
            "revision": revision,
            "exit_code": exit_code,
            "report": report,
            "report_readable": report_readable if report_readable is not None else bool(report),
            "evidence_refs": dict(evidence_refs or {}),
            "tools": list(pending_payload.get("tools") or ()),
            "message_count": len(pending_payload.get("messages") or ()),
            "payload_sha256": digest,
            "payload": pending_payload,
            "captured_at_ns": time.time_ns(),
        }
        self.presentations.append(rec)
        return rec

    def snapshot(self) -> dict[str, Any]:
        new_count = sum(1 for p in self.presentations if p["presentation_kind"] == "new_model_presentation")
        return {
            "observer": "rendered_input",
            "clock_domain": self.clock_domain,
            "new_model_presentation_count": new_count,
            "total_observation_count": len(self.presentations),
            "presentations": list(self.presentations),
            "sha256": canonical_sha256(self.presentations),
        }


class DenObserver:
    """Observer 5: native Den accessibility / keyboard-navigation capture schema."""

    def __init__(
        self,
        *,
        status: str = "not_run",
        surface: str = "",
        os_name: str = "",
        window_id: str = "",
        executable_sha256: str = "",
        before_tree: list[Any] | None = None,
        actions: list[dict[str, Any]] | None = None,
        after_tree: list[Any] | None = None,
        screenshot_path: str = "",
        screenshot_sha256: str = "",
        blocked_reason: str = "",
        os_error: str = "",
    ) -> None:
        self.data = {
            "observer": "den",
            "status": status,
            "surface": surface,
            "os": os_name,
            "window_id": window_id,
            "executable_sha256": executable_sha256,
            "before_tree": list(before_tree or ()),
            "actions": list(actions or ()),
            "after_tree": list(after_tree or ()),
            "screenshot_path": screenshot_path,
            "screenshot_sha256": screenshot_sha256,
            "blocked_reason": blocked_reason,
            "os_error": os_error,
        }

    def snapshot(self) -> dict[str, Any]:
        return {**self.data, "sha256": canonical_sha256(self.data)}


def correlate_observers(
    *,
    source: dict[str, Any],
    storage: dict[str, Any],
    admission: dict[str, Any],
    rendered_input: dict[str, Any],
    den: dict[str, Any],
    correlation: dict[str, Any] | None = None,
) -> dict[str, Any]:
    """Assemble the 5 independent observers with normalized correlation keys."""
    corr = {k: (correlation or {}).get(k) for k in CORRELATION_FIELDS}
    return {
        "source": source,
        "storage": storage,
        "admission": admission,
        "rendered_input": rendered_input,
        "den": den,
        "correlation": corr,
    }
