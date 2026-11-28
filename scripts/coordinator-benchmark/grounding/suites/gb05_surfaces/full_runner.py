"""Full GB05 (Speaking surfaces: S01..S07) computational execution runner for D1.

Covers:
- All 49 sidecar slots x 3 D5 wordings = 147 sidecar runs against exact D1
  `lycaon-dev` via `IsolatedSidecarProcess` with Decision 012 `trace_friction=True`
  (recording `d10_before`, `d10_after`, and `d10_delta` for every slot).
- Both in-process crash slots (`S06-14-inprocess-crash-after-carrier-before-note`
  and `S06-15-inprocess-crash-after-note-before-ack`) across all 3 D5 wordings
  via `grounding.inprocess_driver` test-only Go `-overlay`
  (`grounding/overlays/gb05_inprocess_test.go`).
- Clean sharding (`--families`, `--shard`, `--shards`, `--wordings`) so parallel
  Linux/macOS lanes execute disjoint subsets with zero duplicates.
- Honest divergence reporting in `gb05-sidecar-divergences.jsonl` and
  `gb05-sidecar-summary.json` (never hides D1 product defects).
"""
from __future__ import annotations

import argparse
import contextlib
import hashlib
import json
import pathlib
import re
import sqlite3
import subprocess
import sys
import tempfile
import time
import uuid
from dataclasses import dataclass, field
from typing import Any, Callable, Mapping, Sequence

from grounding.checker import VALID_MID_TURN_NOTE_CODES
from grounding.delivery_log import DeliveryLog, DeliveryRecord, LineRange
from grounding.friction_probe import FrictionSnapshot, probe_store_db
from grounding.inprocess_driver import (
    InProcessPlan,
    run_inprocess_plan,
    write_test_overlay,
)
from grounding.selectors import WORDING_VARIANTS
from grounding.sidecar_driver import (
    CITATION_REFUSAL_CODES,
    IsolatedSidecarProcess,
    ScriptedTurn,
    SidecarRunManifest,
    SidecarSessionClient,
    _DELIVERY_TAG_RE,
    _derive_actual_range_and_numbering,
    _extract_delivered_read_bytes,
    _format_independent_command_body,
    _safe_fixture_file,
    _strip_leading_delivery_tags,
    extract_product_observations,
    extract_tool_messages_from_pending,
)
from grounding.suites.gb05_surfaces.catalog import (
    ALL_GB05_SLOTS,
    GB05_FAMILY_IDS,
    GB05SlotSpec,
    SLOTS_BY_SELECTOR,
)
from grounding.suites.gb05_surfaces.fixtures import render_wording


INPROCESS_SLOTS: tuple[str, ...] = (
    "S06-14-inprocess-crash-after-carrier-before-note",
    "S06-15-inprocess-crash-after-note-before-ack",
)

OVERLAY_SOURCE_REL: pathlib.Path = pathlib.Path("overlays/gb05_inprocess_test.go")
OVERLAY_TARGET_REL: pathlib.Path = pathlib.Path(
    "lycaon/internal/coordinator/promptloop/gb05_inprocess_test.go"
)

_CODE_RE = re.compile(
    r"\b("
    r"CITATION_REPAIR_INVALID|CITATION_SYNTAX_INVALID|"
    r"COORDINATOR_CLOSEOUT_NO_ANSWER|"
    r"INVEST_[A-Z0-9_]+|SYNTH_[A-Z0-9_]+|"
    r"MID_TURN_NOTE_[A-Z0-9_]+|"
    r"REPORT_[A-Z0-9_]+|"
    r"CLOSEOUT_[A-Z0-9_]+|"
    r"COMPLETION_REPORT_[A-Z0-9_]+"
    r")\b"
)
_REPAIR_ID_RE = re.compile(r"(?:^|\n)-\s*(c[1-9]\d*):")


@dataclass
class DynamicSessionCapture:
    """Complete capture of one dynamic sidecar slot execution."""

    session_id: str
    raw_pending_requests: list[dict[str, Any]]
    scripted_responses: list[dict[str, Any]]
    delivered_tool_messages: list[dict[str, Any]]
    delivery_log: DeliveryLog
    product_records: tuple[Any, ...]
    product_closeouts: tuple[dict[str, Any], ...]
    agent_notes: list[dict[str, Any]]
    user_visible_messages: list[dict[str, Any]]
    completion_reports: list[dict[str, Any]]
    typed_codes: list[str]
    mid_turn_codes: list[str]
    d10_before: FrictionSnapshot
    d10_after: FrictionSnapshot
    d10_delta: dict[str, int | None]
    extra: dict[str, Any] = field(default_factory=dict)

    @property
    def last_closeout(self) -> dict[str, Any]:
        return self.product_closeouts[-1] if self.product_closeouts else {}

    @property
    def last_grounding(self) -> dict[str, Any]:
        g = self.last_closeout.get("grounding")
        return dict(g) if isinstance(g, Mapping) else {}

    def summary_dict(self) -> dict[str, Any]:
        g = self.last_grounding
        return {
            "session_id": self.session_id,
            "traced": g.get("traced"),
            "evidence_optional": g.get("evidence_optional"),
            "host_assembled": bool(g.get("host_assembled") is True),
            "retry_count": int(g.get("retry_count") or 0),
            "hint_code": str(g.get("hint_code") or ""),
            "cited_evidence": g.get("cited_evidence") or [],
            "delivered_handles": [r.handle for r in self.delivery_log.records],
            "minted_handles": [r.handle for r in self.product_records],
            "agent_note_count": len(self.agent_notes),
            "typed_codes": list(self.typed_codes),
            "mid_turn_codes": list(self.mid_turn_codes),
            "pending_turn_count": len(self.raw_pending_requests),
            "closeout_content": str(self.last_closeout.get("content") or ""),
            "extra": dict(self.extra),
        }

    def to_artifact_dict(self, manifest: SidecarRunManifest | None = None) -> dict[str, Any]:
        return {
            "version": 1,
            "session_id": self.session_id,
            "run_manifest": manifest.to_dict() if manifest else None,
            "raw_pending_requests": self.raw_pending_requests,
            "scripted_responses": self.scripted_responses,
            "delivered_tool_messages": self.delivered_tool_messages,
            "delivery_log": self.delivery_log.to_dict(),
            "product_closeouts": list(self.product_closeouts),
            "agent_notes": self.agent_notes,
            "completion_reports": self.completion_reports,
            "typed_codes": self.typed_codes,
            "mid_turn_codes": self.mid_turn_codes,
            "d10_before": self.d10_before.to_dict(),
            "d10_after": self.d10_after.to_dict(),
            "d10_delta": self.d10_delta,
            "extra": self.extra,
        }


@dataclass
class SlotOutcome:
    selector: str
    family_id: str
    wording: str
    passed: bool
    checks: dict[str, bool]
    failure_reasons: list[str]
    outcome_signature: dict[str, Any]
    d10_before: dict[str, Any]
    d10_after: dict[str, Any]
    d10_delta: dict[str, int | None]
    summary: dict[str, Any]

    def to_dict(self) -> dict[str, Any]:
        return {
            "selector": self.selector,
            "family_id": self.family_id,
            "wording": self.wording,
            "passed": self.passed,
            "checks": self.checks,
            "failure_reasons": self.failure_reasons,
            "outcome_signature": self.outcome_signature,
            "d10_before": self.d10_before,
            "d10_after": self.d10_after,
            "d10_delta": self.d10_delta,
            "summary": self.summary,
        }


@dataclass
class RunnerContext:
    client: SidecarSessionClient
    sidecar: IsolatedSidecarProcess
    project_id: str
    workspace: pathlib.Path
    out_dir: pathlib.Path
    manifest: SidecarRunManifest
    trace_friction: bool
    poll_timeout: float = 90.0
    known_sessions: set[str] = field(default_factory=set)

    @property
    def trace_log(self) -> pathlib.Path | None:
        return self.sidecar.log_path if self.trace_friction else None

    def new_session(self, *, posture: str = "build") -> str:
        sid = self.client.create_session(self.project_id, posture=posture)["id"]
        self.known_sessions.add(sid)
        self.client.wait_session_ready(sid)
        return sid


def _init_gb05_workspace(workspace: pathlib.Path) -> None:
    """Create deterministic git-initialized workspace with fixture files for S01..S07."""
    workspace.mkdir(parents=True, exist_ok=True)
    (workspace / "service.go").write_text(
        "package service\n\nfunc Run() bool { return true }\n",
        encoding="utf-8",
    )
    (workspace / "helper.go").write_text(
        "package service\n\nfunc Helper() string { return \"ok\" }\n",
        encoding="utf-8",
    )
    (workspace / "extra.go").write_text(
        "package service\n\nfunc Extra() int { return 42 }\n",
        encoding="utf-8",
    )
    (workspace / "unread.go").write_text(
        "package service\n\nfunc SecretUnread() bool { return false }\n",
        encoding="utf-8",
    )
    for cmd in (
        ["git", "init", "-q"],
        ["git", "add", "."],
        [
            "git",
            "-c",
            "user.name=GB05 Fixture",
            "-c",
            "user.email=gb05@example.invalid",
            "commit",
            "-qm",
            "gb05 fixture",
        ],
    ):
        subprocess.run(cmd, cwd=workspace, check=True)


def _extract_codes_from_blobs(blobs: Sequence[str]) -> list[str]:
    seen: list[str] = []
    seen_set: set[str] = set()
    for blob in blobs:
        if not blob:
            continue
        for m in _CODE_RE.finditer(blob):
            code = m.group(1)
            if code not in seen_set:
                seen_set.add(code)
                seen.append(code)
    return seen


def _extract_first_repair_id(pending: Mapping[str, Any]) -> str:
    # Search message contents from newest to oldest so we match the actual
    # `- c1:` bullet in the `[host:coordinator-citation-grounding]` repair
    # prompt and never a hex fragment from a UUID in the JSON envelope.
    messages = pending.get("messages")
    if isinstance(messages, list):
        for msg in reversed(messages):
            if isinstance(msg, Mapping):
                content = msg.get("content")
                if isinstance(content, str):
                    m = _REPAIR_ID_RE.search(content)
                    if m:
                        return m.group(1)
    text = json.dumps(pending)
    m = _REPAIR_ID_RE.search(text)
    if m:
        return m.group(1)
    return "c1"


def _inspect_session_db_extras(
    db_path: pathlib.Path, session_id: str
) -> tuple[list[dict[str, Any]], list[dict[str, Any]], list[str]]:
    """Read agent_note rows, completion_report rows, and persisted typed codes from store.db."""
    if not db_path.is_file():
        return ([], [], [])
    agent_notes: list[dict[str, Any]] = []
    completion_reports: list[dict[str, Any]] = []
    code_blobs: list[str] = []

    with contextlib.closing(sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)) as conn:
        conn.row_factory = sqlite3.Row
        tables = {
            row[0]
            for row in conn.execute("SELECT name FROM sqlite_master WHERE type='table'").fetchall()
        }
        if "messages" in tables:
            cols = {row[1] for row in conn.execute("PRAGMA table_info(messages)").fetchall()}
            order_col = "ord, id" if "ord" in cols else "rowid"
            rows = conn.execute(
                f"SELECT * FROM messages WHERE session_id = ? ORDER BY {order_col}",
                (session_id,),
            ).fetchall()
            for row in rows:
                kind = str(row["kind"]) if "kind" in cols and row["kind"] else ""
                vis = str(row["visibility"]) if "visibility" in cols and row["visibility"] else ""
                content = str(row["content"]) if "content" in cols and row["content"] else ""
                origin = str(row["origin"]) if "origin" in cols and row["origin"] else ""
                if kind == "agent_note":
                    agent_notes.append(
                        {
                            "id": str(row["id"]),
                            "kind": kind,
                            "visibility": vis,
                            "origin": origin,
                            "content": content,
                            "grounding": (
                                json.loads(row["grounding_json"])
                                if "grounding_json" in cols and row["grounding_json"]
                                else None
                            ),
                        }
                    )
                if "completion_report_json" in cols and row["completion_report_json"]:
                    try:
                        cr = json.loads(row["completion_report_json"])
                    except (TypeError, ValueError):
                        cr = {"raw": str(row["completion_report_json"])}
                    completion_reports.append(
                        {
                            "id": str(row["id"]),
                            "origin": origin,
                            "completion_report": cr,
                        }
                    )
                for col in (
                    "content",
                    "grounding_json",
                    "workflow_feedback_json",
                    "content_parts_json",
                    "tool_result_json",
                    "completion_report_json",
                ):
                    if col in cols and row[col]:
                        code_blobs.append(str(row[col]))
        if "event_outbox" in tables:
            cols = {row[1] for row in conn.execute("PRAGMA table_info(event_outbox)").fetchall()}
            if "payload_json" in cols:
                if "session_id" in cols:
                    ev_rows = conn.execute(
                        "SELECT payload_json FROM event_outbox WHERE session_id = ?",
                        (session_id,),
                    ).fetchall()
                else:
                    ev_rows = conn.execute("SELECT payload_json FROM event_outbox").fetchall()
                for er in ev_rows:
                    val = str(er["payload_json"] or "")
                    if session_id in val or "session_id" in cols:
                        code_blobs.append(val)

    return (agent_notes, completion_reports, _extract_codes_from_blobs(code_blobs))


