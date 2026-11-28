"""Full GB07 execution runner (Freshness, closeout gates, friction: G01..G05).

Covers all 34 fixed GB07 slots x 3 D5 wordings = 102 runs per platform matching `catalog.py`,
`ledger.md`, Decision 002 (COV-07), D8 (in-process block mode), D10 (friction counters),
and Decision 045 (report `friction_units` and `worker_report_refusals` side by side, NEVER summed):
- 28 sidecar slots x 3 D5 wordings = 84 sidecar runs (`G01`, `G02`, `G03`, `G04` shipped, `G05`)
- 6 in-process slots x 3 D5 wordings = 18 in-process subtests (`G04-06`..`G04-11`) via test-only Go `-overlay`

Outputs:
- `gb07-sidecar-summary.json`
- `gb07-sidecar-divergences.jsonl`
- `gb07-inprocess-summary.json` (when in-process lane runs)
"""
from __future__ import annotations

import argparse
import contextlib
from dataclasses import dataclass
import hashlib
import json
import pathlib
import re
import sqlite3
import subprocess
import sys
import tempfile
import time
from typing import Any, Mapping, Sequence
import uuid

from grounding.friction_probe import (
    WorkerReportRefusalSummary,
    probe_store_db,
    probe_worker_report_refusals,
)
from grounding.inprocess_driver import (
    InProcessPlan,
    run_inprocess_plan,
    validate_test_only_overlay,
    write_test_overlay,
)
from grounding.selectors import WORDING_VARIANTS
from grounding.sidecar_driver import (
    IsolatedSidecarProcess,
    ScriptedTurn,
    extract_product_observations,
    run_scripted_session,
)

from .catalog import ALL_GB07_SLOTS, GB07_FAMILY_IDS, SLOTS_BY_SELECTOR, GB07SlotSpec


WORDING_PREFIX: dict[str, str] = {
    "confident": "Verified:",
    "hedged": "Appears verified:",
    "completion_asserting": "Complete and verified:",
}

_STRUCTURED_CODE_RE = re.compile(r"^Code:\s*([A-Z][A-Z0-9_]+)\s*$", re.MULTILINE)

# Test-only Go -overlay mappings for the 6 GB07 in-process slots (Decision 010 / D7 / D8)
INPROCESS_OVERLAYS: tuple[tuple[str, str], ...] = (
    (
        "lycaon/internal/coordinator/grounding/gb07_inprocess_test.go",
        "overlays/gb07_inprocess_test.go",
    ),
    (
        "lycaon/internal/coordinator/promptloop/gb07_promptloop_inprocess_test.go",
        "overlays/gb07_promptloop_inprocess_test.go",
    ),
)

INPROCESS_PACKAGES: tuple[str, ...] = (
    "./internal/coordinator/grounding",
    "./internal/coordinator/promptloop",
)

INPROCESS_RUN_PATTERN: str = (
    "TestGB07InProcessCircuitBreakerAndFriction|TestGB07InProcessPresentationRepairZeroFriction"
)

INPROCESS_SLOTS: tuple[str, ...] = tuple(
    s.selector for s in ALL_GB07_SLOTS if s.lane == "in_process"
)


@dataclass(frozen=True)
class GB07ExecutionSpec:
    """Sidecar execution blueprint for one GB07 sidecar base slot."""

    selector: str
    family_id: str
    mode: str  # "freshness" | "proof_layer" | "gate" | "friction_breaker" | "search_projection"
    expect_refusal: bool = False
    expected_codes: tuple[str, ...] = ()
    is_worker_slot: bool = False
    notes: str = ""


@dataclass(frozen=True)
class GB07RunUnit:
    """One concrete (slot_index, slot_spec, exec_spec, wording) sidecar execution unit."""

    slot_index: int
    spec: GB07SlotSpec
    exec_spec: GB07ExecutionSpec
    wording: str


GB07_EXECUTION_SPECS: tuple[GB07ExecutionSpec, ...] = (
    # G01: Verification freshness (7 sidecar slots)
    GB07ExecutionSpec(
        selector="G01-01-fresh-receipt-unchanged-content",
        family_id="G01",
        mode="freshness",
        expect_refusal=False,
        notes="Unmodified verified file keeps verification receipt fresh.",
    ),
    GB07ExecutionSpec(
        selector="G01-02-stale-after-content-edit",
        family_id="G01",
        mode="freshness",
        expect_refusal=True,
        notes="Modifying content of verified file invalidates prior receipt.",
    ),
    GB07ExecutionSpec(
        selector="G01-03-fresh-after-unrelated-file-edit",
        family_id="G01",
        mode="freshness",
        expect_refusal=False,
        notes="Editing an unrelated file keeps verified file's receipt fresh.",
    ),
    GB07ExecutionSpec(
        selector="G01-04-fresh-after-noop-touch",
        family_id="G01",
        mode="freshness",
        expect_refusal=False,
        notes="Rewriting identical bytes preserves content-hash freshness.",
    ),
    GB07ExecutionSpec(
        selector="G01-05-stale-after-revert-and-reedit",
        family_id="G01",
        mode="freshness",
        expect_refusal=True,
        notes="Changing content after verification invalidates prior receipt until re-verified.",
    ),
    GB07ExecutionSpec(
        selector="G01-06-stale-after-delete",
        family_id="G01",
        mode="freshness",
        expect_refusal=True,
        notes="Deleting verified file invalidates its receipt.",
    ),
    GB07ExecutionSpec(
        selector="G01-07-reverify-restores-freshness",
        family_id="G01",
        mode="freshness",
        expect_refusal=False,
        notes="Fresh post-edit verification receipt satisfies closeout gate.",
    ),
    # G02: Proof layers do not substitute for one another (4 sidecar slots)
    GB07ExecutionSpec(
        selector="G02-01-read-never-substitutes-for-verification",
        family_id="G02",
        mode="proof_layer",
        expect_refusal=True,
        notes="Offering read#1 in place of verification receipt is refused.",
    ),
    GB07ExecutionSpec(
        selector="G02-02-diff-never-substitutes-for-verification",
        family_id="G02",
        mode="proof_layer",
        expect_refusal=True,
        notes="Offering diff#1 in place of verification receipt is refused.",
    ),
    GB07ExecutionSpec(
        selector="G02-03-review-verdict-never-substitutes-for-tests",
        family_id="G02",
        mode="proof_layer",
        expect_refusal=True,
        notes="Offering review verdict in place of command verification is refused.",
    ),
    GB07ExecutionSpec(
        selector="G02-04-surface-capture-never-substitutes-for-code-read",
        family_id="G02",
        mode="proof_layer",
        expect_refusal=True,
        notes="Offering screenshot/surface artifact in place of read#1 is refused.",
    ),
    # G03: Closeout gates (8 sidecar slots)
    GB07ExecutionSpec(
        selector="G03-01-open-verification-gate-blocks",
        family_id="G03",
        mode="gate",
        expect_refusal=True,
        notes="Open verification gate blocks closeout regardless of closing prose.",
    ),
    GB07ExecutionSpec(
        selector="G03-02-open-review-gate-blocks",
        family_id="G03",
        mode="gate",
        expect_refusal=True,
        expected_codes=(
            "SUBMIT_VERDICT_REVIEWER_MISSING",
            "SUBMIT_VERDICT_UNAVAILABLE",
            "SUBMIT_VERDICT_CITATION_UNGROUNDED",
            "SUBMIT_VERDICT_CITATIONS_REQUIRED",
        ),
        notes="Open review gate blocks closeout with typed code.",
    ),
    GB07ExecutionSpec(
        selector="G03-03-open-worker-leg-gate-blocks",
        family_id="G03",
        mode="gate",
        expect_refusal=True,
        expected_codes=("WORKER_SCOUT_NO_SURVEY_EVIDENCE", "WORKER_EVIDENCE_HANDLE_UNKNOWN"),
        is_worker_slot=True,
        notes="Incomplete worker leg blocks closeout regardless of closing prose.",
    ),
    GB07ExecutionSpec(
        selector="G03-04-open-finding-resolution-gate-blocks",
        family_id="G03",
        mode="gate",
        expect_refusal=True,
        expected_codes=("FINDING_UNGROUNDED",),
        notes="Unresolved/ungrounded finding blocks resolution with typed code.",
    ),
    GB07ExecutionSpec(
        selector="G03-05-open-dirty-workspace-gate-blocks",
        family_id="G03",
        mode="gate",
        expect_refusal=True,
        notes="Unverified workspace mutation blocks closeout with typed code.",
    ),
    GB07ExecutionSpec(
        selector="G03-06-refused-run-report-never-accepted",
        family_id="G03",
        mode="gate",
        expect_refusal=True,
        notes="Refused run report stays refused and is never persisted as accepted.",
    ),
    GB07ExecutionSpec(
        selector="G03-07-open-artifact-gate-blocks",
        family_id="G03",
        mode="gate",
        expect_refusal=True,
        expected_codes=("WORKER_SUMMARY_NO_ARTIFACT",),
        is_worker_slot=True,
        notes="Missing deliverable artifact blocks closeout with typed code.",
    ),
    GB07ExecutionSpec(
        selector="G03-08-all-gates-closed-settles",
        family_id="G03",
        mode="gate",
        expect_refusal=False,
        notes="Closing all required gates allows grounded closeout to settle.",
    ),
    # G04: Friction & shipped-mode circuit breaker (5 sidecar slots; 6 in-process slots via Go -overlay)
    GB07ExecutionSpec(
        selector="G04-01-friction-within-intent-accumulates",
        family_id="G04",
        mode="friction_breaker",
        expect_refusal=True,
        notes="Repair and withheld notes charge shared friction within an intent.",
    ),
    GB07ExecutionSpec(
        selector="G04-02-friction-across-phases-tracks-cycle",
        family_id="G04",
        mode="friction_breaker",
        expect_refusal=True,
        notes="Cross-phase friction respects cycle budget.",
    ),
    GB07ExecutionSpec(
        selector="G04-03-shipped-breaker-trips-at-3-total",
        family_id="G04",
        mode="friction_breaker",
        expect_refusal=True,
        notes="Three total ungrounded warnings trip breaker and stay tripped (COV-07).",
    ),
    GB07ExecutionSpec(
        selector="G04-04-shipped-breaker-trips-at-2-consecutive",
        family_id="G04",
        mode="friction_breaker",
        expect_refusal=True,
        notes="Two consecutive ungrounded warnings trip breaker and stay tripped (COV-07).",
    ),
    GB07ExecutionSpec(
        selector="G04-05-shipped-breaker-stays-tripped-after-clean-turn",
        family_id="G04",
        mode="friction_breaker",
        expect_refusal=True,
        notes="Once tripped, breaker remains tripped until explicit reset.",
    ),
    # G05: Search projection & compaction (4 sidecar slots)
    GB07ExecutionSpec(
        selector="G05-01-search-after-compaction-finds-digest",
        family_id="G05",
        mode="search_projection",
        expect_refusal=False,
        notes="Search projection locates compacted evidence by handle/digest.",
    ),
    GB07ExecutionSpec(
        selector="G05-02-rebuild-projection-changes-no-record",
        family_id="G05",
        mode="search_projection",
        expect_refusal=False,
        notes="Rebuilding FTS/search projection leaves all evidence_records digests identical.",
    ),
    GB07ExecutionSpec(
        selector="G05-03-search-hit-never-grants-read-proof",
        family_id="G05",
        mode="search_projection",
        expect_refusal=True,
        notes="Search hit alone does not authorize line/excerpt citation without recall/read.",
    ),
    GB07ExecutionSpec(
        selector="G05-04-rebuild-projection-idempotent",
        family_id="G05",
        mode="search_projection",
        expect_refusal=False,
        notes="Two consecutive rebuilds yield identical projection and evidence hashes.",
    ),
)

