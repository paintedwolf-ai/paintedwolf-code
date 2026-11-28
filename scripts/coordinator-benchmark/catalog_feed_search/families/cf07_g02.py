"""CF07 G02 · Surveys and the repository map (survey_repo vs repo-map against independent reference)."""

from __future__ import annotations

from collections import Counter
import json
from pathlib import Path
import tempfile

from ..generators import BoundaryConfig, TreeEntrySpec, TreeGenOptions, TreeSpec, generate_tree_spec, materialize_tree
from ..live_controls import wait_catalog_settled
from ..references import WalkEntry, reference_walk
from ..seeds import ReplaySelector
from .cf07_harness import AgentToolSession, CapturedToolCall, D1SidecarProcess

REQUIRED_SURVEY_KEYS = ("bundle", "path", "digest", "total", "probes_run")


def parse_survey_json(call: CapturedToolCall) -> dict[str, object] | None:
    for candidate in (call.stdout, call.model_facing_message):
        text = candidate.strip()
        if text.startswith("{"):
            try:
                parsed = json.loads(text)
                if isinstance(parsed, dict) and "bundle" in parsed:
                    return parsed
            except ValueError:
                continue
    return None


def _claims_empty_repo(payload: dict[str, object], raw_text: str) -> bool:
    lower = raw_text.lower()
    if "repository is empty" in lower or "empty repository" in lower:
        return True
    total = payload.get("total")
    entries_exam = payload.get("entries_examined")
    inv_entries = payload.get("inventory_entries")
    if total == 0 and entries_exam == 0 and inv_entries == 0:
        return True
    return False


def _has_excluded_component(rel_path: str, component: str = ".paintedwolf") -> bool:
    parts = [p for p in rel_path.strip("/").split("/") if p]
    return component in parts


def _normalize_scope_path(path_str: str, scope_prefix: str) -> str:
    norm = path_str.strip().removeprefix("./").rstrip("/")
    if scope_prefix and scope_prefix != ".":
        pref = scope_prefix.strip("/") + "/"
        if norm.startswith(pref):
            norm = norm[len(pref) :]
        elif norm == scope_prefix.strip("/"):
            norm = "."
    return norm


def _validate_against_reference(
    label: str,
    payload: dict[str, object],
    *,
    ref_entries: list[WalkEntry],
    scope_prefix: str,
    boundary_dirs: set[str],
    nested_checkouts: set[str],
) -> list[str]:
    """Validate one survey/repo-map payload independently against the reference walk."""
    issues: list[str] = []
    ref_files = {e.path for e in ref_entries if e.kind == "file"}
    ref_dirs = {e.path for e in ref_entries if e.kind == "dir"}
    ref_all = ref_files | ref_dirs | {"."}

    # Check bounded projection count against reference (catches equal-but-wrong inventories).
    inv_entries = payload.get("inventory_entries")
    entries_exam = payload.get("entries_examined")
    truncated = bool(payload.get("truncated"))
    if ref_files:
        if isinstance(inv_entries, int) and inv_entries == 0:
            issues.append(f"{label} reported inventory_entries=0 on non-empty reference ({len(ref_files)} files)")
        if isinstance(entries_exam, int) and entries_exam == 0:
            issues.append(f"{label} reported entries_examined=0 on non-empty reference ({len(ref_files)} files)")
        if not truncated and isinstance(inv_entries, int) and inv_entries < len(ref_files):
            issues.append(
                f"{label} untruncated inventory_entries={inv_entries} < reference visible files={len(ref_files)}"
            )

    # Check every snapshot handle path against independent reference, boundaries, nested checkouts, and exclusions.
    for snap in payload.get("snapshot") or []:
        if not isinstance(snap, dict):
            continue
        raw_p = str(snap.get("path") or "").strip()
        if not raw_p:
            continue
        norm = _normalize_scope_path(raw_p, scope_prefix)
        if _has_excluded_component(norm, ".paintedwolf"):
            issues.append(f"{label} exposed excluded .paintedwolf path component: {raw_p!r}")
        for bdir in boundary_dirs:
            if norm.startswith(bdir + "/"):
                issues.append(f"{label} descended into boundary dir {bdir!r}: {raw_p!r}")
        for nrepo in nested_checkouts:
            if norm.startswith(nrepo + "/"):
                issues.append(f"{label} descended into nested checkout {nrepo!r}: {raw_p!r}")
        # If snapshot names a concrete path, it must exist in the reference walk (or be a parent dir of one).
        if norm != "." and norm not in ref_all:
            if not any(r.startswith(norm + "/") for r in ref_all):
                issues.append(f"{label} claimed path absent from independent reference walk: {raw_p!r}")

    return issues