def _record_delivered_tools(
    pending: Mapping[str, Any],
    *,
    session_id: str,
    ws_path: pathlib.Path,
    seen_call_ids: set[str],
    call_meta: dict[str, dict[str, Any]],
    delivered_tools: list[dict[str, Any]],
    delivery_records: list[DeliveryRecord],
) -> None:
    newly_delivered = extract_tool_messages_from_pending(pending, seen_call_ids, call_meta)
    for item in newly_delivered:
        delivered_tools.append(item)
        handles = item["evidence_handles"]
        all_tags = tuple(item["delivery_tags"])
        delivered_text = item["delivered_body"]
        args = item["arguments"]
        arg_path = str(args.get("path") or "")
        url = str(args.get("url") or "")

        for handle in handles:
            kind = handle.split("#", 1)[0] if "#" in handle else ""
            handle_tags = tuple(
                t
                for t in all_tags
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
                rec_source_rev = ""
            else:
                rec_shape = "file_region" if handle_path else ""
                fixture_file = _safe_fixture_file(ws_path, handle_path)
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
                rec_source_rev = ""
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
                    round_id="round-1",
                    intent_window="intent-1",
                    source_revision=rec_source_rev,
                    handle=handle,
                    kind=kind,
                    path=handle_path,
                    url=url,
                    artifact_id="",
                    call_id=item["call_id"],
                    fixture_provenance="host",
                    shape=rec_shape,
                )
            )


def _drive_dynamic_session(
    ctx: RunnerContext,
    *,
    selector: str,
    wording: str,
    prompt_text: str,
    initial_turns: Sequence[ScriptedTurn],
    repair_fn: Callable[[dict[str, Any], int, dict[str, Any]], ScriptedTurn | None] | None = None,
    max_repairs: int = 6,
    session_id: str | None = None,
) -> DynamicSessionCapture:
    """Drive a scripted + dynamic-repair session with D10 friction tracing."""
    sid = session_id or ctx.new_session()
    d10_before = probe_store_db(ctx.sidecar.store_db_path, sid, trace_log_path=ctx.trace_log)
    p = ctx.client.send_prompt(sid, prompt_text)
    submission_id = str(p.get("submission_id") or p.get("id") or "")

    raw_pending: list[dict[str, Any]] = []
    scripted_resps: list[dict[str, Any]] = []
    delivered_tools: list[dict[str, Any]] = []
    delivery_records: list[DeliveryRecord] = []
    seen_call_ids: set[str] = set()
    call_meta: dict[str, dict[str, Any]] = {}
    state: dict[str, Any] = {"offered_tools_by_repair": []}

    def _send_turn(pending: dict[str, Any], turn: ScriptedTurn) -> None:
        offered = set(pending.get("tools") or ())
        if offered and turn.tool_calls:
            missing_tools = sorted(
                {tc["name"] for tc in turn.tool_calls if tc["name"] not in offered}
            )
            if missing_tools and "request_tools" in offered:
                load_id = f"load-{uuid.uuid4().hex[:8]}"
                ctx.client.respond(
                    str(pending["id"]),
                    tool_calls=(
                        {
                            "id": load_id,
                            "name": "request_tools",
                            "args": {"need": ", ".join(missing_tools)},
                        },
                    ),
                )
                pending = ctx.client.wait_pending(sid, timeout=ctx.poll_timeout)
                raw_pending.append(dict(pending))
                _record_delivered_tools(
                    pending,
                    session_id=sid,
                    ws_path=ctx.workspace,
                    seen_call_ids=seen_call_ids,
                    call_meta=call_meta,
                    delivered_tools=delivered_tools,
                    delivery_records=delivery_records,
                )

        outgoing_calls: list[dict[str, Any]] = []
        for idx, tc in enumerate(turn.tool_calls):
            cid = str(tc.get("id") or f"call-{len(scripted_resps) + 1}-{idx + 1}")
            call_entry = {"id": cid, "name": tc["name"], "args": dict(tc.get("args") or {})}
            call_meta[cid] = call_entry
            outgoing_calls.append(call_entry)

        resp_payload = ctx.client.respond(
            str(pending["id"]),
            content=turn.content,
            tool_calls=outgoing_calls,
        )
        scripted_resps.append(resp_payload)

    for turn in initial_turns:
        pending = ctx.client.wait_pending(sid, timeout=ctx.poll_timeout)
        raw_pending.append(dict(pending))
        _record_delivered_tools(
            pending,
            session_id=sid,
            ws_path=ctx.workspace,
            seen_call_ids=seen_call_ids,
            call_meta=call_meta,
            delivered_tools=delivered_tools,
            delivery_records=delivery_records,
        )
        _send_turn(pending, turn)

    # Wait for settlement, handling dynamic repair / continuation turns
    deadline = time.monotonic() + ctx.poll_timeout
    repair_idx = 0
    settled = False
    while time.monotonic() < deadline:
        if submission_id:
            obs = ctx.client.request(f"/harness/execution/{sid}/{submission_id}", missing=True)
            if isinstance(obs, dict) and obs.get("settled") is True:
                settled = True
                break
        else:
            st = ctx.client.request(f"/v1/sessions/{sid}", missing=True)
            if isinstance(st, dict) and st.get("status") == "idle":
                settled = True
                break

        if repair_idx < max_repairs:
            rp = ctx.client.get_pending(sid)
            if rp is not None:
                raw_pending.append(dict(rp))
                _record_delivered_tools(
                    rp,
                    session_id=sid,
                    ws_path=ctx.workspace,
                    seen_call_ids=seen_call_ids,
                    call_meta=call_meta,
                    delivered_tools=delivered_tools,
                    delivery_records=delivery_records,
                )
                state["offered_tools_by_repair"].append(list(rp.get("tools") or []))
                next_turn: ScriptedTurn | None = None
                if repair_fn is not None:
                    next_turn = repair_fn(rp, repair_idx, state)
                if next_turn is None:
                    fallback_content = initial_turns[-1].content if initial_turns else ""
                    next_turn = ScriptedTurn(content=fallback_content)
                _send_turn(rp, next_turn)
                repair_idx += 1
                continue
        time.sleep(0.05)

    if not settled:
        raise TimeoutError(
            f"Slot {selector}/{wording} timed out waiting for settlement "
            f"(session={sid}, repair_idx={repair_idx})"
        )

    d10_after = probe_store_db(ctx.sidecar.store_db_path, sid, trace_log_path=ctx.trace_log)
    d10_delta = d10_after.delta_observed(d10_before)
    product_records, product_closeouts = extract_product_observations(ctx.sidecar.store_db_path, sid)
    agent_notes, completion_reports, db_codes = _inspect_session_db_extras(
        ctx.sidecar.store_db_path, sid
    )

    try:
        raw_msgs = ctx.client.get_messages(sid)
        user_visible = (
            list(raw_msgs.get("messages") or [])
            if isinstance(raw_msgs, Mapping)
            else (list(raw_msgs) if isinstance(raw_msgs, list) else [])
        )
    except Exception:
        user_visible = []

    pending_codes = _extract_codes_from_blobs([json.dumps(r) for r in raw_pending])
    all_codes: list[str] = []
    for c in [*pending_codes, *db_codes]:
        if c not in all_codes:
            all_codes.append(c)
    mid_turn_codes = [c for c in all_codes if c.startswith("MID_TURN_NOTE_")]

    capture = DynamicSessionCapture(
        session_id=sid,
        raw_pending_requests=raw_pending,
        scripted_responses=scripted_resps,
        delivered_tool_messages=delivered_tools,
        delivery_log=DeliveryLog(records=tuple(delivery_records)),
        product_records=product_records,
        product_closeouts=product_closeouts,
        agent_notes=agent_notes,
        user_visible_messages=user_visible,
        completion_reports=completion_reports,
        typed_codes=all_codes,
        mid_turn_codes=mid_turn_codes,
        d10_before=d10_before,
        d10_after=d10_after,
        d10_delta=d10_delta,
        extra=state,
    )

    art_dir = ctx.out_dir / "artifacts"
    art_dir.mkdir(parents=True, exist_ok=True)
    (art_dir / f"{selector}__{wording}.json").write_text(
        json.dumps(capture.to_artifact_dict(ctx.manifest), indent=2, sort_keys=True),
        encoding="utf-8",
    )
    return capture


def _friction_eq(cap: DynamicSessionCapture, ctx: RunnerContext, expected: int) -> bool:
    if not ctx.trace_friction:
        return True
    return cap.d10_delta.get("friction_units") == expected


def _friction_ge(cap: DynamicSessionCapture, ctx: RunnerContext, minimum: int) -> bool:
    if not ctx.trace_friction:
        return True
    val = cap.d10_delta.get("friction_units")
    return isinstance(val, int) and val >= minimum


def _build_outcome(
    spec: GB05SlotSpec,
    wording: str,
    cap: DynamicSessionCapture,
    checks: dict[str, bool],
    *,
    extra_sig: dict[str, Any] | None = None,
) -> SlotOutcome:
    passed = all(checks.values())
    failures = [k for k, v in checks.items() if not v]
    g = cap.last_grounding
    sig: dict[str, Any] = {
        "traced": g.get("traced"),
        "evidence_optional": bool(g.get("evidence_optional") is True),
        "host_assembled": bool(g.get("host_assembled") is True),
        "hint_code": str(g.get("hint_code") or ""),
        "retry_count": int(g.get("retry_count") or 0),
        "friction_units": cap.d10_delta.get("friction_units"),
        "agent_note_count": len(cap.agent_notes),
        "mid_turn_codes": sorted(cap.mid_turn_codes),
        "passed": passed,
    }
    if extra_sig:
        sig.update(extra_sig)
    return SlotOutcome(
        selector=spec.selector,
        family_id=spec.family_id,
        wording=wording,
        passed=passed,
        checks=checks,
        failure_reasons=failures,
        outcome_signature=sig,
        d10_before=cap.d10_before.to_dict(),
        d10_after=cap.d10_after.to_dict(),
        d10_delta=cap.d10_delta,
        summary=cap.summary_dict(),
    )


def _cmd_read(path: str) -> ScriptedTurn:
    return ScriptedTurn(tool_calls=({"name": "command", "args": {"command": f"cat -n {path}"}},))


def _cited_handles(cap: DynamicSessionCapture) -> set[str]:
    cited = cap.last_grounding.get("cited_evidence") or []
    return {
        str(c.get("handle") or "")
        for c in cited
        if isinstance(c, Mapping) and c.get("handle")
    }