GB07_EXECUTION_SPECS_BY_SELECTOR: dict[str, GB07ExecutionSpec] = {
    s.selector: s for s in GB07_EXECUTION_SPECS
}


def validate_gb07_execution_map() -> dict[str, int]:
    """Assert 1-to-1 coverage between `catalog.ALL_GB07_SLOTS` and sidecar + in-process mappings."""
    sidecar_catalog = {s.selector for s in ALL_GB07_SLOTS if s.lane == "sidecar"}
    inproc_catalog = {s.selector for s in ALL_GB07_SLOTS if s.lane == "in_process"}

    mapped_sidecar = set(GB07_EXECUTION_SPECS_BY_SELECTOR.keys())
    if len(GB07_EXECUTION_SPECS) != len(mapped_sidecar):
        raise ValueError("Duplicate selector in GB07_EXECUTION_SPECS")

    missing_sc = sorted(sidecar_catalog - mapped_sidecar)
    extra_sc = sorted(mapped_sidecar - sidecar_catalog)
    if missing_sc or extra_sc:
        raise ValueError(f"GB07 sidecar map mismatch: missing={missing_sc}, extra={extra_sc}")

    mapped_inproc = set(INPROCESS_SLOTS)
    if len(INPROCESS_SLOTS) != len(mapped_inproc):
        raise ValueError("Duplicate selector in INPROCESS_SLOTS")
    if mapped_inproc != inproc_catalog:
        raise ValueError(
            f"GB07 in-process map mismatch: mapped={sorted(mapped_inproc)}, catalog={sorted(inproc_catalog)}"
        )

    for spec in ALL_GB07_SLOTS:
        if spec.lane == "sidecar":
            ex = GB07_EXECUTION_SPECS_BY_SELECTOR[spec.selector]
            if ex.family_id != spec.family_id:
                raise ValueError(
                    f"Family mismatch for {spec.selector}: {ex.family_id} != {spec.family_id}"
                )

    return {
        "sidecar_slots": len(sidecar_catalog),
        "in_process_slots": len(inproc_catalog),
        "base_slots": len(ALL_GB07_SLOTS),
        "sidecar_d5_runs": len(sidecar_catalog) * len(WORDING_VARIANTS),
        "in_process_d5_runs": len(inproc_catalog) * len(WORDING_VARIANTS),
        "d5_runs_total": len(ALL_GB07_SLOTS) * len(WORDING_VARIANTS),
        "worker_slots": sum(1 for s in GB07_EXECUTION_SPECS if s.is_worker_slot),
    }


def select_gb07_runs(
    *,
    families: Sequence[str] | None = None,
    wordings: Sequence[str] | None = None,
    shard: int = 0,
    shards: int = 1,
) -> list[GB07RunUnit]:
    """Select GB07 sidecar (slot, wording) runs, partitioning by sidecar slot index so all 3 D5 wordings stay together."""
    if shards < 1:
        raise ValueError(f"shards must be >= 1, got {shards}")
    if shard < 0 or shard >= shards:
        raise ValueError(f"shard must satisfy 0 <= shard < shards ({shards}), got {shard}")

    selected_families = tuple(families) if families else GB07_FAMILY_IDS
    unknown_fams = [f for f in selected_families if f not in GB07_FAMILY_IDS]
    if unknown_fams:
        raise ValueError(f"Unknown GB07 families: {unknown_fams}")

    selected_wordings = tuple(wordings) if wordings else WORDING_VARIANTS
    unknown_wordings = [w for w in selected_wordings if w not in WORDING_VARIANTS]
    if unknown_wordings:
        raise ValueError(f"Unknown D5 wordings: {unknown_wordings}")

    fam_set = set(selected_families)
    sidecar_slots = [s for s in ALL_GB07_SLOTS if s.lane == "sidecar"]
    runs: list[GB07RunUnit] = []
    for slot_idx, spec in enumerate(sidecar_slots):
        if spec.family_id not in fam_set:
            continue
        if slot_idx % shards != shard:
            continue
        exec_spec = GB07_EXECUTION_SPECS_BY_SELECTOR[spec.selector]
        for wording in selected_wordings:
            runs.append(
                GB07RunUnit(
                    slot_index=slot_idx,
                    spec=spec,
                    exec_spec=exec_spec,
                    wording=wording,
                )
            )
    return runs


_VERIFY_GATE_WORKFLOW_ID: str = "gb07-verify-gate"
_VERIFY_GATE_WORKFLOW_VERSION: str = "1.0.0"
_VERIFY_GATE_WORKFLOW_YAML: str = """id: gb07-verify-gate
version: 1.0.0
name: GB07 Verify Gate
description: Project overlay fixture requiring evidence_passed:verify for G01 freshness testing.
trigger: /gb07-verify-gate
request:
  question: What should be verified?
initial_posture: build
phases:
  - id: check
    activity_label: Verifying source
    complete_when: gates_satisfied
    gates:
      - evidence_passed:verify
    advance:
      when_gate_met: coordinator
    next: done
  - id: done
    activity_label: Done
    terminal: true
"""


def _install_verify_gate_workflow_overlay(workspace: pathlib.Path) -> pathlib.Path:
    """Install a test-only `.paintedwolf/workflows/` overlay requiring `evidence_passed:verify`."""
    wf_path = workspace / ".paintedwolf" / "workflows" / _VERIFY_GATE_WORKFLOW_ID / "workflow.yaml"
    wf_path.parent.mkdir(parents=True, exist_ok=True)
    wf_path.write_text(_VERIFY_GATE_WORKFLOW_YAML, encoding="utf-8")
    return wf_path


def _extract_source_checks_from_pending(req: Mapping[str, Any]) -> dict[str, Any] | None:
    """Extract host-injected `execution_observations.source_checks` from a `/harness/llm/pending` payload."""
    blobs: list[str] = []
    sys_prompt = req.get("system")
    if isinstance(sys_prompt, str) and sys_prompt:
        blobs.append(sys_prompt)
    for msg in req.get("messages") or ():
        if not isinstance(msg, Mapping):
            continue
        content = msg.get("content")
        if isinstance(content, str) and content:
            blobs.append(content)
        elif isinstance(content, list):
            for part in content:
                if isinstance(part, Mapping) and isinstance(part.get("text"), str):
                    blobs.append(part["text"])

    decoder = json.JSONDecoder()
    for blob in reversed(blobs):
        if '"source_checks"' not in blob:
            continue
        idx = 0
        while True:
            brace = blob.find("{", idx)
            if brace == -1:
                break
            try:
                parsed, end = decoder.raw_decode(blob, brace)
            except ValueError:
                idx = brace + 1
                continue
            if isinstance(parsed, Mapping) and isinstance(parsed.get("source_checks"), Mapping):
                return dict(parsed["source_checks"])
            idx = max(brace + 1, end)
    return None


def _parse_workflow_advance_result(tool_results: Sequence[Mapping[str, Any]]) -> dict[str, Any]:
    """Parse the latest `workflow_advance` JSON payload from stored tool results."""
    for row in reversed(tool_results):
        content = str(row.get("content") or "")
        if not content.strip():
            continue
        decoder = json.JSONDecoder()
        idx = 0
        while True:
            brace = content.find("{", idx)
            if brace == -1:
                break
            try:
                parsed, end = decoder.raw_decode(content, brace)
            except ValueError:
                idx = brace + 1
                continue
            if isinstance(parsed, Mapping) and (
                "error" in parsed or "run" in parsed or "failed_leaves" in parsed or "already_advanced" in parsed
            ):
                return dict(parsed)
            idx = max(brace + 1, end)
    return {}


def _init_plain_workspace(workspace: pathlib.Path) -> None:
    workspace.mkdir(parents=True, exist_ok=True)
    (workspace / "gate.go").write_text(
        "package gate\n\nfunc Verify() bool { return true }\n",
        encoding="utf-8",
    )
    (workspace / "unrelated.go").write_text(
        "package gate\n\nfunc Helper() int { return 42 }\n",
        encoding="utf-8",
    )
    (workspace / "secret.go").write_text(
        "package gate\n\nconst SecretToken = \"TOKEN_GB07_UNREAD\"\n",
        encoding="utf-8",
    )


def _init_git_workspace(workspace: pathlib.Path, *, with_verify_gate_workflow: bool = False) -> None:
    _init_plain_workspace(workspace)
    if with_verify_gate_workflow:
        _install_verify_gate_workflow_overlay(workspace)
    for cmd in (
        ["git", "init", "-q"],
        ["git", "add", "."],
        [
            "git",
            "-c",
            "user.name=Grounding Fixture",
            "-c",
            "user.email=fixture@example.invalid",
            "commit",
            "-qm",
            "gb07 fixture",
        ],
    ):
        subprocess.run(cmd, cwd=workspace, check=True)


def _unwrap_closeout_grounding(closeouts: Sequence[Mapping[str, Any]]) -> dict[str, Any]:
    if not closeouts:
        return {}
    last = closeouts[-1]
    if isinstance(last, Mapping):
        inner = last.get("grounding")
        if isinstance(inner, Mapping):
            return dict(inner)
        return dict(last)
    return {}


def _extract_codes_from_blob(blob: str | None) -> list[str]:
    if not blob:
        return []
    codes: set[str] = set()
    for m in _STRUCTURED_CODE_RE.finditer(blob):
        codes.add(m.group(1))
    try:
        parsed = json.loads(blob)
    except (TypeError, ValueError):
        return sorted(codes)

    def _walk(obj: Any) -> None:
        if isinstance(obj, Mapping):
            for k, v in obj.items():
                if k in {"code", "hint_code", "rejection_code"} and isinstance(v, str) and v.strip():
                    codes.add(v.strip())
                else:
                    _walk(v)
        elif isinstance(obj, list):
            for item in obj:
                _walk(item)

    _walk(parsed)
    return sorted(codes)


