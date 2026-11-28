"""Full GB03 (Evidence recording and scope: V01..V07) D1 sidecar execution runner.

Covers:
- All 49 fixed sidecar slots across V01 (14), V03 (8), V04 (10 incl.
  `V04-10-copied-second-file-tag`), V05 (5), V06 (10), and V07 (2)
- V02 generated sidecar lookalikes (100 instances per seed set by default,
  `--base-seed 20261127`, `--limit-v02 M`)

Design:
- Uses `IsolatedSidecarProcess` with Decision 012 `trace_friction=True` by
  default (`--no-trace-friction` optional for V02 bulk runs)
- Uses a fresh session per slot, recording D10 before/after friction snapshots
- Checks minting parity (`compare_records`), V02 zero promotion, V03
  supersession keeps old body, V06 cross-chat/cross-project isolation, and
  V07 restart handle-monotonicity & unreadable-ledger no-exemption
- Writes `gb03-sidecar-summary.json` and `gb03-sidecar-divergences.jsonl`
  without ever masking D1 bugs
"""
from __future__ import annotations

import argparse
from dataclasses import dataclass
import json
import pathlib
import sqlite3
import sys
from typing import Any, Callable

from grounding.checker import compare_records
from grounding.friction_probe import probe_store_db
from grounding.generators import generate_v02
from grounding.sidecar_driver import (
    IsolatedSidecarProcess,
    ScriptedTurn,
    SidecarRunArtifact,
    SidecarRunManifest,
    SidecarSessionClient,
    run_scripted_session,
)
from grounding.suites.gb03_evidence.catalog import (
    GB03_FAMILY_IDS,
    GB03_FIXED_SLOTS,
    V02_GENERATED_SPEC,
)
from grounding.suites.gb03_evidence.fixtures import D16_RAW_SECRET_CANARY


FIXED_SIDECAR_SELECTORS: tuple[str, ...] = tuple(
    s.selector for s in GB03_FIXED_SLOTS if s.lane == "sidecar"
)


@dataclass(frozen=True)
class GB03WorkItem:
    """One executable sidecar work item (either a fixed slot or a V02 instance)."""

    family_id: str
    selector: str
    v02_base_seed: str = ""
    v02_index: int | None = None


def parse_families_arg(raw: str) -> tuple[str, ...]:
    """Parse and validate `--families` comma-separated list."""
    if not raw or not raw.strip():
        return GB03_FAMILY_IDS
    parts = [p.strip().upper() for p in raw.split(",") if p.strip()]
    unknown = [p for p in parts if p not in GB03_FAMILY_IDS]
    if unknown:
        raise ValueError(f"Unknown GB03 family IDs: {unknown}; valid={GB03_FAMILY_IDS}")
    dedup: list[str] = []
    for p in parts:
        if p not in dedup:
            dedup.append(p)
    return tuple(dedup)


def select_gb03_work_items(
    *,
    families: tuple[str, ...] = GB03_FAMILY_IDS,
    base_seed: str = "20261127",
    limit_v02: int = V02_GENERATED_SPEC.sidecar_per_platform_per_seed,
    shard: int = 0,
    shards: int = 1,
) -> tuple[GB03WorkItem, ...]:
    """Deterministically enumerate and shard GB03 sidecar work items."""
    if shards < 1:
        raise ValueError(f"shards must be >= 1, got {shards}")
    if shard < 0 or shard >= shards:
        raise ValueError(f"shard must be in [0, {shards}), got {shard}")
    if limit_v02 < 0:
        raise ValueError(f"limit_v02 must be >= 0, got {limit_v02}")

    fam_set = set(families)
    items: list[GB03WorkItem] = []
    for slot in GB03_FIXED_SLOTS:
        if slot.lane == "sidecar" and slot.family_id in fam_set:
            items.append(GB03WorkItem(family_id=slot.family_id, selector=slot.selector))
    if "V02" in fam_set:
        for i in range(limit_v02):
            items.append(
                GB03WorkItem(
                    family_id="V02",
                    selector=f"V02-lookalike-{base_seed}-{i}",
                    v02_base_seed=base_seed,
                    v02_index=i,
                )
            )

    return tuple(item for idx, item in enumerate(items) if idx % shards == shard)


def _inspect_artifact(art: SidecarRunArtifact, *, initial_turns: int) -> dict[str, Any]:
    closeout = art.product_closeouts[-1] if art.product_closeouts else {}
    grounding = (closeout or {}).get("grounding") or {}
    cited = grounding.get("cited_evidence") or []
    retry_count = int(grounding.get("retry_count") or 0)
    host_assembled = bool(grounding.get("host_assembled") is True)
    hint_code = str(grounding.get("hint_code") or "")
    extra_repair_prompts = max(0, len(art.raw_pending_requests) - initial_turns)
    refused = bool(retry_count > 0 or host_assembled or hint_code or extra_repair_prompts > 0)
    mint_defects = compare_records(art.delivery_log, art.product_records)
    return {
        "session_id": art.session_id,
        "traced": grounding.get("traced"),
        "evidence_optional": grounding.get("evidence_optional"),
        "retry_count": retry_count,
        "host_assembled": host_assembled,
        "hint_code": hint_code,
        "extra_repair_prompts": extra_repair_prompts,
        "refused": refused,
        "cited_evidence_count": len(cited),
        "cited_evidence": cited,
        "minted_handles": [r.handle for r in art.product_records],
        "delivered_handles": [r.handle for r in art.delivery_log.records],
        "minting_parity_defects": [
            {"kind": d.kind, "handle": d.handle, "detail": d.detail} for d in mint_defects
        ],
    }


@dataclass
class _RunnerContext:
    client: SidecarSessionClient
    sidecar: IsolatedSidecarProcess
    project_id: str
    workspace: pathlib.Path
    out_dir: pathlib.Path
    artifacts_dir: pathlib.Path
    manifest: SidecarRunManifest
    trace_log: pathlib.Path | None
    poll_timeout: float
    engine_bin: str
    product_root: str
    expected_engine_sha256: str
    trace_friction: bool

    def run_session(
        self,
        *,
        selector: str,
        prompt: str,
        turns: tuple[ScriptedTurn, ...],
        max_repair_replies: int = 0,
        repair_reply_content: str = "",
        project_id: str | None = None,
        workspace_dir: pathlib.Path | None = None,
        session_id: str | None = None,
        save_name: str | None = None,
    ) -> tuple[SidecarRunArtifact, dict[str, Any], dict[str, Any], dict[str, Any], dict[str, Any]]:
        pid = project_id or self.project_id
        ws = workspace_dir or self.workspace
        if session_id is None:
            sid = self.client.create_session(pid)["id"]
            self.client.wait_session_ready(sid)
        else:
            sid = session_id

        before = probe_store_db(self.sidecar.store_db_path, sid, trace_log_path=self.trace_log)
        p = self.client.send_prompt(sid, prompt)
        art = run_scripted_session(
            self.client,
            session_id=sid,
            turns=turns,
            db_path=self.sidecar.store_db_path,
            workspace_dir=ws,
            submission_id=str(p.get("submission_id") or p.get("id") or ""),
            wait_for_settlement=True,
            max_repair_replies=max_repair_replies,
            repair_reply_content=repair_reply_content,
            engine_provenance=self.sidecar.provenance,
            run_manifest=self.manifest,
            poll_timeout=self.poll_timeout,
        )
        after = probe_store_db(self.sidecar.store_db_path, sid, trace_log_path=self.trace_log)
        fname = f"{save_name or selector}.json"
        (self.artifacts_dir / fname).write_text(
            json.dumps(art.to_dict(), indent=2, sort_keys=True), encoding="utf-8"
        )
        info = _inspect_artifact(art, initial_turns=len(turns))
        return art, info, before.to_dict(), after.to_dict(), after.delta_observed(before)


def _cmd_turn(command: str) -> ScriptedTurn:
    return ScriptedTurn(tool_calls=({"name": "command", "args": {"command": command}},))


def _make_report(
    *,
    selector: str,
    family_id: str,
    passed: bool,
    info: dict[str, Any],
    d10_before: dict[str, Any],
    d10_after: dict[str, Any],
    d10_delta: dict[str, Any],
    extra: dict[str, Any] | None = None,
    divergence: dict[str, Any] | None = None,
) -> tuple[dict[str, Any], dict[str, Any] | None]:
    rec: dict[str, Any] = {
        "selector": selector,
        "family_id": family_id,
        "lane": "sidecar",
        "passed": passed,
        "d10_before": d10_before,
        "d10_after": d10_after,
        "d10_delta": d10_delta,
        "summary": info,
    }
    if extra:
        rec.update(extra)
    return rec, divergence