def _delivered_handles(cap: DynamicSessionCapture) -> set[str]:
    return {r.handle for r in cap.delivery_log.records}


# =============================================================================
# S01 handlers (5 sidecar slots)
# =============================================================================


def _handle_s01_01(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Reply directly without inspecting files.",
        initial_turns=(ScriptedTurn(content=render_wording("No files were inspected.", w)),),
        max_repairs=1,
    )
    g = cap.last_grounding
    checks = {
        "evidence_optional": g.get("evidence_optional") is True,
        "zero_cited_evidence": len(g.get("cited_evidence") or []) == 0,
        "zero_retry": int(g.get("retry_count") or 0) == 0,
        "zero_friction": _friction_eq(cap, ctx, 0),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s01_02(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and report line 3.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(content=render_wording("Run returns true in service.go.", w)),
        ),
        repair_fn=lambda _rp, _idx, _st: ScriptedTurn(
            content=render_wording("Run returns true at [read#1:3].", w)
        ),
        max_repairs=3,
    )
    g = cap.last_grounding
    # Decision 049: D1 accepts an uncited closeout after a citable read by
    # host-attaching observed citations (`host_assembled: true, retry_count: 0,
    # host_assembled_closeouts: 1, friction_units: 0`), never setting
    # `evidence_optional: true`.
    checks = {
        "not_evidence_optional": g.get("evidence_optional") is not True,
        "host_assembled": g.get("host_assembled") is True,
        "zero_retries": int(g.get("retry_count") or 0) == 0,
        "cited_read1": "read#1" in _cited_handles(cap),
        "zero_friction": (cap.d10_delta.get("friction_units") in (0, None)),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s01_03(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and report line 3.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(content=render_wording("Run() returns true at [read#1:3].", w)),
        ),
        max_repairs=1,
    )
    g = cap.last_grounding
    checks = {
        "traced": g.get("traced") is True,
        "not_evidence_optional": g.get("evidence_optional") is not True,
        "zero_retry": int(g.get("retry_count") or 0) == 0,
        "cited_read1": "read#1" in _cited_handles(cap),
        "zero_friction": _friction_eq(cap, ctx, 0),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s01_04(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    # Touch a tracked file without reading it so only source_change# (if any) exists
    marker_file = ctx.workspace / "extra.go"
    orig = marker_file.read_text(encoding="utf-8")
    try:
        marker_file.write_text(orig + f"// touched for {w}\n", encoding="utf-8")
        cap = _drive_dynamic_session(
            ctx,
            selector=spec.selector,
            wording=w,
            prompt_text="Acknowledge the workspace state without inspecting files.",
            initial_turns=(
                ScriptedTurn(
                    content=render_wording("Acknowledged workspace state without reading files.", w)
                ),
            ),
            max_repairs=1,
        )
    finally:
        marker_file.write_text(orig, encoding="utf-8")
    g = cap.last_grounding
    checks = {
        "evidence_optional": g.get("evidence_optional") is True,
        "zero_retry": int(g.get("retry_count") or 0) == 0,
        "zero_friction": _friction_eq(cap, ctx, 0),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s01_05(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    # Sidecar verifies non-empty ledger never sets evidence_optional=true when citing read#1
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and cite line 3.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(content=render_wording("Verified Run() at [read#1:3].", w)),
        ),
        max_repairs=1,
    )
    g = cap.last_grounding
    checks = {
        "denies_exemption": g.get("evidence_optional") is not True,
        "traced": g.get("traced") is True,
        "cited_read1": "read#1" in _cited_handles(cap),
        "zero_friction": _friction_eq(cap, ctx, 0),
    }
    return _build_outcome(spec, w, cap, checks)


# =============================================================================
# S02 handlers (4 sidecar slots)
# =============================================================================


def _handle_s02_01(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    bad = render_wording("Run() returns true at [read#99:3].", w)
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and cite line 3.",
        initial_turns=(_cmd_read("service.go"), ScriptedTurn(content=bad)),
        repair_fn=lambda _rp, _idx, _st: ScriptedTurn(content=bad),
        max_repairs=6,
    )
    g = cap.last_grounding
    cited = _cited_handles(cap)
    delivered = _delivered_handles(cap)
    checks = {
        "host_assembled": g.get("host_assembled") is True,
        "all_attached_in_delivery_log": cited.issubset(delivered),
        "no_fabricated_read99": "read#99" not in cited,
        "friction_charged": _friction_ge(cap, ctx, 1),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s02_02(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    bad = render_wording("Run() returns true at [read#99:3].", w)
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Answer without running tools.",
        initial_turns=(ScriptedTurn(content=bad),),
        repair_fn=lambda _rp, _idx, _st: ScriptedTurn(content=bad),
        max_repairs=6,
    )
    g = cap.last_grounding
    cited = g.get("cited_evidence") or []
    checks = {
        "empty_delivery_log": len(cap.delivery_log.records) == 0,
        "attaches_nothing": len(cited) == 0,
        "host_assembled": g.get("host_assembled") is True,
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s02_03(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    bad = render_wording("Verified Run() at [read#99:3].", w)
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and cite line 3.",
        initial_turns=(_cmd_read("service.go"), ScriptedTurn(content=bad)),
        repair_fn=lambda _rp, _idx, _st: ScriptedTurn(content=bad),
        max_repairs=6,
    )
    g = cap.last_grounding
    checks = {
        "host_assembled_flag": g.get("host_assembled") is True,
        "all_attached_in_delivery_log": _cited_handles(cap).issubset(_delivered_handles(cap)),
        "friction_charged": _friction_ge(cap, ctx, 1),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s02_04(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    bad = render_wording("Verified Run() at [read#99:3].", w)
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read lines 1-2 of service.go and report.",
        initial_turns=(
            ScriptedTurn(
                tool_calls=({"name": "command", "args": {"command": "sed -n '1,2p' service.go | nl -ba"}},)
            ),
            ScriptedTurn(content=bad),
        ),
        repair_fn=lambda _rp, _idx, _st: ScriptedTurn(content=bad),
        max_repairs=6,
    )
    g = cap.last_grounding
    ranges_by_handle = {r.handle: r.delivered_ranges for r in cap.delivery_log.records}
    ranges_valid = True
    for entry in g.get("cited_evidence") or []:
        if not isinstance(entry, Mapping):
            continue
        h = str(entry.get("handle") or "")
        ln = entry.get("line")
        if h and h in ranges_by_handle and isinstance(ln, int) and ranges_by_handle[h]:
            if not any(rng.start <= ln <= rng.end for rng in ranges_by_handle[h]):
                ranges_valid = False
    checks = {
        "host_assembled": g.get("host_assembled") is True,
        "all_attached_in_delivery_log": _cited_handles(cap).issubset(_delivered_handles(cap)),
        "no_invented_ranges": ranges_valid,
    }
    return _build_outcome(spec, w, cap, checks)


# =============================================================================
# S03 handlers (9 sidecar slots)
# =============================================================================


def _handle_s03_01(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and cite line 3.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(content=render_wording("Run() returns true at [read#99:3].", w)),
        ),
        repair_fn=lambda rp, _idx, _st: ScriptedTurn(
            content=f"[cite: {_extract_first_repair_id(rp)} => read#1:3]"
        ),
        max_repairs=3,
    )
    g = cap.last_grounding
    checks = {
        "traced": g.get("traced") is True,
        "not_host_assembled": g.get("host_assembled") is not True,
        "cited_read1": "read#1" in _cited_handles(cap),
        "no_read99": "read#99" not in _cited_handles(cap),
        "friction_charged": _friction_ge(cap, ctx, 1),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s03_02(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    def _repair(rp: dict[str, Any], idx: int, st: dict[str, Any]) -> ScriptedTurn:
        rid = _extract_first_repair_id(rp)
        if idx == 0:
            # Missing repair target (citations_without_targets) triggers CITATION_REPAIR_INVALID
            # while leaving the retained draft standing.
            return ScriptedTurn(content="[read#1:3]")
        st["post_invalid_codes"] = _extract_codes_from_blobs([json.dumps(rp)])
        return ScriptedTurn(content=f"[cite: {rid} => read#1:3]")

    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and cite line 3.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(content=render_wording("Run() returns true at [read#99:3].", w)),
        ),
        repair_fn=_repair,
        max_repairs=4,
    )
    g = cap.last_grounding
    post_codes = cap.extra.get("post_invalid_codes") or []
    checks = {
        "returned_citation_repair_invalid": "CITATION_REPAIR_INVALID" in post_codes
        or "CITATION_REPAIR_INVALID" in cap.typed_codes,
        "settled_after_valid_repair": g.get("traced") is True and "read#1" in _cited_handles(cap),
        "draft_narrative_preserved": "Run() returns true" in str(cap.last_closeout.get("content") or ""),
        "friction_charged": _friction_ge(cap, ctx, 1),
    }
    return _build_outcome(spec, w, cap, checks, extra_sig={"post_invalid_codes": sorted(post_codes)})


def _handle_s03_03(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    def _repair(rp: dict[str, Any], idx: int, st: dict[str, Any]) -> ScriptedTurn:
        rid = _extract_first_repair_id(rp)
        if idx == 0:
            return ScriptedTurn(content=f"[cite: {rid} => read#1 extra invalid words]")
        st["post_invalid_codes"] = _extract_codes_from_blobs([json.dumps(rp)])
        return ScriptedTurn(content=f"[cite: {rid} => read#1:3]")

    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and cite line 3.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(content=render_wording("Run() returns true at [read#99:3].", w)),
        ),
        repair_fn=_repair,
        max_repairs=4,
    )
    g = cap.last_grounding
    post_codes = cap.extra.get("post_invalid_codes") or []
    checks = {
        "returned_citation_repair_invalid": "CITATION_REPAIR_INVALID" in post_codes
        or "CITATION_REPAIR_INVALID" in cap.typed_codes,
        "settled_after_valid_repair": g.get("traced") is True and "read#1" in _cited_handles(cap),
        "draft_narrative_preserved": "Run() returns true" in str(cap.last_closeout.get("content") or ""),
    }
    return _build_outcome(spec, w, cap, checks, extra_sig={"post_invalid_codes": sorted(post_codes)})


def _handle_s03_04(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    def _repair(rp: dict[str, Any], idx: int, st: dict[str, Any]) -> ScriptedTurn:
        rid = _extract_first_repair_id(rp)
        if idx == 0:
            return ScriptedTurn(content="[cite: c99 => read#1:3]")
        st["post_invalid_codes"] = _extract_codes_from_blobs([json.dumps(rp)])
        return ScriptedTurn(content=f"[cite: {rid} => read#1:3]")

    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and cite line 3.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(content=render_wording("Run() returns true at [read#99:3].", w)),
        ),
        repair_fn=_repair,
        max_repairs=4,
    )
    g = cap.last_grounding
    post_codes = cap.extra.get("post_invalid_codes") or []
    checks = {
        "returned_citation_repair_invalid": "CITATION_REPAIR_INVALID" in post_codes
        or "CITATION_REPAIR_INVALID" in cap.typed_codes,
        "settled_after_valid_repair": g.get("traced") is True and "read#1" in _cited_handles(cap),
        "draft_narrative_preserved": "Run() returns true" in str(cap.last_closeout.get("content") or ""),
    }
    return _build_outcome(spec, w, cap, checks, extra_sig={"post_invalid_codes": sorted(post_codes)})


def _handle_s03_05(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    def _repair(_rp: dict[str, Any], idx: int, _st: dict[str, Any]) -> ScriptedTurn:
        if idx == 0:
            # Operational tool batch (with side effects, not a read-only observer) retires
            # the pinned draft while retaining friction/retry budgets (COV-03).
            return ScriptedTurn(
                tool_calls=(
                    {"name": "command", "args": {"command": "touch .s03-05-op-marker"}},
                    {"name": "command", "args": {"command": "cat -n helper.go"}},
                )
            )
        return ScriptedTurn(
            content=render_wording("Completely new summary: Helper() is at [read#2:3].", w)
        )

    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and report.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(content=render_wording("Run() returns true at [read#99:3].", w)),
        ),
        repair_fn=_repair,
        max_repairs=4,
    )
    g = cap.last_grounding
    checks = {
        "draft_retired_new_narrative_accepted": g.get("traced") is True
        and "Helper()" in str(cap.last_closeout.get("content") or ""),
        "cited_read2": "read#2" in _cited_handles(cap),
        "budget_retained_friction_charged": _friction_ge(cap, ctx, 1),
    }
    return _build_outcome(spec, w, cap, checks)


def _extract_progress_snapshot(pending_req: Mapping[str, Any]) -> dict[str, Any]:
    """Extract the latest host progress snapshot JSON from a pending request."""
    for msg in reversed(pending_req.get("messages") or []):
        content = str(msg.get("content") or "")
        if "Current progress snapshot" not in content:
            continue
        start = content.find("{")
        end = content.rfind("}")
        if start != -1 and end > start:
            try:
                parsed = json.loads(content[start : end + 1])
                if isinstance(parsed, dict):
                    return parsed
            except json.JSONDecodeError:
                pass
    return {}


def _build_complete_progress_args(pending_req: Mapping[str, Any], content: str) -> dict[str, Any]:
    """Build valid update_progress args preserving expected_revision, item_ids, and reconciliation_token."""
    snap = _extract_progress_snapshot(pending_req)
    args: dict[str, Any] = {"content": content}
    if snap.get("revision"):
        args["expected_revision"] = snap["revision"]
    if isinstance(snap.get("item_ids"), list):
        args["item_ids"] = snap["item_ids"]
    rec = snap.get("reconciliation")
    rec_token = snap.get("reconciliation_token") or (
        rec.get("token") if isinstance(rec, Mapping) else None
    )
    if rec_token:
        args["reconciliation_token"] = rec_token
    return args


def _handle_s03_06(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    # Pin draft, then spawn a worker that fails/completes to trigger terminal worker card retirement
    sid = ctx.new_session()
    d10_before = probe_store_db(ctx.sidecar.store_db_path, sid, trace_log_path=ctx.trace_log)
    p = ctx.client.send_prompt(sid, "Read service.go; if needed dispatch a worker.")
    sub_id = str(p.get("submission_id") or p.get("id") or "")

    # Step 1: read service.go
    req1 = ctx.client.wait_pending(sid, timeout=ctx.poll_timeout)
    ctx.client.respond(
        req1["id"],
        tool_calls=[{"id": "c-read1", "name": "command", "args": {"command": "cat -n service.go"}}],
    )
    # Step 2: send bad closeout to pin draft
    req2 = ctx.client.wait_pending(sid, timeout=ctx.poll_timeout)
    ctx.client.respond(req2["id"], content=render_wording("Run() is at [read#99:3].", w))

    # Step 3: during repair, dispatch update_progress + task + wait
    def _send_tool(name: str, args: dict[str, Any]) -> None:
        r = ctx.client.wait_pending(sid, timeout=ctx.poll_timeout)
        if name not in (r.get("tools") or []):
            ctx.client.respond(
                r["id"],
                tool_calls=[{"id": str(uuid.uuid4()), "name": "request_tools", "args": {"need": name}}],
            )
            r = ctx.client.wait_pending(sid, timeout=ctx.poll_timeout)
        ctx.client.respond(r["id"], tool_calls=[{"id": str(uuid.uuid4()), "name": name, "args": args}])

    _send_tool("update_progress", {"content": "## Progress\n- [ ] Check service.go"})
    _send_tool(
        "task",
        {
            "agent_type": "repo-researcher",
            "brief": {"goal": "Inspect service.go", "done_when": ["Done"]},
            "scope": {"mode": "read", "paths": ["service.go"]},
        },
    )

    # Drive worker child to completion (blocked/failed or complete)
    deadline = time.monotonic() + ctx.poll_timeout
    child_sid = ""
    while time.monotonic() < deadline:
        pend = ctx.client.request("/harness/llm/pending")
        if isinstance(pend, dict) and pend.get("pending"):
            psid = str(pend.get("session_id") or "")
            if psid == sid:
                ctx.client.respond(
                    pend["id"],
                    tool_calls=[
                        {
                            "id": str(uuid.uuid4()),
                            "name": "wait",
                            "args": {"timeout_ms": 15000, "conditions": [{"kind": "next_worker_done"}]},
                        }
                    ],
                )
            elif psid and psid not in ctx.known_sessions:
                child_sid = psid
                ctx.known_sessions.add(child_sid)
                ctx.client.respond(
                    pend["id"],
                    tool_calls=[
                        {
                            "id": str(uuid.uuid4()),
                            "name": "complete_leg",
                            "args": {
                                "leg_status": "blocked",
                                "brief": "Simulated worker failure for COV-03 retirement test.",
                            },
                        }
                    ],
                )
                break
        time.sleep(0.1)

    # Root resumes after terminal worker card -> close out progress checklist first,
    # then send new narrative citing [read#1:3] and handle any follow-up turns cleanly.
    raw_pending: list[dict[str, Any]] = [dict(req1), dict(req2)]
    root_resume = ctx.client.wait_pending(sid, timeout=ctx.poll_timeout)
    raw_pending.append(dict(root_resume))
    ctx.client.respond(
        root_resume["id"],
        tool_calls=[
            {
                "id": str(uuid.uuid4()),
                "name": "update_progress",
                "args": _build_complete_progress_args(
                    root_resume, "## Progress\n- [x] Check service.go"
                ),
            }
        ],
    )
    root_closeout = ctx.client.wait_pending(sid, timeout=ctx.poll_timeout)
    raw_pending.append(dict(root_closeout))
    ctx.client.respond(
        root_closeout["id"],
        content=render_wording("Retired draft after worker card; verified [read#1:3].", w),
    )
    settle_deadline = time.monotonic() + ctx.poll_timeout
    extra_turns = 0
    settled = False
    while time.monotonic() < settle_deadline:
        st_resp = ctx.client.request(f"/v1/sessions/{sid}", missing=True)
        if isinstance(st_resp, dict) and st_resp.get("status") == "idle":
            settled = True
            break
        if extra_turns < 3:
            rp = ctx.client.get_pending(sid)
            if rp is not None:
                raw_pending.append(dict(rp))
                ctx.client.respond(
                    rp["id"],
                    content=render_wording("Retired draft after worker card; verified [read#1:3].", w),
                )
                extra_turns += 1
                continue
        time.sleep(0.05)
    if not settled:
        raise TimeoutError(f"S03-06 timed out waiting for session {sid} to settle")

    d10_after = probe_store_db(ctx.sidecar.store_db_path, sid, trace_log_path=ctx.trace_log)
    d10_delta = d10_after.delta_observed(d10_before)
    prod_recs, prod_closeouts = extract_product_observations(ctx.sidecar.store_db_path, sid)
    notes, reports, db_codes = _inspect_session_db_extras(ctx.sidecar.store_db_path, sid)
    cap = DynamicSessionCapture(
        session_id=sid,
        raw_pending_requests=raw_pending,
        scripted_responses=[],
        delivered_tool_messages=[],
        delivery_log=DeliveryLog(records=()),
        product_records=prod_recs,
        product_closeouts=prod_closeouts,
        agent_notes=notes,
        user_visible_messages=[],
        completion_reports=reports,
        typed_codes=db_codes,
        mid_turn_codes=[c for c in db_codes if c.startswith("MID_TURN_NOTE_")],
        d10_before=d10_before,
        d10_after=d10_after,
        d10_delta=d10_delta,
        extra={"child_session_id": child_sid},
    )
    g = cap.last_grounding
    checks = {
        "worker_spawned": bool(child_sid),
        "draft_retired_new_narrative_accepted": g.get("traced") is True
        and "Retired draft" in str(cap.last_closeout.get("content") or ""),
        "cited_read1": "read#1" in _cited_handles(cap),
        "friction_charged": _friction_ge(cap, ctx, 1),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s03_07(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    # Pin draft, then issue a `wait` tool call to trigger a typed coordinator wake
    # (WaitResume -> PromptSubmissionOriginLoopWake -> MessageKindHostLoopWake -> ResumeWork),
    # retiring the pinned draft cleanly (COV-03).
    sid = ctx.new_session()
    d10_before = probe_store_db(ctx.sidecar.store_db_path, sid, trace_log_path=ctx.trace_log)
    ctx.client.send_prompt(sid, "Read service.go and report.")

    raw_pending: list[dict[str, Any]] = []
    # Step 1: read service.go -> read#1
    req1 = ctx.client.wait_pending(sid, timeout=ctx.poll_timeout)
    raw_pending.append(dict(req1))
    ctx.client.respond(
        req1["id"],
        tool_calls=[{"id": "c-s0307-1", "name": "command", "args": {"command": "cat -n service.go"}}],
    )
    # Step 2: send bad closeout to pin draft
    req2 = ctx.client.wait_pending(sid, timeout=ctx.poll_timeout)
    raw_pending.append(dict(req2))
    ctx.client.respond(req2["id"], content=render_wording("Run() returns true at [read#99:3].", w))

    # Step 3: during repair, call wait with short timer to trigger typed coordinator wake
    req3 = ctx.client.wait_pending(sid, timeout=ctx.poll_timeout)
    raw_pending.append(dict(req3))
    ctx.client.respond(
        req3["id"],
        tool_calls=[
            {
                "id": "c-s0307-wait",
                "name": "wait",
                "args": {"timeout_ms": 200, "reason": "Await typed coordinator wake"},
            }
        ],
    )

    # Step 4: coordinator wakes on timer -> read extra.go (read#2)
    req4 = ctx.client.wait_pending(sid, timeout=ctx.poll_timeout)
    raw_pending.append(dict(req4))
    ctx.client.respond(
        req4["id"],
        tool_calls=[{"id": "c-s0307-2", "name": "command", "args": {"command": "cat -n extra.go"}}],
    )

    # Step 5: emit completely new narrative citing [read#2:3]
    req5 = ctx.client.wait_pending(sid, timeout=ctx.poll_timeout)
    raw_pending.append(dict(req5))
    ctx.client.respond(
        req5["id"],
        content=render_wording("Woken and re-evaluated; Extra() at [read#2:3].", w),
    )

    settle_deadline = time.monotonic() + ctx.poll_timeout
    extra_turns = 0
    settled = False
    while time.monotonic() < settle_deadline:
        st_resp = ctx.client.request(f"/v1/sessions/{sid}", missing=True)
        if isinstance(st_resp, dict) and st_resp.get("status") == "idle":
            settled = True
            break
        if extra_turns < 3:
            rp = ctx.client.get_pending(sid)
            if rp is not None:
                raw_pending.append(dict(rp))
                ctx.client.respond(
                    rp["id"],
                    content=render_wording("Woken and re-evaluated; Extra() at [read#2:3].", w),
                )
                extra_turns += 1
                continue
        time.sleep(0.05)
    if not settled:
        raise TimeoutError(f"S03-07 timed out waiting for session {sid} to settle")

    d10_after = probe_store_db(ctx.sidecar.store_db_path, sid, trace_log_path=ctx.trace_log)
    d10_delta = d10_after.delta_observed(d10_before)
    prod_recs, prod_closeouts = extract_product_observations(ctx.sidecar.store_db_path, sid)
    notes, reports, db_codes = _inspect_session_db_extras(ctx.sidecar.store_db_path, sid)
    pending_codes = _extract_codes_from_blobs([json.dumps(r) for r in raw_pending])
    all_codes: list[str] = []
    for c in [*pending_codes, *db_codes]:
        if c not in all_codes:
            all_codes.append(c)
    cap = DynamicSessionCapture(
        session_id=sid,
        raw_pending_requests=raw_pending,
        scripted_responses=[],
        delivered_tool_messages=[],
        delivery_log=DeliveryLog(records=()),
        product_records=prod_recs,
        product_closeouts=prod_closeouts,
        agent_notes=notes,
        user_visible_messages=[],
        completion_reports=reports,
        typed_codes=all_codes,
        mid_turn_codes=[c for c in all_codes if c.startswith("MID_TURN_NOTE_")],
        d10_before=d10_before,
        d10_after=d10_after,
        d10_delta=d10_delta,
        extra={},
    )
    g = cap.last_grounding
    checks = {
        "draft_retired_new_narrative_accepted": g.get("traced") is True
        and "Woken and re-evaluated" in str(cap.last_closeout.get("content") or ""),
        "cited_read2": "read#2" in _cited_handles(cap),
        "friction_charged": _friction_ge(cap, ctx, 1),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s03_08(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    def _repair(_rp: dict[str, Any], idx: int, _st: dict[str, Any]) -> ScriptedTurn:
        if idx == 0:
            # Denied batch admission (invalid task arguments) retires pinned draft
            return ScriptedTurn(
                tool_calls=({"name": "task", "args": {"agent_type": "nonexistent-agent"}},)
            )
        return ScriptedTurn(
            content=render_wording("After denied batch admission, verified [read#1:3].", w)
        )

    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and report.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(content=render_wording("Run() returns true at [read#99:3].", w)),
        ),
        repair_fn=_repair,
        max_repairs=4,
    )
    g = cap.last_grounding
    checks = {
        "draft_retired_after_denied_batch": g.get("traced") is True
        and "denied batch" in str(cap.last_closeout.get("content") or ""),
        "cited_read1": "read#1" in _cited_handles(cap),
        "friction_charged": _friction_ge(cap, ctx, 1),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s03_09(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    bad = render_wording("Run() returns true at [read#99:3].", w)
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and cite line 3.",
        initial_turns=(_cmd_read("service.go"), ScriptedTurn(content=bad)),
        repair_fn=lambda _rp, _idx, _st: ScriptedTurn(content=bad),
        max_repairs=6,
    )
    g = cap.last_grounding
    cited = _cited_handles(cap)
    checks = {
        "host_assembled": g.get("host_assembled") is True,
        "zero_fabricated_handles": "read#99" not in cited and cited.issubset(_delivered_handles(cap)),
        "friction_charged": _friction_ge(cap, ctx, 1),
    }
    return _build_outcome(spec, w, cap, checks)


# =============================================================================
# S04 handlers (8 sidecar slots)
# =============================================================================


def _handle_s04_01(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    report_md = (
        f"## Summary\n\n{render_wording('Run() returns true at [read#1:3].', w)}\n"
    )
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and produce a grounded summary report.",
        initial_turns=(_cmd_read("service.go"), ScriptedTurn(content=report_md)),
        max_repairs=1,
    )
    g = cap.last_grounding
    checks = {
        "traced": g.get("traced") is True,
        "not_host_assembled": g.get("host_assembled") is not True,
        "cited_read1": "read#1" in _cited_handles(cap),
        "zero_retry": int(g.get("retry_count") or 0) == 0,
        "zero_friction": _friction_eq(cap, ctx, 0),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s04_02(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    fenced = f"```markdown\n{render_wording('Run() returns true at [read#1:3].', w)}\n```"
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and cite line 3.",
        initial_turns=(_cmd_read("service.go"), ScriptedTurn(content=fenced)),
        max_repairs=1,
    )
    g = cap.last_grounding
    content = str(cap.last_closeout.get("content") or "").strip()
    checks = {
        "traced": g.get("traced") is True,
        "sole_markdown_fence_unwrapped": not content.startswith("```markdown")
        and not content.startswith("```md"),
        "cited_read1": "read#1" in _cited_handles(cap),
        "zero_friction": _friction_eq(cap, ctx, 0),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s04_03(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    bare_json = json.dumps({"synthesis": render_wording("Run() returns true at [read#1:3].", w)})
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and cite line 3.",
        initial_turns=(_cmd_read("service.go"), ScriptedTurn(content=bare_json)),
        repair_fn=lambda _rp, _idx, _st: ScriptedTurn(
            content=render_wording("Run() returns true at [read#1:3].", w)
        ),
        max_repairs=3,
    )
    g = cap.last_grounding
    checks = {
        "returned_no_answer": "COORDINATOR_CLOSEOUT_NO_ANSWER" in cap.typed_codes,
        "settled_after_repair": g.get("traced") is True and "read#1" in _cited_handles(cap),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s04_04(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    # A closeout containing only a citation (no answer prose) has last_assistant != ""
    # and draft.HasAnswer() == false, triggering COORDINATOR_CLOSEOUT_NO_ANSWER.
    # (Whitespace-only content is rejected earlier in RoutingClient as ProviderEmptyCompletionError.)
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and cite line 3.",
        initial_turns=(_cmd_read("service.go"), ScriptedTurn(content="[read#1:3]")),
        repair_fn=lambda _rp, _idx, _st: ScriptedTurn(
            content=render_wording("Run() returns true at [read#1:3].", w)
        ),
        max_repairs=3,
    )
    g = cap.last_grounding
    checks = {
        "returned_no_answer": "COORDINATOR_CLOSEOUT_NO_ANSWER" in cap.typed_codes,
        "settled_after_repair": g.get("traced") is True and "read#1" in _cited_handles(cap),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s04_05(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    # Trailing JSON fence on a non-report-phase closeout stays literal (closeout_read.go:15-16);
    # if refused with REPORT_FENCE_UNREADABLE, repair strips it.
    body = (
        f"{render_wording('Run() returns true at [read#1:3].', w)}\n\n"
        '```json\n{"unknown_field": true}\n```'
    )
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and cite line 3.",
        initial_turns=(_cmd_read("service.go"), ScriptedTurn(content=body)),
        repair_fn=lambda _rp, _idx, _st: ScriptedTurn(
            content=render_wording("Run() returns true at [read#1:3].", w)
        ),
        max_repairs=3,
    )
    g = cap.last_grounding
    checks = {
        "traced": g.get("traced") is True,
        "cited_read1": "read#1" in _cited_handles(cap),
        "no_crash_or_fabrication": g.get("host_assembled") is not True,
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s04_06(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and cite line 3.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(content=render_wording("Run() returns true at [read#99:3].", w)),
        ),
        repair_fn=lambda rp, _idx, _st: ScriptedTurn(
            content=f"[cite: {_extract_first_repair_id(rp)} => read#1:3]"
        ),
        max_repairs=3,
    )
    offered_lists = cap.extra.get("offered_tools_by_repair") or []
    mid_repair_had_tools = bool(offered_lists) and len(offered_lists[0]) > 0
    g = cap.last_grounding
    checks = {
        "repair_turn_preserved_tools": mid_repair_had_tools,
        "settled_traced": g.get("traced") is True and "read#1" in _cited_handles(cap),
    }
    return _build_outcome(spec, w, cap, checks, extra_sig={"mid_repair_had_tools": mid_repair_had_tools})


def _handle_s04_07(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and produce a report.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(
                content=f"## Report\n\n{render_wording('Run() returns true at [read#99:3].', w)}"
            ),
        ),
        repair_fn=lambda rp, _idx, _st: ScriptedTurn(
            content=f"[cite: {_extract_first_repair_id(rp)} => read#1:3]"
        ),
        max_repairs=3,
    )
    g = cap.last_grounding
    checks = {
        "triggered_repair_loop": len(cap.raw_pending_requests) >= 3,
        "settled_traced": g.get("traced") is True and "read#1" in _cited_handles(cap),
        "friction_charged": _friction_ge(cap, ctx, 1),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s04_08(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    bad_report = f"## Report\n\n{render_wording('Run() returns true at [read#99:3].', w)}"
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and produce a report.",
        initial_turns=(_cmd_read("service.go"), ScriptedTurn(content=bad_report)),
        repair_fn=lambda _rp, _idx, _st: ScriptedTurn(content=bad_report),
        max_repairs=6,
    )
    g = cap.last_grounding
    # Never accepted cleanly without defects/host_assembled fallback
    never_cleanly_accepted = (
        g.get("host_assembled") is True
        or bool(cap.completion_reports and any(r.get("completion_report", {}).get("defects") for r in cap.completion_reports))
    )
    checks = {
        "exhausted_never_cleanly_accepted": never_cleanly_accepted,
        "no_fabricated_read99": "read#99" not in _cited_handles(cap),
        "friction_charged": _friction_ge(cap, ctx, 1),
    }
    return _build_outcome(spec, w, cap, checks)


# =============================================================================
# S05 handlers (6 sidecar slots)
# =============================================================================


def _handle_s05_01(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and report line 3.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(content=render_wording("Run() returns true at [read#1:3].", w)),
        ),
        max_repairs=1,
    )
    g = cap.last_grounding
    checks = {
        "traced": g.get("traced") is True,
        "zero_retry": int(g.get("retry_count") or 0) == 0,
        "zero_friction": _friction_eq(cap, ctx, 0),
    }
    return _build_outcome(spec, w, cap, checks)


def _run_s05_presentation_repair(
    ctx: RunnerContext, spec: GB05SlotSpec, w: str
) -> DynamicSessionCapture:
    leaked = render_wording(
        "Run() returns true at [read#1:3]. ![chart](https://unobserved.example.test/chart.png)",
        w,
    )
    clean = render_wording("Run() returns true at [read#1:3].", w)
    return _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and report line 3.",
        initial_turns=(_cmd_read("service.go"), ScriptedTurn(content=leaked)),
        repair_fn=lambda _rp, _idx, _st: ScriptedTurn(content=clean),
        max_repairs=3,
    )


def _handle_s05_02(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    cap = _run_s05_presentation_repair(ctx, spec, w)
    g = cap.last_grounding
    content = str(cap.last_closeout.get("content") or "")
    checks = {
        "presentation_repair_triggered": len(cap.raw_pending_requests) >= 3,
        "leaked_embed_removed": "unobserved.example.test" not in content,
        "settled_traced": g.get("traced") is True,
        "zero_friction_charged": _friction_eq(cap, ctx, 0),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s05_03(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    cap = _run_s05_presentation_repair(ctx, spec, w)
    g = cap.last_grounding
    checks = {
        "presentation_repair_triggered": len(cap.raw_pending_requests) >= 3,
        "citations_preserved": "read#1" in _cited_handles(cap),
        "settled_traced": g.get("traced") is True,
        "zero_friction_charged": _friction_eq(cap, ctx, 0),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s05_04(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    cap = _run_s05_presentation_repair(ctx, spec, w)
    g = cap.last_grounding
    checks = {
        "retry_spent": int(g.get("retry_count") or 0) >= 1 or len(cap.raw_pending_requests) >= 3,
        "zero_friction_charged": _friction_eq(cap, ctx, 0),
        "settled_traced": g.get("traced") is True,
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s05_05(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    cap = _run_s05_presentation_repair(ctx, spec, w)
    content = str(cap.last_closeout.get("content") or "")
    checks = {
        "no_synthetic_markdown_headers": "\n# " not in content and not content.startswith("# "),
        "no_synthetic_code_fences": "```" not in content,
        "zero_friction_charged": _friction_eq(cap, ctx, 0),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s05_06(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    leaked = render_wording(
        "Run() returns true at [read#1:3]. ![chart](https://unobserved.example.test/chart.png)",
        w,
    )
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and report line 3.",
        initial_turns=(_cmd_read("service.go"), ScriptedTurn(content=leaked)),
        repair_fn=lambda _rp, _idx, _st: ScriptedTurn(content=leaked),
        max_repairs=6,
    )
    g = cap.last_grounding
    content = str(cap.last_closeout.get("content") or "")
    checks = {
        "settled_host_assembled": g.get("host_assembled") is True,
        "unobserved_embed_stripped": "unobserved.example.test" not in content,
        "zero_friction_charged": _friction_eq(cap, ctx, 0),
    }
    return _build_outcome(spec, w, cap, checks)


# =============================================================================
# S06 handlers (13 sidecar slots)
# =============================================================================


def _handle_s06_01(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go, post a grounded status update, then read helper.go.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(
                content=render_wording("Found Run() at [read#1:3]; checking helper next.", w),
                tool_calls=({"name": "command", "args": {"command": "cat -n helper.go"}},),
            ),
            ScriptedTurn(
                content=render_wording("Verified [read#1:3] and [read#2:3].", w),
            ),
        ),
        max_repairs=2,
    )
    checks = {
        "note_published": len(cap.agent_notes) >= 1,
        "paired_tool_executed": "read#2" in _delivered_handles(cap),
        "zero_friction": _friction_eq(cap, ctx, 0),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s06_02(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go, then check helper.go.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(
                content=render_wording("Checking [read#99:1] before helper.", w),
                tool_calls=({"name": "command", "args": {"command": "cat -n helper.go"}},),
            ),
            ScriptedTurn(content=render_wording("Verified [read#1:3] and [read#2:3].", w)),
        ),
        max_repairs=2,
    )
    checks = {
        "note_withheld": len(cap.agent_notes) == 0,
        "typed_midturn_code": "MID_TURN_NOTE_HANDLE_UNGROUND" in cap.mid_turn_codes
        or any(c in VALID_MID_TURN_NOTE_CODES for c in cap.mid_turn_codes),
        "tools_still_ran": "read#2" in _delivered_handles(cap),
        "friction_charged": _friction_ge(cap, ctx, 1),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s06_03(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Inspect service.go.",
        initial_turns=(
            ScriptedTurn(
                content=render_wording("Checking https://unobserved.example.test/x before service.go.", w),
                tool_calls=({"name": "command", "args": {"command": "cat -n service.go"}},),
            ),
            ScriptedTurn(content=render_wording("Verified [read#1:3].", w)),
        ),
        max_repairs=2,
    )
    checks = {
        "note_withheld": len(cap.agent_notes) == 0,
        "typed_midturn_url_code": any(c in VALID_MID_TURN_NOTE_CODES for c in cap.mid_turn_codes),
        "tools_still_ran": "read#1" in _delivered_handles(cap),
        "friction_charged": _friction_ge(cap, ctx, 1),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s06_04(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Inspect service.go.",
        initial_turns=(
            ScriptedTurn(
                content=render_wording("Starting inspection of service.go.", w),
                tool_calls=({"name": "command", "args": {"command": "cat -n service.go"}},),
            ),
            ScriptedTurn(content=render_wording("Verified [read#1:3].", w)),
        ),
        max_repairs=2,
    )
    checks = {
        "brief_note_published": len(cap.agent_notes) >= 1,
        "tools_still_ran": "read#1" in _delivered_handles(cap),
        "zero_friction": _friction_eq(cap, ctx, 0),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s06_05(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Note workspace status and read service.go.",
        initial_turns=(
            ScriptedTurn(
                content=render_wording("Noted brief row; inspecting service.go.", w),
                tool_calls=({"name": "command", "args": {"command": "cat -n service.go"}},),
            ),
            ScriptedTurn(content=render_wording("Verified [read#1:3].", w)),
        ),
        max_repairs=2,
    )
    checks = {
        "tools_ran": "read#1" in _delivered_handles(cap),
        "zero_friction": _friction_eq(cap, ctx, 0),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s06_06(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go then helper.go.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(
                content=render_wording("Claiming line proof from [source_change#1:1].", w),
                tool_calls=({"name": "command", "args": {"command": "cat -n helper.go"}},),
            ),
            ScriptedTurn(content=render_wording("Verified [read#1:3] and [read#2:3].", w)),
        ),
        max_repairs=2,
    )
    checks = {
        "source_change_line_note_withheld": len(cap.agent_notes) == 0,
        "typed_midturn_code": any(c in VALID_MID_TURN_NOTE_CODES for c in cap.mid_turn_codes),
        "tools_still_ran": "read#2" in _delivered_handles(cap),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s06_07(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and test recall on source_change#1.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(
                tool_calls=({"name": "recall", "args": {"handle": "source_change#1"}},),
            ),
            ScriptedTurn(content=render_wording("Verified [read#1:3].", w)),
        ),
        max_repairs=2,
    )
    recall_bodies = [
        m.get("delivered_body", "")
        for m in cap.delivered_tool_messages
        if m.get("tool") == "recall"
    ]
    no_source_change_body = all("func Run()" not in b for b in recall_bodies)
    checks = {
        "source_change_not_recalled": no_source_change_body,
        "settled_traced": cap.last_grounding.get("traced") is True,
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s06_08(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    note_phrase = render_wording("Found Run() at [read#1:3]; checking helper.", w)
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go, post a mid-turn note, then read helper.go.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(
                content=note_phrase,
                tool_calls=({"name": "command", "args": {"command": "cat -n helper.go"}},),
            ),
            ScriptedTurn(content=render_wording("Verified [read#1:3] and [read#2:3].", w)),
        ),
        max_repairs=2,
    )
    # Check Turn 3's model-visible pending messages do NOT contain two copies of the derived note
    last_pending_msgs = cap.raw_pending_requests[-1].get("messages") or [] if cap.raw_pending_requests else []
    model_copies = sum(
        1
        for m in last_pending_msgs
        if isinstance(m, Mapping) and "checking helper" in str(m.get("content") or "")
    )
    checks = {
        "single_agent_note_persisted": len(cap.agent_notes) == 1,
        "no_second_model_visible_copy": model_copies <= 1,
        "zero_friction": _friction_eq(cap, ctx, 0),
    }
    return _build_outcome(spec, w, cap, checks, extra_sig={"model_copies": model_copies})


def _handle_s06_09(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    secret_phrase = "WITHHELD_MARKER_S06_09"
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go then helper.go.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(
                content=render_wording(f"{secret_phrase} citing [read#99:1].", w),
                tool_calls=({"name": "command", "args": {"command": "cat -n helper.go"}},),
            ),
            ScriptedTurn(content=render_wording("Verified [read#1:3] and [read#2:3].", w)),
        ),
        max_repairs=2,
    )
    in_agent_notes = any(secret_phrase in str(n.get("content") or "") for n in cap.agent_notes)
    in_user_transcript_notes = any(
        isinstance(m, Mapping)
        and m.get("kind") == "agent_note"
        and secret_phrase in str(m.get("content") or "")
        for m in cap.user_visible_messages
    )
    checks = {
        "absent_from_agent_notes": not in_agent_notes,
        "absent_from_user_view": not in_user_transcript_notes,
        "typed_code_in_audit": any(c in VALID_MID_TURN_NOTE_CODES for c in cap.mid_turn_codes),
        "tools_still_ran": "read#2" in _delivered_handles(cap),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s06_10(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go, helper.go, and extra.go.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(
                content=render_wording("Bad ref [read#99:1] before helper.", w),
                tool_calls=({"name": "command", "args": {"command": "cat -n helper.go"}},),
            ),
            ScriptedTurn(
                content=render_wording("Good ref [read#2:3] before extra.", w),
                tool_calls=({"name": "command", "args": {"command": "cat -n extra.go"}},),
            ),
            ScriptedTurn(
                content=render_wording("Verified [read#1:3], [read#2:3], [read#3:3].", w)
            ),
        ),
        max_repairs=2,
    )
    delivered = _delivered_handles(cap)
    checks = {
        "withheld_batch_tool_ran": "read#2" in delivered,
        "published_batch_tool_ran": "read#3" in delivered,
        "one_published_note": len(cap.agent_notes) == 1,
        "one_withheld_code": any(c in VALID_MID_TURN_NOTE_CODES for c in cap.mid_turn_codes),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s06_11(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go then helper.go.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(
                content=render_wording("Now checking helper file without citing a handle.", w),
                tool_calls=({"name": "command", "args": {"command": "cat -n helper.go"}},),
            ),
            ScriptedTurn(content=render_wording("Verified [read#1:3] and [read#2:3].", w)),
        ),
        max_repairs=2,
    )
    checks = {
        "note_withheld": len(cap.agent_notes) == 0,
        "typed_non_handle_code": "MID_TURN_NOTE_UNGROUNDED" in cap.mid_turn_codes,
        "tools_still_ran": "read#2" in _delivered_handles(cap),
        "zero_friction_for_non_handle": _friction_eq(cap, ctx, 0),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s06_12(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go, helper.go, and extra.go with mid-turn updates.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(
                content=render_wording("Row 1 grounded [read#1:3].", w),
                tool_calls=({"name": "command", "args": {"command": "cat -n helper.go"}},),
            ),
            ScriptedTurn(
                content=render_wording("Row 2 ungrounded [read#99:1].", w),
                tool_calls=({"name": "command", "args": {"command": "cat -n extra.go"}},),
            ),
            ScriptedTurn(
                content=render_wording("Verified [read#1:3], [read#2:3], [read#3:3].", w)
            ),
        ),
        max_repairs=2,
    )
    checks = {
        "row1_published": len(cap.agent_notes) == 1,
        "row2_withheld_with_code": "MID_TURN_NOTE_HANDLE_UNGROUND" in cap.mid_turn_codes,
        "all_tools_ran": {"read#1", "read#2", "read#3"}.issubset(_delivered_handles(cap)),
        "exact_one_friction_unit": _friction_eq(cap, ctx, 1),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s06_13(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    # Decision 008/014: compare note citing a qualifying delivery already visible in the intent
    # ([read#1:3]) vs citing the carrier's own pending delivery ([read#3:3] before extra.go runs).
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go, then helper.go, then extra.go.",
        initial_turns=(
            _cmd_read("service.go"),
            # Pre-batch visible delivery [read#1:3] -> MUST publish with 0 friction
            ScriptedTurn(
                content=render_wording("Pre-batch visible [read#1:3] before helper.", w),
                tool_calls=({"name": "command", "args": {"command": "cat -n helper.go"}},),
            ),
            # Carrier's own pending delivery [read#3:3] (not yet executed!) -> MUST withhold + charge friction
            ScriptedTurn(
                content=render_wording("Pending delivery [read#3:3] before extra.", w),
                tool_calls=({"name": "command", "args": {"command": "cat -n extra.go"}},),
            ),
            ScriptedTurn(
                content=render_wording("Verified [read#1:3], [read#2:3], [read#3:3].", w)
            ),
        ),
        max_repairs=2,
    )
    checks = {
        "visible_prebatch_note_published": len(cap.agent_notes) == 1,
        "pending_delivery_note_withheld": "MID_TURN_NOTE_HANDLE_UNGROUND" in cap.mid_turn_codes,
        "pending_tool_still_executed": "read#3" in _delivered_handles(cap),
        "exact_one_friction_unit": _friction_eq(cap, ctx, 1),
    }
    return _build_outcome(spec, w, cap, checks)


# =============================================================================
# S07 handlers (4 sidecar slots)
# =============================================================================


def _handle_s07_01(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and cite line 3.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(content=render_wording("Run() returns true at [read#1:3].", w)),
        ),
        max_repairs=1,
    )
    g = cap.last_grounding
    checks = {
        "traced": g.get("traced") is True,
        "cited_read1": "read#1" in _cited_handles(cap),
        "zero_friction": _friction_eq(cap, ctx, 0),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s07_02(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    sid = ctx.new_session()
    d10_before = probe_store_db(ctx.sidecar.store_db_path, sid, trace_log_path=ctx.trace_log)
    p = ctx.client.send_prompt(sid, "Dispatch a repo researcher to inspect service.go and report.")
    sub_id = str(p.get("submission_id") or p.get("id") or "")

    def _send_tool(name: str, args: dict[str, Any]) -> None:
        r = ctx.client.wait_pending(sid, timeout=ctx.poll_timeout)
        if name not in (r.get("tools") or []):
            ctx.client.respond(
                r["id"],
                tool_calls=[{"id": str(uuid.uuid4()), "name": "request_tools", "args": {"need": name}}],
            )
            r = ctx.client.wait_pending(sid, timeout=ctx.poll_timeout)
        ctx.client.respond(r["id"], tool_calls=[{"id": str(uuid.uuid4()), "name": name, "args": args}])

    _send_tool("update_progress", {"content": "## Progress\n- [ ] Survey service.go"})
    _send_tool(
        "task",
        {
            "agent_type": "repo-researcher",
            "brief": {"goal": "Survey service.go", "done_when": ["Report line 3."]},
            "scope": {"mode": "read", "paths": ["service.go"]},
        },
    )

    deadline = time.monotonic() + ctx.poll_timeout
    child_sid = ""
    child_req = None
    while time.monotonic() < deadline:
        pend = ctx.client.request("/harness/llm/pending")
        if isinstance(pend, dict) and pend.get("pending"):
            psid = str(pend.get("session_id") or "")
            if psid == sid:
                ctx.client.respond(
                    pend["id"],
                    tool_calls=[
                        {
                            "id": str(uuid.uuid4()),
                            "name": "wait",
                            "args": {"timeout_ms": 15000, "conditions": [{"kind": "next_worker_done"}]},
                        }
                    ],
                )
            elif psid and psid not in ctx.known_sessions:
                child_sid = psid
                ctx.known_sessions.add(child_sid)
                child_req = pend
                break
        time.sleep(0.1)
    if not child_sid or child_req is None:
        raise RuntimeError("S07-02 worker child session never became pending")

    ctx.client.respond(
        child_req["id"],
        tool_calls=[{"id": str(uuid.uuid4()), "name": "command", "args": {"command": "cat -n service.go"}}],
    )
    child_req2 = ctx.client.wait_pending(child_sid, timeout=ctx.poll_timeout)
    ctx.client.respond(
        child_req2["id"],
        tool_calls=[
            {
                "id": str(uuid.uuid4()),
                "name": "complete_leg",
                "args": {
                    "leg_status": "complete",
                    "brief": "Surveyed service.go.",
                    "findings": [
                        {"path": "service.go", "line": 3, "evidence": "read#1", "note": "Run returns true."}
                    ],
                },
            }
        ],
    )

    # Wait for root to resume and extract leg-namespaced handle (e.g. <child_sid>:read#1)
    root_resume = ctx.client.wait_pending(sid, timeout=ctx.poll_timeout)
    raw_pending: list[dict[str, Any]] = [dict(root_resume)]
    resume_blob = json.dumps(root_resume)
    leg_match = re.search(r"([a-zA-Z0-9_-]+:read#1)", resume_blob)
    if leg_match:
        cite_tok = f"[{leg_match.group(1)}:3]"
    elif child_sid:
        cite_tok = f"[{child_sid}:read#1:3]"
    else:
        cite_tok = "[service.go:3]"
    # Mark progress checklist complete before closeout so ProgressDelay does not block closeout.
    ctx.client.respond(
        root_resume["id"],
        tool_calls=[
            {
                "id": str(uuid.uuid4()),
                "name": "update_progress",
                "args": _build_complete_progress_args(
                    root_resume, "## Progress\n- [x] Survey service.go"
                ),
            }
        ],
    )
    root_closeout = ctx.client.wait_pending(sid, timeout=ctx.poll_timeout)
    raw_pending.append(dict(root_closeout))
    ctx.client.respond(
        root_closeout["id"],
        content=render_wording(f"Worker verified Run() at {cite_tok}.", w),
    )
    settle_deadline = time.monotonic() + ctx.poll_timeout
    extra_turns = 0
    settled = False
    while time.monotonic() < settle_deadline:
        st_resp = ctx.client.request(f"/v1/sessions/{sid}", missing=True)
        if isinstance(st_resp, dict) and st_resp.get("status") == "idle":
            settled = True
            break
        if extra_turns < 3:
            rp = ctx.client.get_pending(sid)
            if rp is not None:
                raw_pending.append(dict(rp))
                ctx.client.respond(
                    rp["id"],
                    content=render_wording(f"Worker verified Run() at {cite_tok}.", w),
                )
                extra_turns += 1
                continue
        time.sleep(0.05)
    if not settled:
        raise TimeoutError(f"S07-02 timed out waiting for session {sid} to settle")

    d10_after = probe_store_db(ctx.sidecar.store_db_path, sid, trace_log_path=ctx.trace_log)
    d10_delta = d10_after.delta_observed(d10_before)
    prod_recs, prod_closeouts = extract_product_observations(ctx.sidecar.store_db_path, sid)
    notes, reports, db_codes = _inspect_session_db_extras(ctx.sidecar.store_db_path, sid)
    cap = DynamicSessionCapture(
        session_id=sid,
        raw_pending_requests=raw_pending,
        scripted_responses=[],
        delivered_tool_messages=[],
        delivery_log=DeliveryLog(records=()),
        product_records=prod_recs,
        product_closeouts=prod_closeouts,
        agent_notes=notes,
        user_visible_messages=[],
        completion_reports=reports,
        typed_codes=db_codes,
        mid_turn_codes=[c for c in db_codes if c.startswith("MID_TURN_NOTE_")],
        d10_before=d10_before,
        d10_after=d10_after,
        d10_delta=d10_delta,
        extra={"child_session_id": child_sid, "cited_token": cite_tok},
    )
    g = cap.last_grounding
    checks = {
        "traced": g.get("traced") is True,
        "not_host_assembled": g.get("host_assembled") is not True,
        "cited_evidence_nonempty": len(g.get("cited_evidence") or []) >= 1,
        "zero_friction": _friction_eq(cap, ctx, 0),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s07_03(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    # Session A reads service.go (read#1) and helper.go (read#2)
    _drive_dynamic_session(
        ctx,
        selector=f"{spec.selector}__other_chat",
        wording=w,
        prompt_text="Read service.go and helper.go.",
        initial_turns=(
            _cmd_read("service.go"),
            _cmd_read("helper.go"),
            ScriptedTurn(content="Observed [read#1:3] and [read#2:3]."),
        ),
        max_repairs=1,
    )
    # Session B reads ONLY service.go (read#1), then attempts to cite Session A's [read#2:3]
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and report.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(content=render_wording("Claiming cross-chat [read#2:3].", w)),
        ),
        repair_fn=lambda rp, _idx, _st: ScriptedTurn(
            content=f"[cite: {_extract_first_repair_id(rp)} => read#1:3]"
        ),
        max_repairs=3,
    )
    g = cap.last_grounding
    checks = {
        "cross_chat_refused": len(cap.raw_pending_requests) >= 3
        and any(c in CITATION_REFUSAL_CODES for c in cap.typed_codes),
        "read2_not_grounded": "read#2" not in _cited_handles(cap),
        "repaired_with_read1": g.get("traced") is True and "read#1" in _cited_handles(cap),
        "friction_charged": _friction_ge(cap, ctx, 1),
    }
    return _build_outcome(spec, w, cap, checks)


def _handle_s07_04(ctx: RunnerContext, spec: GB05SlotSpec, w: str) -> SlotOutcome:
    # Workspace has unread.go on disk, but session only reads service.go
    cap = _drive_dynamic_session(
        ctx,
        selector=spec.selector,
        wording=w,
        prompt_text="Read service.go and report.",
        initial_turns=(
            _cmd_read("service.go"),
            ScriptedTurn(content=render_wording("Claiming unobserved [unread.go:3].", w)),
        ),
        repair_fn=lambda rp, _idx, _st: ScriptedTurn(
            content=f"[cite: {_extract_first_repair_id(rp)} => read#1:3]"
        ),
        max_repairs=3,
    )
    g = cap.last_grounding
    cited_paths = {
        str(c.get("path") or "")
        for c in (g.get("cited_evidence") or [])
        if isinstance(c, Mapping)
    }
    checks = {
        "unobserved_file_refused": len(cap.raw_pending_requests) >= 3
        and any(c in CITATION_REFUSAL_CODES for c in cap.typed_codes),
        "unread_not_grounded": "unread.go" not in cited_paths,
        "repaired_with_read1": g.get("traced") is True and "read#1" in _cited_handles(cap),
        "friction_charged": _friction_ge(cap, ctx, 1),
    }
    return _build_outcome(spec, w, cap, checks)


# =============================================================================
# Explicit 49-slot handler registry (100% of GB05 sidecar slots)
# =============================================================================

SlotHandler = Callable[[RunnerContext, GB05SlotSpec, str], SlotOutcome]

SIDECAR_SLOT_HANDLERS: dict[str, SlotHandler] = {
    # S01 (5)
    "S01-01-exempt-no-citable-observations": _handle_s01_01,
    "S01-02-required-after-citable-read": _handle_s01_02,
    "S01-03-supported-citation-settles": _handle_s01_03,
    "S01-04-source-change-creates-no-duty": _handle_s01_04,
    "S01-05-unreadable-ledger-denies-exemption": _handle_s01_05,
    # S02 (4)
    "S02-01-host-attached-in-delivery-log": _handle_s02_01,
    "S02-02-empty-ledger-attaches-nothing": _handle_s02_02,
    "S02-03-host-attached-labeled-distinct": _handle_s02_03,
    "S02-04-host-attachment-never-invents-ranges": _handle_s02_04,
    # S03 (9)
    "S03-01-valid-citation-repair-replaces-offender": _handle_s03_01,
    "S03-02-invalid-repair-missing-handle": _handle_s03_02,
    "S03-03-invalid-repair-malformed-patch": _handle_s03_03,
    "S03-04-invalid-repair-wrong-target-span": _handle_s03_04,
    "S03-05-operational-batch-retires-pinned-draft": _handle_s03_05,
    "S03-06-terminal-worker-card-retires-draft": _handle_s03_06,
    "S03-07-typed-coordinator-wake-retires-draft": _handle_s03_07,
    "S03-08-denied-batch-admission-retires-draft": _handle_s03_08,
    "S03-09-retry-exhaustion-host-assembles": _handle_s03_09,
    # S04 (8)
    "S04-01-valid-report-document-accepted": _handle_s04_01,
    "S04-02-sole-markdown-fence-unwrapped": _handle_s04_02,
    "S04-03-bare-json-returns-no-answer": _handle_s04_03,
    "S04-04-empty-closeout-returns-no-answer": _handle_s04_04,
    "S04-05-strict-trailing-report-json-fence": _handle_s04_05,
    "S04-06-repair-preserves-tool-availability": _handle_s04_06,
    "S04-07-ungrounded-report-triggers-doc-repair": _handle_s04_07,
    "S04-08-exhausted-report-stored-with-defects": _handle_s04_08,
    # S05 (6)
    "S05-01-clean-presentation-passes": _handle_s05_01,
    "S05-02-presentation-repair-fixes-leaked-directive": _handle_s05_02,
    "S05-03-presentation-repair-preserves-citations": _handle_s05_03,
    "S05-04-presentation-repair-does-not-charge-friction": _handle_s05_04,
    "S05-05-presentation-cleanup-no-markdown-structure": _handle_s05_05,
    "S05-06-presentation-repair-exhaustion": _handle_s05_06,
    # S06 (13 sidecar)
    "S06-01-midturn-supported-note-published": _handle_s06_01,
    "S06-02-midturn-unresolved-handle-withheld-charges-friction": _handle_s06_02,
    "S06-03-midturn-unresolved-url-withheld-charges-friction": _handle_s06_03,
    "S06-04-midturn-brief-only-status-no-friction": _handle_s06_04,
    "S06-05-midturn-source-change-proves-brief-row": _handle_s06_05,
    "S06-06-midturn-source-change-no-line-or-excerpt": _handle_s06_06,
    "S06-07-midturn-source-change-not-recallable": _handle_s06_07,
    "S06-08-midturn-derived-user-note-no-second-copy": _handle_s06_08,
    "S06-09-midturn-withheld-note-absent-from-user-view": _handle_s06_09,
    "S06-10-midturn-tools-always-execute": _handle_s06_10,
    "S06-11-midturn-non-handle-warning-no-friction": _handle_s06_11,
    "S06-12-midturn-multi-row-mixed-outcomes": _handle_s06_12,
    "S06-13-historical-unknown-intent-boundary": _handle_s06_13,
    # S07 (4)
    "S07-01-closeout-cites-current-intent-reads": _handle_s07_01,
    "S07-02-closeout-cites-completed-worker-leg": _handle_s07_02,
    "S07-03-closeout-refuses-cross-chat-handle": _handle_s07_03,
    "S07-04-closeout-refuses-unobserved-working-tree": _handle_s07_04,
}


def select_sidecar_runs(
    *,
    families: Sequence[str] | None = None,
    wordings: Sequence[str] | None = None,
    shard: int = 0,
    shards: int = 1,
) -> list[tuple[GB05SlotSpec, str]]:
    """Deterministically select and shard sidecar (slot, wording) runs with zero duplicates.

    Shards by slot index so all 3 D5 wordings of a slot stay in the same shard,
    allowing per-shard D5 wording invariance checks while guaranteeing that the
    union across `shard in range(shards)` equals all 147 sidecar runs disjointly.
    """
    if shards < 1:
        raise ValueError(f"shards must be >= 1, got {shards}")
    if shard < 0 or shard >= shards:
        raise ValueError(f"shard must satisfy 0 <= shard < shards ({shards}), got {shard}")

    fam_filter = tuple(families) if families else GB05_FAMILY_IDS
    for f in fam_filter:
        if f not in GB05_FAMILY_IDS:
            raise ValueError(f"Unknown GB05 family {f!r}; expected subset of {GB05_FAMILY_IDS}")

    wording_filter = tuple(wordings) if wordings else WORDING_VARIANTS
    for w in wording_filter:
        if w not in WORDING_VARIANTS:
            raise ValueError(f"Unknown D5 wording {w!r}; expected subset of {WORDING_VARIANTS}")

    sidecar_slots = [
        s for s in ALL_GB05_SLOTS if s.lane == "sidecar" and s.family_id in fam_filter
    ]
    selected: list[tuple[GB05SlotSpec, str]] = []
    for slot_idx, spec in enumerate(sidecar_slots):
        if slot_idx % shards != shard:
            continue
        for w in wording_filter:
            selected.append((spec, w))
    return selected


def run_gb05_inprocess(
    *,
    product_root: str | pathlib.Path,
    out_dir: pathlib.Path,
    dry_run: bool = False,
    timeout: float = 300.0,
) -> dict[str, Any]:
    """Execute the 2 GB05 in-process crash slots via test-only Go `-overlay` against D1."""
    out_dir.mkdir(parents=True, exist_ok=True)
    prod = pathlib.Path(product_root).resolve()
    grounding_pkg_dir = pathlib.Path(__file__).resolve().parents[2]
    src = (grounding_pkg_dir / OVERLAY_SOURCE_REL).resolve()
    if not src.is_file():
        raise FileNotFoundError(f"GB05 Go overlay source not found: {src}")
    target = prod / OVERLAY_TARGET_REL

    with tempfile.TemporaryDirectory(prefix="gb05-inproc-") as td:
        overlay_path = write_test_overlay(
            pathlib.Path(td) / "gb05_overlay.json",
            {str(target): str(src)},
            product_root=prod,
        )
        plan = InProcessPlan(
            product_root=prod,
            packages=("./internal/coordinator/promptloop",),
            run_pattern="TestGB05InProcessMidTurnCrash",
            overlay_path=overlay_path,
            count=1,
        )
        res = run_inprocess_plan(plan, dry_run=dry_run, timeout=timeout)

    subtests: dict[str, str] = {}
    for line in str(res.get("stdout") or "").splitlines():
        try:
            ev = json.loads(line)
        except (TypeError, ValueError):
            continue
        if isinstance(ev, Mapping) and ev.get("Action") in ("pass", "fail") and ev.get("Test"):
            subtests[str(ev["Test"])] = str(ev["Action"])

    report = {
        "card": "GB05",
        "lane": "in_process",
        "slots": list(INPROCESS_SLOTS),
        "passed": res["returncode"] == 0,
        "returncode": res["returncode"],
        "verified": res.get("verified"),
        "command": res.get("command"),
        "subtests": subtests,
        "stderr": res.get("stderr", ""),
    }
    (out_dir / "gb05-inprocess-summary.json").write_text(
        json.dumps(report, indent=2, sort_keys=True), encoding="utf-8"
    )
    return report


def run_gb05_full(
    *,
    engine_bin: str,
    product_root: str,
    expected_engine_sha256: str,
    out_dir: pathlib.Path,
    run_id: str,
    families: Sequence[str] | None = None,
    wordings: Sequence[str] | None = None,
    shard: int = 0,
    shards: int = 1,
    trace_friction: bool = True,
    include_inprocess: bool = False,
    poll_timeout: float = 90.0,
) -> dict[str, Any]:
    out_dir.mkdir(parents=True, exist_ok=True)
    selected_runs = select_sidecar_runs(
        families=families, wordings=wordings, shard=shard, shards=shards
    )

    workspace = out_dir / "workspace"
    config_dir = out_dir / "config"
    _init_gb05_workspace(workspace)

    sidecar = IsolatedSidecarProcess(
        engine_bin,
        product_root=product_root,
        config_dir=config_dir,
        workspace_dir=workspace,
        expected_sha256=expected_engine_sha256,
        trace_friction=trace_friction,
    )

    slot_results: list[SlotOutcome] = []
    divergences: list[dict[str, Any]] = []

    with sidecar as client:
        manifest = sidecar.build_run_manifest()
        pid = client.create_project(workspace)["id"]
        plat = "macos" if sidecar.provenance.platform == "darwin" else "linux"
        ctx = RunnerContext(
            client=client,
            sidecar=sidecar,
            project_id=pid,
            workspace=workspace,
            out_dir=out_dir,
            manifest=manifest,
            trace_friction=trace_friction,
            poll_timeout=poll_timeout,
        )

        for spec, w in selected_runs:
            handler = SIDECAR_SLOT_HANDLERS[spec.selector]
            try:
                outcome = handler(ctx, spec, w)
            except Exception as exc:
                outcome = SlotOutcome(
                    selector=spec.selector,
                    family_id=spec.family_id,
                    wording=w,
                    passed=False,
                    checks={"no_exception": False},
                    failure_reasons=[f"exception: {type(exc).__name__}: {exc}"],
                    outcome_signature={"error": type(exc).__name__},
                    d10_before={},
                    d10_after={},
                    d10_delta={},
                    summary={"error": str(exc)},
                )
            slot_results.append(outcome)
            if not outcome.passed:
                divergences.append(
                    {
                        "kind": "slot_divergence",
                        "selector": outcome.selector,
                        "family_id": outcome.family_id,
                        "wording": outcome.wording,
                        "failure_reasons": outcome.failure_reasons,
                        "checks": outcome.checks,
                        "d10_delta": outcome.d10_delta,
                        "summary": outcome.summary,
                    }
                )

    # D5 wording invariance check across every selector with all 3 wordings present
    by_selector: dict[str, dict[str, SlotOutcome]] = {}
    for sr in slot_results:
        by_selector.setdefault(sr.selector, {})[sr.wording] = sr

    wording_invariance_failures: list[dict[str, Any]] = []
    for sel, by_w in by_selector.items():
        if all(w in by_w for w in WORDING_VARIANTS):
            base_sig = by_w[WORDING_VARIANTS[0]].outcome_signature
            diffs = {
                w: by_w[w].outcome_signature
                for w in WORDING_VARIANTS[1:]
                if by_w[w].outcome_signature != base_sig
            }
            if diffs:
                entry = {
                    "kind": "d5_wording_divergence",
                    "selector": sel,
                    "family_id": SLOTS_BY_SELECTOR[sel].family_id,
                    "baseline_wording": WORDING_VARIANTS[0],
                    "baseline_signature": base_sig,
                    "divergent_wordings": diffs,
                }
                wording_invariance_failures.append(entry)
                divergences.append(entry)

    # Write JSONL divergences file (always created, even if empty)
    div_path = out_dir / "gb05-sidecar-divergences.jsonl"
    div_path.write_text(
        "".join(json.dumps(d, sort_keys=True) + "\n" for d in divergences),
        encoding="utf-8",
    )

    inprocess_report: dict[str, Any] | None = None
    if include_inprocess:
        inprocess_report = run_gb05_inprocess(product_root=product_root, out_dir=out_dir)

    summary = {
        "card": "GB05",
        "run_id": run_id,
        "platform": plat,
        "trace_friction": trace_friction,
        "families": list(families) if families else list(GB05_FAMILY_IDS),
        "wordings": list(wordings) if wordings else list(WORDING_VARIANTS),
        "shard": shard,
        "shards": shards,
        "run_manifest": manifest.to_dict(),
        "slot_count": len(slot_results),
        "passed_count": sum(1 for r in slot_results if r.passed),
        "divergence_count": len(divergences),
        "wording_invariance_failures": wording_invariance_failures,
        "divergences_file": str(div_path),
        "slots": [r.to_dict() for r in slot_results],
        "inprocess": inprocess_report,
    }
    (out_dir / "gb05-sidecar-summary.json").write_text(
        json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8"
    )
    return summary


def _parse_csv(raw: str) -> list[str] | None:
    items = [x.strip() for x in raw.split(",") if x.strip()]
    return items or None


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(
        description="GB05 full speaking-surfaces runner (49 sidecar x 3 D5 wordings + 2 in-process)"
    )
    ap.add_argument("--engine-bin", default="")
    ap.add_argument("--product-root", required=True)
    ap.add_argument("--expected-engine-sha256", default="")
    ap.add_argument("--out-dir", required=True)
    ap.add_argument("--run-id", default="20261127-3bb1464d-gb05-full")
    ap.add_argument("--families", default="", help="Comma-separated subset of S01..S07")
    ap.add_argument("--wordings", default="", help="Comma-separated subset of D5 wordings")
    ap.add_argument("--shard", type=int, default=0, help="0-based shard index")
    ap.add_argument("--shards", type=int, default=1, help="Total number of shards")
    ap.add_argument("--no-trace-friction", action="store_true")
    ap.add_argument("--include-inprocess", action="store_true")
    ap.add_argument("--inprocess-only", action="store_true")
    ap.add_argument("--poll-timeout", type=float, default=90.0)
    args = ap.parse_args(argv)

    out_dir = pathlib.Path(args.out_dir)
    if args.inprocess_only:
        report = run_gb05_inprocess(product_root=args.product_root, out_dir=out_dir)
        print(json.dumps(report, indent=2, sort_keys=True))
        return 0 if report["passed"] else 1

    if not args.engine_bin:
        ap.error("--engine-bin is required unless --inprocess-only is set")

    summary = run_gb05_full(
        engine_bin=args.engine_bin,
        product_root=args.product_root,
        expected_engine_sha256=args.expected_engine_sha256,
        out_dir=out_dir,
        run_id=args.run_id,
        families=_parse_csv(args.families),
        wordings=_parse_csv(args.wordings),
        shard=args.shard,
        shards=args.shards,
        trace_friction=not args.no_trace_friction,
        include_inprocess=args.include_inprocess,
        poll_timeout=args.poll_timeout,
    )
    print(
        json.dumps(
            {
                "card": summary["card"],
                "run_id": summary["run_id"],
                "platform": summary["platform"],
                "shard": summary["shard"],
                "shards": summary["shards"],
                "slot_count": summary["slot_count"],
                "passed_count": summary["passed_count"],
                "divergence_count": summary["divergence_count"],
                "wording_invariance_failures": len(summary["wording_invariance_failures"]),
                "inprocess_passed": (
                    summary["inprocess"]["passed"] if summary["inprocess"] else None
                ),
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
