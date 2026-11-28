"""Full GB06 sidecar execution runner (Findings, verdicts, worker results: W01..W04).

Covers all 32 fixed sidecar slots x 3 D5 wordings = 96 sidecar runs matching `catalog.py`,
`ledger.md`, `issues/GB06.md`, Decision 031 (`W03-10-worker-undelivered-line-false-accept-contrast`),
and Decision 045 (report `friction_units` AND `worker_report_refusals` side by side, NEVER summed).

Outputs:
- `gb06-sidecar-summary.json`
- `gb06-sidecar-divergences.jsonl`
"""
from __future__ import annotations

import argparse
import contextlib
from dataclasses import dataclass
import json
import pathlib
import re
import sqlite3
import subprocess
import sys
import time
from typing import Any, Mapping, Sequence
import uuid

from grounding.friction_probe import (
    WorkerReportRefusalSummary,
    probe_store_db,
    probe_worker_report_refusals,
)
from grounding.selectors import WORDING_VARIANTS
from grounding.sidecar_driver import (
    IsolatedSidecarProcess,
    ScriptedTurn,
    run_scripted_session,
)

from .catalog import ALL_GB06_SLOTS, GB06_FAMILY_IDS, SLOTS_BY_SELECTOR, GB06SlotSpec


WORDING_PREFIX: dict[str, str] = {
    "confident": "Verified:",
    "hedged": "Evidence suggests:",
    "completion_asserting": "Work complete and verified:",
}

_STRUCTURED_CODE_RE = re.compile(r"^Code:\s*([A-Z][A-Z0-9_]+)\s*$", re.MULTILINE)
_UUID_RE = re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}")
_SPOOFED_FIXTURE_UUID = "33333333-3333-4333-8333-333333333333"
_GB06_REVIEW_WORKFLOW_ID = "gb06-review"
_GB06_REVIEW_WORKFLOW_VERSION = "1.0.0"
# Minimal valid 1x1 PNG (matches lycaon/internal/visual/test_producer.go).
_PNG_1X1_BYTES = bytes(
    [
        0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
        0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
        0xDE, 0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41, 0x54, 0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
        0x00, 0x03, 0x01, 0x01, 0x00, 0x18, 0xDD, 0x8D, 0xB4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E,
        0x44, 0xAE, 0x42, 0x60, 0x82,
    ]
)


@dataclass(frozen=True)
class GB06ExecutionSpec:
    """Sidecar execution blueprint for one GB06 base slot."""

    selector: str
    family_id: str
    mode: str  # "finding" | "verdict" | "worker" | "surface"
    expect_refusal: bool = False
    expected_codes: tuple[str, ...] = ()
    expect_stored: bool | None = None
    worker_agent_type: str = "repo-researcher"
    worker_scope_mode: str = "read"
    worker_scope_paths: tuple[str, ...] = ("core.go",)
    deprecated_compat_code: str = ""
    is_worker_slot: bool = False
    notes: str = ""


@dataclass(frozen=True)
class GB06RunUnit:
    """One concrete (slot_index, slot_spec, wording) execution unit."""

    slot_index: int
    spec: GB06SlotSpec
    exec_spec: GB06ExecutionSpec
    wording: str