def evaluate_g02_pair(
    survey_call: CapturedToolCall,
    repomap_call: CapturedToolCall,
    *,
    ref_entries: list[WalkEntry],
    scope_prefix: str = "",
    expect_non_empty: bool = True,
) -> dict[str, object]:
    """Compare survey_repo and repo-map BOTH against each other AND against the independent reference walk."""
    s_json = parse_survey_json(survey_call)
    r_json = parse_survey_json(repomap_call)
    issues: list[str] = []

    ref_file_count = sum(1 for e in ref_entries if e.kind == "file")
    boundary_dirs = {e.path for e in ref_entries if e.kind == "dir" and e.boundary}
    nested_checkouts = {
        e.path
        for e in ref_entries
        if e.kind == "dir" and e.boundary and e.path not in {"node_modules", "build", "dist", "target", ".git", "__pycache__", ".venv", ".cache"}
    }

    if s_json is None:
        issues.append(f"survey_repo returned non-JSON or missing bundle (outcome={survey_call.outcome})")
    if r_json is None:
        issues.append(f"repo-map returned non-JSON or missing bundle (outcome={repomap_call.outcome})")

    schema_match = False
    digest_match = False
    empty_claim_violation = False

    if s_json is not None and r_json is not None:
        missing_s = [k for k in REQUIRED_SURVEY_KEYS if k not in s_json]
        missing_r = [k for k in REQUIRED_SURVEY_KEYS if k not in r_json]
        if missing_s:
            issues.append(f"survey_repo missing keys {missing_s}")
        if missing_r:
            issues.append(f"repo-map missing keys {missing_r}")
        schema_match = (not missing_s) and (not missing_r) and (s_json.get("bundle") == r_json.get("bundle"))
        digest_match = (
            s_json.get("bundle") == r_json.get("bundle")
            and s_json.get("total") == r_json.get("total")
            and s_json.get("inventory_entries") == r_json.get("inventory_entries")
        )
        if not digest_match:
            issues.append(
                f"survey_repo vs repo-map mismatch: survey(total={s_json.get('total')}, inv={s_json.get('inventory_entries')}) "
                f"!= repo-map(total={r_json.get('total')}, inv={r_json.get('inventory_entries')})"
            )
        if expect_non_empty and ref_file_count > 0:
            if _claims_empty_repo(s_json, survey_call.stdout + survey_call.model_facing_message):
                empty_claim_violation = True
                issues.append("survey_repo bounded brief claimed non-empty repository is empty")
            if _claims_empty_repo(r_json, repomap_call.stdout + repomap_call.model_facing_message):
                empty_claim_violation = True
                issues.append("repo-map bounded brief claimed non-empty repository is empty")

        # Independent reference checks on BOTH routes (prevents equal-but-wrong passes).
        issues.extend(
            _validate_against_reference(
                "survey_repo",
                s_json,
                ref_entries=ref_entries,
                scope_prefix=scope_prefix,
                boundary_dirs=boundary_dirs,
                nested_checkouts=nested_checkouts,
            )
        )
        issues.extend(
            _validate_against_reference(
                "repo-map",
                r_json,
                ref_entries=ref_entries,
                scope_prefix=scope_prefix,
                boundary_dirs=boundary_dirs,
                nested_checkouts=nested_checkouts,
            )
        )

    declared_gaps = tuple(dict.fromkeys((*survey_call.declared_gaps, *repomap_call.declared_gaps)))
    if issues:
        verdict = "DEFECT"
    elif declared_gaps:
        verdict = "PASS_DECLARED_GAP"
    else:
        verdict = "PASS"

    return {
        "verdict": verdict,
        "issues": issues,
        "schema_match": schema_match,
        "digest_match": digest_match,
        "empty_claim_violation": empty_claim_violation,
        "ref_file_count": ref_file_count,
        "ref_boundary_count": len(boundary_dirs),
        "declared_gaps": list(declared_gaps),
        "survey_summary": {
            "bundle": s_json.get("bundle") if s_json else None,
            "total": s_json.get("total") if s_json else None,
            "inventory_entries": s_json.get("inventory_entries") if s_json else None,
            "inventory_state": s_json.get("inventory_state") if s_json else None,
            "truncated": s_json.get("truncated") if s_json else None,
        },
        "repomap_summary": {
            "bundle": r_json.get("bundle") if r_json else None,
            "total": r_json.get("total") if r_json else None,
            "inventory_entries": r_json.get("inventory_entries") if r_json else None,
            "inventory_state": r_json.get("inventory_state") if r_json else None,
            "truncated": r_json.get("truncated") if r_json else None,
        },
    }