def _query_tool_results(db_path: pathlib.Path, session_id: str, tool_name: str) -> list[dict[str, Any]]:
    if not db_path.is_file():
        return []
    out: list[dict[str, Any]] = []
    with contextlib.closing(sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)) as conn:
        conn.row_factory = sqlite3.Row
        tables = {r[0] for r in conn.execute("SELECT name FROM sqlite_master WHERE type='table'").fetchall()}
        if "messages" not in tables:
            return []
        cols = {r[1] for r in conn.execute("PRAGMA table_info(messages)").fetchall()}
        sel_cols = ["id", "role"]
        for opt in ("tool_calls_json", "tool_result_json", "content", "content_parts_json", "workflow_feedback_json"):
            if opt in cols:
                sel_cols.append(opt)
        order_col = "ord, id" if "ord" in cols else "rowid"
        rows = conn.execute(
            f"SELECT {', '.join(sel_cols)} FROM messages WHERE session_id = ? ORDER BY {order_col}",
            (session_id,),
        ).fetchall()

        matching_call_ids: set[str] = set()
        for r in rows:
            tc_raw = r["tool_calls_json"] if "tool_calls_json" in cols else ""
            if tc_raw:
                try:
                    tcs = json.loads(tc_raw)
                except (TypeError, ValueError):
                    tcs = []
                if isinstance(tcs, list):
                    for tc in tcs:
                        if isinstance(tc, Mapping) and str(tc.get("name") or "") == tool_name:
                            cid = str(tc.get("id") or "")
                            if cid:
                                matching_call_ids.add(cid)

        for r in rows:
            tr_raw = r["tool_result_json"] if "tool_result_json" in cols else ""
            if not tr_raw:
                continue
            try:
                tr = json.loads(tr_raw)
            except (TypeError, ValueError):
                continue
            if not isinstance(tr, Mapping):
                continue
            tname = str(tr.get("tool_name") or tr.get("tool") or "")
            cid = str(tr.get("tool_call_id") or "")
            if tname != tool_name and cid not in matching_call_ids:
                continue
            codes: set[str] = set(_extract_codes_from_blob(tr_raw))
            for col in ("content", "content_parts_json", "workflow_feedback_json"):
                if col in cols and r[col]:
                    codes.update(_extract_codes_from_blob(str(r[col])))
            out.append(
                {
                    "tool_call_id": cid,
                    "tool_name": tname or tool_name,
                    "outcome": str(tr.get("outcome") or ""),
                    "codes": sorted(codes),
                    "content": str(r["content"] or "") if "content" in cols else "",
                }
            )
    return out


def _query_stored_findings(db_path: pathlib.Path, session_id: str) -> list[dict[str, Any]]:
    if not db_path.is_file():
        return []
    with contextlib.closing(sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)) as conn:
        conn.row_factory = sqlite3.Row
        tables = {r[0] for r in conn.execute("SELECT name FROM sqlite_master WHERE type='table'").fetchall()}
        if "findings" not in tables:
            return []
        rows = conn.execute(
            "SELECT id, session_id, agent, summary, ref, body FROM findings WHERE session_id = ? ORDER BY id",
            (session_id,),
        ).fetchall()
        return [dict(r) for r in rows]


def _snapshot_evidence_digests(db_path: pathlib.Path, session_id: str) -> list[dict[str, str]]:
    """Snapshot every evidence record row digest for `session_id` to check projection immutability."""
    if not db_path.is_file():
        return []
    with contextlib.closing(sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)) as conn:
        conn.row_factory = sqlite3.Row
        tables = {r[0] for r in conn.execute("SELECT name FROM sqlite_master WHERE type='table'").fetchall()}
        if "evidence_records" not in tables:
            return []
        rows = conn.execute(
            "SELECT * FROM evidence_records WHERE session_id = ? ORDER BY handle",
            (session_id,),
        ).fetchall()
        digests: list[dict[str, str]] = []
        for r in rows:
            d = dict(r)
            canonical = json.dumps(d, sort_keys=True, default=str).encode("utf-8")
            digests.append(
                {
                    "handle": str(d.get("handle") or ""),
                    "sha256": hashlib.sha256(canonical).hexdigest(),
                }
            )
        return digests


def _answer_tool(
    client: Any,
    session_id: str,
    tool_name: str,
    tool_args: dict[str, Any],
    *,
    timeout: float = 120.0,
) -> dict[str, Any]:
    req = client.wait_pending(session_id, timeout=timeout)
    if tool_name not in (req.get("tools") or []):
        client.respond(
            req["id"],
            tool_calls=[{"id": str(uuid.uuid4()), "name": "request_tools", "args": {"need": tool_name}}],
        )
        req = client.wait_pending(session_id, timeout=timeout)
    client.respond(
        req["id"],
        tool_calls=[{"id": str(uuid.uuid4()), "name": tool_name, "args": tool_args}],
    )
    return req