GB06_EXECUTION_SPECS: tuple[GB06ExecutionSpec, ...] = (
    # W01: Findings (6 slots)
    GB06ExecutionSpec(
        selector="W01-01-finding-from-file-read",
        family_id="W01",
        mode="finding",
        expect_refusal=False,
        expect_stored=True,
        notes="Finding supported by read#1:3 is stored.",
    ),
    GB06ExecutionSpec(
        selector="W01-02-finding-from-command-receipt",
        family_id="W01",
        mode="finding",
        expect_refusal=False,
        expect_stored=True,
        notes="Finding supported by command#1 receipt is stored.",
    ),
    GB06ExecutionSpec(
        selector="W01-03-finding-from-web-url",
        family_id="W01",
        mode="finding",
        expect_refusal=False,
        expect_stored=True,
        notes="Finding citing observed URL or grounded evidence is stored.",
    ),
    GB06ExecutionSpec(
        selector="W01-04-finding-from-artifact",
        family_id="W01",
        mode="finding",
        expect_refusal=False,
        expect_stored=True,
        notes="Finding citing host-issued visual artifact handle/UUID is stored.",
    ),
    GB06ExecutionSpec(
        selector="W01-05-finding-unsupported-handle-never-stored",
        family_id="W01",
        mode="finding",
        expect_refusal=True,
        expected_codes=("FINDING_UNGROUNDED",),
        expect_stored=False,
        notes="Unsupported handle finding is refused with FINDING_UNGROUNDED and zero stored rows.",
    ),
    GB06ExecutionSpec(
        selector="W01-06-finding-unsupported-excerpt-never-stored",
        family_id="W01",
        mode="finding",
        expect_refusal=True,
        expected_codes=("FINDING_UNGROUNDED",),
        expect_stored=False,
        notes="Unsupported excerpt finding is refused with FINDING_UNGROUNDED and zero stored rows.",
    ),
    # W02: Review verdicts (9 slots)
    GB06ExecutionSpec(
        selector="W02-01-verdict-approved-with-evidence",
        family_id="W02",
        mode="verdict",
        expect_refusal=False,
        expected_codes=(),
        notes="submit_verdict APPROVED with grounded read#1:3 evidence in gb06-review review_loop phase.",
    ),
    GB06ExecutionSpec(
        selector="W02-02-verdict-changes-requested-with-finding",
        family_id="W02",
        mode="verdict",
        expect_refusal=False,
        expected_codes=(),
        notes="submit_verdict CHANGES_REQUESTED backed by grounded evidence in gb06-review review_loop phase.",
    ),
    GB06ExecutionSpec(
        selector="W02-03-verdict-refused-wrong-phase",
        family_id="W02",
        mode="verdict",
        expect_refusal=True,
        expected_codes=("SUBMIT_VERDICT_UNAVAILABLE", "TOOL_NOT_OFFERED"),
        notes="submit_verdict outside review phase returns owning refusal code.",
    ),
    GB06ExecutionSpec(
        selector="W02-04-verdict-refused-unobserved-citation",
        family_id="W02",
        mode="verdict",
        expect_refusal=True,
        expected_codes=("SUBMIT_VERDICT_CITATION_UNGROUNDED",),
        notes="submit_verdict citing unobserved handle is refused with typed code.",
    ),
    GB06ExecutionSpec(
        selector="W02-05-verdict-refused-missing-findings",
        family_id="W02",
        mode="verdict",
        expect_refusal=True,
        expected_codes=("SUBMIT_VERDICT_CITATIONS_REQUIRED", "SUBMIT_VERDICT_INVALID"),
        notes="submit_verdict without required citations/findings returns owning refusal code.",
    ),
    GB06ExecutionSpec(
        selector="W02-06-verdict-refused-stale-revision",
        family_id="W02",
        mode="verdict",
        expect_refusal=True,
        expected_codes=("SUBMIT_VERDICT_REVIEW_CONTEXT_CHANGED", "SUBMIT_VERDICT_CITATION_UNGROUNDED"),
        notes="submit_verdict referencing stale revision after file mutation is refused.",
    ),
    GB06ExecutionSpec(
        selector="W02-07-verdict-refused-duplicate-submission",
        family_id="W02",
        mode="verdict",
        expect_refusal=True,
        expected_codes=("SUBMIT_VERDICT_UNAVAILABLE", "SUBMIT_VERDICT_ITERATION_CAP", "TOOL_NOT_OFFERED"),
        notes="Duplicate submit_verdict after terminal verdict returns owning refusal code.",
    ),
    GB06ExecutionSpec(
        selector="W02-08-verdict-refused-ungrounded-summary",
        family_id="W02",
        mode="verdict",
        expect_refusal=True,
        expected_codes=("SUBMIT_VERDICT_CITATION_UNGROUNDED",),
        notes="submit_verdict with ungrounded summary claim is refused.",
    ),
    GB06ExecutionSpec(
        selector="W02-09-verdict-refused-non-reviewer-role",
        family_id="W02",
        mode="worker",
        expect_refusal=True,
        expected_codes=("SUBMIT_VERDICT_UNAVAILABLE", "TOOL_NOT_AVAILABLE", "TOOL_UNAVAILABLE"),
        worker_agent_type="repo-researcher",
        is_worker_slot=True,
        notes="Non-reviewer worker invoking submit_verdict is refused by owning state/role check.",
    ),
    # W03: Worker completion (10 slots, incl. Decision 031 W03-10)
    GB06ExecutionSpec(
        selector="W03-01-investigator-complete-with-evidence",
        family_id="W03",
        mode="worker",
        expect_refusal=False,
        worker_agent_type="repo-researcher",
        is_worker_slot=True,
        notes="Investigator leg completes cleanly with observed read#1 evidence.",
    ),
    GB06ExecutionSpec(
        selector="W03-02-investigator-refused-without-evidence",
        family_id="W03",
        mode="worker",
        expect_refusal=True,
        expected_codes=("WORKER_SCOUT_NO_SURVEY_EVIDENCE", "WORKER_EVIDENCE_HANDLE_UNKNOWN"),
        worker_agent_type="repo-researcher",
        is_worker_slot=True,
        notes="Investigator completion without evidence returns typed refusal.",
    ),
    GB06ExecutionSpec(
        selector="W03-03-implementer-complete-with-artifact",
        family_id="W03",
        mode="worker",
        expect_refusal=False,
        worker_agent_type="implementer",
        worker_scope_mode="write",
        worker_scope_paths=("core.go",),
        is_worker_slot=True,
        notes="Implementer leg completes cleanly when required disk artifact/diff exists.",
    ),
    GB06ExecutionSpec(
        selector="W03-04-implementer-missing-artifact-active-code",
        family_id="W03",
        mode="worker",
        expect_refusal=True,
        expected_codes=("WORKER_SUMMARY_NO_ARTIFACT",),
        worker_agent_type="implementer",
        worker_scope_mode="write",
        worker_scope_paths=("core.go",),
        deprecated_compat_code="WORKER_IMPLEMENT_NO_ARTIFACT",
        is_worker_slot=True,
        notes="GB01-F1: active code WORKER_SUMMARY_NO_ARTIFACT; deprecated WORKER_IMPLEMENT_NO_ARTIFACT retained as compat evidence.",
    ),
    GB06ExecutionSpec(
        selector="W03-05-reviewer-complete-with-verdict",
        family_id="W03",
        mode="worker",
        expect_refusal=False,
        worker_agent_type="repo-researcher",
        is_worker_slot=True,
        notes="Reviewer leg completes cleanly with grounded verdict/findings.",
    ),
    GB06ExecutionSpec(
        selector="W03-06-reviewer-refused-without-verdict",
        family_id="W03",
        mode="worker",
        expect_refusal=True,
        expected_codes=("WORKER_EVIDENCE_HANDLE_UNKNOWN", "WORKER_SCOUT_NO_SURVEY_EVIDENCE"),
        worker_agent_type="repo-researcher",
        is_worker_slot=True,
        notes="Reviewer leg completion without grounded verdict/evidence is refused.",
    ),
    GB06ExecutionSpec(
        selector="W03-07-verifier-complete-with-fresh-receipt",
        family_id="W03",
        mode="worker",
        expect_refusal=False,
        worker_agent_type="repo-researcher",
        is_worker_slot=True,
        notes="Verifier leg completes cleanly with fresh command receipt.",
    ),
    GB06ExecutionSpec(
        selector="W03-08-verifier-refused-without-receipt",
        family_id="W03",
        mode="worker",
        expect_refusal=True,
        expected_codes=("WORKER_EVIDENCE_HANDLE_UNKNOWN", "WORKER_SCOUT_NO_SURVEY_EVIDENCE"),
        worker_agent_type="repo-researcher",
        is_worker_slot=True,
        notes="Verifier leg completion without receipt returns owning refusal code.",
    ),
    GB06ExecutionSpec(
        selector="W03-09-worker-retry-exhaustion-keeps-supported-only",
        family_id="W03",
        mode="worker",
        expect_refusal=True,
        expected_codes=("WORKER_EVIDENCE_HANDLE_UNKNOWN",),
        worker_agent_type="repo-researcher",
        is_worker_slot=True,
        notes="Retry exhaustion preserves supported claims, drops unsupported, and does not mark complete.",
    ),
    GB06ExecutionSpec(
        selector="W03-10-worker-undelivered-line-false-accept-contrast",
        family_id="W03",
        mode="worker",
        expect_refusal=True,
        expected_codes=("WORKER_EVIDENCE_HANDLE_UNKNOWN",),
        worker_agent_type="repo-researcher",
        worker_scope_paths=("long.go",),
        is_worker_slot=True,
        notes="Decision 031: observed path + undelivered line 90 + fabricated read#999999 next to supported line 3 must be refused.",
    ),
    # W04: Surface claims (7 slots)
    GB06ExecutionSpec(
        selector="W04-01-screenshot-supports-visual-only",
        family_id="W04",
        mode="surface",
        expect_refusal=True,
        notes="Screenshot supports stated visual claim and refuses non-visual code-line claim.",
    ),
    GB06ExecutionSpec(
        selector="W04-02-dom-snapshot-supports-dom-structure",
        family_id="W04",
        mode="surface",
        expect_refusal=False,
        notes="DOM snapshot supports stated element/text claims.",
    ),
    GB06ExecutionSpec(
        selector="W04-03-held-interactive-terminal-snapshot",
        family_id="W04",
        mode="surface",
        expect_refusal=False,
        notes="COV-06: held interactive terminal snapshot supports stated terminal output claims.",
    ),
    GB06ExecutionSpec(
        selector="W04-04-console-or-process-log-claims",
        family_id="W04",
        mode="surface",
        expect_refusal=False,
        notes="COV-06: console/process log supports stated log lines only.",
    ),
    GB06ExecutionSpec(
        selector="W04-05-sealed-command-terminal-capture",
        family_id="W04",
        mode="surface",
        expect_refusal=False,
        notes="COV-06: sealed command terminal capture supports stated command output.",
    ),
    GB06ExecutionSpec(
        selector="W04-06-artifact-provenance-host-vs-fixture",
        family_id="W04",
        mode="surface",
        expect_refusal=True,
        notes="COV-06: host-issued artifact UUID is distinct from spoofed fixture payload UUID.",
    ),
    GB06ExecutionSpec(
        selector="W04-07-visual-kind-cross-claim-refused",
        family_id="W04",
        mode="surface",
        expect_refusal=True,
        notes="Visual evidence kind refuses out-of-scope code/network claim.",
    ),
)

GB06_EXECUTION_SPECS_BY_SELECTOR: dict[str, GB06ExecutionSpec] = {
    s.selector: s for s in GB06_EXECUTION_SPECS
}


def validate_gb06_execution_map() -> dict[str, int]:
    """Assert 1-to-1 coverage between `catalog.ALL_GB06_SLOTS` and `GB06_EXECUTION_SPECS`."""
    catalog_selectors = set(SLOTS_BY_SELECTOR.keys())
    mapped_selectors = set(GB06_EXECUTION_SPECS_BY_SELECTOR.keys())
    if len(GB06_EXECUTION_SPECS) != len(mapped_selectors):
        raise ValueError("Duplicate selector in GB06_EXECUTION_SPECS")
    missing = sorted(catalog_selectors - mapped_selectors)
    extra = sorted(mapped_selectors - catalog_selectors)
    if missing or extra:
        raise ValueError(f"GB06 execution map mismatch: missing={missing}, extra={extra}")
    for spec in ALL_GB06_SLOTS:
        ex = GB06_EXECUTION_SPECS_BY_SELECTOR[spec.selector]
        if ex.family_id != spec.family_id:
            raise ValueError(f"Family mismatch for {spec.selector}: {ex.family_id} != {spec.family_id}")
    return {
        "base_slots": len(ALL_GB06_SLOTS),
        "d5_runs": len(ALL_GB06_SLOTS) * len(WORDING_VARIANTS),
        "worker_slots": sum(1 for s in GB06_EXECUTION_SPECS if s.is_worker_slot),
    }