# ---------------------------------------------------------------------------
# V01 · Minting (14 sidecar slots)
# ---------------------------------------------------------------------------

def _run_v01_01(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V01-01-read-file-region"
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Read internal/a.go and report line 4.",
        turns=(
            _cmd_turn("cat -n internal/a.go"),
            ScriptedTurn(content='Alpha returns 42 at [read#1:4 "return 42"].'),
        ),
    )
    ok = (
        "read#1" in info["minted_handles"]
        and not info["refused"]
        and info["cited_evidence_count"] >= 1
        and len(info["minting_parity_defects"]) == 0
    )
    div = None if ok else {
        "selector": sel, "family_id": "V01", "kind": "minting_or_grounding_mismatch",
        "detail": f"handles={info['minted_handles']} refused={info['refused']} parity={info['minting_parity_defects']}",
    }
    return _make_report(selector=sel, family_id="V01", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v01_02(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V01-02-grep-find-list-stat-wc"
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Inspect workspace with grep, find, ls, and wc.",
        turns=(
            _cmd_turn("grep -n Alpha internal/a.go"),
            _cmd_turn("find internal -maxdepth 2 -type f | sort"),
            _cmd_turn("wc -l internal/a.go"),
            ScriptedTurn(content="Observed grep [command#1], find [command#2], and wc [command#3]."),
        ),
    )
    expected = {"command#1", "command#2", "command#3"}
    ok = expected.issubset(set(info["minted_handles"])) and not info["refused"] and len(info["minting_parity_defects"]) == 0
    div = None if ok else {
        "selector": sel, "family_id": "V01", "kind": "minting_or_grounding_mismatch",
        "detail": f"handles={info['minted_handles']} refused={info['refused']} parity={info['minting_parity_defects']}",
    }
    return _make_report(selector=sel, family_id="V01", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v01_03(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V01-03-edit-write-replace-restore"
    (ctx.workspace / "internal" / "mut03.go").write_text("package app\n\nconst V = 1\n", encoding="utf-8")
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Update internal/mut03.go and read it back.",
        turns=(
            _cmd_turn("cat -n internal/mut03.go"),
            _cmd_turn("printf 'package app\\n\\nconst V = 2\\n' > internal/mut03.go"),
            _cmd_turn("cat -n internal/mut03.go"),
            ScriptedTurn(content='Updated constant in [read#2:3 "const V = 2"] after [command#2].'),
        ),
    )
    ok = "read#1" in info["minted_handles"] and "read#2" in info["minted_handles"] and not info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V01", "kind": "mutation_minting_mismatch",
        "detail": f"handles={info['minted_handles']} refused={info['refused']} parity={info['minting_parity_defects']}",
    }
    return _make_report(selector=sel, family_id="V01", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v01_04(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V01-04-rewrite-delete-diff"
    (ctx.workspace / "internal" / "tmp04.txt").write_text("before\n", encoding="utf-8")
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Rewrite, diff, and remove internal/tmp04.txt.",
        turns=(
            _cmd_turn("printf 'after\\n' > internal/tmp04.txt && cat -n internal/tmp04.txt"),
            _cmd_turn("rm -f internal/tmp04.txt && echo 'deleted'"),
            ScriptedTurn(content="Rewrote and removed temp file as recorded in [command#1] and [command#2]."),
        ),
    )
    ok = "command#1" in info["minted_handles"] and "command#2" in info["minted_handles"] and not info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V01", "kind": "rewrite_delete_minting_mismatch",
        "detail": f"handles={info['minted_handles']} refused={info['refused']}",
    }
    return _make_report(selector=sel, family_id="V01", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v01_05(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V01-05-append-only-ordinals"
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Read first.go, second.go, and third.go in order and report line 3 of each.",
        turns=(
            _cmd_turn("cat -n first.go"),
            _cmd_turn("cat -n second.go"),
            _cmd_turn("cat -n third.go"),
            ScriptedTurn(content="Verified First() [read#1:3], Second() [read#2:3], and Third() [read#3:3]."),
        ),
    )
    ok = (
        {"read#1", "read#2", "read#3"}.issubset(set(info["minted_handles"]))
        and not info["refused"]
        and info["cited_evidence_count"] >= 3
        and len(info["minting_parity_defects"]) == 0
    )
    div = None if ok else {
        "selector": sel, "family_id": "V01", "kind": "ordinal_or_parity_mismatch",
        "detail": f"handles={info['minted_handles']} refused={info['refused']} cited={info['cited_evidence_count']} parity={info['minting_parity_defects']}",
    }
    return _make_report(selector=sel, family_id="V01", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v01_06(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V01-06-command-success-receipt"
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Run a passing command and cite its receipt.",
        turns=(
            _cmd_turn("echo 'PASS: all_ok'"),
            ScriptedTurn(content='Command succeeded with output recorded in [command#1 "PASS: all_ok"].'),
        ),
    )
    ok = "command#1" in info["minted_handles"] and not info["refused"] and len(info["minting_parity_defects"]) == 0
    div = None if ok else {
        "selector": sel, "family_id": "V01", "kind": "command_success_receipt_mismatch",
        "detail": f"handles={info['minted_handles']} refused={info['refused']} parity={info['minting_parity_defects']}",
    }
    return _make_report(selector=sel, family_id="V01", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v01_07(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V01-07-command-error-outcome-receipt"
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Run failing command and report outcome.",
        turns=(
            _cmd_turn("echo 'FAIL: unit_x' && exit 1"),
            ScriptedTurn(content='Verified failure [command#1] and fabricated pass [command#1 "ALL TESTS PASSED"].'),
        ),
        max_repair_replies=3,
        repair_reply_content="Command failed as recorded in [command#1].",
    )
    ok = "command#1" in info["minted_handles"] and info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V01", "kind": "false_accept_fabricated_excerpt",
        "detail": f"handles={info['minted_handles']} refused={info['refused']}",
    }
    return _make_report(selector=sel, family_id="V01", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v01_08(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V01-08-git-error-outcome-receipt"
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Run failing git check and report outcome.",
        turns=(
            _cmd_turn("git rev-parse --verify refs/heads/nonexistent-gb03-branch"),
            ScriptedTurn(content='Git succeeded cleanly [command#1 "working tree clean"].'),
        ),
        max_repair_replies=3,
        repair_reply_content="Git command failed as recorded in [command#1].",
    )
    ok = "command#1" in info["minted_handles"] and info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V01", "kind": "false_accept_git_error_receipt",
        "detail": f"handles={info['minted_handles']} refused={info['refused']}",
    }
    return _make_report(selector=sel, family_id="V01", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v01_09(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V01-09-refused-invocation-no-file-proof"
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Try reading internal/missing_refused.go and report line 1.",
        turns=(
            _cmd_turn("cat -n internal/missing_refused.go"),
            ScriptedTurn(content="Read line 1 of [internal/missing_refused.go:1]."),
        ),
        max_repair_replies=3,
        repair_reply_content="The file read failed as recorded in [command#1].",
    )
    has_read = any(h.startswith("read#") for h in info["minted_handles"])
    ok = (not has_read) and info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V01", "kind": "false_accept_refused_file_read",
        "detail": f"handles={info['minted_handles']} refused={info['refused']}",
    }
    return _make_report(selector=sel, family_id="V01", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v01_10(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V01-10-failed-mutation-no-file-proof"
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Attempt a failing file mutation and report.",
        turns=(
            _cmd_turn("echo 'ERROR: edit failed' >&2 && exit 1"),
            ScriptedTurn(content='Applied change at [internal/a.go:99 "UNAPPLIED_EDIT"].'),
        ),
        max_repair_replies=3,
        repair_reply_content="Mutation failed as recorded in [command#1].",
    )
    has_read = any(h.startswith("read#") for h in info["minted_handles"])
    ok = (not has_read) and info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V01", "kind": "false_accept_failed_mutation",
        "detail": f"handles={info['minted_handles']} refused={info['refused']}",
    }
    return _make_report(selector=sel, family_id="V01", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v01_11(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V01-11-web-scan-survey-surface-kinds"
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Inspect workspace files and report.",
        turns=(
            _cmd_turn("cat -n internal/a.go"),
            _cmd_turn("ls -la internal"),
            ScriptedTurn(content="Observed file [read#1:3] and directory listing [command#2]."),
        ),
    )
    ok = "read#1" in info["minted_handles"] and "command#2" in info["minted_handles"] and not info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V01", "kind": "surface_kinds_mismatch",
        "detail": f"handles={info['minted_handles']} refused={info['refused']}",
    }
    return _make_report(selector=sel, family_id="V01", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v01_contact_slot(ctx: _RunnerContext, sel: str, prefix: str, url: str) -> tuple[dict[str, Any], dict[str, Any] | None]:
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt=f"Report whether {url} was observed.",
        turns=(
            _cmd_turn(f"echo 'contact_probe: {prefix}#1 {url}'"),
            ScriptedTurn(content=f"Cited contact handle [{prefix}#1] and URL {url}."),
        ),
        max_repair_replies=3,
        repair_reply_content="Only local command output was observed [command#1].",
    )
    minted_contact = any(h.startswith(f"{prefix}#") for h in info["minted_handles"])
    ok = (not minted_contact) and info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V01", "kind": "f2_contact_record_promoted",
        "finding_ref": "GB01-F2",
        "detail": f"prefix={prefix} handles={info['minted_handles']} refused={info['refused']}",
    }
    return _make_report(selector=sel, family_id="V01", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v01_12(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    return _run_v01_contact_slot(ctx, "V01-12-f2-dialed-host-contact-only", "dialed_host", "https://dialed.example.test")


def _run_v01_13(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    return _run_v01_contact_slot(ctx, "V01-13-f2-retrieved-host-contact-only", "retrieved_host", "https://retrieved.example.test")


def _run_v01_14(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V01-14-f2-audited-host-contact-inventory"
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Check all three mediated contact prefixes.",
        turns=(
            _cmd_turn("echo 'dialed_host#1 retrieved_host#1 audited_host#1 https://audited.example.test'"),
            ScriptedTurn(content="Cited [dialed_host#1], [retrieved_host#1], and [audited_host#1] for https://audited.example.test."),
        ),
        max_repair_replies=3,
        repair_reply_content="No web observation was admitted; only command output [command#1].",
    )
    bad = [h for h in info["minted_handles"] if h.startswith(("dialed_host#", "retrieved_host#", "audited_host#"))]
    ok = (not bad) and info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V01", "kind": "f2_contact_inventory_promoted",
        "finding_ref": "GB01-F2",
        "detail": f"bad_handles={bad} handles={info['minted_handles']} refused={info['refused']}",
    }
    return _make_report(selector=sel, family_id="V01", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


# ---------------------------------------------------------------------------
# V03 · Supersession and history (8 sidecar slots)
# ---------------------------------------------------------------------------

def _v03_setup_turns(rel_path: str) -> tuple[ScriptedTurn, ScriptedTurn, ScriptedTurn]:
    return (
        _cmd_turn(f"cat -n {rel_path}"),
        _cmd_turn(f"printf 'package app\\n\\nfunc Val() int {{ return 200 }}\\n' > {rel_path}"),
        _cmd_turn(f"cat -n {rel_path}"),
    )


def _prepare_v03_file(ctx: _RunnerContext, name: str) -> str:
    rel = f"internal/{name}"
    (ctx.workspace / rel).write_text("package app\n\nfunc Val() int { return 100 }\n", encoding="utf-8")
    return rel


def _run_v03_01(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V03-01-historical-handle-resolves-captured-body"
    rel = _prepare_v03_file(ctx, "v03_01.go")
    t1, t2, t3 = _v03_setup_turns(rel)
    art, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt=f"Read {rel}, update it, read again, and cite the historical value.",
        turns=(t1, t2, t3, ScriptedTurn(content='Original body was [read#1:3 "return 100"] before [read#2:3 "return 200"].')),
    )
    r1 = next((r for r in art.product_records if r.handle == "read#1"), None)
    old_body_preserved = bool(r1 and r1.raw_bytes and b"return 100" in r1.raw_bytes)
    ok = old_body_preserved and not info["refused"] and info["cited_evidence_count"] >= 2
    div = None if ok else {
        "selector": sel, "family_id": "V03", "kind": "supersession_historical_body_failure",
        "detail": f"old_body_preserved={old_body_preserved} refused={info['refused']} cited={info['cited_evidence_count']}",
    }
    return _make_report(selector=sel, family_id="V03", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v03_02(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V03-02-unqualified-path-resolves-current-only"
    rel = _prepare_v03_file(ctx, "v03_02.go")
    t1, t2, t3 = _v03_setup_turns(rel)
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt=f"Read {rel}, update it, read again, and cite unqualified path.",
        turns=(t1, t2, t3, ScriptedTurn(content=f'Unqualified old excerpt [{rel}:3 "return 100"].')),
        max_repair_replies=2,
        repair_reply_content=f'Unqualified current excerpt [{rel}:3 "return 200"].',
    )
    ok = info["refused"] and info["cited_evidence_count"] >= 1
    div = None if ok else {
        "selector": sel, "family_id": "V03", "kind": "unqualified_path_accepted_superseded",
        "detail": f"refused={info['refused']} cited={info['cited_evidence_count']}",
    }
    return _make_report(selector=sel, family_id="V03", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v03_03(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V03-03-historical-handle-newer-excerpt-refused"
    rel = _prepare_v03_file(ctx, "v03_03.go")
    t1, t2, t3 = _v03_setup_turns(rel)
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt=f"Read {rel}, update it, read again, and test cross-version citation.",
        turns=(t1, t2, t3, ScriptedTurn(content='Mismatch: [read#1:3 "return 200"].')),
        max_repair_replies=2,
        repair_reply_content='Corrected: [read#2:3 "return 200"].',
    )
    ok = info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V03", "kind": "false_accept_historical_handle_newer_excerpt",
        "detail": f"refused={info['refused']}",
    }
    return _make_report(selector=sel, family_id="V03", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v03_04(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V03-04-current-handle-older-excerpt-refused"
    rel = _prepare_v03_file(ctx, "v03_04.go")
    t1, t2, t3 = _v03_setup_turns(rel)
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt=f"Read {rel}, update it, read again, and test current handle with old excerpt.",
        turns=(t1, t2, t3, ScriptedTurn(content='Mismatch: [read#2:3 "return 100"].')),
        max_repair_replies=2,
        repair_reply_content='Corrected: [read#1:3 "return 100"].',
    )
    ok = info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V03", "kind": "false_accept_current_handle_older_excerpt",
        "detail": f"refused={info['refused']}",
    }
    return _make_report(selector=sel, family_id="V03", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v03_05(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V03-05-handle-contradictory-path-refused"
    rel = _prepare_v03_file(ctx, "v03_05.go")
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt=f"Read {rel} and test contradictory path on handle.",
        turns=(
            _cmd_turn(f"cat -n {rel}"),
            ScriptedTurn(content="Contradictory path: [read#1 second.go:3]."),
        ),
        max_repair_replies=2,
        repair_reply_content=f"Corrected: [read#1 {rel}:3].",
    )
    ok = info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V03", "kind": "false_accept_contradictory_path",
        "detail": f"refused={info['refused']}",
    }
    return _make_report(selector=sel, family_id="V03", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v03_06(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V03-06-command-receipt-survives-file-edit"
    rel = _prepare_v03_file(ctx, "v03_06.go")
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt=f"Run command mentioning {rel}, edit {rel}, re-read, and cite command#1.",
        turns=(
            _cmd_turn(f"echo 'checked {rel} OK'"),
            _cmd_turn(f"printf 'package app\\n\\nfunc Val() int {{ return 200 }}\\n' > {rel}"),
            _cmd_turn(f"cat -n {rel}"),
            ScriptedTurn(content=f'Earlier command receipt [command#1 "checked {rel} OK"] remains valid alongside [read#1:3].'),
        ),
    )
    ok = "command#1" in info["minted_handles"] and not info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V03", "kind": "command_receipt_did_not_survive_edit",
        "detail": f"handles={info['minted_handles']} refused={info['refused']}",
    }
    return _make_report(selector=sel, family_id="V03", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v03_07(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V03-07-git-and-merge-receipts-survive-file-edit"
    rel = _prepare_v03_file(ctx, "v03_07.go")
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt=f"Run git status/check, edit {rel}, re-read, and cite receipt.",
        turns=(
            _cmd_turn("git --version"),
            _cmd_turn(f"printf 'package app\\n\\nfunc Val() int {{ return 200 }}\\n' > {rel}"),
            _cmd_turn(f"cat -n {rel}"),
            ScriptedTurn(content="Git receipt [command#1] remains valid after [read#1:3]."),
        ),
    )
    ok = "command#1" in info["minted_handles"] and not info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V03", "kind": "git_receipt_did_not_survive_edit",
        "detail": f"handles={info['minted_handles']} refused={info['refused']}",
    }
    return _make_report(selector=sel, family_id="V03", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v03_08(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V03-08-trust-survives-supersession"
    rel = _prepare_v03_file(ctx, "v03_08.go")
    t1, t2, t3 = _v03_setup_turns(rel)
    art, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt=f"Read {rel}, update it, read again, and verify superseded record metadata survives.",
        turns=(t1, t2, t3, ScriptedTurn(content='Verified superseded [read#1:3 "return 100"] and current [read#2:3 "return 200"].')),
    )
    r1 = next((r for r in art.product_records if r.handle == "read#1"), None)
    r2 = next((r for r in art.product_records if r.handle == "read#2"), None)
    ok = bool(r1 and r2 and r1.superseded_by == "read#2" and r1.trust in {"trusted", "untrusted"} and not info["refused"])
    div = None if ok else {
        "selector": sel, "family_id": "V03", "kind": "supersession_metadata_mismatch",
        "detail": f"r1_superseded_by={r1.superseded_by if r1 else None} refused={info['refused']}",
    }
    return _make_report(selector=sel, family_id="V03", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


# ---------------------------------------------------------------------------
# V04 · Command observations (10 sidecar slots, incl. Decision 005/014 slot 10)
# ---------------------------------------------------------------------------

def _run_v04_01(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V04-01-command-handle-carried"
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Run echo command and cite command#1.",
        turns=(
            _cmd_turn("echo 'v04-handle-check'"),
            ScriptedTurn(content="Observed command output [command#1]."),
        ),
    )
    ok = "command#1" in info["minted_handles"] and not info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V04", "kind": "command_handle_missing",
        "detail": f"handles={info['minted_handles']} refused={info['refused']}",
    }
    return _make_report(selector=sel, family_id="V04", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v04_02(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V04-02-f3-correctly-numbered-single-file"
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Run cat -n first.go and cite line 3.",
        turns=(
            _cmd_turn("cat -n first.go"),
            ScriptedTurn(content="Verified First() at [read#1:3]."),
        ),
    )
    ok = "read#1" in info["minted_handles"] and not info["refused"] and len(info["minting_parity_defects"]) == 0
    div = None if ok else {
        "selector": sel, "family_id": "V04", "kind": "f3_numbered_single_file_failure",
        "finding_ref": "GB01-F3",
        "detail": f"handles={info['minted_handles']} refused={info['refused']} parity={info['minting_parity_defects']}",
    }
    return _make_report(selector=sel, family_id="V04", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v04_03(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V04-03-f3-unnumbered-source-delivery"
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Run cat first.go (unnumbered) and cite line 3.",
        turns=(
            _cmd_turn("cat first.go"),
            ScriptedTurn(content="Observed line 3 at [read#1:3]."),
        ),
        max_repair_replies=2,
        repair_reply_content='Observed [read#1:3 "func First() int { return 1 }"] via [command#1].',
    )
    ok = ("read#1" in info["minted_handles"] or "command#1" in info["minted_handles"]) and info["cited_evidence_count"] >= 1
    div = None if ok else {
        "selector": sel, "family_id": "V04", "kind": "f3_unnumbered_source_failure",
        "finding_ref": "GB01-F3",
        "detail": f"handles={info['minted_handles']} refused={info['refused']} cited={info['cited_evidence_count']}",
    }
    return _make_report(selector=sel, family_id="V04", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v04_04(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V04-04-multi-run-split-per-file-and-run"
    (ctx.workspace / "multi.go").write_text(
        "\n".join(f"// line {i}" for i in range(1, 11)) + "\n", encoding="utf-8"
    )
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Read lines 1-2 and 8-9 of multi.go and test gap line 5.",
        turns=(
            _cmd_turn("nl -ba multi.go | sed -n '1,2p;8,9p'"),
            ScriptedTurn(content="Claimed undelivered gap line [multi.go:5]."),
        ),
        max_repair_replies=2,
        repair_reply_content="Only observed command output [command#1].",
    )
    ok = info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V04", "kind": "false_accept_gap_line",
        "detail": f"handles={info['minted_handles']} refused={info['refused']}",
    }
    return _make_report(selector=sel, family_id="V04", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v04_05(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V04-05-sixteen-delivery-tag-cap"
    cap_dir = ctx.workspace / "cap20"
    cap_dir.mkdir(exist_ok=True)
    paths: list[str] = []
    for i in range(1, 21):
        rel = f"cap20/f{i:02d}.go"
        (ctx.workspace / rel).write_text(f"package cap20\nfunc F{i:02d}() int {{ return {i} }}\n", encoding="utf-8")
        paths.append(rel)
    art, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Run cat -n on 20 files and verify 16-tag cap while citing command#1.",
        turns=(
            _cmd_turn("cat -n " + " ".join(paths)),
            ScriptedTurn(content="Executed 20-file read recorded in [command#1]."),
        ),
    )
    read_handles = [h for h in info["minted_handles"] if h.startswith("read#")]
    all_tags = [t for m in art.delivered_tool_messages for t in (m.get("delivery_tags") or ())]
    file_delivery_tags = [t for t in all_tags if str(t).startswith("[read#")]
    # Decision 054: D1 mints all 20 read# records and caps emitted file delivery tags at 16
    # (excluding the [command#1] receipt tag).
    ok = (
        "command#1" in info["minted_handles"]
        and len(file_delivery_tags) <= 16
        and not info["refused"]
    )
    div = None if ok else {
        "selector": sel, "family_id": "V04", "kind": "delivery_tag_cap_exceeded",
        "detail": f"read_handle_count={len(read_handles)} file_delivery_tags={len(file_delivery_tags)} all_tags={len(all_tags)} refused={info['refused']}",
    }
    return _make_report(
        selector=sel, family_id="V04", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d,
        extra={"read_handle_count": len(read_handles), "file_delivery_tags": len(file_delivery_tags), "emitted_tags": len(all_tags)},
        divergence=div,
    )


def _run_v04_06(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V04-06-opaque-command-no-read-authority"
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Run echo printing a file path and test read authority.",
        turns=(
            _cmd_turn("echo 'internal/secret.go:10: secret_token'"),
            ScriptedTurn(content="Read secret at [internal/secret.go:10] via [command#1]."),
        ),
        max_repair_replies=2,
        repair_reply_content="Only observed echoed text in [command#1].",
    )
    has_read = any(h.startswith("read#") for h in info["minted_handles"])
    ok = (not has_read) and "command#1" in info["minted_handles"] and info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V04", "kind": "false_accept_opaque_command_read_authority",
        "detail": f"handles={info['minted_handles']} refused={info['refused']}",
    }
    return _make_report(selector=sel, family_id="V04", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v04_07(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V04-07-line-filter-pipeline-command-only"
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Run cat -n first.go | grep -v NEVER_MATCH and test coordinate citation.",
        turns=(
            _cmd_turn("cat -n first.go | grep -v NEVER_MATCH"),
            ScriptedTurn(content="Cited filtered line [first.go:3]."),
        ),
        max_repair_replies=2,
        repair_reply_content="Filtered pipeline output is recorded in [command#1].",
    )
    has_read = any(h.startswith("read#") for h in info["minted_handles"])
    ok = (not has_read) and "command#1" in info["minted_handles"] and info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V04", "kind": "false_accept_filtered_pipeline_source",
        "detail": f"handles={info['minted_handles']} refused={info['refused']}",
    }
    return _make_report(selector=sel, family_id="V04", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v04_08(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V04-08-f3-mismatched-printed-numbers-no-excerpt"
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Run cat -n first.go second.go and cite [read#2:3] without excerpt.",
        turns=(
            _cmd_turn("cat -n first.go second.go"),
            ScriptedTurn(content="Inferred coordinate without excerpt: [read#2:3]."),
        ),
        max_repair_replies=2,
        repair_reply_content='Recovered with excerpt: [read#2:3 "func Second() int { return 2 }"].',
    )
    ok = "read#2" in info["minted_handles"] and info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V04", "kind": "f3_mismatched_numbers_unexpected_outcome",
        "finding_ref": "GB01-F3",
        "detail": f"handles={info['minted_handles']} refused={info['refused']}",
    }
    return _make_report(
        selector=sel, family_id="V04", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d,
        extra={"disputed_expectation": True},
        divergence=div,
    )


def _run_v04_09(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V04-09-f3-mismatched-printed-numbers-with-excerpt"
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Run cat -n first.go second.go and cite read#2 with verified excerpt.",
        turns=(
            _cmd_turn("cat -n first.go second.go"),
            ScriptedTurn(content='Verified excerpt: [read#2:3 "func Second() int { return 2 }"].'),
        ),
    )
    ok = "read#2" in info["minted_handles"] and not info["refused"] and info["cited_evidence_count"] >= 1
    div = None if ok else {
        "selector": sel, "family_id": "V04", "kind": "f3_verified_excerpt_rejected",
        "finding_ref": "GB01-F3",
        "detail": f"handles={info['minted_handles']} refused={info['refused']} cited={info['cited_evidence_count']}",
    }
    return _make_report(selector=sel, family_id="V04", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v04_10(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V04-10-copied-second-file-tag"
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Read first.go and second.go with cat -n and cite exact copied host tag for second.go.",
        turns=(
            _cmd_turn("cat -n first.go second.go"),
            ScriptedTurn(content="Copied host coverage tag: [read#2 second.go:1-3]."),
        ),
        max_repair_replies=2,
        repair_reply_content='Verified excerpt recovery: [read#2 second.go:3 "func Second() int { return 2 }"].',
    )
    refused_copied = info["refused"]
    recovered = info["cited_evidence_count"] >= 1
    ok = not refused_copied
    div = None if ok else {
        "selector": sel, "family_id": "V04", "kind": "false_reject",
        "finding_ref": "GB01-F3",
        "disputed_expectation": True,
        "detail": (
            f"D1 refused exact copied host coverage tag [read#2 second.go:1-3] "
            f"(retry_count={info['retry_count']}, extra_repair_prompts={info['extra_repair_prompts']}, "
            f"d10_delta={d}), verified-excerpt recovery settled={recovered}."
        ),
    }
    return _make_report(
        selector=sel, family_id="V04", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d,
        extra={
            "reproduced_gb01_f3_false_reject": refused_copied,
            "recovered_via_verified_excerpt": recovered,
            "disputed_expectation": True,
        },
        divergence=div,
    )


# ---------------------------------------------------------------------------
# V05 · Compaction and recall (5 sidecar slots)
# ---------------------------------------------------------------------------

def _run_v05_01(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V05-01-compaction-preserves-evidence-by-digest"
    art1, info1, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Read first.go and preserve digest across subsequent turns.",
        turns=(
            _cmd_turn("cat -n first.go"),
            ScriptedTurn(content="Read first.go [read#1:3]."),
        ),
        save_name=f"{sel}-turn1",
    )
    r1_before = next((r for r in art1.product_records if r.handle == "read#1"), None)
    art2, info2, _, a2, d2 = ctx.run_session(
        selector=sel,
        prompt="Cite read#1 again in a follow-up turn.",
        turns=(ScriptedTurn(content='Still matches [read#1:3 "func First() int { return 1 }"].'),),
        session_id=art1.session_id,
        save_name=f"{sel}-turn2",
    )
    r1_after = next((r for r in art2.product_records if r.handle == "read#1"), None)
    digest_same = bool(
        r1_before and r1_after and r1_before.effective_body_sha256() == r1_after.effective_body_sha256()
    )
    ok = digest_same and not info1["refused"] and not info2["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V05", "kind": "digest_not_preserved",
        "detail": f"digest_same={digest_same} refused1={info1['refused']} refused2={info2['refused']}",
    }
    return _make_report(selector=sel, family_id="V05", passed=ok, info=info2, d10_before=b, d10_after=a2, d10_delta=d2, divergence=div)


def _run_v05_02(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V05-02-recall-by-handle-after-compaction"
    art1, _, b, _, _ = ctx.run_session(
        selector=sel,
        prompt="Read first.go.",
        turns=(_cmd_turn("cat -n first.go"), ScriptedTurn(content="Captured [read#1:3].")),
        save_name=f"{sel}-turn1",
    )
    _, info2, _, a2, d2 = ctx.run_session(
        selector=sel,
        prompt="Recall and cite read#1 line 3.",
        turns=(ScriptedTurn(content='Recalled [read#1:3 "func First() int { return 1 }"].'),),
        session_id=art1.session_id,
        save_name=f"{sel}-turn2",
    )
    ok = "read#1" in info2["minted_handles"] and not info2["refused"] and info2["cited_evidence_count"] >= 1
    div = None if ok else {
        "selector": sel, "family_id": "V05", "kind": "recall_by_handle_failed",
        "detail": f"handles={info2['minted_handles']} refused={info2['refused']}",
    }
    return _make_report(selector=sel, family_id="V05", passed=ok, info=info2, d10_before=b, d10_after=a2, d10_delta=d2, divergence=div)


def _run_v05_03(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V05-03-recalled-search-index-not-fresh-read"
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Search first.go for First and test uncovered line 1.",
        turns=(
            _cmd_turn("grep -n First first.go"),
            ScriptedTurn(content='Claimed uncovered line [first.go:1 "package demo"].'),
        ),
        max_repair_replies=2,
        repair_reply_content="Search match only [command#1].",
    )
    ok = info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V05", "kind": "false_accept_search_index_as_fresh_read",
        "detail": f"handles={info['minted_handles']} refused={info['refused']}",
    }
    return _make_report(selector=sel, family_id="V05", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v05_04(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V05-04-source-change-excluded-from-recall"
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Test source_change#1 exclusion.",
        turns=(
            _cmd_turn("echo 'source_change brief'"),
            ScriptedTurn(content="Claimed line proof via [source_change#1:1]."),
        ),
        max_repair_replies=2,
        repair_reply_content="Observed command output [command#1].",
    )
    ok = info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V05", "kind": "false_accept_source_change_line_proof",
        "detail": f"handles={info['minted_handles']} refused={info['refused']}",
    }
    return _make_report(selector=sel, family_id="V05", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v05_05(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V05-05-d16-secret-canary-screened-before-commit"
    canary_file = ctx.workspace / ".env"
    canary_file.write_text(f"API_KEY={D16_RAW_SECRET_CANARY}\n", encoding="utf-8")
    art, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Read .env containing synthetic D16 secret canary.",
        turns=(
            _cmd_turn("cat -n .env"),
            ScriptedTurn(content="Inspected .env [command#1]."),
        ),
        max_repair_replies=2,
        repair_reply_content="Inspected .env [command#1].",
    )
    leaked_handles = [
        r.handle
        for r in art.product_records
        if r.raw_bytes and D16_RAW_SECRET_CANARY.encode("utf-8") in r.raw_bytes
    ]
    ok = len(leaked_handles) == 0
    div = None if ok else {
        "selector": sel, "family_id": "V05", "kind": "d16_secret_canary_leaked_in_evidence",
        "detail": f"raw D16 secret canary present in committed evidence records: {leaked_handles}",
    }
    return _make_report(
        selector=sel, family_id="V05", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d,
        extra={"leaked_handles": leaked_handles},
        divergence=div,
    )


# ---------------------------------------------------------------------------
# V06 · Scope and provenance (10 sidecar slots)
# ---------------------------------------------------------------------------

def _run_v06_01(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V06-01-coordinator-own-observations"
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Read first.go and cite own observation.",
        turns=(
            _cmd_turn("cat -n first.go"),
            ScriptedTurn(content="Coordinator observed [read#1:3]."),
        ),
    )
    ok = "read#1" in info["minted_handles"] and not info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V06", "kind": "coordinator_own_observation_failed",
        "detail": f"handles={info['minted_handles']} refused={info['refused']}",
    }
    return _make_report(selector=sel, family_id="V06", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v06_cross_session_refusal(
    ctx: _RunnerContext,
    *,
    sel: str,
    other_file: str,
    bad_citation: str,
    project_id_other: str | None = None,
    workspace_other: pathlib.Path | None = None,
) -> tuple[dict[str, Any], dict[str, Any] | None]:
    # Session A observes other_file
    ctx.run_session(
        selector=f"{sel}-donor",
        prompt=f"Read {other_file}.",
        turns=(
            _cmd_turn(f"cat -n {other_file}"),
            ScriptedTurn(content="Donor read [read#1:3]."),
        ),
        project_id=project_id_other,
        workspace_dir=workspace_other,
        save_name=f"{sel}-donor",
    )
    # Fresh Session B (separate scope) reads first.go and tries bad_citation from Session A
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt="Read first.go and test cross-scope citation.",
        turns=(
            _cmd_turn("cat -n first.go"),
            ScriptedTurn(content=f"Cross-scope attempt {bad_citation} alongside own [read#1:3]."),
        ),
        max_repair_replies=2,
        repair_reply_content="Only own observation [read#1:3].",
    )
    ok = info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V06", "kind": "cross_scope_isolation_breach",
        "detail": f"bad_citation={bad_citation} was not refused (handles={info['minted_handles']})",
    }
    return _make_report(selector=sel, family_id="V06", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


def _run_v06_02(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    return _run_v06_cross_session_refusal(
        ctx, sel="V06-02-coordinator-dispatched-worker-a-namespaced",
        other_file="second.go", bad_citation="[worker-a:read#1:3]",
    )


def _run_v06_03(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    return _run_v06_cross_session_refusal(
        ctx, sel="V06-03-coordinator-two-workers-leg-namespaced-union",
        other_file="third.go", bad_citation="[worker-b:read#1:3]",
    )


def _run_v06_04(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    return _run_v06_cross_session_refusal(
        ctx, sel="V06-04-worker-read-not-parent-first-hand",
        other_file="second.go", bad_citation="[second.go:3]",
    )


def _run_v06_05(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    return _run_v06_cross_session_refusal(
        ctx, sel="V06-05-worker-cannot-cite-sibling-worker",
        other_file="third.go", bad_citation="[worker-b:read#1:3]",
    )


def _run_v06_06(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    return _run_v06_cross_session_refusal(
        ctx, sel="V06-06-worker-cannot-borrow-parent-evidence",
        other_file="second.go", bad_citation="[second.go:3]",
    )


def _run_v06_07(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    (ctx.workspace / "chat2.go").write_text("package demo\n\nfunc Chat2() int { return 22 }\n", encoding="utf-8")
    return _run_v06_cross_session_refusal(
        ctx, sel="V06-07-second-chat-isolation",
        other_file="chat2.go", bad_citation="[chat2.go:3]",
    )


def _run_v06_08(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    ws2 = ctx.out_dir / "workspace-proj2"
    ws2.mkdir(parents=True, exist_ok=True)
    (ws2 / "proj2.go").write_text("package proj2\n\nfunc Proj2() int { return 99 }\n", encoding="utf-8")
    pid2 = ctx.client.create_project(ws2)["id"]
    return _run_v06_cross_session_refusal(
        ctx, sel="V06-08-second-project-isolation",
        other_file="proj2.go", bad_citation="[proj2.go:3]",
        project_id_other=pid2, workspace_other=ws2,
    )


def _run_v06_09(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    return _run_v06_cross_session_refusal(
        ctx, sel="V06-09-worker-round-boundary-cited-results",
        other_file="second.go", bad_citation="[round1:read#1:3]",
    )


def _run_v06_10(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V06-10-workspace-refresh-rejection-invalidation"
    rel = _prepare_v03_file(ctx, "v06_10.go")
    t1, t2, t3 = _v03_setup_turns(rel)
    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt=f"Read {rel}, refresh workspace with edit, re-read, and test stale excerpt.",
        turns=(t1, t2, t3, ScriptedTurn(content=f'Stale excerpt [{rel}:3 "return 100"].')),
        max_repair_replies=2,
        repair_reply_content=f'Fresh excerpt [{rel}:3 "return 200"].',
    )
    ok = info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V06", "kind": "stale_workspace_refresh_accepted",
        "detail": f"refused={info['refused']}",
    }
    return _make_report(selector=sel, family_id="V06", passed=ok, info=info, d10_before=b, d10_after=a, d10_delta=d, divergence=div)


# ---------------------------------------------------------------------------
# V07 · Restart and unreadable ledger (2 sidecar slots)
# ---------------------------------------------------------------------------

def _run_v07_01(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V07-01-sidecar-restart-after-commit-no-handle-reuse"
    cfg = ctx.out_dir / "v07-01-config"
    ws = ctx.out_dir / "v07-01-workspace"
    ws.mkdir(parents=True, exist_ok=True)
    (ws / "first.go").write_text("package demo\n\nfunc First() int { return 1 }\n", encoding="utf-8")
    (ws / "second.go").write_text("package demo\n\nfunc Second() int { return 2 }\n", encoding="utf-8")

    # Phase 1: start sidecar, commit read#1, stop sidecar
    sc1 = IsolatedSidecarProcess(
        ctx.engine_bin,
        product_root=ctx.product_root,
        config_dir=cfg,
        workspace_dir=ws,
        expected_sha256=ctx.expected_engine_sha256,
        trace_friction=ctx.trace_friction,
    )
    with sc1 as c1:
        m1 = sc1.build_run_manifest()
        pid = c1.create_project(ws)["id"]
        sid = c1.create_session(pid)["id"]
        c1.wait_session_ready(sid)
        p1 = c1.send_prompt(sid, "Read first.go.")
        art1 = run_scripted_session(
            c1, session_id=sid,
            turns=(_cmd_turn("cat -n first.go"), ScriptedTurn(content="Read [read#1:3].")),
            db_path=sc1.store_db_path, workspace_dir=ws,
            submission_id=str(p1.get("submission_id") or p1.get("id") or ""),
            wait_for_settlement=True, max_repair_replies=0,
            engine_provenance=sc1.provenance, run_manifest=m1,
            poll_timeout=ctx.poll_timeout,
        )

    # Phase 2: restart a fresh sidecar on the SAME config_dir / store.db
    sc2 = IsolatedSidecarProcess(
        ctx.engine_bin,
        product_root=ctx.product_root,
        config_dir=cfg,
        workspace_dir=ws,
        expected_sha256=ctx.expected_engine_sha256,
        trace_friction=ctx.trace_friction,
    )
    with sc2 as c2:
        m2 = sc2.build_run_manifest()
        tl2 = sc2.log_path if ctx.trace_friction else None
        b = probe_store_db(sc2.store_db_path, sid, trace_log_path=tl2)
        p2 = c2.send_prompt(sid, "Read second.go and cite both read#1 and read#2.")
        turns2 = (
            _cmd_turn("cat -n second.go"),
            ScriptedTurn(content="After restart: [read#1:3] and [read#2:3]."),
        )
        art2 = run_scripted_session(
            c2, session_id=sid, turns=turns2,
            db_path=sc2.store_db_path, workspace_dir=ws,
            submission_id=str(p2.get("submission_id") or p2.get("id") or ""),
            wait_for_settlement=True, max_repair_replies=0,
            engine_provenance=sc2.provenance, run_manifest=m2,
            poll_timeout=ctx.poll_timeout,
        )
        a = probe_store_db(sc2.store_db_path, sid, trace_log_path=tl2)

    (ctx.artifacts_dir / f"{sel}-pre.json").write_text(json.dumps(art1.to_dict(), indent=2, sort_keys=True), encoding="utf-8")
    (ctx.artifacts_dir / f"{sel}-post.json").write_text(json.dumps(art2.to_dict(), indent=2, sort_keys=True), encoding="utf-8")
    info = _inspect_artifact(art2, initial_turns=len(turns2))
    r1_pre = next((r for r in art1.product_records if r.handle == "read#1"), None)
    r1_post = next((r for r in art2.product_records if r.handle == "read#1"), None)
    r2_post = next((r for r in art2.product_records if r.handle == "read#2"), None)
    ok = bool(
        r1_pre and r1_post and r2_post
        and r1_pre.effective_body_sha256() == r1_post.effective_body_sha256()
        and not info["refused"]
        and info["cited_evidence_count"] >= 2
    )
    div = None if ok else {
        "selector": sel, "family_id": "V07", "kind": "restart_handle_reuse_or_loss",
        "detail": f"handles={info['minted_handles']} refused={info['refused']} cited={info['cited_evidence_count']}",
    }
    return _make_report(selector=sel, family_id="V07", passed=ok, info=info, d10_before=b.to_dict(), d10_after=a.to_dict(), d10_delta=a.delta_observed(b), divergence=div)


def _corrupt_read1_blob(db_path: pathlib.Path, session_id: str) -> bool:
    conn = sqlite3.connect(str(db_path))
    try:
        row = conn.execute(
            "SELECT project_id, content_blob_sha256 FROM evidence_records WHERE session_id = ? AND handle = 'read#1'",
            (session_id,),
        ).fetchone()
    finally:
        conn.close()
    if not row or not row[1] or len(str(row[1])) != 64:
        return False
    pid, sha = str(row[0]), str(row[1])
    blob = db_path.parent / "projects" / pid / "model-content" / sha[:2] / sha[2:]
    if not blob.is_file():
        return False
    blob.write_bytes(b"corrupt-not-valid-zstd-gb03-v07")
    return True


def _run_v07_02(ctx: _RunnerContext) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = "V07-02-sidecar-unreadable-ledger-no-exemption"
    cfg = ctx.out_dir / "v07-02-config"
    ws = ctx.out_dir / "v07-02-workspace"
    ws.mkdir(parents=True, exist_ok=True)
    (ws / "first.go").write_text("package demo\n\nfunc First() int { return 1 }\n", encoding="utf-8")

    # Phase 1: commit read#1
    sc1 = IsolatedSidecarProcess(
        ctx.engine_bin,
        product_root=ctx.product_root,
        config_dir=cfg,
        workspace_dir=ws,
        expected_sha256=ctx.expected_engine_sha256,
        trace_friction=ctx.trace_friction,
    )
    with sc1 as c1:
        m1 = sc1.build_run_manifest()
        pid = c1.create_project(ws)["id"]
        sid = c1.create_session(pid)["id"]
        c1.wait_session_ready(sid)
        p1 = c1.send_prompt(sid, "Read first.go.")
        run_scripted_session(
            c1, session_id=sid,
            turns=(_cmd_turn("cat -n first.go"), ScriptedTurn(content="Read [read#1:3].")),
            db_path=sc1.store_db_path, workspace_dir=ws,
            submission_id=str(p1.get("submission_id") or p1.get("id") or ""),
            wait_for_settlement=True, max_repair_replies=0,
            engine_provenance=sc1.provenance, run_manifest=m1,
            poll_timeout=ctx.poll_timeout,
        )

    corrupted = _corrupt_read1_blob(sc1.store_db_path, sid)

    # Phase 2: restart sidecar with unreadable ledger and attempt exempt prose / citation
    sc2 = IsolatedSidecarProcess(
        ctx.engine_bin,
        product_root=ctx.product_root,
        config_dir=cfg,
        workspace_dir=ws,
        expected_sha256=ctx.expected_engine_sha256,
        trace_friction=ctx.trace_friction,
    )
    with sc2 as c2:
        m2 = sc2.build_run_manifest()
        tl2 = sc2.log_path if ctx.trace_friction else None
        b = probe_store_db(sc2.store_db_path, sid, trace_log_path=tl2)
        p2 = c2.send_prompt(sid, "Summarize findings.")
        turns2 = (ScriptedTurn(content="All checks passed with no citations needed [read#1:3]."),)
        art2 = run_scripted_session(
            c2, session_id=sid, turns=turns2,
            db_path=sc2.store_db_path, workspace_dir=ws,
            submission_id=str(p2.get("submission_id") or p2.get("id") or ""),
            wait_for_settlement=True, max_repair_replies=2,
            repair_reply_content="Unable to verify due to unreadable ledger.",
            engine_provenance=sc2.provenance, run_manifest=m2,
            poll_timeout=ctx.poll_timeout,
        )
        a = probe_store_db(sc2.store_db_path, sid, trace_log_path=tl2)

    (ctx.artifacts_dir / f"{sel}.json").write_text(json.dumps(art2.to_dict(), indent=2, sort_keys=True), encoding="utf-8")
    # Decision 055: Inspect ONLY Phase 2 closeouts (excluding Phase 1's pre-corruption closeout).
    phase1_closeout_ids = {str(c.get("id") or "") for c in art1.product_closeouts}
    phase2_closeouts = [
        c for c in art2.product_closeouts
        if str(c.get("id") or "") not in phase1_closeout_ids
    ]
    info = _inspect_artifact(art2, initial_turns=len(turns2))
    granted_exemption = any(
        isinstance(c.get("grounding"), dict) and c["grounding"].get("evidence_optional") is True
        for c in phase2_closeouts
    )
    grounded_read1 = any(
        isinstance(e, dict) and e.get("handle") == "read#1" and e.get("verdict") in {"matched", "traced", "bound"}
        for c in phase2_closeouts
        if isinstance(c.get("grounding"), dict)
        for e in (c["grounding"].get("cited_evidence") or [])
    )
    ok = corrupted and (not granted_exemption) and (not grounded_read1)
    div = None if ok else {
        "selector": sel, "family_id": "V07", "kind": "unreadable_ledger_exemption_or_false_accept",
        "detail": f"corrupted={corrupted} phase2_closeouts={len(phase2_closeouts)} evidence_optional={granted_exemption} grounded_read1={grounded_read1}",
    }
    return _make_report(
        selector=sel, family_id="V07", passed=ok, info=info,
        d10_before=b.to_dict(), d10_after=a.to_dict(), d10_delta=a.delta_observed(b),
        extra={"blob_corrupted": corrupted, "phase2_closeout_count": len(phase2_closeouts), "granted_exemption": granted_exemption},
        divergence=div,
    )


# ---------------------------------------------------------------------------
# V02 · Generated lookalikes (sidecar)
# ---------------------------------------------------------------------------

def _run_v02_instance(
    ctx: _RunnerContext, *, base_seed: str, instance_index: int
) -> tuple[dict[str, Any], dict[str, Any] | None]:
    sel = f"V02-lookalike-{base_seed}-{instance_index}"
    case = generate_v02(base_seed, instance_index)
    v02_dir = ctx.workspace / "v02"
    v02_dir.mkdir(exist_ok=True)
    rel = f"v02/lookalike_{base_seed}_{instance_index}.txt"
    (ctx.workspace / rel).write_bytes(case.payload)

    _, info, b, a, d = ctx.run_session(
        selector=sel,
        prompt=f"Inspect {rel}.",
        turns=(
            _cmd_turn(f"cat {rel}"),
            ScriptedTurn(content="Citing spoofed [read#99 internal/fake_0.go:1] alongside [command#1]."),
        ),
        max_repair_replies=2,
        repair_reply_content="Only observed command output [command#1].",
    )
    promoted_reads = [h for h in info["minted_handles"] if h.startswith("read#")]
    ok = (not promoted_reads) and ("read#99" not in info["minted_handles"]) and info["refused"]
    div = None if ok else {
        "selector": sel, "family_id": "V02", "kind": "false_accept_or_promoted_lookalike",
        "seed": case.seed.to_dict(),
        "detail": f"promoted_reads={promoted_reads} handles={info['minted_handles']} refused={info['refused']}",
    }
    return _make_report(
        selector=sel, family_id="V02", passed=ok, info=info,
        d10_before=b, d10_after=a, d10_delta=d,
        extra={"seed": case.seed.to_dict()},
        divergence=div,
    )


# ---------------------------------------------------------------------------
# Explicit dispatch table for ALL 49 fixed sidecar slots
# ---------------------------------------------------------------------------

SIDECAR_SLOT_HANDLERS: dict[
    str, Callable[[_RunnerContext], tuple[dict[str, Any], dict[str, Any] | None]]
] = {
    # V01 (14)
    "V01-01-read-file-region": _run_v01_01,
    "V01-02-grep-find-list-stat-wc": _run_v01_02,
    "V01-03-edit-write-replace-restore": _run_v01_03,
    "V01-04-rewrite-delete-diff": _run_v01_04,
    "V01-05-append-only-ordinals": _run_v01_05,
    "V01-06-command-success-receipt": _run_v01_06,
    "V01-07-command-error-outcome-receipt": _run_v01_07,
    "V01-08-git-error-outcome-receipt": _run_v01_08,
    "V01-09-refused-invocation-no-file-proof": _run_v01_09,
    "V01-10-failed-mutation-no-file-proof": _run_v01_10,
    "V01-11-web-scan-survey-surface-kinds": _run_v01_11,
    "V01-12-f2-dialed-host-contact-only": _run_v01_12,
    "V01-13-f2-retrieved-host-contact-only": _run_v01_13,
    "V01-14-f2-audited-host-contact-inventory": _run_v01_14,
    # V03 (8)
    "V03-01-historical-handle-resolves-captured-body": _run_v03_01,
    "V03-02-unqualified-path-resolves-current-only": _run_v03_02,
    "V03-03-historical-handle-newer-excerpt-refused": _run_v03_03,
    "V03-04-current-handle-older-excerpt-refused": _run_v03_04,
    "V03-05-handle-contradictory-path-refused": _run_v03_05,
    "V03-06-command-receipt-survives-file-edit": _run_v03_06,
    "V03-07-git-and-merge-receipts-survive-file-edit": _run_v03_07,
    "V03-08-trust-survives-supersession": _run_v03_08,
    # V04 (10)
    "V04-01-command-handle-carried": _run_v04_01,
    "V04-02-f3-correctly-numbered-single-file": _run_v04_02,
    "V04-03-f3-unnumbered-source-delivery": _run_v04_03,
    "V04-04-multi-run-split-per-file-and-run": _run_v04_04,
    "V04-05-sixteen-delivery-tag-cap": _run_v04_05,
    "V04-06-opaque-command-no-read-authority": _run_v04_06,
    "V04-07-line-filter-pipeline-command-only": _run_v04_07,
    "V04-08-f3-mismatched-printed-numbers-no-excerpt": _run_v04_08,
    "V04-09-f3-mismatched-printed-numbers-with-excerpt": _run_v04_09,
    "V04-10-copied-second-file-tag": _run_v04_10,
    # V05 (5)
    "V05-01-compaction-preserves-evidence-by-digest": _run_v05_01,
    "V05-02-recall-by-handle-after-compaction": _run_v05_02,
    "V05-03-recalled-search-index-not-fresh-read": _run_v05_03,
    "V05-04-source-change-excluded-from-recall": _run_v05_04,
    "V05-05-d16-secret-canary-screened-before-commit": _run_v05_05,
    # V06 (10)
    "V06-01-coordinator-own-observations": _run_v06_01,
    "V06-02-coordinator-dispatched-worker-a-namespaced": _run_v06_02,
    "V06-03-coordinator-two-workers-leg-namespaced-union": _run_v06_03,
    "V06-04-worker-read-not-parent-first-hand": _run_v06_04,
    "V06-05-worker-cannot-cite-sibling-worker": _run_v06_05,
    "V06-06-worker-cannot-borrow-parent-evidence": _run_v06_06,
    "V06-07-second-chat-isolation": _run_v06_07,
    "V06-08-second-project-isolation": _run_v06_08,
    "V06-09-worker-round-boundary-cited-results": _run_v06_09,
    "V06-10-workspace-refresh-rejection-invalidation": _run_v06_10,
    # V07 (2 sidecar)
    "V07-01-sidecar-restart-after-commit-no-handle-reuse": _run_v07_01,
    "V07-02-sidecar-unreadable-ledger-no-exemption": _run_v07_02,
}


def _seed_workspace(workspace: pathlib.Path) -> None:
    (workspace / "internal").mkdir(parents=True, exist_ok=True)
    (workspace / "internal" / "a.go").write_text(
        "package app\n\nfunc Alpha() int {\n    return 42\n}\n", encoding="utf-8"
    )
    (workspace / "first.go").write_text(
        "package demo\n\nfunc First() int { return 1 }\n", encoding="utf-8"
    )
    (workspace / "second.go").write_text(
        "package demo\n\nfunc Second() int { return 2 }\n", encoding="utf-8"
    )
    (workspace / "third.go").write_text(
        "package demo\n\nfunc Third() int { return 3 }\n", encoding="utf-8"
    )


def run_gb03_full_sidecar(
    *,
    engine_bin: str,
    product_root: str,
    expected_engine_sha256: str = "",
    out_dir: pathlib.Path,
    run_id: str = "20261127-3bb1464d-gb03-sidecar",
    families: tuple[str, ...] = GB03_FAMILY_IDS,
    base_seed: str = "20261127",
    limit_v02: int = V02_GENERATED_SPEC.sidecar_per_platform_per_seed,
    shard: int = 0,
    shards: int = 1,
    trace_friction: bool = True,
    poll_timeout: float = 120.0,
) -> dict[str, Any]:
    out_dir.mkdir(parents=True, exist_ok=True)
    artifacts_dir = out_dir / "artifacts"
    artifacts_dir.mkdir(parents=True, exist_ok=True)
    workspace = out_dir / "workspace"
    config_dir = out_dir / "config"
    workspace.mkdir(parents=True, exist_ok=True)
    _seed_workspace(workspace)

    work_items = select_gb03_work_items(
        families=families,
        base_seed=base_seed,
        limit_v02=limit_v02,
        shard=shard,
        shards=shards,
    )

    sidecar = IsolatedSidecarProcess(
        engine_bin,
        product_root=product_root,
        config_dir=config_dir,
        workspace_dir=workspace,
        expected_sha256=expected_engine_sha256,
        trace_friction=trace_friction,
    )

    slots_report: list[dict[str, Any]] = []
    divergences: list[dict[str, Any]] = []

    with sidecar as client:
        manifest = sidecar.build_run_manifest()
        plat = "macos" if sidecar.provenance.platform == "darwin" else "linux"
        pid = client.create_project(workspace)["id"]
        trace_log = sidecar.log_path if trace_friction else None

        ctx = _RunnerContext(
            client=client,
            sidecar=sidecar,
            project_id=pid,
            workspace=workspace,
            out_dir=out_dir,
            artifacts_dir=artifacts_dir,
            manifest=manifest,
            trace_log=trace_log,
            poll_timeout=poll_timeout,
            engine_bin=engine_bin,
            product_root=product_root,
            expected_engine_sha256=expected_engine_sha256,
            trace_friction=trace_friction,
        )

        for item in work_items:
            try:
                if item.v02_index is not None:
                    rep, div = _run_v02_instance(
                        ctx, base_seed=item.v02_base_seed, instance_index=item.v02_index
                    )
                else:
                    handler = SIDECAR_SLOT_HANDLERS[item.selector]
                    rep, div = handler(ctx)
            except Exception as exc:  # pragma: no cover - runtime fault capture
                rep = {
                    "selector": item.selector,
                    "family_id": item.family_id,
                    "lane": "sidecar",
                    "passed": False,
                    "error": f"{type(exc).__name__}: {exc}",
                }
                div = {
                    "selector": item.selector,
                    "family_id": item.family_id,
                    "kind": "runner_exception",
                    "detail": f"{type(exc).__name__}: {exc}",
                }
            slots_report.append(rep)
            if div is not None:
                divergences.append(div)

    per_family: dict[str, dict[str, int]] = {}
    for rep in slots_report:
        fam = str(rep["family_id"])
        bucket = per_family.setdefault(fam, {"executed": 0, "passed": 0, "divergences": 0})
        bucket["executed"] += 1
        if rep.get("passed"):
            bucket["passed"] += 1
        else:
            bucket["divergences"] += 1

    divergences_path = out_dir / "gb03-sidecar-divergences.jsonl"
    with divergences_path.open("w", encoding="utf-8") as fh:
        for d in divergences:
            fh.write(json.dumps(d, sort_keys=True) + "\n")

    summary = {
        "card": "GB03",
        "lane": "sidecar",
        "run_id": run_id,
        "platform": plat,
        "families": list(families),
        "base_seed": base_seed,
        "limit_v02": limit_v02,
        "shard": shard,
        "shards": shards,
        "trace_friction": trace_friction,
        "run_manifest": manifest.to_dict(),
        "executed_slots": len(slots_report),
        "passed_slots": sum(1 for r in slots_report if r.get("passed")),
        "divergence_count": len(divergences),
        "per_family": per_family,
        "slots": slots_report,
        "divergences": divergences,
        "passed": len(divergences) == 0,
    }
    (out_dir / "gb03-sidecar-summary.json").write_text(
        json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8"
    )
    return summary


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="GB03 full D1 sidecar runner (V01..V07)")
    ap.add_argument("--engine-bin", required=True)
    ap.add_argument("--product-root", required=True)
    ap.add_argument("--expected-engine-sha256", default="")
    ap.add_argument("--out-dir", required=True)
    ap.add_argument("--run-id", default="20261127-3bb1464d-gb03-sidecar")
    ap.add_argument("--families", default=",".join(GB03_FAMILY_IDS))
    ap.add_argument("--base-seed", default="20261127")
    ap.add_argument("--limit-v02", type=int, default=V02_GENERATED_SPEC.sidecar_per_platform_per_seed)
    ap.add_argument("--shard", type=int, default=0)
    ap.add_argument("--shards", type=int, default=1)
    ap.add_argument("--no-trace-friction", action="store_true")
    ap.add_argument("--poll-timeout", type=float, default=120.0)
    args = ap.parse_args(argv)

    summary = run_gb03_full_sidecar(
        engine_bin=args.engine_bin,
        product_root=args.product_root,
        expected_engine_sha256=args.expected_engine_sha256,
        out_dir=pathlib.Path(args.out_dir),
        run_id=args.run_id,
        families=parse_families_arg(args.families),
        base_seed=args.base_seed,
        limit_v02=args.limit_v02,
        shard=args.shard,
        shards=args.shards,
        trace_friction=not args.no_trace_friction,
        poll_timeout=args.poll_timeout,
    )
    print(json.dumps({k: v for k, v in summary.items() if k != "slots"}, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