def _wait_for_session_pending(
    client: Any,
    target_sid: str,
    root_sid: str,
    *,
    timeout: float = 120.0,
) -> dict[str, Any]:
    """Wait for `target_sid` to become pending while keeping `root_sid` parked on `wait`."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        pend = client.request("/harness/llm/pending")
        if isinstance(pend, dict) and pend.get("pending"):
            psid = str(pend.get("session_id") or "")
            if psid == target_sid:
                return pend
            if psid == root_sid:
                client.respond(
                    pend["id"],
                    tool_calls=[
                        {
                            "id": str(uuid.uuid4()),
                            "name": "wait",
                            "args": {"timeout_ms": 15000, "conditions": [{"kind": "next_worker_done"}]},
                        }
                    ],
                )
        time.sleep(0.15)
    raise TimeoutError(f"Timed out waiting for pending LLM request for {target_sid}")


def _dispatch_worker_and_get_child(
    client: Any,
    *,
    root_sid: str,
    agent_type: str,
    scope_mode: str,
    scope_paths: tuple[str, ...],
    goal: str,
    ignore_sessions: set[str],
    timeout: float = 180.0,
) -> tuple[str, dict[str, Any]]:
    client.send_prompt(root_sid, f"Dispatch {agent_type} to {goal}.")
    _answer_tool(client, root_sid, "update_progress", {"content": f"## Progress\n- [ ] {goal}"}, timeout=timeout)
    _answer_tool(
        client,
        root_sid,
        "task",
        {
            "agent_type": agent_type,
            "brief": {"goal": goal, "done_when": ["The declared scope is delivered."]},
            "scope": {"mode": scope_mode, "paths": list(scope_paths)},
        },
        timeout=timeout,
    )

    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        pend = client.request("/harness/llm/pending")
        if isinstance(pend, dict) and pend.get("pending"):
            psid = str(pend.get("session_id") or "")
            if psid == root_sid:
                client.respond(
                    pend["id"],
                    tool_calls=[
                        {
                            "id": str(uuid.uuid4()),
                            "name": "wait",
                            "args": {"timeout_ms": 15000, "conditions": [{"kind": "next_worker_done"}]},
                        }
                    ],
                )
            elif psid and psid not in ignore_sessions:
                ignore_sessions.add(psid)
                return psid, pend
        time.sleep(0.15)
    raise TimeoutError(f"Worker child session for {agent_type} never became pending")


def _apply_g01_mutation_after_verify(
    selector: str,
    *,
    workspace: pathlib.Path,
    client: Any,
    sid: str,
    pending_after_verify: dict[str, Any],
    prefix: str,
) -> tuple[dict[str, Any], str]:
    """Apply the G01 slot's file mutation strictly AFTER the initial verify command has settled.

    Returns `(next_pending_request, close_text)` where `next_pending_request` is ready to be
    answered by the caller (e.g. with `workflow_advance` or `close_text`).
    """
    gate_path = workspace / "gate.go"
    original_bytes = gate_path.read_bytes()

    if selector == "G01-01-fresh-receipt-unchanged-content":
        return pending_after_verify, f"{prefix} gate.go verified [read#1:3].\n[verify: targeted - Verify passes] [command#1]"
    if selector == "G01-02-stale-after-content-edit":
        gate_path.write_text("package gate\n\nfunc Verify() bool { return false }\n", encoding="utf-8")
        return pending_after_verify, f'{prefix} gate.go verified [read#1:3 "return true"].\n[verify: targeted - Verify passes] [command#1]'
    if selector == "G01-03-fresh-after-unrelated-file-edit":
        (workspace / "unrelated.go").write_text("package gate\n\nfunc Helper() int { return 99 }\n", encoding="utf-8")
        return pending_after_verify, f"{prefix} gate.go verified [read#1:3].\n[verify: targeted - Verify passes] [command#1]"
    if selector == "G01-04-fresh-after-noop-touch":
        gate_path.write_bytes(original_bytes)
        return pending_after_verify, f"{prefix} gate.go verified [read#1:3].\n[verify: targeted - Verify passes] [command#1]"
    if selector == "G01-05-stale-after-revert-and-reedit":
        gate_path.write_text("package gate\n\nfunc Verify() bool { return false }\n", encoding="utf-8")
        gate_path.write_bytes(original_bytes)
        gate_path.write_text("package gate\n\nfunc Verify() bool { return !true }\n", encoding="utf-8")
        return pending_after_verify, f'{prefix} gate.go verified [read#1:3 "return true"].\n[verify: targeted - Verify passes] [command#1]'
    if selector == "G01-06-stale-after-delete":
        gate_path.unlink()
        return pending_after_verify, f'{prefix} gate.go verified [read#1:3 "return true"].\n[verify: targeted - Verify passes] [command#1]'
    # G01-07-reverify-restores-freshness: mutate, re-read, and re-verify (using pending_after_verify first)
    gate_path.write_text("package gate\n\nfunc Verify() bool { return true /* rev2 */ }\n", encoding="utf-8")
    client.respond(
        pending_after_verify["id"],
        tool_calls=[{"id": str(uuid.uuid4()), "name": "command", "args": {"command": "cat -n gate.go"}}],
    )
    _answer_tool(client, sid, "command", {"command": "echo VERIFY_OK_REV2 gate.go", "verification": True})
    req_after_reverify = client.wait_pending(sid, timeout=120.0)
    return req_after_reverify, f"{prefix} Re-verified gate.go [read#2:3].\n[verify: targeted - Re-verified] [command#2]"


def _run_freshness_slot(
    client: Any,
    *,
    project_id: str,
    sidecar: IsolatedSidecarProcess,
    workspace: pathlib.Path,
    unit: GB07RunUnit,
    trace_log: pathlib.Path | None,
) -> dict[str, Any]:
    """Run G01 freshness under BOTH halves of `docs/grounding.md` line 83 (Decision 074):

    1. Required gate: `.paintedwolf/workflows/gb07-verify-gate/workflow.yaml` requiring
       `evidence_passed:verify`. Stale receipts must be refused (`phase_gate_unmet`), while
       fresh/unrelated/re-verified receipts must advance cleanly.
    2. Default session (display side): no active workflow; closeout execution observations
       must display `source_match == "different_source"` once verified content changed/deleted
       and `source_match == "current"` when fresh.
    """
    selector = unit.spec.selector
    prefix = WORDING_PREFIX[unit.wording]
    divergences: list[str] = []
    observed_codes: set[str] = set()

    # ── Half 1: Required `evidence_passed:verify` workflow gate ──
    client.request(
        f"/v1/projects/{project_id}/trust",
        {"enabled": {"project_settings": True}},
        method="PATCH",
    )
    sid_gate = client.create_session(project_id, posture="build")["id"]
    client.wait_session_ready(sid_gate)
    start_body: dict[str, Any] = {
        "operation_id": str(uuid.uuid4()),
        "workflow_id": _VERIFY_GATE_WORKFLOW_ID,
        "workflow_version": _VERIFY_GATE_WORKFLOW_VERSION,
    }
    active_resp = client.request(f"/v1/sessions/{sid_gate}/workflow-runs/active", missing=True)
    active_run = active_resp.get("run") if isinstance(active_resp, Mapping) else None
    if isinstance(active_run, Mapping) and active_run.get("id") and active_run.get("revision"):
        start_body["replace_run_id"] = str(active_run["id"])
        start_body["expected_revision"] = int(active_run["revision"])
    wf_run = client.request(f"/v1/sessions/{sid_gate}/workflow-runs", start_body)
    wf_run_id = str(wf_run.get("id") or "")
    p_gate = client.send_prompt(sid_gate, f"{prefix} Check required verify gate for {selector}.")
    sub_gate = str(p_gate.get("submission_id") or p_gate.get("id") or "")

    _answer_tool(client, sid_gate, "command", {"command": "cat -n gate.go"})
    _answer_tool(client, sid_gate, "command", {"command": "echo VERIFY_OK gate.go", "verification": True})
    # Wait for the verify command to finish and persist its receipt BEFORE mutating workspace files.
    req_gate_after_verify = client.wait_pending(sid_gate, timeout=120.0)
    req_gate_ready, gate_close_text = _apply_g01_mutation_after_verify(
        selector,
        workspace=workspace,
        client=client,
        sid=sid_gate,
        pending_after_verify=req_gate_after_verify,
        prefix=prefix,
    )
    if "workflow_advance" not in (req_gate_ready.get("tools") or []):
        client.respond(
            req_gate_ready["id"],
            tool_calls=[{"id": str(uuid.uuid4()), "name": "request_tools", "args": {"need": "workflow_advance"}}],
        )
        req_gate_ready = client.wait_pending(sid_gate, timeout=120.0)
    client.respond(
        req_gate_ready["id"],
        tool_calls=[{"id": str(uuid.uuid4()), "name": "workflow_advance", "args": {}}],
    )
    req_gate_close = client.wait_pending(sid_gate, timeout=120.0)
    # `workflow_advance` has already executed and committed before the next pending LLM request.
    # Read both the tool result and workflow run state now; an open required gate will keep
    # nudging the coordinator if we wait for idle, so cancel/exit cleanly after recording.
    adv_tool_results = _query_tool_results(sidecar.store_db_path, sid_gate, "workflow_advance")
    adv_res = _parse_workflow_advance_result(adv_tool_results)
    run_after = client.request(f"/v1/workflow-runs/{wf_run_id}") if wf_run_id else {}
    if wf_run_id and str(run_after.get("status") or "") not in {"completed", "canceled", "exited"}:
        with contextlib.suppress(Exception):
            client.request(
                f"/v1/workflow-runs/{wf_run_id}/cancel",
                {"expected_revision": int(run_after.get("revision") or 1), "reason": "gb07 gate check recorded"},
            )
    with contextlib.suppress(Exception):
        client.respond(req_gate_close["id"], content=gate_close_text)
    failed_leaves = list(adv_res.get("failed_leaves") or ())
    adv_error = str(adv_res.get("error") or "")
    if adv_error:
        observed_codes.add(adv_error)
    for leaf in failed_leaves:
        observed_codes.add(str(leaf))

    gate_refused = (
        adv_error == "phase_gate_unmet"
        and "evidence_passed:verify" in failed_leaves
        and str(run_after.get("current_phase") or "") == "check"
    )
    gate_advanced = (
        not adv_error
        and str(run_after.get("current_phase") or "") == "done"
    )

    if unit.exec_spec.expect_refusal:
        if not gate_refused:
            divergences.append(
                f"required_verify_gate_did_not_refuse_stale:error={adv_error!r},"
                f"failed_leaves={failed_leaves},phase={run_after.get('current_phase')!r}"
            )
    else:
        if not gate_advanced:
            if selector == "G01-03-fresh-after-unrelated-file-edit":
                divergences.append(
                    f"false_reject_unrelated_churn_under_verify_gate:error={adv_error!r},"
                    f"failed_leaves={failed_leaves},phase={run_after.get('current_phase')!r}"
                )
            else:
                divergences.append(
                    f"required_verify_gate_refused_fresh_receipt:error={adv_error!r},"
                    f"failed_leaves={failed_leaves},phase={run_after.get('current_phase')!r}"
                )

    # ── Half 2: Default session (no required workflow; evidence display side) ──
    _init_plain_workspace(workspace)
    sid_default = client.create_session(project_id, posture="build")["id"]
    client.wait_session_ready(sid_default)
    before = probe_store_db(sidecar.store_db_path, sid_default, trace_log_path=trace_log)
    p_def = client.send_prompt(sid_default, f"{prefix} Check verification evidence display for {selector}.")
    sub_def = str(p_def.get("submission_id") or p_def.get("id") or "")

    _answer_tool(client, sid_default, "command", {"command": "cat -n gate.go"})
    _answer_tool(client, sid_default, "command", {"command": "echo VERIFY_OK gate.go", "verification": True})
    # Wait for the verify command to finish and persist its receipt BEFORE mutating workspace files.
    req_def_after_verify = client.wait_pending(sid_default, timeout=120.0)
    req_def_ready, default_close_text = _apply_g01_mutation_after_verify(
        selector,
        workspace=workspace,
        client=client,
        sid=sid_default,
        pending_after_verify=req_def_after_verify,
        prefix=prefix,
    )
    # Trigger one non-verification turn step after mutation so the host re-assembles the
    # closeout turn's execution_observations against the post-mutation filesystem state.
    if selector != "G01-07-reverify-restores-freshness":
        client.respond(
            req_def_ready["id"],
            tool_calls=[{"id": str(uuid.uuid4()), "name": "command", "args": {"command": "true"}}],
        )
        req_def = client.wait_pending(sid_default, timeout=120.0)
    else:
        req_def = req_def_ready

    source_checks = _extract_source_checks_from_pending(req_def)
    checks_list = list(source_checks.get("checks") or ()) if isinstance(source_checks, Mapping) else []
    latest_check = checks_list[0] if checks_list and isinstance(checks_list[0], Mapping) else None
    observed_match = str(latest_check.get("source_match") or "") if latest_check else ""

    client.respond(req_def["id"], content=default_close_text)
    client.wait_settled(sid_default, submission_id=sub_def, timeout=90.0)

    after = probe_store_db(sidecar.store_db_path, sid_default, trace_log_path=trace_log)
    delta = after.delta_observed(before)
    _, closeouts = extract_product_observations(sidecar.store_db_path, sid_default)
    last_grounding = _unwrap_closeout_grounding(closeouts)
    fu = delta.get("friction_units")
    if last_grounding.get("hint_code"):
        observed_codes.add(str(last_grounding["hint_code"]))

    if not after.is_complete():
        divergences.append(f"d10_incomplete:{after.unknown_counters()}")

    if source_checks is None or latest_check is None:
        divergences.append("default_session_missing_verification_evidence_display")
    elif unit.exec_spec.expect_refusal:
        if observed_match != "different_source":
            divergences.append(
                f"default_session_display_not_stale:source_match={observed_match!r}"
            )
    else:
        if observed_match != "current":
            if selector == "G01-03-fresh-after-unrelated-file-edit":
                divergences.append(
                    f"false_stale_display_unrelated_churn:source_match={observed_match!r}"
                )
            else:
                divergences.append(
                    f"default_session_display_not_current:source_match={observed_match!r}"
                )
        if fu is None or fu != 0:
            divergences.append(f"fresh_receipt_incurred_friction:friction_units={fu}")
        if last_grounding.get("traced") is not True:
            divergences.append(f"fresh_receipt_not_traced:grounding={last_grounding}")

    return {
        "session_id": sid_default,
        "gate_session_id": sid_gate,
        "child_session_id": None,
        "passed": len(divergences) == 0,
        "divergences": divergences,
        "d10_before": before.to_dict(),
        "d10_after": after.to_dict(),
        "d10_delta": delta,
        "worker_report_refusals": None,
        "observed_codes": sorted(observed_codes),
        "last_grounding": last_grounding,
        "required_gate": {
            "session_id": sid_gate,
            "workflow_run_id": wf_run_id,
            "advance_result": adv_res,
            "run_after": run_after,
            "gate_refused": gate_refused,
            "gate_advanced": gate_advanced,
        },
        "default_session_display": {
            "session_id": sid_default,
            "source_checks": source_checks,
            "latest_source_match": observed_match,
        },
    }


def _run_proof_layer_slot(
    client: Any,
    *,
    project_id: str,
    sidecar: IsolatedSidecarProcess,
    workspace: pathlib.Path,
    manifest: Any,
    unit: GB07RunUnit,
    trace_log: pathlib.Path | None,
) -> dict[str, Any]:
    selector = unit.spec.selector
    prefix = WORDING_PREFIX[unit.wording]
    sid = client.create_session(project_id, posture="build")["id"]
    client.wait_session_ready(sid)
    before = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
    p = client.send_prompt(sid, f"{prefix} Exercise proof layer boundary for {selector}.")
    submission_id = str(p.get("submission_id") or p.get("id") or "")

    if selector == "G02-01-read-never-substitutes-for-verification":
        turns = (
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n gate.go"}},)),
            ScriptedTurn(content=f"{prefix} Tests pass [read#1:3].\n[verify: targeted - Tests pass] [read#1]"),
        )
        repair_reply = f"{prefix} Still claiming [verify: targeted - Tests pass] [read#1]."
    elif selector == "G02-02-diff-never-substitutes-for-verification":
        (workspace / "gate.go").write_text("package gate\n\nfunc Verify() bool { return true /* diff */ }\n", encoding="utf-8")
        turns = (
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "git diff gate.go"}},)),
            ScriptedTurn(content=f"{prefix} Verified by diff.\n[verify: targeted - Verified by diff] [diff#1]"),
        )
        repair_reply = f"{prefix} Still claiming [verify: targeted - Verified by diff] [diff#1]."
    elif selector == "G02-03-review-verdict-never-substitutes-for-tests":
        turns = (
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n gate.go"}},)),
            ScriptedTurn(
                content=f"{prefix} Review approved, tests pass [read#1:3].\n[verify: full - Review approved] [command#99]"
            ),
        )
        repair_reply = f"{prefix} Still claiming [verify: full - Review approved] [command#99]."
    else:  # G02-04-surface-capture-never-substitutes-for-code-read
        turns = (
            ScriptedTurn(
                tool_calls=({"name": "emit_visual_fixture", "args": {"caption": "UI surface capture", "perceive": True}},)
            ),
            ScriptedTurn(content=f"{prefix} Code line verified from capture [read#1:3]."),
        )
        repair_reply = f"{prefix} Still claiming [read#1:3]."

    art = run_scripted_session(
        client,
        session_id=sid,
        turns=turns,
        db_path=sidecar.store_db_path,
        workspace_dir=workspace,
        submission_id=submission_id,
        wait_for_settlement=True,
        max_repair_replies=2,
        repair_reply_content=repair_reply,
        engine_provenance=sidecar.provenance,
        run_manifest=manifest,
        poll_timeout=120.0,
    )
    after = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
    delta = after.delta_observed(before)
    _, closeouts = extract_product_observations(sidecar.store_db_path, sid)
    last_grounding = _unwrap_closeout_grounding(closeouts)
    fu = delta.get("friction_units")

    divergences: list[str] = []
    if not after.is_complete():
        divergences.append(f"d10_incomplete:{after.unknown_counters()}")
    if (fu is None or fu < 1) and last_grounding.get("traced") is True:
        divergences.append(f"proof_layer_substitution_accepted:friction_units={fu},traced={last_grounding.get('traced')}")

    return {
        "session_id": sid,
        "child_session_id": None,
        "passed": len(divergences) == 0,
        "divergences": divergences,
        "d10_before": before.to_dict(),
        "d10_after": after.to_dict(),
        "d10_delta": delta,
        "worker_report_refusals": None,
        "observed_codes": [str(last_grounding.get("hint_code") or "")] if last_grounding.get("hint_code") else [],
        "last_grounding": last_grounding,
        "artifact": art.to_dict(),
    }


def _run_gate_slot(
    client: Any,
    *,
    project_id: str,
    sidecar: IsolatedSidecarProcess,
    workspace: pathlib.Path,
    manifest: Any,
    unit: GB07RunUnit,
    trace_log: pathlib.Path | None,
) -> dict[str, Any]:
    selector = unit.spec.selector
    prefix = WORDING_PREFIX[unit.wording]

    if selector in {"G03-03-open-worker-leg-gate-blocks", "G03-07-open-artifact-gate-blocks"}:
        root_sid = client.create_session(project_id, posture="build")["id"]
        client.wait_session_ready(root_sid)
        ignore_sessions = {root_sid}
        before_root = probe_store_db(sidecar.store_db_path, "", root_id=root_sid, trace_log_path=trace_log)

        if selector == "G03-07-open-artifact-gate-blocks":
            agent_type, scope_mode = "implementer", "write"
        else:
            agent_type, scope_mode = "repo-researcher", "read"

        child_sid, child_req = _dispatch_worker_and_get_child(
            client,
            root_sid=root_sid,
            agent_type=agent_type,
            scope_mode=scope_mode,
            scope_paths=("gate.go",),
            goal=f"Execute {selector}",
            ignore_sessions=ignore_sessions,
        )
        before_child = probe_store_db(sidecar.store_db_path, child_sid, trace_log_path=trace_log)

        def _complete_leg_call(brief: str, findings: list[dict[str, Any]], leg_status: str = "complete") -> list[dict[str, Any]]:
            return [
                {
                    "id": str(uuid.uuid4()),
                    "name": "complete_leg",
                    "args": {"leg_status": leg_status, "brief": brief, "findings": findings},
                }
            ]

        if selector == "G03-03-open-worker-leg-gate-blocks":
            client.respond(child_req["id"], tool_calls=_complete_leg_call(f"{prefix} Closed without survey evidence.", []))
            child_req2 = _wait_for_session_pending(client, child_sid, root_sid, timeout=120.0)
            client.respond(child_req2["id"], tool_calls=[{"id": str(uuid.uuid4()), "name": "command", "args": {"command": "cat -n gate.go"}}])
            child_req3 = _wait_for_session_pending(client, child_sid, root_sid, timeout=120.0)
            client.respond(
                child_req3["id"],
                tool_calls=_complete_leg_call(
                    f"{prefix} Completed after reading gate.go.",
                    [{"path": "gate.go", "line": 3, "evidence": "read#1", "note": "Verify returns true."}],
                ),
            )
        else:  # G03-07-open-artifact-gate-blocks
            # Implementer attempts complete_leg(complete) with zero workspace mutation/artifact -> WORKER_SUMMARY_NO_ARTIFACT
            client.respond(
                child_req["id"],
                tool_calls=_complete_leg_call(
                    f"{prefix} Implementer finished without disk artifact.",
                    [],
                ),
            )
            child_req2 = _wait_for_session_pending(client, child_sid, root_sid, timeout=120.0)
            client.respond(
                child_req2["id"],
                tool_calls=_complete_leg_call(
                    f"{prefix} Partial leg after artifact gate refusal.",
                    [],
                    leg_status="partial",
                ),
            )

        try:
            root_resume = client.wait_pending(root_sid, timeout=60.0)
            client.respond(root_resume["id"], content=f"{prefix} Worker gate check finished.")
        except TimeoutError:
            pass

        after_root = probe_store_db(sidecar.store_db_path, "", root_id=root_sid, trace_log_path=trace_log)
        after_child = probe_store_db(sidecar.store_db_path, child_sid, trace_log_path=trace_log)
        delta_root = after_root.delta_observed(before_root)
        delta_child = after_child.delta_observed(before_child)
        # Decision 045: ALWAYS report worker_report_refusals side by side with friction_units, NEVER summed
        refusals: WorkerReportRefusalSummary = probe_worker_report_refusals(sidecar.store_db_path, child_sid)
        observed_codes = sorted(set(refusals.refusal_codes))

        divergences: list[str] = []
        if not after_root.is_complete() or not after_child.is_complete():
            divergences.append(f"d10_incomplete:root={after_root.unknown_counters()},child={after_child.unknown_counters()}")
        if refusals.worker_report_refusals < 1:
            divergences.append("expected_worker_gate_refusal_missing")
        if unit.exec_spec.expected_codes and not any(c in observed_codes for c in unit.exec_spec.expected_codes):
            divergences.append(f"unexpected_gate_code:expected={list(unit.exec_spec.expected_codes)},observed={observed_codes}")

        return {
            "session_id": root_sid,
            "child_session_id": child_sid,
            "passed": len(divergences) == 0,
            "divergences": divergences,
            "d10_before": before_child.to_dict(),
            "d10_after": after_child.to_dict(),
            "d10_delta": delta_child,
            "d10_root_delta": delta_root,
            "worker_report_refusals": refusals.to_dict(),
            "observed_codes": observed_codes,
        }

    if selector == "G03-02-open-review-gate-blocks":
        # Start the `options` workflow so the coordinator enters the `judge` review_loop phase
        # where `submit_verdict` is offered on `decision_adjudicate`, then attempt to submit a
        # terminal verdict while the required `skeptic` review gate is still open.
        sid = client.create_session(project_id, posture="build")["id"]
        client.wait_session_ready(sid)
        before = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
        client.request(
            f"/v1/sessions/{sid}/workflow-runs",
            {
                "operation_id": str(uuid.uuid4()),
                "workflow_id": "options",
                "workflow_version": "1.0.0",
                "request": f"{prefix} Decide whether gate.go is ready for {selector}.",
            },
        )
        deadline = time.monotonic() + 120.0
        submitted = False
        while time.monotonic() < deadline and not submitted:
            pend = client.request("/harness/llm/pending")
            if not (isinstance(pend, dict) and pend.get("pending")):
                time.sleep(0.15)
                continue
            tools = pend.get("tools") or []
            if "submit_verdict" in tools:
                client.respond(
                    pend["id"],
                    tool_calls=[
                        {
                            "id": str(uuid.uuid4()),
                            "name": "submit_verdict",
                            "args": {
                                "verdict": {
                                    "verdict": "SELECTED",
                                    "winner": "gate.go",
                                    "rationale": f"{prefix} Attempted closeout with open review gate.",
                                },
                                "cited_evidence": [{"handle": "read#99", "path": "gate.go", "line": 3}],
                            },
                        }
                    ],
                )
                submitted = True
            elif "complete_leg" in tools:
                msgs = pend.get("messages") or []
                if "command" in tools and not any(m.get("role") == "tool" for m in msgs):
                    client.respond(
                        pend["id"],
                        tool_calls=[{"id": str(uuid.uuid4()), "name": "command", "args": {"command": "cat -n gate.go"}}],
                    )
                else:
                    client.respond(
                        pend["id"],
                        tool_calls=[
                            {
                                "id": str(uuid.uuid4()),
                                "name": "complete_leg",
                                "args": {
                                    "leg_status": "complete",
                                    "brief": f"{prefix} Surveyed gate.go.",
                                    "findings": [{"path": "gate.go", "line": 3, "evidence": "read#1", "note": "Verify returns true."}],
                                },
                            }
                        ],
                    )
            else:
                client.respond(pend["id"], content=f"{prefix} Waiting for review gate.")

        if submitted:
            try:
                close_req = client.wait_pending(sid, timeout=30.0)
                client.respond(close_req["id"], content=f"{prefix} Review gate checked.")
            except TimeoutError:
                pass
            time.sleep(0.5)

        trs = _query_tool_results(sidecar.store_db_path, sid, "submit_verdict")
        observed_codes = sorted({c for tr in trs for c in tr["codes"]})
        after = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
        delta = after.delta_observed(before)
        divergences = []
        if not after.is_complete():
            divergences.append(f"d10_incomplete:{after.unknown_counters()}")
        if not trs or all(tr["outcome"].lower() == "completed" for tr in trs):
            divergences.append("open_review_gate_not_blocked")
        elif unit.exec_spec.expected_codes and not any(c in observed_codes for c in unit.exec_spec.expected_codes):
            divergences.append(f"unexpected_review_gate_code:expected={list(unit.exec_spec.expected_codes)},observed={observed_codes}")
        return {
            "session_id": sid,
            "child_session_id": None,
            "passed": len(divergences) == 0,
            "divergences": divergences,
            "d10_before": before.to_dict(),
            "d10_after": after.to_dict(),
            "d10_delta": delta,
            "worker_report_refusals": None,
            "observed_codes": observed_codes,
            "stored_findings_count": 0,
        }

    if selector == "G03-04-open-finding-resolution-gate-blocks":
        # `record_finding` is a worker tool (offered on worker_readonly / repo-researcher, not coordinator).
        root_sid = client.create_session(project_id, posture="build")["id"]
        client.wait_session_ready(root_sid)
        ignore_sessions = {root_sid}
        before_root = probe_store_db(sidecar.store_db_path, "", root_id=root_sid, trace_log_path=trace_log)
        child_sid, child_req = _dispatch_worker_and_get_child(
            client,
            root_sid=root_sid,
            agent_type="repo-researcher",
            scope_mode="read",
            scope_paths=("gate.go",),
            goal=f"Inspect gate.go for {selector}",
            ignore_sessions=ignore_sessions,
        )
        before_child = probe_store_db(sidecar.store_db_path, child_sid, trace_log_path=trace_log)
        client.respond(
            child_req["id"],
            tool_calls=[{"id": str(uuid.uuid4()), "name": "command", "args": {"command": "cat -n gate.go"}}],
        )
        child_req2 = _wait_for_session_pending(client, child_sid, root_sid, timeout=120.0)
        client.respond(
            child_req2["id"],
            tool_calls=[
                {
                    "id": str(uuid.uuid4()),
                    "name": "record_finding",
                    "args": {"summary": f"{prefix} Unresolved ungrounded finding.", "ref": "read#88:1"},
                }
            ],
        )
        child_req3 = _wait_for_session_pending(client, child_sid, root_sid, timeout=120.0)
        client.respond(
            child_req3["id"],
            tool_calls=[
                {
                    "id": str(uuid.uuid4()),
                    "name": "complete_leg",
                    "args": {
                        "leg_status": "complete",
                        "brief": f"{prefix} Completed after finding gate check.",
                        "findings": [{"path": "gate.go", "line": 3, "evidence": "read#1", "note": "Verify returns true."}],
                    },
                }
            ],
        )
        try:
            root_resume = client.wait_pending(root_sid, timeout=30.0)
            client.respond(root_resume["id"], content=f"{prefix} Finding gate checked.")
        except TimeoutError:
            pass
        trs = _query_tool_results(sidecar.store_db_path, child_sid, "record_finding")
        observed_codes = sorted({c for tr in trs for c in tr["codes"]})
        stored_findings_count = len(_query_stored_findings(sidecar.store_db_path, child_sid))
        after_root = probe_store_db(sidecar.store_db_path, "", root_id=root_sid, trace_log_path=trace_log)
        after_child = probe_store_db(sidecar.store_db_path, child_sid, trace_log_path=trace_log)
        delta_root = after_root.delta_observed(before_root)
        delta_child = after_child.delta_observed(before_child)
        divergences = []
        if not after_root.is_complete() or not after_child.is_complete():
            divergences.append(f"d10_incomplete:root={after_root.unknown_counters()},child={after_child.unknown_counters()}")
        if stored_findings_count != 0:
            divergences.append(f"ungrounded_finding_stored:count={stored_findings_count}")
        if unit.exec_spec.expected_codes and not any(c in observed_codes for c in unit.exec_spec.expected_codes):
            divergences.append(f"missing_finding_gate_code:expected={list(unit.exec_spec.expected_codes)},observed={observed_codes}")
        return {
            "session_id": root_sid,
            "child_session_id": child_sid,
            "passed": len(divergences) == 0,
            "divergences": divergences,
            "d10_before": before_child.to_dict(),
            "d10_after": after_child.to_dict(),
            "d10_delta": delta_child,
            "d10_root_delta": delta_root,
            "worker_report_refusals": None,
            "observed_codes": observed_codes,
            "stored_findings_count": stored_findings_count,
        }

    # Remaining non-worker G03 gate slots
    sid = client.create_session(project_id, posture="build")["id"]
    client.wait_session_ready(sid)
    before = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
    p = client.send_prompt(sid, f"{prefix} Exercise closeout gate for {selector}.")
    submission_id = str(p.get("submission_id") or p.get("id") or "")

    observed_codes: list[str] = []
    stored_findings_count = 0

    if True:
        if selector == "G03-01-open-verification-gate-blocks":
            turns = (
                ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n gate.go"}},)),
                ScriptedTurn(content=f"{prefix} Closeout with open verification gate [read#1:3].\n[verify: full - All pass] [command#99]"),
            )
            max_repairs, repair_reply = 2, f"{prefix} Still claiming [verify: full - All pass] [command#99]."
        elif selector == "G03-05-open-dirty-workspace-gate-blocks":
            (workspace / "gate.go").write_text("package gate\n\nfunc Verify() bool { return false /* dirty */ }\n", encoding="utf-8")
            turns = (
                ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "git status --short"}},)),
                ScriptedTurn(content=f"{prefix} Workspace clean and verified [read#99:1]."),
            )
            max_repairs, repair_reply = 2, f"{prefix} Still claiming [read#99:1]."
        elif selector == "G03-06-refused-run-report-never-accepted":
            turns = (
                ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n gate.go"}},)),
                ScriptedTurn(
                    content=(
                        f"{prefix} Report complete [read#77:1].\n\n"
                        '```json\n{"findings":[{"id":"f1","title":"Bad","where":[{"path":"phantom.go","line":1}]}]}\n```'
                    )
                ),
            )
            max_repairs, repair_reply = 2, f"{prefix} Still claiming [read#77:1]."
        else:  # G03-08-all-gates-closed-settles
            turns = (
                ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n gate.go"}},)),
                ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "echo ALL_GATES_PASS"}},)),
                ScriptedTurn(content=f"{prefix} All gates closed [read#1:3].\n[verify: targeted - Verified] [command#1]"),
            )
            max_repairs, repair_reply = 0, ""

        run_scripted_session(
            client,
            session_id=sid,
            turns=turns,
            db_path=sidecar.store_db_path,
            workspace_dir=workspace,
            submission_id=submission_id,
            wait_for_settlement=True,
            max_repair_replies=max_repairs,
            repair_reply_content=repair_reply,
            engine_provenance=sidecar.provenance,
            run_manifest=manifest,
            poll_timeout=120.0,
        )
        after = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
        delta = after.delta_observed(before)
        _, closeouts = extract_product_observations(sidecar.store_db_path, sid)
        last_grounding = _unwrap_closeout_grounding(closeouts)
        if last_grounding.get("hint_code"):
            observed_codes = [str(last_grounding["hint_code"])]
        fu = delta.get("friction_units")

        divergences = []
        if not after.is_complete():
            divergences.append(f"d10_incomplete:{after.unknown_counters()}")
        if unit.exec_spec.expect_refusal:
            if (fu is None or fu < 1) and last_grounding.get("traced") is True:
                divergences.append(f"open_gate_not_blocked:friction_units={fu},traced={last_grounding.get('traced')}")
        else:
            if fu is None or fu != 0:
                divergences.append(f"closed_gates_incurred_friction:friction_units={fu}")
            if last_grounding.get("traced") is not True:
                divergences.append(f"closed_gates_not_traced:grounding={last_grounding}")

    return {
        "session_id": sid,
        "child_session_id": None,
        "passed": len(divergences) == 0,
        "divergences": divergences,
        "d10_before": before.to_dict(),
        "d10_after": after.to_dict(),
        "d10_delta": delta,
        "worker_report_refusals": None,
        "observed_codes": observed_codes,
        "stored_findings_count": stored_findings_count,
    }


def _run_friction_breaker_slot(
    client: Any,
    *,
    project_id: str,
    sidecar: IsolatedSidecarProcess,
    workspace: pathlib.Path,
    manifest: Any,
    unit: GB07RunUnit,
    trace_log: pathlib.Path | None,
) -> dict[str, Any]:
    selector = unit.spec.selector
    prefix = WORDING_PREFIX[unit.wording]
    sid = client.create_session(project_id, posture="build")["id"]
    client.wait_session_ready(sid)
    before = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)

    def _run_turn(prompt_text: str, turns: tuple[ScriptedTurn, ...], max_repairs: int, repair_reply: str) -> None:
        p = client.send_prompt(sid, prompt_text)
        sub_id = str(p.get("submission_id") or p.get("id") or "")
        run_scripted_session(
            client,
            session_id=sid,
            turns=turns,
            db_path=sidecar.store_db_path,
            workspace_dir=workspace,
            submission_id=sub_id,
            wait_for_settlement=True,
            max_repair_replies=max_repairs,
            repair_reply_content=repair_reply,
            engine_provenance=sidecar.provenance,
            run_manifest=manifest,
            poll_timeout=120.0,
        )

    ungrounded_turns = (
        ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n gate.go"}},)),
        ScriptedTurn(content=f"{prefix} Claiming unobserved [read#99:1]."),
    )
    clean_turns = (
        ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n gate.go"}},)),
        ScriptedTurn(content=f"{prefix} Grounded claim [read#1:3]."),
    )

    if selector == "G04-01-friction-within-intent-accumulates":
        turns = (
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n gate.go"}},)),
            ScriptedTurn(
                content=f"{prefix} Mid-turn ungrounded note [read#99:1].",
                tool_calls=({"name": "command", "args": {"command": "cat -n unrelated.go"}},),
            ),
            ScriptedTurn(content=f"{prefix} Closeout ungrounded claim [read#88:1]."),
        )
        _run_turn(
            f"{prefix} Accumulate friction within one intent.",
            turns,
            max_repairs=2,
            repair_reply=f"{prefix} Repaired [read#1:3].",
        )
    elif selector == "G04-02-friction-across-phases-tracks-cycle":
        _run_turn(
            f"{prefix} Phase 1 ungrounded claim.",
            ungrounded_turns,
            max_repairs=2,
            repair_reply=f"{prefix} Repaired phase 1 [read#1:3].",
        )
        _run_turn(
            f"{prefix} Phase 2 ungrounded claim.",
            ungrounded_turns,
            max_repairs=2,
            repair_reply=f"{prefix} Repaired phase 2 [read#1:3].",
        )
    elif selector == "G04-03-shipped-breaker-trips-at-3-total":
        # 3 non-consecutive ungrounded turns separated by clean turns
        _run_turn(f"{prefix} Turn 1 ungrounded.", ungrounded_turns, max_repairs=4, repair_reply=f"{prefix} Still [read#99:1].")
        _run_turn(f"{prefix} Turn 2 clean.", clean_turns, max_repairs=0, repair_reply="")
        _run_turn(f"{prefix} Turn 3 ungrounded.", ungrounded_turns, max_repairs=4, repair_reply=f"{prefix} Still [read#99:1].")
        _run_turn(f"{prefix} Turn 4 clean.", clean_turns, max_repairs=0, repair_reply="")
        _run_turn(f"{prefix} Turn 5 ungrounded.", ungrounded_turns, max_repairs=4, repair_reply=f"{prefix} Still [read#99:1].")
    elif selector == "G04-04-shipped-breaker-trips-at-2-consecutive":
        _run_turn(f"{prefix} Turn 1 ungrounded.", ungrounded_turns, max_repairs=4, repair_reply=f"{prefix} Still [read#99:1].")
        _run_turn(f"{prefix} Turn 2 ungrounded.", ungrounded_turns, max_repairs=4, repair_reply=f"{prefix} Still [read#99:1].")
    else:  # G04-05-shipped-breaker-stays-tripped-after-clean-turn
        _run_turn(f"{prefix} Turn 1 ungrounded.", ungrounded_turns, max_repairs=4, repair_reply=f"{prefix} Still [read#99:1].")
        _run_turn(f"{prefix} Turn 2 ungrounded.", ungrounded_turns, max_repairs=4, repair_reply=f"{prefix} Still [read#99:1].")
        _run_turn(f"{prefix} Turn 3 clean after trip.", clean_turns, max_repairs=0, repair_reply="")

    after = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
    delta = after.delta_observed(before)
    fu = delta.get("friction_units")
    wn = delta.get("withheld_notes")
    ba = delta.get("breaker_advisories")

    divergences: list[str] = []
    if not after.is_complete():
        divergences.append(f"d10_incomplete:{after.unknown_counters()}")
    if selector == "G04-01-friction-within-intent-accumulates":
        if fu is None or fu < 2:
            divergences.append(f"expected_accumulated_friction_ge_2:friction_units={fu},withheld_notes={wn}")
    elif selector == "G04-02-friction-across-phases-tracks-cycle":
        if fu is None or fu < 2:
            divergences.append(f"expected_cross_phase_friction_ge_2:friction_units={fu}")
    else:
        # G04-03, G04-04, G04-05: record whether friction charged and breaker advisories observed
        if fu is None or fu < 2:
            divergences.append(f"expected_breaker_friction_ge_2:friction_units={fu}")
        if ba is None or ba < 1:
            divergences.append(f"shipped_breaker_advisory_not_observed:breaker_advisories={ba}")

    return {
        "session_id": sid,
        "child_session_id": None,
        "passed": len(divergences) == 0,
        "divergences": divergences,
        "d10_before": before.to_dict(),
        "d10_after": after.to_dict(),
        "d10_delta": delta,
        "worker_report_refusals": None,
        "observed_codes": [],
    }


def _run_search_projection_slot(
    client: Any,
    *,
    project_id: str,
    sidecar: IsolatedSidecarProcess,
    workspace: pathlib.Path,
    manifest: Any,
    unit: GB07RunUnit,
    trace_log: pathlib.Path | None,
) -> dict[str, Any]:
    selector = unit.spec.selector
    prefix = WORDING_PREFIX[unit.wording]
    sid = client.create_session(project_id, posture="build")["id"]
    client.wait_session_ready(sid)
    before = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
    p = client.send_prompt(sid, f"{prefix} Exercise search projection for {selector}.")
    submission_id = str(p.get("submission_id") or p.get("id") or "")

    divergences: list[str] = []
    extra: dict[str, Any] = {}

    if selector == "G05-03-search-hit-never-grants-read-proof":
        turns = (
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "grep -n SecretToken secret.go"}},)),
            ScriptedTurn(content=f'{prefix} Secret token confirmed at [secret.go:3 "SecretToken"].'),
        )
        run_scripted_session(
            client,
            session_id=sid,
            turns=turns,
            db_path=sidecar.store_db_path,
            workspace_dir=workspace,
            submission_id=submission_id,
            wait_for_settlement=True,
            max_repair_replies=2,
            repair_reply_content=f'{prefix} Still claiming [secret.go:3 "SecretToken"].',
            engine_provenance=sidecar.provenance,
            run_manifest=manifest,
            poll_timeout=120.0,
        )
        after = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
        delta = after.delta_observed(before)
        _, closeouts = extract_product_observations(sidecar.store_db_path, sid)
        last_grounding = _unwrap_closeout_grounding(closeouts)
        fu = delta.get("friction_units")
        if not after.is_complete():
            divergences.append(f"d10_incomplete:{after.unknown_counters()}")
        if (fu is None or fu < 1) and last_grounding.get("traced") is True:
            divergences.append(f"search_hit_granted_read_proof:friction_units={fu},traced={last_grounding.get('traced')}")
    else:
        _answer_tool(client, sid, "command", {"command": "cat -n gate.go"})
        snap_before = _snapshot_evidence_digests(sidecar.store_db_path, sid)
        _answer_tool(client, sid, "recall", {"query": "Verify"})
        snap_after_1 = _snapshot_evidence_digests(sidecar.store_db_path, sid)
        if selector == "G05-04-rebuild-projection-idempotent":
            _answer_tool(client, sid, "recall", {"query": "Verify"})
        snap_after_2 = _snapshot_evidence_digests(sidecar.store_db_path, sid)

        close_req = client.wait_pending(sid, timeout=120.0)
        client.respond(close_req["id"], content=f"{prefix} Projection verified [read#1:3].")
        client.wait_settled(sid, submission_id=submission_id, timeout=90.0)

        after = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
        delta = after.delta_observed(before)
        fu = delta.get("friction_units")
        extra = {
            "evidence_count_before": len(snap_before),
            "evidence_count_after_1": len(snap_after_1),
            "evidence_count_after_2": len(snap_after_2),
        }
        if not after.is_complete():
            divergences.append(f"d10_incomplete:{after.unknown_counters()}")
        if fu is None or fu != 0:
            divergences.append(f"projection_search_incurred_friction:friction_units={fu}")
        # Verify prior evidence records were never mutated by projection/recall
        by_handle_before = {r["handle"]: r["sha256"] for r in snap_before}
        by_handle_after_1 = {r["handle"]: r["sha256"] for r in snap_after_1}
        by_handle_after_2 = {r["handle"]: r["sha256"] for r in snap_after_2}
        for h, sha in by_handle_before.items():
            if by_handle_after_1.get(h) != sha:
                divergences.append(f"evidence_record_mutated_after_recall_1:{h}")
            if by_handle_after_2.get(h) != sha:
                divergences.append(f"evidence_record_mutated_after_recall_2:{h}")

    return {
        "session_id": sid,
        "child_session_id": None,
        "passed": len(divergences) == 0,
        "divergences": divergences,
        "d10_before": before.to_dict(),
        "d10_after": after.to_dict(),
        "d10_delta": delta,
        "worker_report_refusals": None,
        "observed_codes": [],
        **extra,
    }


def evaluate_wording_invariance(slot_results: Sequence[Mapping[str, Any]]) -> list[dict[str, Any]]:
    """Verify D5 wording invariance across all 3 wordings for every slot that ran all 3."""
    by_selector: dict[str, list[Mapping[str, Any]]] = {}
    for row in slot_results:
        by_selector.setdefault(str(row["selector"]), []).append(row)

    invariance_rows: list[dict[str, Any]] = []
    for selector, rows in sorted(by_selector.items()):
        wordings_present = {str(r["wording"]) for r in rows}
        if not set(WORDING_VARIANTS).issubset(wordings_present):
            invariance_rows.append(
                {
                    "selector": selector,
                    "checked": False,
                    "invariant": True,
                    "wordings_present": sorted(wordings_present),
                    "reason": "subset_of_wordings_run",
                }
            )
            continue

        signatures: dict[str, tuple[Any, ...]] = {}
        for r in rows:
            wrr = r.get("worker_report_refusals")
            wrr_sig = (
                (bool(wrr.get("finally_accepted")), int(wrr.get("worker_report_refusals", 0)) > 0)
                if isinstance(wrr, Mapping)
                else None
            )
            signatures[str(r["wording"])] = (
                bool(r["passed"]),
                tuple(sorted(r.get("observed_codes") or ())),
                wrr_sig,
            )
        unique_sigs = set(signatures.values())
        invariance_rows.append(
            {
                "selector": selector,
                "checked": True,
                "invariant": len(unique_sigs) == 1,
                "wordings_present": sorted(wordings_present),
                "signatures": {k: list(v) for k, v in sorted(signatures.items())},
            }
        )
    return invariance_rows


def build_inprocess_overlay_map(product_root: str | pathlib.Path) -> dict[str, str]:
    """Return validated `{target_in_d1: source_in_harness}` map for GB07 test-only Go overlays."""
    prod = pathlib.Path(product_root).resolve()
    grounding_pkg_dir = pathlib.Path(__file__).resolve().parents[2]
    replacements: dict[str, str] = {}
    for target_rel, source_rel in INPROCESS_OVERLAYS:
        src = (grounding_pkg_dir / source_rel).resolve()
        if not src.is_file():
            raise FileNotFoundError(f"GB07 Go overlay source not found: {src}")
        target = prod / target_rel
        replacements[str(target)] = str(src)
    return validate_test_only_overlay(replacements)


def run_gb07_inprocess(
    *,
    product_root: str | pathlib.Path,
    out_dir: pathlib.Path,
    dry_run: bool = False,
    timeout: float = 300.0,
) -> dict[str, Any]:
    """Execute the 6 GB07 in-process G04 slots via test-only Go `-overlay` against verified D1."""
    validate_gb07_execution_map()
    out_dir.mkdir(parents=True, exist_ok=True)
    prod = pathlib.Path(product_root).resolve()
    replacements = build_inprocess_overlay_map(prod)

    with tempfile.TemporaryDirectory(prefix="gb07-inproc-") as td:
        overlay_path = write_test_overlay(
            pathlib.Path(td) / "gb07_overlay.json",
            replacements,
            product_root=prod,
        )
        plan = InProcessPlan(
            product_root=prod,
            packages=INPROCESS_PACKAGES,
            run_pattern=INPROCESS_RUN_PATTERN,
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
        "card": "GB07",
        "lane": "in_process",
        "dry_run": dry_run,
        "slots": list(INPROCESS_SLOTS),
        "slot_count": len(INPROCESS_SLOTS),
        "d5_subtest_count": len(INPROCESS_SLOTS) * len(WORDING_VARIANTS),
        "passed": res["returncode"] == 0,
        "returncode": res["returncode"],
        "verified": res.get("verified"),
        "command": res.get("command"),
        "subtests": subtests,
        "stderr": res.get("stderr", ""),
    }
    (out_dir / "gb07-inprocess-summary.json").write_text(
        json.dumps(report, indent=2, sort_keys=True), encoding="utf-8"
    )
    return report


def run_gb07_full_sidecar(
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
    dry_run: bool = False,
) -> dict[str, Any]:
    counts = validate_gb07_execution_map()
    runs = select_gb07_runs(families=families, wordings=wordings, shard=shard, shards=shards)
    out_dir.mkdir(parents=True, exist_ok=True)

    if dry_run:
        div_path = out_dir / "gb07-sidecar-divergences.jsonl"
        div_path.write_text("", encoding="utf-8")
        summary = {
            "card": "GB07",
            "run_id": run_id,
            "dry_run": True,
            "platform": "dry_run",
            "trace_friction": trace_friction,
            "families": list(families) if families else list(GB07_FAMILY_IDS),
            "wordings": list(wordings) if wordings else list(WORDING_VARIANTS),
            "shard": shard,
            "shards": shards,
            "catalog_counts": counts,
            "base_slot_count": len({u.spec.selector for u in runs}),
            "run_count": len(runs),
            "passed_count": 0,
            "failed_count": 0,
            "divergence_count": 0,
            "wording_invariance_failures": 0,
            "passed": True,
            "planned_runs": [
                {
                    "slot_index": u.slot_index,
                    "selector": u.spec.selector,
                    "family_id": u.spec.family_id,
                    "wording": u.wording,
                    "mode": u.exec_spec.mode,
                    "is_worker_slot": u.exec_spec.is_worker_slot,
                }
                for u in runs
            ],
        }
        (out_dir / "gb07-sidecar-summary.json").write_text(
            json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8"
        )
        return summary

    artifacts_dir = out_dir / "artifacts"
    artifacts_dir.mkdir(parents=True, exist_ok=True)

    slot_results: list[dict[str, Any]] = []
    divergences_records: list[dict[str, Any]] = []
    manifest_dict: dict[str, Any] | None = None
    plat = "unknown"

    for run_unit in runs:
        run_tag = f"{run_unit.spec.selector}__{run_unit.wording}"
        case_dir = out_dir / "cases" / run_tag
        workspace = case_dir / "workspace"
        config_dir = case_dir / "config"
        if run_unit.spec.selector == "G03-07-open-artifact-gate-blocks":
            _init_plain_workspace(workspace)
        else:
            _init_git_workspace(
                workspace,
                with_verify_gate_workflow=(run_unit.exec_spec.mode == "freshness"),
            )

        sidecar = IsolatedSidecarProcess(
            engine_bin,
            product_root=product_root,
            config_dir=config_dir,
            workspace_dir=workspace,
            expected_sha256=expected_engine_sha256,
            trace_friction=trace_friction,
        )

        with sidecar as client:
            manifest = sidecar.build_run_manifest()
            manifest_dict = manifest.to_dict()
            plat = "macos" if sidecar.provenance.platform == "darwin" else "linux"
            pid = client.create_project(workspace)["id"]
            trace_log = sidecar.log_path if trace_friction else None

            try:
                if run_unit.exec_spec.mode == "freshness":
                    outcome = _run_freshness_slot(
                        client,
                        project_id=pid,
                        sidecar=sidecar,
                        workspace=workspace,
                        unit=run_unit,
                        trace_log=trace_log,
                    )
                elif run_unit.exec_spec.mode == "proof_layer":
                    outcome = _run_proof_layer_slot(
                        client,
                        project_id=pid,
                        sidecar=sidecar,
                        workspace=workspace,
                        manifest=manifest,
                        unit=run_unit,
                        trace_log=trace_log,
                    )
                elif run_unit.exec_spec.mode == "gate":
                    outcome = _run_gate_slot(
                        client,
                        project_id=pid,
                        sidecar=sidecar,
                        workspace=workspace,
                        manifest=manifest,
                        unit=run_unit,
                        trace_log=trace_log,
                    )
                elif run_unit.exec_spec.mode == "friction_breaker":
                    outcome = _run_friction_breaker_slot(
                        client,
                        project_id=pid,
                        sidecar=sidecar,
                        workspace=workspace,
                        manifest=manifest,
                        unit=run_unit,
                        trace_log=trace_log,
                    )
                else:
                    outcome = _run_search_projection_slot(
                        client,
                        project_id=pid,
                        sidecar=sidecar,
                        workspace=workspace,
                        manifest=manifest,
                        unit=run_unit,
                        trace_log=trace_log,
                    )
            except Exception as exc:  # noqa: BLE001 - record runtime/D1 errors cleanly without aborting shard
                outcome = {
                    "session_id": "",
                    "child_session_id": None,
                    "passed": False,
                    "divergences": [f"runner_exception:{type(exc).__name__}:{exc}"],
                    "d10_before": None,
                    "d10_after": None,
                    "d10_delta": None,
                    "worker_report_refusals": None,
                    "observed_codes": [],
                }

        artifact_path = artifacts_dir / f"{run_tag}.json"
        artifact_path.write_text(json.dumps(outcome, indent=2, sort_keys=True), encoding="utf-8")

        row = {
            "slot_index": run_unit.slot_index,
            "selector": run_unit.spec.selector,
            "family_id": run_unit.spec.family_id,
            "wording": run_unit.wording,
            "mode": run_unit.exec_spec.mode,
            "is_worker_slot": run_unit.exec_spec.is_worker_slot,
            "passed": bool(outcome["passed"]),
            "divergences": list(outcome.get("divergences") or ()),
            "d10_before": outcome.get("d10_before"),
            "d10_after": outcome.get("d10_after"),
            "d10_delta": outcome.get("d10_delta"),
            # Decision 045: side-by-side worker_report_refusals, NEVER summed with friction_units
            "worker_report_refusals": outcome.get("worker_report_refusals"),
            "observed_codes": list(outcome.get("observed_codes") or ()),
            "artifact_path": str(artifact_path),
        }
        slot_results.append(row)
        if not row["passed"]:
            divergences_records.append(
                {
                    "kind": "slot_divergence",
                    "selector": row["selector"],
                    "family_id": row["family_id"],
                    "wording": row["wording"],
                    "divergences": row["divergences"],
                    "d10_delta": row["d10_delta"],
                    "worker_report_refusals": row["worker_report_refusals"],
                    "observed_codes": row["observed_codes"],
                }
            )

    invariance = evaluate_wording_invariance(slot_results)
    invariance_failures = [inv for inv in invariance if inv["checked"] and not inv["invariant"]]
    for inv in invariance_failures:
        divergences_records.append(
            {
                "kind": "d5_wording_invariance_divergence",
                "selector": inv["selector"],
                "signatures": inv["signatures"],
            }
        )

    div_path = out_dir / "gb07-sidecar-divergences.jsonl"
    with div_path.open("w", encoding="utf-8") as fh:
        for rec in divergences_records:
            fh.write(json.dumps(rec, sort_keys=True) + "\n")

    passed_count = sum(1 for r in slot_results if r["passed"])
    summary = {
        "card": "GB07",
        "run_id": run_id,
        "dry_run": False,
        "platform": plat,
        "trace_friction": trace_friction,
        "families": list(families) if families else list(GB07_FAMILY_IDS),
        "wordings": list(wordings) if wordings else list(WORDING_VARIANTS),
        "shard": shard,
        "shards": shards,
        "base_slot_count": len({r["selector"] for r in slot_results}),
        "run_count": len(slot_results),
        "passed_count": passed_count,
        "failed_count": len(slot_results) - passed_count,
        "divergence_count": len(divergences_records),
        "wording_invariance_failures": len(invariance_failures),
        "passed": passed_count == len(slot_results) and len(invariance_failures) == 0,
        "decision_045_note": "friction_units and worker_report_refusals are reported side by side and NEVER summed.",
        "run_manifest": manifest_dict,
        "wording_invariance": invariance,
        "slots": slot_results,
    }
    (out_dir / "gb07-sidecar-summary.json").write_text(
        json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8"
    )
    return summary


def build_arg_parser() -> argparse.ArgumentParser:
    ap = argparse.ArgumentParser(
        description="GB07 full execution runner (28 sidecar x 3 D5 wordings + 6 in-process x 3 D5 wordings = 102)"
    )
    ap.add_argument("--engine-bin", default="")
    ap.add_argument("--product-root", required=True)
    ap.add_argument("--expected-engine-sha256", default="")
    ap.add_argument("--out-dir", required=True)
    ap.add_argument("--run-id", default="20261127-3bb1464d-gb07-full")
    ap.add_argument("--families", default=",".join(GB07_FAMILY_IDS))
    ap.add_argument("--wordings", default=",".join(WORDING_VARIANTS))
    ap.add_argument("--shard", type=int, default=0)
    ap.add_argument("--shards", type=int, default=1)
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--inprocess-only", action="store_true")
    ap.add_argument("--sidecar-only", action="store_true")
    ap.add_argument("--no-trace-friction", action="store_true")
    return ap


def parse_and_validate_args(argv: list[str] | None = None) -> argparse.Namespace:
    ap = build_arg_parser()
    args = ap.parse_args(argv)
    if args.inprocess_only and args.sidecar_only:
        ap.error("--inprocess-only and --sidecar-only are mutually exclusive")
    if not args.inprocess_only and not args.dry_run and not args.engine_bin:
        ap.error("--engine-bin is required unless --inprocess-only or --dry-run is set")
    families = [f.strip() for f in args.families.split(",") if f.strip()]
    wordings = [w.strip() for w in args.wordings.split(",") if w.strip()]
    # Validate shard/shards/families/wordings up front
    try:
        select_gb07_runs(families=families, wordings=wordings, shard=args.shard, shards=args.shards)
    except ValueError as exc:
        ap.error(str(exc))
    args.parsed_families = families
    args.parsed_wordings = wordings
    return args


def main(argv: list[str] | None = None) -> int:
    args = parse_and_validate_args(argv)
    out_dir = pathlib.Path(args.out_dir)

    if args.inprocess_only:
        report = run_gb07_inprocess(
            product_root=args.product_root,
            out_dir=out_dir,
            dry_run=args.dry_run,
        )
        print(json.dumps(report, indent=2, sort_keys=True))
        return 0 if report["passed"] else 1

    summary = run_gb07_full_sidecar(
        engine_bin=args.engine_bin,
        product_root=args.product_root,
        expected_engine_sha256=args.expected_engine_sha256,
        out_dir=out_dir,
        run_id=args.run_id,
        families=args.parsed_families,
        wordings=args.parsed_wordings,
        shard=args.shard,
        shards=args.shards,
        trace_friction=not args.no_trace_friction,
        dry_run=args.dry_run,
    )

    inprocess_ok = True
    if not args.sidecar_only and "G04" in args.parsed_families and args.shard == 0:
        inproc = run_gb07_inprocess(
            product_root=args.product_root,
            out_dir=out_dir,
            dry_run=args.dry_run,
        )
        summary["inprocess"] = inproc
        inprocess_ok = bool(inproc["passed"])
        (out_dir / "gb07-sidecar-summary.json").write_text(
            json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8"
        )

    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0 if (summary["passed"] and inprocess_ok) else 1


if __name__ == "__main__":
    sys.exit(main())