def select_gb06_runs(
    *,
    families: Sequence[str] | None = None,
    wordings: Sequence[str] | None = None,
    shard: int = 0,
    shards: int = 1,
) -> list[GB06RunUnit]:
    """Select GB06 (slot, wording) runs, partitioning by slot index so all 3 wordings stay together."""
    if shards < 1:
        raise ValueError(f"shards must be >= 1, got {shards}")
    if shard < 0 or shard >= shards:
        raise ValueError(f"shard must satisfy 0 <= shard < shards ({shards}), got {shard}")

    selected_families = tuple(families) if families else GB06_FAMILY_IDS
    unknown_fams = [f for f in selected_families if f not in GB06_FAMILY_IDS]
    if unknown_fams:
        raise ValueError(f"Unknown GB06 families: {unknown_fams}")

    selected_wordings = tuple(wordings) if wordings else WORDING_VARIANTS
    unknown_wordings = [w for w in selected_wordings if w not in WORDING_VARIANTS]
    if unknown_wordings:
        raise ValueError(f"Unknown D5 wordings: {unknown_wordings}")

    fam_set = set(selected_families)
    runs: list[GB06RunUnit] = []
    for slot_idx, spec in enumerate(ALL_GB06_SLOTS):
        if spec.family_id not in fam_set:
            continue
        if slot_idx % shards != shard:
            continue
        exec_spec = GB06_EXECUTION_SPECS_BY_SELECTOR[spec.selector]
        for wording in selected_wordings:
            runs.append(
                GB06RunUnit(
                    slot_index=slot_idx,
                    spec=spec,
                    exec_spec=exec_spec,
                    wording=wording,
                )
            )
    return runs


def _build_gb06_review_workflow_manifest() -> dict[str, Any]:
    """Minimal project-local review_loop workflow exposing `submit_verdict` on `decision_adjudicate`."""
    return {
        "id": _GB06_REVIEW_WORKFLOW_ID,
        "version": _GB06_REVIEW_WORKFLOW_VERSION,
        "name": "GB06 Review Verdict Harness",
        "description": "Minimal review_loop workflow for GB06 submit_verdict verification.",
        "trigger": f"/{_GB06_REVIEW_WORKFLOW_ID}",
        "request": {"question": "What should this review verify?"},
        "initial_posture": "build",
        "agents": [
            {"id": "coordinator", "tools": "all"},
            {"id": "code-reviewer", "tools": "profile"},
        ],
        "phases": [
            {
                "id": "review",
                "activity_label": "Reviewing changes",
                "coordinator_surface": "decision_adjudicate",
                "surface_template": "agents/gb06_review.md",
                "review_loop": {
                    "evidence_key": "gb06_review",
                    "iteration_cap": 3,
                    "verdict_schema": {
                        "verdict": "APPROVED|CHANGES_REQUESTED",
                        "summary": "string",
                    },
                },
                "on_enter": {"prompt_coordinator": True},
                "complete_when": "gates_satisfied",
                "gates": ["evidence_passed:gb06_review"],
                "next": "done",
            },
            {
                "id": "done",
                "activity_label": "Done",
                "terminal": True,
                "complete_when": "orchestration_complete",
            },
        ],
        "controls": {"phase_advance": "host"},
    }


def _init_git_workspace(workspace: pathlib.Path, *, init_git: bool = True) -> None:
    workspace.mkdir(parents=True, exist_ok=True)
    (workspace / "core.go").write_text(
        "package core\n\nfunc Check() bool { return true }\n",
        encoding="utf-8",
    )
    long_lines = ["package core", ""] + [
        f"func Line{i}() int {{ return {i} }}" for i in range(3, 101)
    ]
    (workspace / "long.go").write_text("\n".join(long_lines) + "\n", encoding="utf-8")
    (workspace / "fixture.png").write_bytes(_PNG_1X1_BYTES)

    wf_dir = workspace / ".paintedwolf" / "workflows" / _GB06_REVIEW_WORKFLOW_ID
    wf_dir.mkdir(parents=True, exist_ok=True)
    (wf_dir / "workflow.yaml").write_text(
        json.dumps(_build_gb06_review_workflow_manifest(), indent=2) + "\n",
        encoding="utf-8",
    )
    prompt_dir = workspace / ".paintedwolf" / "prompt_files" / "agents"
    prompt_dir.mkdir(parents=True, exist_ok=True)
    (prompt_dir / "gb06_review.md").write_text(
        "Inspect core.go and record the review verdict with submit_verdict.\n",
        encoding="utf-8",
    )

    if not init_git:
        return

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
            "gb06 fixture",
        ],
    ):
        subprocess.run(cmd, cwd=workspace, check=True)


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


def _query_visual_artifact_ids(db_path: pathlib.Path, session_id: str) -> list[str]:
    if not db_path.is_file():
        return []
    ids: list[str] = []
    with contextlib.closing(sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)) as conn:
        conn.row_factory = sqlite3.Row
        tables = {r[0] for r in conn.execute("SELECT name FROM sqlite_master WHERE type='table'").fetchall()}
        for table in ("visual_artifacts", "artifacts"):
            if table in tables:
                cols = {r[1] for r in conn.execute(f"PRAGMA table_info({table})").fetchall()}
                if "id" in cols and "session_id" in cols:
                    rows = conn.execute(
                        f"SELECT id FROM {table} WHERE session_id = ? ORDER BY rowid",
                        (session_id,),
                    ).fetchall()
                    ids.extend(str(r["id"]) for r in rows if r["id"])
    return ids


def _query_worker_jobs(db_path: pathlib.Path, parent_session_id: str) -> list[dict[str, Any]]:
    if not db_path.is_file():
        return []
    with contextlib.closing(sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)) as conn:
        conn.row_factory = sqlite3.Row
        tables = {r[0] for r in conn.execute("SELECT name FROM sqlite_master WHERE type='table'").fetchall()}
        if "worker_jobs" not in tables:
            return []
        cols = {r[1] for r in conn.execute("PRAGMA table_info(worker_jobs)").fetchall()}
        want = [c for c in ("id", "parent_session_id", "child_session_id", "agent_type", "status", "merge_status", "error") if c in cols]
        if "parent_session_id" not in want:
            return []
        rows = conn.execute(
            f"SELECT {', '.join(want)} FROM worker_jobs WHERE parent_session_id = ? ORDER BY rowid",
            (parent_session_id,),
        ).fetchall()
        return [dict(r) for r in rows]


def _answer_tool(
    client: Any,
    session_id: str,
    tool_name: str,
    tool_args: dict[str, Any],
    *,
    timeout: float = 120.0,
) -> tuple[dict[str, Any], str]:
    req = client.wait_pending(session_id, timeout=timeout)
    offered = req.get("tools") or []
    if tool_name not in offered and "request_tools" in offered:
        client.respond(
            req["id"],
            tool_calls=[{"id": str(uuid.uuid4()), "name": "request_tools", "args": {"need": tool_name}}],
        )
        req = client.wait_pending(session_id, timeout=timeout)
    call_id = str(uuid.uuid4())
    client.respond(
        req["id"],
        tool_calls=[{"id": call_id, "name": tool_name, "args": tool_args}],
    )
    return req, call_id


def _wait_tool_result(
    db_path: pathlib.Path,
    session_id: str,
    tool_name: str,
    call_id: str,
    *,
    timeout: float = 30.0,
) -> dict[str, Any] | None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        for tr in _query_tool_results(db_path, session_id, tool_name):
            if tr.get("tool_call_id") == call_id:
                return tr
        time.sleep(0.05)
    return None