def run_g02_sidecar(
    sidecar: D1SidecarProcess,
    *,
    base_seed: str,
    instances: int = 100,
    boundary_config: BoundaryConfig | None = None,
) -> dict[str, object]:
    """Run G02 sidecar instances (100 generated trees) + 4 hand-written edges."""
    assert sidecar.driver is not None
    verdict_counts: Counter[str] = Counter()
    gap_counts: Counter[str] = Counter()
    gap_samples: list[dict[str, object]] = []
    defects: list[dict[str, object]] = []

    with tempfile.TemporaryDirectory(prefix="cf07-g02-") as tmp:
        tmp_path = Path(tmp)
        batch_size = 20
        for batch_start in range(0, instances, batch_size):
            batch_end = min(instances, batch_start + batch_size)
            batch_dir = tmp_path / f"batch_{batch_start:03d}"
            batch_dir.mkdir(parents=True, exist_ok=True)
            batch_meta: list[tuple[int, ReplaySelector, str, list[WalkEntry]]] = []
            for idx in range(batch_start, batch_end):
                selector = ReplaySelector(base_seed=base_seed, family_id="G02", instance_index=idx)
                spec = generate_tree_spec(
                    base_seed,
                    "G02",
                    idx,
                    TreeGenOptions(
                        entry_count=10 + (idx % 16),
                        max_depth=5,
                        include_boundaries=True,
                        include_nested_checkouts=(idx % 5 == 0),
                        include_paintedwolf=(idx % 3 == 0),
                    ),
                )
                sub_name = f"tree_{idx:03d}"
                tree_dir = batch_dir / sub_name
                materialize_tree(spec, tree_dir)
                ref_entries = reference_walk(
                    tree_dir,
                    boundary_config=boundary_config,
                    include_hidden=False,
                    agent_exclusions=(".paintedwolf", ".paintedwolf/**"),
                    prune_nested_checkouts=True,
                )
                batch_meta.append((idx, selector, sub_name, ref_entries))

            pid, rids = sidecar.driver.attach_project([batch_dir])
            wait_catalog_settled(sidecar.driver, pid, rids[0], max_wait_s=25.0)
            session = AgentToolSession(sidecar, pid)
            try:
                for idx, selector, sub_name, ref_entries in batch_meta:
                    s_call = session.invoke_tool("survey_repo", {"bundle": "layout_overview", "path": sub_name})
                    r_call = session.invoke_tool("command", {"command": f"repo-map --path {sub_name}"})
                    ev = evaluate_g02_pair(s_call, r_call, ref_entries=ref_entries, scope_prefix=sub_name)
                    verdict_counts[str(ev["verdict"])] += 1
                    for g in ev["declared_gaps"]:
                        gap_counts[str(g)] += 1
                    if ev["declared_gaps"] and len(gap_samples) < 10:
                        gap_samples.append(
                            {
                                "instance": idx,
                                "survey_call": s_call.to_dict(),
                                "repomap_call": r_call.to_dict(),
                            }
                        )
                    if ev["verdict"] == "DEFECT" and len(defects) < 10:
                        defects.append(
                            {
                                "instance": idx,
                                "selector": selector.to_dict(),
                                "evaluation": ev,
                                "survey_call": s_call.to_dict(),
                                "repomap_call": r_call.to_dict(),
                            }
                        )
            finally:
                session.close()

        # 4 hand-written edges: boundaries, nested checkout, .paintedwolf exclusion, bounded --depth 1.
        edge_specs = [
            (
                "G02-edge-boundaries",
                (
                    TreeEntrySpec("src/app.go", "file", b"package app\n"),
                    TreeEntrySpec("node_modules/pkg/index.js", "file", b"module.exports = 1\n"),
                    TreeEntrySpec("dist/out.js", "file", b"console.log(1)\n"),
                ),
                "repo-map",
            ),
            (
                "G02-edge-nested-checkout",
                (
                    TreeEntrySpec("src/main.go", "file", b"package main\n"),
                    TreeEntrySpec("vendor_repo/.git/HEAD", "file", b"ref: refs/heads/main\n"),
                    TreeEntrySpec("vendor_repo/lib.go", "file", b"package vendor\n"),
                ),
                "repo-map",
            ),
            (
                "G02-edge-paintedwolf-exclusion",
                (
                    TreeEntrySpec("src/visible.go", "file", b"package visible\n"),
                    TreeEntrySpec(".paintedwolf/policy.yaml", "file", b"rules: []\n"),
                ),
                "repo-map",
            ),
            (
                "G02-edge-bounded-depth-nonempty",
                (
                    TreeEntrySpec("a/b/c/deep.go", "file", b"package deep\n"),
                    TreeEntrySpec("README.md", "file", b"# hello\n"),
                ),
                "repo-map --depth 1",
            ),
        ]
        edge_results: list[dict[str, object]] = []
        for edge_name, entries, repomap_cmd in edge_specs:
            edir = tmp_path / edge_name
            spec = TreeSpec(ReplaySelector(base_seed, "G02", 9000 + len(edge_results)), entries)
            materialize_tree(spec, edir)
            ref_entries = reference_walk(
                edir,
                boundary_config=boundary_config,
                include_hidden=False,
                agent_exclusions=(".paintedwolf", ".paintedwolf/**"),
                prune_nested_checkouts=True,
            )
            pid, rids = sidecar.driver.attach_project([edir])
            wait_catalog_settled(sidecar.driver, pid, rids[0], max_wait_s=15.0)
            session = AgentToolSession(sidecar, pid)
            try:
                s_call = session.invoke_tool("survey_repo", {"bundle": "layout_overview", "path": "."})
                r_call = session.invoke_tool("command", {"command": repomap_cmd})
            finally:
                session.close()
            ev = evaluate_g02_pair(s_call, r_call, ref_entries=ref_entries, scope_prefix="")
            edge_results.append(
                {
                    "edge": edge_name,
                    "repomap_cmd": repomap_cmd,
                    "evaluation": ev,
                    "survey_call": s_call.to_dict(),
                    "repomap_call": r_call.to_dict(),
                }
            )

    return {
        "family": "G02",
        "lane": "sidecar",
        "base_seed": base_seed,
        "planned_instances": instances,
        "executed_instances": sum(verdict_counts.values()),
        "verdict_counts": dict(verdict_counts),
        "declared_gap_counts": dict(gap_counts),
        "declared_gap_samples": gap_samples,
        "defects": defects,
        "edges": edge_results,
    }
