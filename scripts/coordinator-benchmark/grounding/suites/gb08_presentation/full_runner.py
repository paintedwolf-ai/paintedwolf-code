"""Full GB08 headless sidecar execution runner and native Den scenario planner.

Covers:
- All 17 headless/sidecar GB08 slots in `catalog.py` (families P01..P04):
  - P01: Presentation wire payload (`cited_evidence`, `openable`, `evidence_optional`,
         `host_assembled`, refused partial state, withheld mid-turn note absence)
  - P02: Markdown & copy-text presentation rendering (pathless handle stripping,
         control directive removal / code-span preservation, link rewriting without new
         Markdown structure, raw HTML & broken link neutralization)
  - P03: Serialized refusal feedback readback audit (unknown handle, uncovered range,
         excerpt mismatch, wrong path)
  - P04: Opt-in dev diagnostic parity & contradiction detection
- Clean separation of the 5 native Den desktop UI slots (family DEN, released on both
  hosts by Decision 050: staged D1 `Painted Wolf Code.app` on macOS and Xvfb on Linux),
  exposed via `--list-native-slots` and included as `native_plan` in output manifests.

Outputs:
- `gb08-sidecar-summary.json`
- `gb08-sidecar-divergences.jsonl`
- `gb08-native-plan.json`
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
from typing import Any, Mapping, Sequence

from grounding.delivery_log import GROUNDED_VERDICTS, ProductVerdict, SurfaceOutcome
from grounding.friction_probe import probe_store_db
from grounding.reference_resolver import Citation, PermittedScope
from grounding.sidecar_driver import (
    IsolatedSidecarProcess,
    ScriptedTurn,
    run_scripted_session,
)

from .catalog import (
    ALL_GB08_SLOTS,
    GB08_FAMILY_IDS,
    GB08_HEADLESS_FAMILY_IDS,
    GB08_NATIVE_FAMILY_IDS,
    HEADLESS_GB08_SLOTS,
    NATIVE_GB08_SLOTS,
    SLOTS_BY_SELECTOR,
    GB08SlotSpec,
    validate_gb08_catalog,
)
from .diagnostic import (
    DEN_PRESENTATION_SCENARIOS,
    DenPresentationScenario,
    audit_refusal_feedback,
    run_dev_diagnostic,
)
from .pilot import _extract_repair_feedback_offers


_RAW_HTML_ANCHOR_RE = re.compile(r"(?<!\\)<a\s+[^>]*href\s*=", re.IGNORECASE)
_MD_HEADING_RE = re.compile(r"^\s{0,3}#{1,6}\s+", re.MULTILINE)
_MD_LIST_RE = re.compile(r"^\s{0,3}[-*+]\s+", re.MULTILINE)
_RAW_COMMAND_HANDLE_RE = re.compile(r"\[command#\d+\]")


@dataclass(frozen=True)
class GB08ExecutionSpec:
    """Headless sidecar execution blueprint for one GB08 sidecar slot."""

    selector: str
    family_id: str
    mode: str  # "wire" | "markdown" | "feedback" | "diagnostic"
    notes: str = ""


@dataclass(frozen=True)
class GB08RunUnit:
    """One concrete headless GB08 execution unit."""

    slot_index: int
    spec: GB08SlotSpec
    exec_spec: GB08ExecutionSpec


GB08_HEADLESS_EXECUTION_SPECS: tuple[GB08ExecutionSpec, ...] = (
    # P01: Presentation wire payload (6 slots)
    GB08ExecutionSpec(
        selector="P01-01-grounded-cited-evidence-openable",
        family_id="P01",
        mode="wire",
        notes="Grounded [read#1:3] produces traced=true, evidence_optional=false, openable=true.",
    ),
    GB08ExecutionSpec(
        selector="P01-02-exempt-speech-evidence-optional",
        family_id="P01",
        mode="wire",
        notes="Exempt closeout with empty observation window sets evidence_optional=true.",
    ),
    GB08ExecutionSpec(
        selector="P01-03-host-assembled-closeout-flag",
        family_id="P01",
        mode="wire",
        notes="Uncited closeout after file read triggers host_assembled=true with DeliveryLog handles.",
    ),
    GB08ExecutionSpec(
        selector="P01-04-refused-closeout-partial-wire-state",
        family_id="P01",
        mode="wire",
        notes="Refused [read#99:99] never marks read#99 grounded in cited_evidence.",
    ),
    GB08ExecutionSpec(
        selector="P01-05-openable-false-for-nonfile-target",
        family_id="P01",
        mode="wire",
        notes="Non-regular-file command/directory citation never stamps openable=true.",
    ),
    GB08ExecutionSpec(
        selector="P01-06-withheld-midturn-note-absent-from-feed",
        family_id="P01",
        mode="wire",
        notes="Withheld mid-turn note citing [read#99:99] stays absent from user-facing feed.",
    ),
    # P02: Markdown & copy-text presentation rendering (4 slots)
    GB08ExecutionSpec(
        selector="P02-01-pathless-handle-stripped-or-rewritten",
        family_id="P02",
        mode="markdown",
        notes="Pathless [command#1] removed and file handle rewritten cleanly.",
    ),
    GB08ExecutionSpec(
        selector="P02-02-control-directives-removed-code-preserved",
        family_id="P02",
        mode="markdown",
        notes="Code spans/fences preserved byte-for-byte; no leaked control directives.",
    ),
    GB08ExecutionSpec(
        selector="P02-03-resolved-ref-rewritten-no-new-structure",
        family_id="P02",
        mode="markdown",
        notes="Rewritten reference introduces no new Markdown headings, lists, or raw HTML.",
    ),
    GB08ExecutionSpec(
        selector="P02-04-raw-html-and-broken-links-neutralized",
        family_id="P02",
        mode="markdown",
        notes="Raw HTML anchors and empty/nowhere Markdown links are neutralized.",
    ),
    # P03: Serialized refusal feedback readback audit (4 slots)
    GB08ExecutionSpec(
        selector="P03-01-feedback-unknown-handle-admissible-samples",
        family_id="P03",
        mode="feedback",
        notes="Unknown handle [read#99:99] names offender and offers only admissible handles.",
    ),
    GB08ExecutionSpec(
        selector="P03-02-feedback-uncovered-range-offender-named",
        family_id="P03",
        mode="feedback",
        notes="Uncovered range [read#1:99] names failing citation and offers only admissible handles.",
    ),
    GB08ExecutionSpec(
        selector="P03-03-feedback-excerpt-mismatch-offender-named",
        family_id="P03",
        mode="feedback",
        notes="Mismatched excerpt on [read#1:3] names failing citation and offers only admissible handles.",
    ),
    GB08ExecutionSpec(
        selector="P03-04-feedback-wrong-path-offender-named",
        family_id="P03",
        mode="feedback",
        notes="Wrong path [read#1 wrong.go:3] names failing citation and offers only admissible handles.",
    ),
    # P04: Opt-in dev diagnostic parity & contradiction detection (3 slots)
    GB08ExecutionSpec(
        selector="P04-01-diagnostic-silent-on-healthy-closeout",
        family_id="P04",
        mode="diagnostic",
        notes="Dev diagnostic emits 0 contradictions on healthy grounded closeout.",
    ),
    GB08ExecutionSpec(
        selector="P04-02-diagnostic-detects-all-three-contradictions",
        family_id="P04",
        mode="diagnostic",
        notes="Dev diagnostic detects all 3 contradiction classes when enabled and 0 when disabled.",
    ),
    GB08ExecutionSpec(
        selector="P04-03-diagnostic-enabled-vs-disabled-parity",
        family_id="P04",
        mode="diagnostic",
        notes="Enabled vs disabled diagnostic runs produce identical verdicts and D10 counts.",
    ),
)

GB08_HEADLESS_EXECUTION_SPECS_BY_SELECTOR: dict[str, GB08ExecutionSpec] = {
    s.selector: s for s in GB08_HEADLESS_EXECUTION_SPECS
}

DEN_SCENARIOS_BY_SELECTOR: dict[str, DenPresentationScenario] = {
    s.selector: s for s in DEN_PRESENTATION_SCENARIOS
}


def validate_gb08_execution_map() -> dict[str, int]:
    """Assert 1-to-1 coverage between `catalog.py` (headless + native) and execution specs."""
    counts = validate_gb08_catalog()

    headless_catalog = {s.selector for s in HEADLESS_GB08_SLOTS}
    headless_mapped = set(GB08_HEADLESS_EXECUTION_SPECS_BY_SELECTOR.keys())
    if len(GB08_HEADLESS_EXECUTION_SPECS) != len(headless_mapped):
        raise ValueError("Duplicate selector in GB08_HEADLESS_EXECUTION_SPECS")
    missing_h = sorted(headless_catalog - headless_mapped)
    extra_h = sorted(headless_mapped - headless_catalog)
    if missing_h or extra_h:
        raise ValueError(f"GB08 headless execution map mismatch: missing={missing_h}, extra={extra_h}")

    for spec in HEADLESS_GB08_SLOTS:
        ex = GB08_HEADLESS_EXECUTION_SPECS_BY_SELECTOR[spec.selector]
        if ex.family_id != spec.family_id:
            raise ValueError(f"Family mismatch for {spec.selector}: {ex.family_id} != {spec.family_id}")

    native_catalog = {s.selector for s in NATIVE_GB08_SLOTS}
    native_mapped = set(DEN_SCENARIOS_BY_SELECTOR.keys())
    missing_n = sorted(native_catalog - native_mapped)
    extra_n = sorted(native_mapped - native_catalog)
    if missing_n or extra_n:
        raise ValueError(f"GB08 native Den map mismatch: missing={missing_n}, extra={extra_n}")

    overlap = sorted(headless_mapped & native_mapped)
    if overlap:
        raise ValueError(f"Headless and native GB08 selectors must be disjoint, overlap={overlap}")

    if (headless_mapped | native_mapped) != set(SLOTS_BY_SELECTOR.keys()):
        raise ValueError("Headless + native selectors do not cover ALL_GB08_SLOTS")

    return counts


def select_gb08_runs(
    *,
    families: Sequence[str] | None = None,
    shard: int = 0,
    shards: int = 1,
) -> list[GB08RunUnit]:
    """Select headless/sidecar GB08 slots, partitioned disjointly across `0..shards-1`.

    Native Den UI slots (`DEN`) are never placed into the headless sidecar execution list;
    they are surfaced via `build_native_den_plan()` and `--list-native-slots`.
    """
    if shards < 1:
        raise ValueError(f"shards must be >= 1, got {shards}")
    if shard < 0 or shard >= shards:
        raise ValueError(f"shard must satisfy 0 <= shard < shards ({shards}), got {shard}")

    selected_families = tuple(families) if families else GB08_HEADLESS_FAMILY_IDS
    unknown_fams = [f for f in selected_families if f not in GB08_FAMILY_IDS]
    if unknown_fams:
        raise ValueError(f"Unknown GB08 families: {unknown_fams}")

    fam_set = set(selected_families)
    runs: list[GB08RunUnit] = []
    for slot_idx, spec in enumerate(HEADLESS_GB08_SLOTS):
        if spec.family_id not in fam_set:
            continue
        if slot_idx % shards != shard:
            continue
        exec_spec = GB08_HEADLESS_EXECUTION_SPECS_BY_SELECTOR[spec.selector]
        runs.append(GB08RunUnit(slot_index=slot_idx, spec=spec, exec_spec=exec_spec))
    return runs


def build_native_den_plan(
    *,
    families: Sequence[str] | None = None,
    run_id: str = "",
    product_root: str = "",
) -> dict[str, Any]:
    """Build the Decision 050 native Den scenario manifest for macOS and Linux desktop runs."""
    validate_gb08_execution_map()
    selected_families = tuple(families) if families else GB08_FAMILY_IDS
    unknown_fams = [f for f in selected_families if f not in GB08_FAMILY_IDS]
    if unknown_fams:
        raise ValueError(f"Unknown GB08 families: {unknown_fams}")

    include_den = "DEN" in selected_families or families is None
    scenarios = []
    if include_den:
        for idx, spec in enumerate(NATIVE_GB08_SLOTS):
            sc = DEN_SCENARIOS_BY_SELECTOR[spec.selector]
            scenarios.append(
                {
                    "slot_index": idx,
                    "selector": sc.selector,
                    "family_id": spec.family_id,
                    "lane": spec.lane,
                    "state": sc.state,
                    "expect_navigable_link": sc.expect_navigable_link,
                    "expect_evidence_badge": sc.expect_evidence_badge,
                    "expect_host_added_label": sc.expect_host_added_label,
                    "expect_visible_in_feed": sc.expect_visible_in_feed,
                    "assertion": sc.assertion,
                    "contract_refs": list(spec.contract_refs),
                }
            )

    return {
        "card": "GB08",
        "lane": "native_den",
        "decision_ref": "Decision 050 (native Den scenarios released on macOS and Linux)",
        "run_id": run_id,
        "product_root": product_root,
        "native_slot_count": len(scenarios),
        "hosts": {
            "macos": {
                "app": "Staged D1 Painted Wolf Code.app",
                "driver": "Scripted Accessibility keyboard probe + API readback + screenshot",
                "seat_rule": "Claim serial macOS native seat in STATUS.md before launch and release promptly.",
            },
            "linux": {
                "app": "D1 Den desktop window under Xvfb",
                "driver": "Desktop-target Playwright specs in Docker (docker-01..docker-03) or Pop!_OS",
                "seat_rule": "Run under Xvfb; a browser page never satisfies a native Den slot (D15).",
            },
        },
        "scenarios": scenarios,
    }


def _init_git_workspace(workspace: pathlib.Path) -> None:
    workspace.mkdir(parents=True, exist_ok=True)
    (workspace / "app.go").write_text(
        'package app\n\nfunc Main() string { return "ready" }\n',
        encoding="utf-8",
    )
    (workspace / "subdir").mkdir(exist_ok=True)
    (workspace / "subdir" / "note.txt").write_text("hello from subdir\n", encoding="utf-8")
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
            "gb08 fixture",
        ],
    ):
        subprocess.run(cmd, cwd=workspace, check=True)


def _query_session_messages(db_path: pathlib.Path, session_id: str) -> list[dict[str, Any]]:
    if not db_path.is_file():
        return []
    with contextlib.closing(sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)) as conn:
        conn.row_factory = sqlite3.Row
        tables = {r[0] for r in conn.execute("SELECT name FROM sqlite_master WHERE type='table'").fetchall()}
        if "messages" not in tables:
            return []
        cols = {r[1] for r in conn.execute("PRAGMA table_info(messages)").fetchall()}
        sel_cols = ["id", "role"]
        for opt in ("kind", "visibility", "content", "grounding_json", "tool_result_json"):
            if opt in cols:
                sel_cols.append(opt)
        order_col = "ord, id" if "ord" in cols else "rowid"
        rows = conn.execute(
            f"SELECT {', '.join(sel_cols)} FROM messages WHERE session_id = ? ORDER BY {order_col}",
            (session_id,),
        ).fetchall()
        out: list[dict[str, Any]] = []
        for r in rows:
            item = {c: r[c] for c in sel_cols}
            raw_g = item.get("grounding_json")
            if isinstance(raw_g, str) and raw_g.strip():
                try:
                    item["grounding"] = json.loads(raw_g)
                except ValueError:
                    item["grounding"] = None
            else:
                item["grounding"] = None
            out.append(item)
        return out


def _extract_last_closeout(art: Any, db_path: pathlib.Path, session_id: str) -> tuple[dict[str, Any], str]:
    closeout = art.product_closeouts[-1] if art.product_closeouts else {}
    content = str((closeout or {}).get("content") or "")
    if not content:
        msgs = _query_session_messages(db_path, session_id)
        for m in reversed(msgs):
            if m.get("role") == "assistant" and str(m.get("visibility") or "transcript") != "hidden":
                content = str(m.get("content") or "")
                if not closeout and m.get("grounding"):
                    closeout = {"content": content, "grounding": m["grounding"]}
                break
    return (closeout if isinstance(closeout, dict) else {}), content


def _extract_repair_feedback_text(raw_pending_requests: list[dict[str, Any]]) -> str:
    if not raw_pending_requests:
        return ""
    last_req = raw_pending_requests[-1]
    parts: list[str] = []
    for msg in last_req.get("messages") or ():
        content = str(msg.get("content") or "")
        if "Observed handles:" in content or "per rejected citation" in content or "CITATION" in content:
            parts.append(content)
    return "\n".join(parts)


def _run_wire_slot(
    client: Any,
    *,
    project_id: str,
    sidecar: IsolatedSidecarProcess,
    workspace: pathlib.Path,
    manifest: Any,
    unit: GB08RunUnit,
    trace_log: pathlib.Path | None,
) -> dict[str, Any]:
    selector = unit.spec.selector
    sid = client.create_session(project_id, posture="build")["id"]
    client.wait_session_ready(sid)
    before = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
    p = client.send_prompt(sid, f"Execute GB08 presentation wire check for {selector}.")
    submission_id = str(p.get("submission_id") or p.get("id") or "")

    max_repairs = 0
    repair_reply = ""
    if selector == "P01-01-grounded-cited-evidence-openable":
        turns = (
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n app.go"}},)),
            ScriptedTurn(content="Verified Main() at [read#1:3]."),
        )
    elif selector == "P01-02-exempt-speech-evidence-optional":
        turns = (
            ScriptedTurn(content="Understood. No files were read in this window; ready when you are."),
        )
    elif selector == "P01-03-host-assembled-closeout-flag":
        turns = (
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n app.go"}},)),
            ScriptedTurn(content="Checked the application entry point and confirmed it returns ready."),
        )
    elif selector == "P01-04-refused-closeout-partial-wire-state":
        turns = (
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n app.go"}},)),
            ScriptedTurn(content="Claimed unobserved coordinate [read#99:99]."),
        )
        max_repairs = 3
        repair_reply = "Still claiming unobserved coordinate [read#99:99]."
    elif selector == "P01-05-openable-false-for-nonfile-target":
        turns = (
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "ls -la subdir"}},)),
            ScriptedTurn(content="Listed subdir contents [command#1]."),
        )
    else:  # P01-06-withheld-midturn-note-absent-from-feed
        turns = (
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n app.go"}},)),
            ScriptedTurn(
                content="Mid-turn note citing fabricated [read#99:99].",
                tool_calls=({"name": "command", "args": {"command": "wc -l app.go"}},),
            ),
            ScriptedTurn(content="Completed checks on app.go [read#1:3]."),
        )

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
    closeout, presented_content = _extract_last_closeout(art, sidecar.store_db_path, sid)
    grounding = (closeout or {}).get("grounding") or {}
    cited = grounding.get("cited_evidence") or []

    divergences: list[str] = []
    if not after.is_complete():
        divergences.append(f"d10_incomplete:{after.unknown_counters()}")

    if selector == "P01-01-grounded-cited-evidence-openable":
        if not grounding.get("traced"):
            divergences.append("expected_traced_true")
        if grounding.get("evidence_optional"):
            divergences.append("unexpected_evidence_optional_true")
        if grounding.get("host_assembled"):
            divergences.append("unexpected_host_assembled_true")
        if not cited:
            divergences.append("missing_cited_evidence")
        else:
            first = cited[0]
            if str(first.get("verdict") or "") not in GROUNDED_VERDICTS:
                divergences.append(f"unexpected_verdict:{first.get('verdict')}")
            if first.get("openable") is not True:
                divergences.append(f"expected_openable_true:got={first.get('openable')!r}")
    elif selector == "P01-02-exempt-speech-evidence-optional":
        if not grounding.get("traced"):
            divergences.append("exempt_expected_traced_true")
        if not grounding.get("evidence_optional"):
            divergences.append("exempt_expected_evidence_optional_true")
        if grounding.get("host_assembled"):
            divergences.append("exempt_unexpected_host_assembled_true")
        if len(cited) != 0:
            divergences.append(f"exempt_expected_empty_cited_evidence:count={len(cited)}")
    elif selector == "P01-03-host-assembled-closeout-flag":
        if not grounding.get("host_assembled"):
            divergences.append("expected_host_assembled_true")
        if not cited:
            divergences.append("host_assembled_missing_cited_evidence")
        valid_handles = {r.handle for r in art.delivery_log.records if r.model_visible}
        for item in cited:
            h = str(item.get("handle") or "")
            if h and h not in valid_handles:
                divergences.append(f"host_assembled_handle_not_in_delivery_log:{h}")
            if item.get("path") and item.get("openable") is False:
                divergences.append(f"host_assembled_regular_file_not_openable:{item.get('path')}")
    elif selector == "P01-04-refused-closeout-partial-wire-state":
        for item in cited:
            if str(item.get("handle") or "") == "read#99" and str(item.get("verdict") or "") in GROUNDED_VERDICTS:
                divergences.append("invalid_read99_marked_grounded")
        fu = delta.get("friction_units")
        if fu is None or fu < 1:
            divergences.append(f"refused_closeout_expected_friction:friction_units={fu}")
    elif selector == "P01-05-openable-false-for-nonfile-target":
        for item in cited:
            p_str = str(item.get("path") or "")
            if (not p_str or p_str.rstrip("/") == "subdir") and item.get("openable") is True:
                divergences.append(f"nonfile_target_marked_openable_true:{item}")
    else:  # P01-06-withheld-midturn-note-absent-from-feed
        # Check user-facing transcript messages excluding tool-call carriers
        # (which carry tool_calls_json and are not published as agent_note feed items).
        msgs = _query_session_messages(sidecar.store_db_path, sid)
        visible_notes = [
            m
            for m in msgs
            if m.get("role") == "assistant"
            and str(m.get("visibility") or "transcript") != "hidden"
            and not str(m.get("tool_calls_json") or "").strip().startswith("[{")
            and "read#99" in str(m.get("content") or "")
        ]
        if visible_notes:
            divergences.append(f"withheld_midturn_note_visible_in_feed:count={len(visible_notes)}")
        if len(art.delivery_log.records) < 2:
            divergences.append(f"midturn_tool_batch_blocked:records={len(art.delivery_log.records)}")

    return {
        "session_id": sid,
        "passed": len(divergences) == 0,
        "divergences": divergences,
        "d10_before": before.to_dict(),
        "d10_after": after.to_dict(),
        "d10_delta": delta,
        "grounding": grounding,
        "presented_content": presented_content,
        "artifact": art.to_dict(),
    }


def _run_markdown_slot(
    client: Any,
    *,
    project_id: str,
    sidecar: IsolatedSidecarProcess,
    workspace: pathlib.Path,
    manifest: Any,
    unit: GB08RunUnit,
    trace_log: pathlib.Path | None,
) -> dict[str, Any]:
    selector = unit.spec.selector
    sid = client.create_session(project_id, posture="build")["id"]
    client.wait_session_ready(sid)
    before = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
    p = client.send_prompt(sid, f"Execute GB08 markdown presentation check for {selector}.")
    submission_id = str(p.get("submission_id") or p.get("id") or "")

    if selector == "P02-01-pathless-handle-stripped-or-rewritten":
        turns = (
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n app.go"}},)),
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "echo ok"}},)),
            ScriptedTurn(content="Verified Main() at [read#1:3] and ran command [command#2]."),
        )
    elif selector == "P02-02-control-directives-removed-code-preserved":
        turns = (
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n app.go"}},)),
            ScriptedTurn(
                content=(
                    "Literal syntax `[read#1:3]` stays code, and block:\n"
                    "```\n"
                    "[read#99:99]\n"
                    "```\n"
                    "while grounded ref is [read#1:3]."
                )
            ),
        )
    elif selector == "P02-03-resolved-ref-rewritten-no-new-structure":
        turns = (
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n app.go"}},)),
            ScriptedTurn(content="Main returns ready at [read#1:3]."),
        )
    else:  # P02-04-raw-html-and-broken-links-neutralized
        turns = (
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n app.go"}},)),
            ScriptedTurn(
                content='See <a href="https://example.invalid">click</a> and [nowhere]() with [read#1:3].'
            ),
        )

    art = run_scripted_session(
        client,
        session_id=sid,
        turns=turns,
        db_path=sidecar.store_db_path,
        workspace_dir=workspace,
        submission_id=submission_id,
        wait_for_settlement=True,
        max_repair_replies=0,
        engine_provenance=sidecar.provenance,
        run_manifest=manifest,
        poll_timeout=120.0,
    )
    after = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
    delta = after.delta_observed(before)
    closeout, presented_content = _extract_last_closeout(art, sidecar.store_db_path, sid)
    grounding = (closeout or {}).get("grounding") or {}

    divergences: list[str] = []
    if not after.is_complete():
        divergences.append(f"d10_incomplete:{after.unknown_counters()}")
    if not presented_content.strip():
        divergences.append("empty_presented_content")

    if selector == "P02-01-pathless-handle-stripped-or-rewritten":
        if _RAW_COMMAND_HANDLE_RE.search(presented_content):
            divergences.append("pathless_command_handle_leaked_in_presentation")
        if "[read#1:3]" in presented_content:
            divergences.append("raw_read_handle_not_rewritten_in_presentation")
    elif selector == "P02-02-control-directives-removed-code-preserved":
        if "`[read#1:3]`" not in presented_content:
            divergences.append("inline_code_span_altered_in_presentation")
        if "[read#99:99]" not in presented_content:
            divergences.append("fenced_code_block_altered_in_presentation")
    elif selector == "P02-03-resolved-ref-rewritten-no-new-structure":
        if _MD_HEADING_RE.search(presented_content):
            divergences.append("presentation_introduced_markdown_heading")
        if _MD_LIST_RE.search(presented_content):
            divergences.append("presentation_introduced_markdown_list")
        if _RAW_HTML_ANCHOR_RE.search(presented_content):
            divergences.append("presentation_introduced_raw_html_anchor")
        if not grounding.get("traced"):
            divergences.append("expected_grounded_closeout_traced")
    else:  # P02-04-raw-html-and-broken-links-neutralized
        if _RAW_HTML_ANCHOR_RE.search(presented_content):
            divergences.append("raw_html_anchor_not_neutralized")
        if "[nowhere]()" in presented_content:
            divergences.append("broken_empty_link_not_neutralized")

    return {
        "session_id": sid,
        "passed": len(divergences) == 0,
        "divergences": divergences,
        "d10_before": before.to_dict(),
        "d10_after": after.to_dict(),
        "d10_delta": delta,
        "grounding": grounding,
        "presented_content": presented_content,
        "artifact": art.to_dict(),
    }


def _run_feedback_slot(
    client: Any,
    *,
    project_id: str,
    sidecar: IsolatedSidecarProcess,
    workspace: pathlib.Path,
    manifest: Any,
    unit: GB08RunUnit,
    trace_log: pathlib.Path | None,
) -> dict[str, Any]:
    selector = unit.spec.selector
    sid = client.create_session(project_id, posture="build")["id"]
    client.wait_session_ready(sid)
    before = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
    p = client.send_prompt(sid, f"Read app.go and report line 3 ({selector}).")
    submission_id = str(p.get("submission_id") or p.get("id") or "")

    if selector == "P03-01-feedback-unknown-handle-admissible-samples":
        bad_reply = "Verified [read#1:3] and claimed [read#99:99]."
        expected_offender_token = "read#99"
    elif selector == "P03-02-feedback-uncovered-range-offender-named":
        bad_reply = "Claimed uncovered line [read#1:99]."
        expected_offender_token = "read#1"
    elif selector == "P03-03-feedback-excerpt-mismatch-offender-named":
        bad_reply = 'Claimed wrong excerpt [read#1:3 "nonexistent_symbol()"].'
        expected_offender_token = "read#1"
    else:  # P03-04-feedback-wrong-path-offender-named
        bad_reply = "Claimed wrong path [read#1 wrong.go:3]."
        expected_offender_token = "wrong.go"

    art = run_scripted_session(
        client,
        session_id=sid,
        turns=(
            ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n app.go"}},)),
            ScriptedTurn(content=bad_reply),
        ),
        db_path=sidecar.store_db_path,
        workspace_dir=workspace,
        submission_id=submission_id,
        wait_for_settlement=True,
        max_repair_replies=2,
        repair_reply_content="Verified Main() at [read#1:3].",
        engine_provenance=sidecar.provenance,
        run_manifest=manifest,
        poll_timeout=120.0,
    )
    after = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
    delta = after.delta_observed(before)

    offered, _ = _extract_repair_feedback_offers(art.raw_pending_requests)
    feedback_blob = _extract_repair_feedback_text(art.raw_pending_requests)
    offender_named = expected_offender_token in feedback_blob
    audit = audit_refusal_feedback(
        art.delivery_log,
        offered_handles=offered,
        named_offenders=(expected_offender_token,) if offender_named else (),
        actual_failing_citations=(expected_offender_token,),
    )

    divergences: list[str] = []
    if not after.is_complete():
        divergences.append(f"d10_incomplete:{after.unknown_counters()}")
    if not feedback_blob.strip():
        divergences.append("missing_serialized_repair_feedback")
    if not offender_named:
        divergences.append(f"expected_offender_not_named:{expected_offender_token}")
    if not audit.passed:
        if audit.inadmissible_samples:
            divergences.append(f"inadmissible_feedback_samples:{list(audit.inadmissible_samples)}")
        if audit.misnamed_offenders:
            divergences.append(f"misnamed_feedback_offenders:{list(audit.misnamed_offenders)}")

    return {
        "session_id": sid,
        "passed": len(divergences) == 0,
        "divergences": divergences,
        "d10_before": before.to_dict(),
        "d10_after": after.to_dict(),
        "d10_delta": delta,
        "feedback_audit": {
            "passed": audit.passed,
            "offered_handles": offered,
            "expected_offender_token": expected_offender_token,
            "offender_named": offender_named,
            "inadmissible_samples": list(audit.inadmissible_samples),
            "misnamed_offenders": list(audit.misnamed_offenders),
        },
        "artifact": art.to_dict(),
    }


def _run_diagnostic_slot(
    client: Any,
    *,
    project_id: str,
    sidecar: IsolatedSidecarProcess,
    workspace: pathlib.Path,
    manifest: Any,
    unit: GB08RunUnit,
    trace_log: pathlib.Path | None,
) -> dict[str, Any]:
    selector = unit.spec.selector
    plat = "macos" if sidecar.provenance.platform == "darwin" else "linux"
    c_ok = Citation("handle", "[read#1:3]", handle="read#1", start_line=3, citation_id="c1")
    c_bad = Citation("handle", "[read#99:99]", handle="read#99", start_line=99, citation_id="c2")

    def _run_one(reply: str, *, max_repairs: int = 0, repair_reply: str = "") -> tuple[str, Any, Any, Any]:
        sid = client.create_session(project_id, posture="build")["id"]
        client.wait_session_ready(sid)
        b = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
        p = client.send_prompt(sid, f"Read app.go and report line 3 ({selector}).")
        art = run_scripted_session(
            client,
            session_id=sid,
            turns=(
                ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n app.go"}},)),
                ScriptedTurn(content=reply),
            ),
            db_path=sidecar.store_db_path,
            workspace_dir=workspace,
            submission_id=str(p.get("submission_id") or p.get("id") or ""),
            wait_for_settlement=True,
            max_repair_replies=max_repairs,
            repair_reply_content=repair_reply,
            engine_provenance=sidecar.provenance,
            run_manifest=manifest,
            poll_timeout=120.0,
        )
        a = probe_store_db(sidecar.store_db_path, sid, trace_log_path=trace_log)
        return sid, art, b, a

    divergences: list[str] = []
    if selector == "P04-01-diagnostic-silent-on-healthy-closeout":
        sid, art, before, after = _run_one("Verified Main() at [read#1:3].")
        delta = after.delta_observed(before)
        closeout, _ = _extract_last_closeout(art, sidecar.store_db_path, sid)
        cited = ((closeout or {}).get("grounding") or {}).get("cited_evidence") or []
        verdict = str((cited[0] if cited else {}).get("verdict") or "unverifiable")
        scope = PermittedScope(session_id=sid, round_id="round-1", intent_window="intent-1", platform=plat)
        outcome = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(ProductVerdict("c1", verdict, "read#1"),),
            records=art.product_records,
        )
        diag_on = run_dev_diagnostic(art.delivery_log, (c_ok,), scope, outcome, enabled=True)
        diag_off = run_dev_diagnostic(art.delivery_log, (c_ok,), scope, outcome, enabled=False)
        if not after.is_complete():
            divergences.append(f"d10_incomplete:{after.unknown_counters()}")
        if len(diag_on) != 0:
            divergences.append(f"healthy_diagnostic_emitted_findings:{[d.kind for d in diag_on]}")
        if len(diag_off) != 0:
            divergences.append(f"disabled_diagnostic_emitted_findings:{[d.kind for d in diag_off]}")
        return {
            "session_id": sid,
            "passed": len(divergences) == 0,
            "divergences": divergences,
            "d10_before": before.to_dict(),
            "d10_after": after.to_dict(),
            "d10_delta": delta,
            "diagnostic": {
                "enabled_kinds": sorted(d.kind for d in diag_on),
                "disabled_count": len(diag_off),
            },
            "artifact": art.to_dict(),
        }

    if selector == "P04-02-diagnostic-detects-all-three-contradictions":
        sid, art, before, after = _run_one(
            "Verified [read#1:3] and claimed [read#99:99].",
            max_repairs=2,
            repair_reply="Verified Main() at [read#1:3].",
        )
        delta = after.delta_observed(before)
        scope = PermittedScope(session_id=sid, round_id="round-1", intent_window="intent-1", platform=plat)
        injected = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(
                ProductVerdict("c1", "unverifiable", ""),
                ProductVerdict("c2", "traced", "read#99"),
            ),
            records=art.product_records,
        )
        # Pass duplicated citations to also verify deduplication
        diag_on = run_dev_diagnostic(
            art.delivery_log,
            (c_ok, c_ok, c_bad, c_bad),
            scope,
            injected,
            enabled=True,
            had_prior_refusal=True,
            recorded_accepted=True,
        )
        diag_off = run_dev_diagnostic(
            art.delivery_log,
            (c_ok, c_bad),
            scope,
            injected,
            enabled=False,
            had_prior_refusal=True,
            recorded_accepted=True,
        )
        expected_kinds = {"grounded_without_support", "refusal_with_unique_support", "accepted_after_refusal"}
        found_kinds = {d.kind for d in diag_on}
        if not after.is_complete():
            divergences.append(f"d10_incomplete:{after.unknown_counters()}")
        if found_kinds != expected_kinds:
            divergences.append(f"diagnostic_contradiction_mismatch:got={sorted(found_kinds)}")
        if len(diag_on) != 3:
            divergences.append(f"diagnostic_dedup_failed:count={len(diag_on)}")
        if len(diag_off) != 0:
            divergences.append(f"disabled_diagnostic_emitted_findings:{len(diag_off)}")
        return {
            "session_id": sid,
            "passed": len(divergences) == 0,
            "divergences": divergences,
            "d10_before": before.to_dict(),
            "d10_after": after.to_dict(),
            "d10_delta": delta,
            "diagnostic": {
                "enabled_kinds": sorted(found_kinds),
                "enabled_count": len(diag_on),
                "disabled_count": len(diag_off),
            },
            "artifact": art.to_dict(),
        }

    # P04-03-diagnostic-enabled-vs-disabled-parity
    sid_a, art_a, b_a, a_a = _run_one("Verified Main() at [read#1:3].")
    sid_b, art_b, b_b, a_b = _run_one("Verified Main() at [read#1:3].")
    delta_a = a_a.delta_observed(b_a)
    delta_b = a_b.delta_observed(b_b)
    closeout_a, _ = _extract_last_closeout(art_a, sidecar.store_db_path, sid_a)
    closeout_b, _ = _extract_last_closeout(art_b, sidecar.store_db_path, sid_b)
    verdict_a = str((((closeout_a or {}).get("grounding") or {}).get("cited_evidence") or [{}])[0].get("verdict") or "")
    verdict_b = str((((closeout_b or {}).get("grounding") or {}).get("cited_evidence") or [{}])[0].get("verdict") or "")

    scope_a = PermittedScope(session_id=sid_a, round_id="round-1", intent_window="intent-1", platform=plat)
    scope_b = PermittedScope(session_id=sid_b, round_id="round-1", intent_window="intent-1", platform=plat)
    diag_a = run_dev_diagnostic(
        art_a.delivery_log,
        (c_ok,),
        scope_a,
        SurfaceOutcome(
            surface="coordinator_closeout",
            wording="confident",
            verdicts=(ProductVerdict("c1", verdict_a, "read#1"),),
            minted_records=art_a.product_records,
        ),
        enabled=True,
    )
    diag_b = run_dev_diagnostic(
        art_b.delivery_log,
        (c_ok,),
        scope_b,
        SurfaceOutcome(
            surface="coordinator_closeout",
            wording="confident",
            verdicts=(ProductVerdict("c1", verdict_b, "read#1"),),
            minted_records=art_b.product_records,
        ),
        enabled=False,
    )
    if not a_a.is_complete() or not a_b.is_complete():
        divergences.append(f"d10_incomplete:a={a_a.unknown_counters()},b={a_b.unknown_counters()}")
    if verdict_a != verdict_b:
        divergences.append(f"diagnostic_parity_verdict_mismatch:{verdict_a}!={verdict_b}")
    if delta_a != delta_b:
        divergences.append(f"diagnostic_parity_d10_mismatch:{delta_a}!={delta_b}")
    if len(diag_a) != 0 or len(diag_b) != 0:
        divergences.append(f"diagnostic_parity_unexpected_findings:on={len(diag_a)},off={len(diag_b)}")

    return {
        "session_id": sid_a,
        "paired_session_id": sid_b,
        "passed": len(divergences) == 0,
        "divergences": divergences,
        "d10_before": b_a.to_dict(),
        "d10_after": a_a.to_dict(),
        "d10_delta": delta_a,
        "d10_paired_delta": delta_b,
        "diagnostic": {
            "enabled_verdict": verdict_a,
            "disabled_verdict": verdict_b,
            "enabled_count": len(diag_a),
            "disabled_count": len(diag_b),
        },
        "artifact": art_a.to_dict(),
    }


def run_gb08_full_sidecar(
    *,
    engine_bin: str = "",
    product_root: str = "",
    expected_engine_sha256: str = "",
    out_dir: pathlib.Path | None = None,
    run_id: str = "20261127-3bb1464d-gb08-full",
    families: Sequence[str] | None = None,
    shard: int = 0,
    shards: int = 1,
    trace_friction: bool = True,
    dry_run: bool = False,
) -> dict[str, Any]:
    counts = validate_gb08_execution_map()
    runs = select_gb08_runs(families=families, shard=shard, shards=shards)
    native_plan = build_native_den_plan(families=families, run_id=run_id, product_root=product_root)

    if dry_run:
        summary = {
            "card": "GB08",
            "run_id": run_id,
            "dry_run": True,
            "trace_friction": trace_friction,
            "families": list(families) if families else list(GB08_HEADLESS_FAMILY_IDS),
            "shard": shard,
            "shards": shards,
            "catalog_counts": counts,
            "headless_slot_count": len(runs),
            "native_slot_count": native_plan["native_slot_count"],
            "passed": True,
            "planned_headless_slots": [
                {
                    "slot_index": u.slot_index,
                    "selector": u.spec.selector,
                    "family_id": u.spec.family_id,
                    "lane": u.spec.lane,
                    "mode": u.exec_spec.mode,
                    "title": u.spec.title,
                }
                for u in runs
            ],
            "native_plan": native_plan,
        }
        if out_dir is not None:
            out_dir.mkdir(parents=True, exist_ok=True)
            (out_dir / "gb08-sidecar-summary.json").write_text(
                json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8"
            )
            (out_dir / "gb08-native-plan.json").write_text(
                json.dumps(native_plan, indent=2, sort_keys=True), encoding="utf-8"
            )
        return summary

    if not engine_bin or not product_root or out_dir is None:
        raise ValueError("--engine-bin, --product-root, and --out-dir are required unless --dry-run or --list-native-slots is set")

    out_dir.mkdir(parents=True, exist_ok=True)
    artifacts_dir = out_dir / "artifacts"
    artifacts_dir.mkdir(parents=True, exist_ok=True)

    slot_results: list[dict[str, Any]] = []
    divergences_records: list[dict[str, Any]] = []
    manifest_dict: dict[str, Any] | None = None
    plat = "unknown"

    for run_unit in runs:
        run_tag = run_unit.spec.selector
        case_dir = out_dir / "cases" / run_tag
        workspace = case_dir / "workspace"
        config_dir = case_dir / "config"
        _init_git_workspace(workspace)

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
                if run_unit.exec_spec.mode == "wire":
                    outcome = _run_wire_slot(
                        client,
                        project_id=pid,
                        sidecar=sidecar,
                        workspace=workspace,
                        manifest=manifest,
                        unit=run_unit,
                        trace_log=trace_log,
                    )
                elif run_unit.exec_spec.mode == "markdown":
                    outcome = _run_markdown_slot(
                        client,
                        project_id=pid,
                        sidecar=sidecar,
                        workspace=workspace,
                        manifest=manifest,
                        unit=run_unit,
                        trace_log=trace_log,
                    )
                elif run_unit.exec_spec.mode == "feedback":
                    outcome = _run_feedback_slot(
                        client,
                        project_id=pid,
                        sidecar=sidecar,
                        workspace=workspace,
                        manifest=manifest,
                        unit=run_unit,
                        trace_log=trace_log,
                    )
                else:
                    outcome = _run_diagnostic_slot(
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
                    "passed": False,
                    "divergences": [f"runner_exception:{type(exc).__name__}:{exc}"],
                    "d10_before": None,
                    "d10_after": None,
                    "d10_delta": None,
                }

        artifact_path = artifacts_dir / f"{run_tag}.json"
        artifact_path.write_text(json.dumps(outcome, indent=2, sort_keys=True), encoding="utf-8")

        row = {
            "slot_index": run_unit.slot_index,
            "selector": run_unit.spec.selector,
            "family_id": run_unit.spec.family_id,
            "lane": run_unit.spec.lane,
            "mode": run_unit.exec_spec.mode,
            "passed": bool(outcome["passed"]),
            "divergences": list(outcome.get("divergences") or ()),
            "d10_before": outcome.get("d10_before"),
            "d10_after": outcome.get("d10_after"),
            "d10_delta": outcome.get("d10_delta"),
            "grounding": outcome.get("grounding"),
            "feedback_audit": outcome.get("feedback_audit"),
            "diagnostic": outcome.get("diagnostic"),
            "artifact_path": str(artifact_path),
        }
        slot_results.append(row)
        if not row["passed"]:
            divergences_records.append(
                {
                    "kind": "slot_divergence",
                    "selector": row["selector"],
                    "family_id": row["family_id"],
                    "mode": row["mode"],
                    "divergences": row["divergences"],
                    "d10_delta": row["d10_delta"],
                }
            )

    div_path = out_dir / "gb08-sidecar-divergences.jsonl"
    with div_path.open("w", encoding="utf-8") as fh:
        for rec in divergences_records:
            fh.write(json.dumps(rec, sort_keys=True) + "\n")

    native_plan_path = out_dir / "gb08-native-plan.json"
    native_plan_path.write_text(json.dumps(native_plan, indent=2, sort_keys=True), encoding="utf-8")

    passed_count = sum(1 for r in slot_results if r["passed"])
    summary = {
        "card": "GB08",
        "run_id": run_id,
        "dry_run": False,
        "platform": plat,
        "trace_friction": trace_friction,
        "families": list(families) if families else list(GB08_HEADLESS_FAMILY_IDS),
        "shard": shard,
        "shards": shards,
        "catalog_counts": counts,
        "headless_slot_count": len(slot_results),
        "native_slot_count": native_plan["native_slot_count"],
        "passed_count": passed_count,
        "failed_count": len(slot_results) - passed_count,
        "divergence_count": len(divergences_records),
        "passed": passed_count == len(slot_results),
        "run_manifest": manifest_dict,
        "native_plan_path": str(native_plan_path),
        "native_plan": native_plan,
        "slots": slot_results,
    }
    (out_dir / "gb08-sidecar-summary.json").write_text(
        json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8"
    )
    return summary


def build_arg_parser() -> argparse.ArgumentParser:
    ap = argparse.ArgumentParser(
        description="GB08 full headless sidecar execution runner and native Den scenario planner"
    )
    ap.add_argument("--engine-bin", default="")
    ap.add_argument("--product-root", default="")
    ap.add_argument("--expected-engine-sha256", default="")
    ap.add_argument("--out-dir", default="")
    ap.add_argument("--run-id", default="20261127-3bb1464d-gb08-full")
    ap.add_argument("--families", default=",".join(GB08_HEADLESS_FAMILY_IDS))
    ap.add_argument("--shard", type=int, default=0)
    ap.add_argument("--shards", type=int, default=1)
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--list-native-slots", action="store_true")
    ap.add_argument("--no-trace-friction", action="store_true")
    return ap


def main(argv: list[str] | None = None) -> int:
    ap = build_arg_parser()
    args = ap.parse_args(argv)

    families = [f.strip() for f in args.families.split(",") if f.strip()]
    out_dir = pathlib.Path(args.out_dir) if args.out_dir else None

    if args.list_native_slots:
        plan = build_native_den_plan(
            families=None,
            run_id=args.run_id,
            product_root=args.product_root,
        )
        if out_dir is not None:
            out_dir.mkdir(parents=True, exist_ok=True)
            (out_dir / "gb08-native-plan.json").write_text(
                json.dumps(plan, indent=2, sort_keys=True), encoding="utf-8"
            )
        print(json.dumps(plan, indent=2, sort_keys=True))
        return 0

    if not args.dry_run:
        missing = [
            flag
            for flag, val in (
                ("--engine-bin", args.engine_bin),
                ("--product-root", args.product_root),
                ("--out-dir", args.out_dir),
            )
            if not val
        ]
        if missing:
            ap.error(f"the following arguments are required for live execution: {', '.join(missing)}")

    summary = run_gb08_full_sidecar(
        engine_bin=args.engine_bin,
        product_root=args.product_root,
        expected_engine_sha256=args.expected_engine_sha256,
        out_dir=out_dir,
        run_id=args.run_id,
        families=families,
        shard=args.shard,
        shards=args.shards,
        trace_friction=not args.no_trace_friction,
        dry_run=args.dry_run,
    )
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0 if summary["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