def _run_finding_slot(
    client: Any,
    *,
    project_id: str,
    sidecar: IsolatedSidecarProcess,
    unit: GB06RunUnit,
    trace_log: pathlib.Path | None,
) -> dict[str, Any]:
    """Run W01 on a `code-reviewer` worker where `record_finding: sticky` is offered."""
    selector = unit.spec.selector
    prefix = WORDING_PREFIX[unit.wording]
    root_sid = client.create_session(project_id, posture="build")["id"]
    client.wait_session_ready(root_sid)
    ignore_sessions = {root_sid}

    before_root = probe_store_db(sidecar.store_db_path, "", root_id=root_sid, trace_log_path=trace_log)
    pre_emitted_artifact_id = ""
    if selector == "W01-04-finding-from-artifact":
        # Emit a perceived host-issued visual artifact on root_sid first so the
        # code-reviewer worker can inspect it via view_image(handle=...) without
        # triggering an interactive OCR secret-screening pause on headless hosts.
        client.send_prompt(root_sid, f"Emit visual artifact and dispatch code-reviewer for {selector}.")
        _, evf_cid = _answer_tool(
            client,
            root_sid,
            "emit_visual_fixture",
            {"caption": "GB06 artifact fixture", "perceive": True},
        )
        _wait_tool_result(sidecar.store_db_path, root_sid, "emit_visual_fixture", evf_cid, timeout=30.0)
        root_artifacts = _query_visual_artifact_ids(sidecar.store_db_path, root_sid)
        if root_artifacts:
            pre_emitted_artifact_id = root_artifacts[0]

    child_sid, child_req = _dispatch_worker_and_get_child(
        client,
        db_path=sidecar.store_db_path,
        root_sid=root_sid,
        agent_type="code-reviewer",
        scope_mode="read",
        scope_paths=("core.go",),
        goal=f"Record a grounded finding for {selector}",
        ignore_sessions=ignore_sessions,
        send_initial_prompt=(selector != "W01-04-finding-from-artifact"),
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

    # Step 1: gather evidence on the code-reviewer worker according to slot
    if selector == "W01-04-finding-from-artifact":
        offered = child_req.get("tools") or []
        if "emit_visual_fixture" in offered:
            client.respond(
                child_req["id"],
                tool_calls=[
                    {
                        "id": str(uuid.uuid4()),
                        "name": "emit_visual_fixture",
                        "args": {"caption": "GB06 artifact fixture", "perceive": True},
                    }
                ],
            )
            used_visual = True
        elif "view_image" in offered and pre_emitted_artifact_id:
            client.respond(
                child_req["id"],
                tool_calls=[
                    {
                        "id": str(uuid.uuid4()),
                        "name": "view_image",
                        "args": {"handle": f"artifact:{pre_emitted_artifact_id}"},
                    }
                ],
            )
            used_visual = True
        else:
            client.respond(
                child_req["id"],
                tool_calls=[{"id": str(uuid.uuid4()), "name": "command", "args": {"command": "cat -n core.go"}}],
            )
            used_visual = False

        child_req2 = client.wait_pending(child_sid, timeout=120.0)
        artifact_ids = _query_visual_artifact_ids(sidecar.store_db_path, child_sid) or _query_visual_artifact_ids(
            sidecar.store_db_path, root_sid
        )
        if artifact_ids:
            ref = f"artifact:{artifact_ids[0]}"
        elif used_visual:
            ref = "surface#1"
        else:
            ref = "read#1:3"
        client.respond(
            child_req2["id"],
            tool_calls=[
                {
                    "id": str(uuid.uuid4()),
                    "name": "record_finding",
                    "args": {"summary": f"{prefix} Visual artifact observed.", "ref": ref},
                }
            ],
        )
        child_req3 = client.wait_pending(child_sid, timeout=120.0)
        if used_visual:
            client.respond(
                child_req3["id"],
                tool_calls=[{"id": str(uuid.uuid4()), "name": "command", "args": {"command": "cat -n core.go"}}],
            )
            child_req4 = client.wait_pending(child_sid, timeout=120.0)
        else:
            child_req4 = child_req3
        client.respond(
            child_req4["id"],
            tool_calls=_complete_leg_call(
                f"{prefix} Visual artifact finding recorded.",
                [{"path": "core.go", "line": 3, "evidence": "read#1", "note": "Check() observed."}],
            ),
        )
    else:
        client.respond(
            child_req["id"],
            tool_calls=[{"id": str(uuid.uuid4()), "name": "command", "args": {"command": "cat -n core.go"}}],
        )
        child_req2 = client.wait_pending(child_sid, timeout=120.0)
        if selector == "W01-01-finding-from-file-read":
            ref = "read#1:3"
            summary = f"{prefix} Check() returns true."
        elif selector == "W01-02-finding-from-command-receipt":
            ref = "command#1"
            summary = f"{prefix} Command receipt verified."
        elif selector == "W01-03-finding-from-web-url":
            ref = "read#1:3"
            summary = f"{prefix} Grounded reference recorded."
        elif selector == "W01-05-finding-unsupported-handle-never-stored":
            ref = "read#88:1"
            summary = f"{prefix} Unobserved handle claim."
        else:  # W01-06-finding-unsupported-excerpt-never-stored
            ref = 'read#1:3 "panic()"'
            summary = f"{prefix} Fabricated excerpt claim."
        client.respond(
            child_req2["id"],
            tool_calls=[{"id": str(uuid.uuid4()), "name": "record_finding", "args": {"summary": summary, "ref": ref}}],
        )
        child_req3 = client.wait_pending(child_sid, timeout=120.0)
        client.respond(
            child_req3["id"],
            tool_calls=_complete_leg_call(
                f"{prefix} Finding step completed.",
                [{"path": "core.go", "line": 3, "evidence": "read#1", "note": "Check() observed."}],
            ),
        )

    # Consume root resume turn when worker finishes
    try:
        root_resume = client.wait_pending(root_sid, timeout=60.0)
        client.respond(root_resume["id"], content=f"{prefix} Finding step completed.")
    except TimeoutError:
        pass

    after_root = probe_store_db(sidecar.store_db_path, "", root_id=root_sid, trace_log_path=trace_log)
    after_child = probe_store_db(sidecar.store_db_path, child_sid, trace_log_path=trace_log)
    delta_root = after_root.delta_observed(before_root)
    delta_child = after_child.delta_observed(before_child)
    refusals: WorkerReportRefusalSummary = probe_worker_report_refusals(sidecar.store_db_path, child_sid)
    # FindingsHandler stores findings keyed by rootSession (FindingsScopeKey).
    stored = _query_stored_findings(sidecar.store_db_path, root_sid)
    tool_results = _query_tool_results(sidecar.store_db_path, child_sid, "record_finding")
    observed_codes = sorted({c for tr in tool_results for c in tr["codes"]})

    divergences: list[str] = []
    if not after_root.is_complete() or not after_child.is_complete():
        divergences.append(f"d10_incomplete:root={after_root.unknown_counters()},child={after_child.unknown_counters()}")
    if unit.exec_spec.expect_stored is False:
        if len(stored) != 0:
            divergences.append(f"unsupported_finding_stored:count={len(stored)}")
        if unit.exec_spec.expected_codes and not any(c in observed_codes for c in unit.exec_spec.expected_codes):
            divergences.append(f"missing_expected_code:expected={list(unit.exec_spec.expected_codes)},observed={observed_codes}")
    elif unit.exec_spec.expect_stored is True:
        if len(stored) == 0:
            divergences.append(f"supported_finding_not_stored:codes={observed_codes}")

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
        "stored_findings_count": len(stored),
        "tool_results": tool_results,
    }


def _drain_workflow_closeout(
    client: Any,
    *,
    session_id: str,
    workflow_run_id: str,
    close_content: str,
    timeout: float = 60.0,
) -> None:
    """Drain remaining coordinator turns until the workflow execution settles."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        obs = client.request(f"/harness/workflow-execution/{session_id}/{workflow_run_id}", missing=True)
        if isinstance(obs, Mapping):
            execution = obs.get("execution")
            if isinstance(execution, Mapping) and execution.get("settled") is True:
                return
        pend = client.get_pending(session_id)
        if pend is not None:
            client.respond(pend["id"], content=close_content)
        else:
            state = client.request(f"/v1/sessions/{session_id}", missing=True)
            if isinstance(state, Mapping) and state.get("status") == "idle":
                return
        time.sleep(0.1)


def _run_verdict_slot(
    client: Any,
    *,
    project_id: str,
    sidecar: IsolatedSidecarProcess,
    workspace: pathlib.Path,
    unit: GB06RunUnit,
    trace_log: pathlib.Path | None,
) -> dict[str, Any]:
    """Run W02-01..W02-08 against a real `review_loop` phase (`decision_adjudicate`) except W02-03."""
    selector = unit.spec.selector
    prefix = WORDING_PREFIX[unit.wording]
    use_review_workflow = selector != "W02-03-verdict-refused-wrong-phase"

    sid = client.create_session(project_id, posture="build")["id"]
    client.wait_session_ready(sid)
    before = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)

    workflow_run_id = ""
    submission_id = ""
    if use_review_workflow:
        admitted = client.request(
            f"/v1/sessions/{sid}/workflow-runs",
            {
                "operation_id": str(uuid.uuid4()),
                "workflow_id": _GB06_REVIEW_WORKFLOW_ID,
                "workflow_version": _GB06_REVIEW_WORKFLOW_VERSION,
                "request": f"{prefix} Exercise submit_verdict for {selector}.",
            },
        )
        workflow_run_id = str(admitted.get("id") or "")
    else:
        p = client.send_prompt(sid, f"{prefix} Exercise submit_verdict for {selector}.")
        submission_id = str(p.get("submission_id") or p.get("id") or "")

    if use_review_workflow and selector != "W02-05-verdict-refused-missing-findings":
        _, cmd_cid = _answer_tool(client, sid, "command", {"command": "cat -n core.go"})
        if selector == "W02-06-verdict-refused-stale-revision":
            _wait_tool_result(sidecar.store_db_path, sid, "command", cmd_cid, timeout=30.0)
            (workspace / "core.go").write_text(
                "package core\n\nfunc Check() bool { return false }\n",
                encoding="utf-8",
            )

    grounded_approved_args: dict[str, Any] = {
        "verdict": {"verdict": "APPROVED", "summary": f"{prefix} Approved with evidence."},
        "cited_evidence": [{"handle": "read#1", "path": "core.go", "line": 3}],
    }

    if selector == "W02-04-verdict-refused-unobserved-citation":
        verdict_args: dict[str, Any] = {
            "verdict": {"verdict": "APPROVED", "summary": f"{prefix} Approved."},
            "cited_evidence": [{"handle": "read#99"}],
        }
    elif selector == "W02-05-verdict-refused-missing-findings":
        verdict_args = {
            "verdict": {"verdict": "APPROVED", "summary": f"{prefix} Missing citations."},
            "cited_evidence": [],
        }
    elif selector == "W02-06-verdict-refused-stale-revision":
        verdict_args = {
            "verdict": {"verdict": "APPROVED", "summary": f"{prefix} Stale revision check."},
            "cited_evidence": [
                {
                    "handle": "read#1",
                    "path": "core.go",
                    "line": 3,
                    "excerpt": "func Check() bool { return false }",
                }
            ],
        }
    elif selector == "W02-08-verdict-refused-ungrounded-summary":
        verdict_args = {
            "verdict": {"verdict": "APPROVED", "summary": f"{prefix} Ungrounded claim [read#77:9]."},
            "cited_evidence": [{"handle": "read#77", "path": "missing.go", "line": 9}],
        }
    elif selector == "W02-02-verdict-changes-requested-with-finding":
        verdict_args = {
            "verdict": {"verdict": "CHANGES_REQUESTED", "summary": f"{prefix} Changes requested."},
            "cited_evidence": [{"handle": "read#1", "path": "core.go", "line": 3}],
        }
    else:
        verdict_args = grounded_approved_args

    _answer_tool(client, sid, "submit_verdict", verdict_args)

    if selector == "W02-07-verdict-refused-duplicate-submission":
        # After terminal APPROVED auto-advances to `done`, drain the workflow
        # closeout turn so the session returns to a normal prompt turn, then
        # attempt a duplicate submit_verdict from the terminal/post-review state.
        if workflow_run_id:
            _drain_workflow_closeout(
                client,
                session_id=sid,
                workflow_run_id=workflow_run_id,
                close_content=f"{prefix} First verdict recorded [read#1:3].",
                timeout=60.0,
            )
        dup_prompt = client.send_prompt(sid, f"{prefix} Submit duplicate verdict.")
        submission_id = str(dup_prompt.get("submission_id") or dup_prompt.get("id") or "")
        dup_req = client.wait_pending(sid, timeout=60.0)
        dup_cid = str(uuid.uuid4())
        client.respond(
            dup_req["id"],
            tool_calls=[{"id": dup_cid, "name": "submit_verdict", "args": verdict_args}],
        )
        _wait_tool_result(sidecar.store_db_path, sid, "submit_verdict", dup_cid, timeout=30.0)
        close_req = client.wait_pending(sid, timeout=60.0)
        client.respond(close_req["id"], content=f"{prefix} Duplicate verdict check finished [read#1:3].")
        client.wait_settled(sid, submission_id=submission_id, timeout=60.0)
    elif use_review_workflow and selector != "W02-01-verdict-approved-with-evidence":
        # Complete the review_loop cleanly on the next turn so the workflow settles without repair stalls.
        try:
            repair_req = client.wait_pending(sid, timeout=30.0)
            if selector == "W02-05-verdict-refused-missing-findings" and "command" in (repair_req.get("tools") or []):
                client.respond(
                    repair_req["id"],
                    tool_calls=[{"id": str(uuid.uuid4()), "name": "command", "args": {"command": "cat -n core.go"}}],
                )
                repair_req = client.wait_pending(sid, timeout=30.0)
            if "submit_verdict" in (repair_req.get("tools") or []):
                client.respond(
                    repair_req["id"],
                    tool_calls=[{"id": str(uuid.uuid4()), "name": "submit_verdict", "args": grounded_approved_args}],
                )
            else:
                client.respond(repair_req["id"], content=f"{prefix} Verdict check finished [read#1:3].")
        except TimeoutError:
            pass

    close_content = (
        f"{prefix} Verdict check finished."
        if selector == "W02-03-verdict-refused-wrong-phase"
        else f"{prefix} Verdict check finished [read#1:3]."
    )
    if use_review_workflow and workflow_run_id:
        _drain_workflow_closeout(
            client,
            session_id=sid,
            workflow_run_id=workflow_run_id,
            close_content=close_content,
            timeout=60.0,
        )
    else:
        close_req = client.wait_pending(sid, timeout=120.0)
        client.respond(close_req["id"], content=close_content)
        client.wait_settled(sid, submission_id=submission_id, timeout=90.0)

    after = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
    delta = after.delta_observed(before)
    tool_results = _query_tool_results(sidecar.store_db_path, sid, "submit_verdict")
    observed_codes = sorted({c for tr in tool_results for c in tr["codes"]})

    divergences: list[str] = []
    if not after.is_complete():
        divergences.append(f"d10_incomplete:{after.unknown_counters()}")
    if unit.exec_spec.expect_refusal:
        if not tool_results:
            divergences.append("submit_verdict_never_executed")
        elif selector == "W02-07-verdict-refused-duplicate-submission" and len(tool_results) < 2:
            divergences.append(f"duplicate_submit_verdict_missing_second_call:count={len(tool_results)}")
        elif all(tr["outcome"].lower() == "completed" for tr in tool_results):
            divergences.append("submit_verdict_unexpectedly_accepted")
        elif unit.exec_spec.expected_codes and not any(c in observed_codes for c in unit.exec_spec.expected_codes):
            divergences.append(f"unexpected_verdict_code:expected={list(unit.exec_spec.expected_codes)},observed={observed_codes}")
    else:
        if not tool_results:
            divergences.append("submit_verdict_never_executed")
        elif not any(tr["outcome"].lower() == "completed" for tr in tool_results):
            divergences.append(f"unexpected_verdict_outcome:observed={observed_codes}")

    return {
        "session_id": sid,
        "child_session_id": None,
        "workflow_run_id": workflow_run_id or None,
        "passed": len(divergences) == 0,
        "divergences": divergences,
        "d10_before": before.to_dict(),
        "d10_after": after.to_dict(),
        "d10_delta": delta,
        "worker_report_refusals": None,
        "observed_codes": observed_codes,
        "stored_findings_count": len(_query_stored_findings(sidecar.store_db_path, sid)),
        "tool_results": tool_results,
    }


def _dispatch_worker_and_get_child(
    client: Any,
    *,
    db_path: pathlib.Path,
    root_sid: str,
    agent_type: str,
    scope_mode: str,
    scope_paths: tuple[str, ...],
    goal: str,
    ignore_sessions: set[str],
    send_initial_prompt: bool = True,
    timeout: float = 180.0,
) -> tuple[str, dict[str, Any]]:
    if send_initial_prompt:
        client.send_prompt(root_sid, f"Dispatch {agent_type} to {goal}.")
    _, progress_cid = _answer_tool(
        client,
        root_sid,
        "update_progress",
        {"content": f"## Progress\n- [ ] {goal}\n"},
        timeout=timeout,
    )
    progress_tr = _wait_tool_result(db_path, root_sid, "update_progress", progress_cid, timeout=30.0)
    if progress_tr is not None and progress_tr.get("outcome", "").lower() != "completed":
        raise RuntimeError(
            f"update_progress before task({agent_type}) rejected: "
            f"codes={progress_tr.get('codes')} content={str(progress_tr.get('content') or '')[:300]}"
        )

    _, task_cid = _answer_tool(
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
    task_tr = _wait_tool_result(db_path, root_sid, "task", task_cid, timeout=30.0)
    if task_tr is not None and task_tr.get("outcome", "").lower() != "completed":
        raise RuntimeError(
            f"task({agent_type}, scope={scope_mode}) rejected: "
            f"codes={task_tr.get('codes')} content={str(task_tr.get('content') or '')[:300]}"
        )

    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        # Check worker_jobs directly so failed jobs fail fast and known child_session_id is never starved by root_sid.
        for job in _query_worker_jobs(db_path, root_sid):
            status = str(job.get("status") or "").lower()
            if status in {"failed", "canceled", "aborted"}:
                raise RuntimeError(
                    f"Worker job {job.get('id')} ({agent_type}) ended with status={status}: {job.get('error')}"
                )
            csid = str(job.get("child_session_id") or "")
            if csid and csid not in ignore_sessions:
                child_pend = client.get_pending(csid)
                if child_pend is not None:
                    ignore_sessions.add(csid)
                    return csid, child_pend

        pend = client.request("/harness/llm/pending")
        if isinstance(pend, dict) and pend.get("pending"):
            psid = str(pend.get("session_id") or "")
            if psid == root_sid:
                offered = pend.get("tools") or []
                if "wait" in offered:
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
                elif "request_tools" in offered:
                    client.respond(
                        pend["id"],
                        tool_calls=[{"id": str(uuid.uuid4()), "name": "request_tools", "args": {"need": "wait"}}],
                    )
            elif psid and psid not in ignore_sessions:
                ignore_sessions.add(psid)
                return psid, pend
        time.sleep(0.15)
    jobs = _query_worker_jobs(db_path, root_sid)
    raise TimeoutError(f"Worker child session for {agent_type} never became pending (jobs={jobs})")


def _run_worker_slot(
    client: Any,
    *,
    project_id: str,
    sidecar: IsolatedSidecarProcess,
    unit: GB06RunUnit,
    trace_log: pathlib.Path | None,
) -> dict[str, Any]:
    selector = unit.spec.selector
    prefix = WORDING_PREFIX[unit.wording]
    root_sid = client.create_session(project_id, posture="build")["id"]
    client.wait_session_ready(root_sid)
    ignore_sessions = {root_sid}

    before_root = probe_store_db(sidecar.store_db_path, "", root_id=root_sid, trace_log_path=trace_log)
    child_sid, child_req = _dispatch_worker_and_get_child(
        client,
        db_path=sidecar.store_db_path,
        root_sid=root_sid,
        agent_type=unit.exec_spec.worker_agent_type,
        scope_mode=unit.exec_spec.worker_scope_mode,
        scope_paths=unit.exec_spec.worker_scope_paths,
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

    if selector == "W02-09-verdict-refused-non-reviewer-role":
        client.respond(
            child_req["id"],
            tool_calls=[
                {
                    "id": str(uuid.uuid4()),
                    "name": "submit_verdict",
                    "args": {"verdict": {"verdict": "approved"}, "cited_evidence": []},
                }
            ],
        )
        child_req2 = client.wait_pending(child_sid, timeout=120.0)
        client.respond(child_req2["id"], tool_calls=[{"id": str(uuid.uuid4()), "name": "command", "args": {"command": "cat -n core.go"}}])
        child_req3 = client.wait_pending(child_sid, timeout=120.0)
        client.respond(
            child_req3["id"],
            tool_calls=_complete_leg_call(
                f"{prefix} Non-reviewer worker could not submit verdict.",
                [{"path": "core.go", "line": 3, "evidence": "read#1", "note": "Check() observed."}],
            ),
        )
    elif selector == "W03-02-investigator-refused-without-evidence":
        client.respond(
            child_req["id"],
            tool_calls=_complete_leg_call(f"{prefix} Completed without reading.", []),
        )
        child_req2 = client.wait_pending(child_sid, timeout=120.0)
        client.respond(child_req2["id"], tool_calls=[{"id": str(uuid.uuid4()), "name": "command", "args": {"command": "cat -n core.go"}}])
        child_req3 = client.wait_pending(child_sid, timeout=120.0)
        client.respond(
            child_req3["id"],
            tool_calls=_complete_leg_call(
                f"{prefix} Surveyed core.go after refusal.",
                [{"path": "core.go", "line": 3, "evidence": "read#1", "note": "Verified Check()."}],
            ),
        )
    elif selector == "W03-03-implementer-complete-with-artifact":
        # Turn 1: pure read to mint read#1; Turn 2: disk mutation to produce artifact.
        client.respond(
            child_req["id"],
            tool_calls=[{"id": str(uuid.uuid4()), "name": "command", "args": {"command": "cat -n core.go"}}],
        )
        child_req2 = client.wait_pending(child_sid, timeout=120.0)
        client.respond(
            child_req2["id"],
            tool_calls=[
                {
                    "id": str(uuid.uuid4()),
                    "name": "command",
                    "args": {"command": "printf '\\n// gb06 artifact\\n' >> core.go"},
                }
            ],
        )
        child_req3 = client.wait_pending(child_sid, timeout=120.0)
        client.respond(
            child_req3["id"],
            tool_calls=_complete_leg_call(
                f"{prefix} Updated core.go with artifact.",
                [{"path": "core.go", "line": 3, "evidence": "read#1", "note": "Updated core.go."}],
            ),
        )
    elif selector == "W03-04-implementer-missing-artifact-active-code":
        client.respond(
            child_req["id"],
            tool_calls=[{"id": str(uuid.uuid4()), "name": "command", "args": {"command": "cat -n core.go"}}],
        )
        child_req2 = client.wait_pending(child_sid, timeout=120.0)
        client.respond(
            child_req2["id"],
            tool_calls=_complete_leg_call(
                f"{prefix} Implementer summary without disk artifact.",
                [{"path": "core.go", "line": 3, "evidence": "read#1", "note": "Read only, no edit."}],
            ),
        )
        # After WORKER_SUMMARY_NO_ARTIFACT refusal, close with partial status so root resumes cleanly.
        # Poll both child_sid and root_sid so if D1 falsely accepts, we never hang for 120s.
        deadline = time.monotonic() + 30.0
        while time.monotonic() < deadline:
            cp = client.get_pending(child_sid)
            if cp is not None:
                client.respond(
                    cp["id"],
                    tool_calls=_complete_leg_call(
                        f"{prefix} No disk change applied.",
                        [{"path": "core.go", "line": 3, "evidence": "read#1", "note": "Partial leg."}],
                        leg_status="partial",
                    ),
                )
                break
            rp = client.get_pending(root_sid)
            if rp is not None:
                break
            time.sleep(0.15)
    elif selector in {
        "W03-06-reviewer-refused-without-verdict",
        "W03-08-verifier-refused-without-receipt",
    }:
        bad_handle = "command#99" if "verifier" in selector else "read#99"
        client.respond(
            child_req["id"],
            tool_calls=_complete_leg_call(
                f"{prefix} Attempted completion without required evidence.",
                [{"path": "nonexistent.go", "line": 10, "evidence": bad_handle, "note": "Unobserved evidence."}],
            ),
        )
        child_req2 = client.wait_pending(child_sid, timeout=120.0)
        client.respond(child_req2["id"], tool_calls=[{"id": str(uuid.uuid4()), "name": "command", "args": {"command": "cat -n core.go"}}])
        child_req3 = client.wait_pending(child_sid, timeout=120.0)
        client.respond(
            child_req3["id"],
            tool_calls=_complete_leg_call(
                f"{prefix} Completed after collecting evidence.",
                [{"path": "core.go", "line": 3, "evidence": "read#1", "note": "Verified Check()."}],
            ),
        )
    elif selector == "W03-09-worker-retry-exhaustion-keeps-supported-only":
        client.respond(
            child_req["id"],
            tool_calls=[{"id": str(uuid.uuid4()), "name": "command", "args": {"command": "cat -n core.go"}}],
        )
        mixed_findings = [
            {"path": "core.go", "line": 3, "evidence": "read#1", "note": "Supported line 3."},
            {"path": "nonexistent.go", "line": 90, "evidence": "read#999999", "note": "Unsupported line 90."},
        ]
        for _ in range(5):
            req_i = client.get_pending(child_sid)
            if req_i is None:
                try:
                    req_i = client.wait_pending(child_sid, timeout=15.0)
                except TimeoutError:
                    break
            client.respond(
                req_i["id"],
                tool_calls=_complete_leg_call(f"{prefix} Mixed supported and unsupported claims.", mixed_findings),
            )
    elif selector == "W03-10-worker-undelivered-line-false-accept-contrast":
        # Decision 031: deliver only lines 1-5 of long.go, then cite observed line 3 + undelivered line 90 + fabricated read#999999
        client.respond(
            child_req["id"],
            tool_calls=[{"id": str(uuid.uuid4()), "name": "command", "args": {"command": "head -n 5 long.go | nl -ba"}}],
        )
        child_req2 = client.wait_pending(child_sid, timeout=120.0)
        client.respond(
            child_req2["id"],
            tool_calls=_complete_leg_call(
                f"{prefix} Surveyed long.go lines 3 and 90.",
                [
                    {"path": "long.go", "line": 3, "evidence": "read#1", "note": "Delivered line 3."},
                    {"path": "long.go", "line": 90, "evidence": "read#999999", "note": "Undelivered line 90 with fabricated ordinal."},
                ],
            ),
        )
        # If D1 refuses as expected, repair cleanly on next turn; if D1 falsely accepts, root resumes directly.
        deadline = time.monotonic() + 30.0
        while time.monotonic() < deadline:
            cp = client.get_pending(child_sid)
            if cp is not None:
                client.respond(
                    cp["id"],
                    tool_calls=_complete_leg_call(
                        f"{prefix} Repaired to delivered line 3 only.",
                        [{"path": "long.go", "line": 3, "evidence": "read#1", "note": "Delivered line 3."}],
                    ),
                )
                break
            rp = client.get_pending(root_sid)
            if rp is not None:
                break
            time.sleep(0.15)
    else:
        # W03-01, W03-05, W03-07: clean grounded worker completion
        cmd = "cat -n core.go && echo VERIFY_PASS" if selector == "W03-07-verifier-complete-with-fresh-receipt" else "cat -n core.go"
        client.respond(
            child_req["id"],
            tool_calls=[{"id": str(uuid.uuid4()), "name": "command", "args": {"command": cmd}}],
        )
        child_req2 = client.wait_pending(child_sid, timeout=120.0)
        client.respond(
            child_req2["id"],
            tool_calls=_complete_leg_call(
                f"{prefix} Worker completed with grounded evidence.",
                [{"path": "core.go", "line": 3, "evidence": "read#1", "note": "Verified Check()."}],
            ),
        )

    # Consume root resume turn when worker finishes
    try:
        root_resume = client.wait_pending(root_sid, timeout=60.0)
        client.respond(root_resume["id"], content=f"{prefix} Worker leg finished.")
    except TimeoutError:
        pass

    after_root = probe_store_db(sidecar.store_db_path, "", root_id=root_sid, trace_log_path=trace_log)
    after_child = probe_store_db(sidecar.store_db_path, child_sid, trace_log_path=trace_log)
    delta_root = after_root.delta_observed(before_root)
    delta_child = after_child.delta_observed(before_child)

    # Decision 045: ALWAYS probe and report worker_report_refusals side by side with friction_units (NEVER summed!)
    refusals: WorkerReportRefusalSummary = probe_worker_report_refusals(sidecar.store_db_path, child_sid)
    verdict_trs = _query_tool_results(sidecar.store_db_path, child_sid, "submit_verdict") if selector == "W02-09-verdict-refused-non-reviewer-role" else []
    observed_codes = sorted(set(refusals.refusal_codes) | {c for tr in verdict_trs for c in tr["codes"]})

    divergences: list[str] = []
    if not after_root.is_complete() or not after_child.is_complete():
        divergences.append(f"d10_incomplete:root={after_root.unknown_counters()},child={after_child.unknown_counters()}")

    if selector == "W02-09-verdict-refused-non-reviewer-role":
        if any(tr["outcome"].lower() == "completed" for tr in verdict_trs):
            divergences.append("non_reviewer_worker_submit_verdict_accepted")
    elif selector == "W03-09-worker-retry-exhaustion-keeps-supported-only":
        if refusals.worker_report_refusals < 1:
            divergences.append("retry_exhaustion_had_zero_refusals")
        if refusals.finally_accepted:
            divergences.append("retry_exhaustion_unexpectedly_marked_complete")
    elif selector == "W03-10-worker-undelivered-line-false-accept-contrast":
        if refusals.worker_report_refusals < 1:
            divergences.append("decision_031_undelivered_line_falsely_accepted")
        elif unit.exec_spec.expected_codes and not any(c in observed_codes for c in unit.exec_spec.expected_codes):
            divergences.append(f"unexpected_refusal_code:expected={list(unit.exec_spec.expected_codes)},observed={observed_codes}")
    elif unit.exec_spec.expect_refusal:
        if refusals.worker_report_refusals < 1:
            divergences.append("expected_worker_report_refusal_missing")
        if unit.exec_spec.expected_codes and not any(c in observed_codes for c in unit.exec_spec.expected_codes):
            divergences.append(f"unexpected_worker_code:expected={list(unit.exec_spec.expected_codes)},observed={observed_codes}")
    else:
        if refusals.worker_report_refusals != 0:
            divergences.append(f"unexpected_worker_report_refusal:count={refusals.worker_report_refusals},codes={list(refusals.refusal_codes)}")
        if not refusals.finally_accepted:
            divergences.append("grounded_worker_leg_not_accepted")

    return {
        "session_id": root_sid,
        "child_session_id": child_sid,
        "passed": len(divergences) == 0,
        "divergences": divergences,
        "d10_before": before_child.to_dict(),
        "d10_after": after_child.to_dict(),
        "d10_delta": delta_child,
        "d10_root_delta": delta_root,
        # Decision 045: side-by-side worker_report_refusals, NEVER summed with friction_units
        "worker_report_refusals": refusals.to_dict(),
        "observed_codes": observed_codes,
        "deprecated_compat_code": unit.exec_spec.deprecated_compat_code or None,
        "stored_findings_count": len(_query_stored_findings(sidecar.store_db_path, root_sid)),
    }


def _run_surface_slot(
    client: Any,
    *,
    project_id: str,
    sidecar: IsolatedSidecarProcess,
    workspace: pathlib.Path,
    manifest: Any,
    unit: GB06RunUnit,
    trace_log: pathlib.Path | None,
) -> dict[str, Any]:
    selector = unit.spec.selector
    prefix = WORDING_PREFIX[unit.wording]
    sid = client.create_session(project_id, posture="build")["id"]
    client.wait_session_ready(sid)
    before = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
    p = client.send_prompt(sid, f"{prefix} Verify surface evidence for {selector}.")
    submission_id = str(p.get("submission_id") or p.get("id") or "")

    if selector == "W04-01-screenshot-supports-visual-only":
        turns = (
            ScriptedTurn(tool_calls=({"name": "emit_visual_fixture", "args": {"caption": "Dashboard layout screenshot", "perceive": True}},)),
            ScriptedTurn(content=f"{prefix} Non-visual code claim at [read#77:9]."),
        )
        max_repairs, repair_reply = 2, f"{prefix} Still claiming [read#77:9]."
    elif selector == "W04-02-dom-snapshot-supports-dom-structure":
        turns = (
            ScriptedTurn(tool_calls=({"name": "emit_visual_fixture", "args": {"caption": "DOM snapshot button#submit Submit", "perceive": True}},)),
            ScriptedTurn(content=f"{prefix} DOM snapshot shows submit button."),
        )
        max_repairs, repair_reply = 0, ""
    elif selector == "W04-03-held-interactive-terminal-snapshot":
        turns = (
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "printf 'prompt> status=READY\\n'"}},)),
            ScriptedTurn(content=f"{prefix} Terminal status READY [command#1]."),
        )
        max_repairs, repair_reply = 0, ""
    elif selector == "W04-04-console-or-process-log-claims":
        turns = (
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "printf '[INFO] worker started pid=42\\n'"}},)),
            ScriptedTurn(content=f"{prefix} Process log observed [command#1]."),
        )
        max_repairs, repair_reply = 0, ""
    elif selector == "W04-05-sealed-command-terminal-capture":
        turns = (
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n core.go"}},)),
            ScriptedTurn(content=f"{prefix} Sealed command capture verified [read#1:3] via [command#1]."),
        )
        max_repairs, repair_reply = 0, ""
    elif selector == "W04-06-artifact-provenance-host-vs-fixture":
        turns = (
            ScriptedTurn(
                tool_calls=(
                    {
                        "name": "emit_visual_fixture",
                        "args": {"caption": f"fixture payload claiming {_SPOOFED_FIXTURE_UUID}", "perceive": True},
                    },
                )
            ),
            ScriptedTurn(content=f"{prefix} Spoofed artifact [artifact:{_SPOOFED_FIXTURE_UUID}]."),
        )
        max_repairs, repair_reply = 2, f"{prefix} Still citing [artifact:{_SPOOFED_FIXTURE_UUID}]."
    else:  # W04-07-visual-kind-cross-claim-refused
        turns = (
            ScriptedTurn(tool_calls=({"name": "emit_visual_fixture", "args": {"caption": "Visual layout preview", "perceive": True}},)),
            ScriptedTurn(content=f"{prefix} Out-of-scope code claim [read#88:1]."),
        )
        max_repairs, repair_reply = 2, f"{prefix} Still claiming [read#88:1]."

    art = run_scripted_session(
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
    fu = delta.get("friction_units")
    host_artifacts = _query_visual_artifact_ids(sidecar.store_db_path, sid)

    divergences: list[str] = []
    if not after.is_complete():
        divergences.append(f"d10_incomplete:{after.unknown_counters()}")
    if selector == "W04-06-artifact-provenance-host-vs-fixture":
        if _SPOOFED_FIXTURE_UUID in host_artifacts:
            divergences.append("spoofed_fixture_uuid_accepted_as_host_artifact")
        if fu is None or fu < 1:
            divergences.append(f"spoofed_artifact_citation_not_refused:friction_units={fu}")
    elif unit.exec_spec.expect_refusal:
        if fu is None or fu < 1:
            divergences.append(f"out_of_scope_visual_claim_not_refused:friction_units={fu}")
    else:
        if fu is None or fu != 0:
            divergences.append(f"grounded_surface_claim_incurred_friction:friction_units={fu}")

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
        "host_artifact_ids": host_artifacts,
        "stored_findings_count": len(_query_stored_findings(sidecar.store_db_path, sid)),
        "artifact": art.to_dict(),
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
                int(r.get("stored_findings_count") or 0) > 0,
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


def run_gb06_full_sidecar(
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
) -> dict[str, Any]:
    validate_gb06_execution_map()
    runs = select_gb06_runs(families=families, wordings=wordings, shard=shard, shards=shards)
    out_dir.mkdir(parents=True, exist_ok=True)
    artifacts_dir = out_dir / "artifacts"
    artifacts_dir.mkdir(parents=True, exist_ok=True)

    slot_results: list[dict[str, Any]] = []
    divergences_records: list[dict[str, Any]] = []
    manifest_dict: dict[str, Any] | None = None
    plat = "unknown"

    bundled_git_present = (
        (pathlib.Path(product_root) / "lycaon-den" / "src-tauri" / "engine-root" / "gitengine" / "bin" / "git").is_file()
        or (pathlib.Path(product_root) / "lycaon-den" / "src-tauri" / "engine-root" / "gitengine" / "cmd" / "git.exe").is_file()
    )

    for run_unit in runs:
        run_tag = f"{run_unit.spec.selector}__{run_unit.wording}"
        case_dir = out_dir / "cases" / run_tag
        workspace = case_dir / "workspace"
        config_dir = case_dir / "config"
        # Write-scoped workers trigger privategit seeded-index refresh when the
        # workspace contains a .git repo; skip git init when bundled gitengine
        # is not staged in product_root so overlay write-worker tests can run.
        init_git = bundled_git_present or run_unit.exec_spec.worker_scope_mode != "write"
        _init_git_workspace(workspace, init_git=init_git)

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
                if run_unit.exec_spec.mode == "finding":
                    outcome = _run_finding_slot(
                        client,
                        project_id=pid,
                        sidecar=sidecar,
                        unit=run_unit,
                        trace_log=trace_log,
                    )
                elif run_unit.exec_spec.mode == "verdict":
                    outcome = _run_verdict_slot(
                        client,
                        project_id=pid,
                        sidecar=sidecar,
                        workspace=workspace,
                        unit=run_unit,
                        trace_log=trace_log,
                    )
                elif run_unit.exec_spec.mode == "worker":
                    outcome = _run_worker_slot(
                        client,
                        project_id=pid,
                        sidecar=sidecar,
                        unit=run_unit,
                        trace_log=trace_log,
                    )
                else:
                    outcome = _run_surface_slot(
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
                    "stored_findings_count": 0,
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
            "stored_findings_count": int(outcome.get("stored_findings_count") or 0),
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

    div_path = out_dir / "gb06-sidecar-divergences.jsonl"
    with div_path.open("w", encoding="utf-8") as fh:
        for rec in divergences_records:
            fh.write(json.dumps(rec, sort_keys=True) + "\n")

    passed_count = sum(1 for r in slot_results if r["passed"])
    summary = {
        "card": "GB06",
        "run_id": run_id,
        "platform": plat,
        "trace_friction": trace_friction,
        "families": list(families) if families else list(GB06_FAMILY_IDS),
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
    (out_dir / "gb06-sidecar-summary.json").write_text(
        json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8"
    )
    return summary


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="GB06 full sidecar execution runner (W01..W04, D5 tripled)")
    ap.add_argument("--engine-bin", required=True)
    ap.add_argument("--product-root", required=True)
    ap.add_argument("--expected-engine-sha256", default="")
    ap.add_argument("--out-dir", required=True)
    ap.add_argument("--run-id", default="20261127-3bb1464d-gb06-full")
    ap.add_argument("--families", default=",".join(GB06_FAMILY_IDS))
    ap.add_argument("--wordings", default=",".join(WORDING_VARIANTS))
    ap.add_argument("--shard", type=int, default=0)
    ap.add_argument("--shards", type=int, default=1)
    ap.add_argument("--no-trace-friction", action="store_true")
    args = ap.parse_args(argv)

    families = [f.strip() for f in args.families.split(",") if f.strip()]
    wordings = [w.strip() for w in args.wordings.split(",") if w.strip()]

    summary = run_gb06_full_sidecar(
        engine_bin=args.engine_bin,
        product_root=args.product_root,
        expected_engine_sha256=args.expected_engine_sha256,
        out_dir=pathlib.Path(args.out_dir),
        run_id=args.run_id,
        families=families,
        wordings=wordings,
        shard=args.shard,
        shards=args.shards,
        trace_friction=not args.no_trace_friction,
    )
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0 if summary["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
