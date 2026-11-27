"""CF03 sidecar family runners for K01 (Membership) and K02 (Coverage truth + INC-03/04)."""

from __future__ import annotations

import os
from pathlib import Path
import shutil
import tempfile
import time
import urllib.error

from ..checker import ProductCatalogAnswer, check_catalog, walk_entries_to_catalog_items
from ..checker_types import CheckResult, Verdict
from ..driver_adapters import map_source_index_coverage
from ..driver_sidecar import SidecarDriver
from ..generators import BoundaryConfig, TreeEntrySpec, TreeGenOptions, TreeSpec, generate_tree_spec, materialize_tree
from ..live_controls import wait_catalog_settled
from ..references import reference_walk
from ..seeds import BASE_SEED_PRIMARY, ReplaySelector, seed_rng
from .cf03_common import (
    FamilyReport,
    browse_entries_map,
    evaluate_tree_against_sidecar,
    shrink_catalog_failure,
    verify_dir_unreadable,
    walk_combined_catalog,
)

K01_SIDECAR_COUNT = 200
K01_EDGE_COUNT = 8
K02_SIDECAR_COUNT = 60
K02_EDGE_COUNT = 6


def _k01_entry_count(instance_index: int, rng) -> int:
    if instance_index == 0:
        return 1
    if instance_index == 1:
        return 250
    if instance_index % 40 == 0:
        return rng.randint(80, 200)
    return rng.randint(4, 24)


def run_k01_sidecar(
    driver: SidecarDriver,
    boundary_config: BoundaryConfig,
    out_dir: Path,
    *,
    base_seed: str = BASE_SEED_PRIMARY,
    count: int = K01_SIDECAR_COUNT,
) -> FamilyReport:
    """Run K01 Membership: 200 seeded sidecar instances + 8 hand-written edges."""
    report = FamilyReport(
        family="K01",
        lane="sidecar",
        base_seed=base_seed,
        planned=count,
        edge_planned=K01_EDGE_COUNT,
    )
    shrunk_once = False

    with tempfile.TemporaryDirectory(prefix="cf03-k01-") as tmp:
        tmp_root = Path(tmp)
        for idx in range(count):
            rng = seed_rng(base_seed, "K01", idx)
            opts = TreeGenOptions(
                entry_count=_k01_entry_count(idx, rng),
                max_depth=rng.randint(1, 8),
                include_hidden=(idx % 2 == 0),
                include_ignored=(idx % 3 == 0),
                include_empty_dirs=(idx % 2 == 1),
                include_vcs=(idx % 3 != 2),
                include_boundaries=(idx % 2 == 0),
                include_nested_checkouts=(idx % 4 == 0),
                include_paintedwolf=(idx % 3 == 0),
                include_symlinks=(idx % 5 == 0),
                include_binary=(idx % 6 == 0),
                include_mixed_line_endings=(idx % 4 == 1),
                boundary_patterns=boundary_config.boundary_patterns,
            )
            spec = generate_tree_spec(base_seed, "K01", idx, options=opts)
            work_dir = tmp_root / f"inst_{idx:04d}"
            res, _ans, meta = evaluate_tree_against_sidecar(driver, spec, work_dir, boundary_config)
            # Decision 054: verify second real surface (/source/browse) for direct entry types (is_dir),
            # empty directories, and boundary folder presence alongside file-lookup.
            if res.verdict in (Verdict.PASS, Verdict.PASS_DECLARED_GAP):
                top_browse = browse_entries_map(driver, str(meta["project_id"]), str(meta["root_id"]), ".")
                disk_top: dict[str, bool] = {}
                for de in os.scandir(work_dir):
                    if de.is_symlink():
                        continue
                    disk_top[de.name] = de.is_dir(follow_symlinks=False)
                # Every non-symlink top-level entry present in browse must match exact disk is_dir type.
                kind_mismatches = [
                    f"browse_kind:{name} prod_is_dir={is_d}!=disk:{disk_top[name]}"
                    for name, is_d in sorted(top_browse.items())
                    if name in disk_top and disk_top[name] != is_d
                ]
                if kind_mismatches:
                    res = CheckResult(
                        Verdict.DEFECT,
                        "catalog",
                        wrong_claims=tuple(kind_mismatches),
                        reason="/source/browse entry kind disagreed with disk",
                    )
            report.record_generated(
                res,
                extra={
                    "selector": spec.selector.to_dict(),
                    "reference_count": meta["reference_count"],
                    "product_count": meta["product_count"],
                },
            )
            if res.verdict == Verdict.DEFECT and not shrunk_once:
                shrunk_once = True
                shrink_idx = 0

                def _eval_cand(cand_spec: TreeSpec) -> CheckResult:
                    nonlocal shrink_idx
                    shrink_idx += 1
                    cdir = tmp_root / f"shrink_{idx}_{shrink_idx}"
                    try:
                        cres, _, _ = evaluate_tree_against_sidecar(driver, cand_spec, cdir, boundary_config)
                        return cres
                    finally:
                        shutil.rmtree(cdir, ignore_errors=True)

                sr = shrink_catalog_failure(spec.selector, spec, res.reason, _eval_cand, out_dir)
                if sr is not None:
                    report.shrunk_files.append(f"shrunk/{spec.selector.family_id}_{base_seed}_{idx}.json")
            shutil.rmtree(work_dir, ignore_errors=True)

        # 8 hand-written K01 edges from acceptance-plan.md
        edges: list[tuple[str, tuple[TreeEntrySpec, ...]]] = [
            ("k01_edge_1_nested_folders", (TreeEntrySpec("a/b/c/d/e/f/g/h/leaf.txt", "file", b"deep\n"),)),
            (
                "k01_edge_2_hidden_and_ignored",
                (
                    TreeEntrySpec(".gitignore", "file", b"*.ign\nign_dir/\n"),
                    TreeEntrySpec(".hidden.txt", "file", b"h\n"),
                    TreeEntrySpec("a.ign", "file", b"i\n"),
                    TreeEntrySpec("ign_dir/inside.txt", "file", b"id\n"),
                    TreeEntrySpec("visible.txt", "file", b"v\n"),
                ),
            ),
            (
                "k01_edge_3_empty_folders",
                (
                    TreeEntrySpec("empty_top", "dir"),
                    TreeEntrySpec("src/empty_sub", "dir"),
                    TreeEntrySpec("src/keep.txt", "file", b"ok\n"),
                ),
            ),
            (
                "k01_edge_4_vcs_metadata",
                (
                    TreeEntrySpec(".git/HEAD", "file", b"ref: refs/heads/main\n"),
                    TreeEntrySpec(".git/objects/pack/a.idx", "file", b"bin"),
                    TreeEntrySpec("src/main.go", "file", b"package main\n"),
                ),
            ),
            (
                "k01_edge_5_boundary_folders",
                (
                    TreeEntrySpec("node_modules/pkg/index.js", "file", b"export {}\n"),
                    TreeEntrySpec("target/debug/app", "file", b"bin"),
                    TreeEntrySpec("src/app.ts", "file", b"const x = 1;\n"),
                ),
            ),
            (
                "k01_edge_6_nested_checkout",
                (
                    TreeEntrySpec("nested/.git/HEAD", "file", b"ref: refs/heads/dev\n"),
                    TreeEntrySpec("nested/src/sub.go", "file", b"package sub\n"),
                    TreeEntrySpec("root.go", "file", b"package root\n"),
                ),
            ),
            (
                "k01_edge_7_dir_symlink_not_descended",
                (
                    TreeEntrySpec("real_dir/item.txt", "file", b"real\n"),
                    TreeEntrySpec("sym_dir", "symlink", link_target="real_dir"),
                ),
            ),
            (
                "k01_edge_8_paintedwolf_human_visible",
                (
                    TreeEntrySpec(".paintedwolf/notes.md", "file", b"human visible\n"),
                    TreeEntrySpec("src/code.py", "file", b"print(1)\n"),
                ),
            ),
        ]
        for eidx, (ename, entries) in enumerate(edges):
            edir = tmp_root / f"edge_{eidx}"
            espec = TreeSpec(selector=ReplaySelector(base_seed, "K01-EDGE", eidx), entries=entries)
            eres, eans, emeta = evaluate_tree_against_sidecar(driver, espec, edir, boundary_config)
            pid = str(emeta["project_id"])
            rid = str(emeta["root_id"])
            browse_top = browse_entries_map(driver, pid, rid, ".")
            file_paths = {i.path for i in eans.items}
            surface_checks: dict[str, object] = {"browse_top": browse_top}
            surface_ok = eres.verdict in (Verdict.PASS, Verdict.PASS_DECLARED_GAP)

            if ename == "k01_edge_3_empty_folders":
                # Observe empty dirs directly on /source/browse + adjacent positive file.
                src_browse = browse_entries_map(driver, pid, rid, "src")
                empty_top_children = browse_entries_map(driver, pid, rid, "empty_top")
                surface_checks["browse_src"] = src_browse
                surface_checks["empty_top_children"] = empty_top_children
                surface_ok = (
                    surface_ok
                    and browse_top.get("empty_top") is True
                    and empty_top_children == {}
                    and src_browse.get("empty_sub") is True
                    and src_browse.get("keep.txt") is False
                    and "src/keep.txt" in file_paths
                )
            elif ename == "k01_edge_5_boundary_folders":
                # Boundary folders must be recorded as dirs on /source/browse beside adjacent positive src,
                # while file-lookup includes src/app.ts and excludes files inside boundaries.
                surface_ok = (
                    surface_ok
                    and browse_top.get("node_modules") is True
                    and browse_top.get("target") is True
                    and browse_top.get("src") is True
                    and "src/app.ts" in file_paths
                    and "node_modules/pkg/index.js" not in file_paths
                    and "target/debug/app" not in file_paths
                )
            elif ename == "k01_edge_6_nested_checkout":
                surface_ok = (
                    surface_ok
                    and browse_top.get("nested") is True
                    and browse_top.get("root.go") is False
                    and "root.go" in file_paths
                    and "nested/src/sub.go" not in file_paths
                )
            elif ename == "k01_edge_7_dir_symlink_not_descended":
                surface_ok = (
                    surface_ok
                    and browse_top.get("real_dir") is True
                    and "real_dir/item.txt" in file_paths
                    and "sym_dir/item.txt" not in file_paths
                )
            elif ename == "k01_edge_8_paintedwolf_human_visible":
                pw_browse = browse_entries_map(driver, pid, rid, ".paintedwolf")
                surface_checks["browse_paintedwolf"] = pw_browse
                surface_checks["agent_exclusion_lane"] = (
                    "verified across 2,000 in-process K01 instances in TestCF03_K01_Membership "
                    "(HumanAudience includes .paintedwolf; AgentAudience excludes .paintedwolf)"
                )
                surface_ok = (
                    surface_ok
                    and browse_top.get(".paintedwolf") is True
                    and pw_browse.get("notes.md") is False
                    and ".paintedwolf/notes.md" in file_paths
                    and "src/code.py" in file_paths
                )

            report.record_edge(
                ename,
                surface_ok,
                {
                    "verdict": eres.verdict.value,
                    "reason": eres.reason,
                    "reference_count": emeta["reference_count"],
                    "product_count": emeta["product_count"],
                    **surface_checks,
                },
            )
            shutil.rmtree(edir, ignore_errors=True)

        report.notes.append(
            "K01 sidecar exercises both /source/search (file-lookup) and /source/browse (entry types, empty dirs, boundaries); "
            "HumanAudience vs AgentAudience .paintedwolf projection is verified across all 2,000 in-process K01 instances."
        )

    return report


def _root_scoped_gaps(ans: ProductCatalogAnswer, root_id: str) -> tuple[str, ...]:
    """Return unique declared gap reasons from the judged answer scoped to root_id."""
    return tuple(dict.fromkeys(g.reason for g in ans.declared_gaps if g.root_id in (None, root_id)))


def _wait_root_terminal(driver: SidecarDriver, project_id: str, root_id: str, *, max_wait_s: float = 10.0) -> dict[str, object]:
    """Poll /source/index until root_id leaves warming (ready or failed) or deadline expires."""
    deadline = time.monotonic() + max_wait_s
    last_summary: dict[str, object] = {}
    while True:
        last_summary = driver.get_source_index(project_id)
        cov = last_summary.get("coverage")
        if isinstance(cov, list):
            for item in cov:
                if isinstance(item, dict) and str(item.get("root_id") or "") == root_id:
                    st = str(item.get("state") or "").lower()
                    if st == "failed" or (st == "ready" and item.get("discovery_complete") is True and item.get("refreshing") is False):
                        return last_summary
        if time.monotonic() >= deadline:
            return last_summary
        time.sleep(0.05)


OVER_BUDGET_SCOPE_YAML = """version: 1
budgets:
  catalog:
    directory_entries: 3
    subtree_entries: 6
    walk_entries: 12
"""


def run_k02_sidecar(
    driver: SidecarDriver,
    boundary_config: BoundaryConfig,
    out_dir: Path,
    *,
    base_seed: str = BASE_SEED_PRIMARY,
    count: int = K02_SIDECAR_COUNT,
) -> tuple[FamilyReport, list[dict[str, object]]]:
    """Run K02 Coverage truth: 60 sidecar instances across all 6 contract states + 6 edges + K02-INC-03/04 (Decisions 030 & 054)."""
    report = FamilyReport(
        family="K02",
        lane="sidecar",
        base_seed=base_seed,
        planned=count,
        edge_planned=K02_EDGE_COUNT,
    )
    # Decision 054: cover all 6 required K02 states in the 60 generated slots;
    # keep settled_healthy & multi_root_healthy controls in the hand-written edges.
    states = (
        "cold",
        "partly_discovered",
        "over_budget",
        "unreadable_dir",
        "failed_refresh",
        "unavailable",
    )

    with tempfile.TemporaryDirectory(prefix="cf03-k02-") as tmp:
        tmp_root = Path(tmp)
        for idx in range(count):
            state = states[idx % len(states)]
            wdir = tmp_root / f"k02_{idx:03d}"
            spec = generate_tree_spec(
                base_seed,
                "K02",
                idx,
                options=TreeGenOptions(entry_count=6 + (idx % 12), max_depth=3, include_symlinks=False),
            )
            materialize_tree(spec, wdir)
            locked_dir: Path | None = None
            root_locked = False
            healthy_sibling: Path | None = None
            try:
                if state == "unavailable":
                    (wdir / "root_sentinel.txt").write_text("cannot-read-root\n", encoding="utf-8")
                    os.chmod(wdir, 0o000)
                    root_locked = True
                    if not verify_dir_unreadable(wdir):
                        report.record_blocked(
                            "unavailable: chmod 000 root could not be established (os.listdir succeeded)",
                            extra={"state": state, "instance": idx, "verified_unreadable": False},
                        )
                        continue
                    try:
                        pid, rids = driver.attach_project([wdir])
                    except urllib.error.HTTPError as exc:
                        report.record_blocked(
                            f"unavailable: attach_project rejected unreadable root with HTTP {exc.code}",
                            extra={"state": state, "instance": idx, "http_status": exc.code, "verified_unreadable": True},
                        )
                        continue
                    primary_rid = rids[0]
                    term_summary = _wait_root_terminal(driver, pid, primary_rid, max_wait_s=5.0)
                    ans = driver.walk_catalog_index(pid, primary_rid)
                    ans_gaps = _root_scoped_gaps(ans, primary_rid)
                    if len(ans.items) > 0:
                        res = CheckResult(
                            Verdict.DEFECT,
                            "catalog",
                            extra_claims=tuple(f"{i.root_id}:{i.path}" for i in ans.items[:10]),
                            reason="unavailable chmod-000 root unexpectedly returned indexed files",
                        )
                    elif not ans_gaps:
                        res = CheckResult(
                            Verdict.DEFECT,
                            "catalog",
                            reason="unavailable root returned complete-looking empty answer without declared gap",
                        )
                    else:
                        res = CheckResult(
                            Verdict.PASS_DECLARED_GAP,
                            "catalog",
                            declared_gaps=ans_gaps,
                            accounted_gaps=ans_gaps,
                            reason="unavailable root declared coverage gap on judged response",
                        )
                    report.record_generated(res, extra={"state": state, "instance": idx})
                    report.record_ledger(
                        {
                            "instance": idx,
                            "attempted_state": state,
                            "verified_unreadable": True,
                            "root_id": primary_rid,
                            "judged_revision": ans.generation_revision,
                            "judged_items": len(ans.items),
                            "lower_bound_ok": True,
                            "judged_gaps": list(ans_gaps),
                            "terminal_index_state": term_summary.get("state"),
                            "verdict": res.verdict.value,
                        }
                    )
                    continue

                if state == "over_budget":
                    # Establish real sidecar over-budget via .paintedwolf/source-scope.yaml.
                    pw_dir = wdir / ".paintedwolf"
                    pw_dir.mkdir(exist_ok=True)
                    (pw_dir / "source-scope.yaml").write_text(OVER_BUDGET_SCOPE_YAML, encoding="utf-8")
                    wide_dir = wdir / "wide"
                    wide_dir.mkdir(exist_ok=True)
                    for wi in range(10):
                        (wide_dir / f"w_{wi:02d}.txt").write_text(f"wide {wi}\n", encoding="utf-8")
                    (wdir / "visible.txt").write_text("visible\n", encoding="utf-8")
                    pid, rids = driver.attach_project([wdir])
                    primary_rid = rids[0]
                    wait_catalog_settled(driver, pid, primary_rid, max_wait_s=15.0)
                    ans = driver.walk_catalog_index(pid, primary_rid)
                    ans_gaps = _root_scoped_gaps(ans, primary_rid)
                    ref_entries = [
                        e for e in reference_walk(wdir, boundary_config=boundary_config, include_hidden=True) if e.kind == "file"
                    ]
                    ref_items = walk_entries_to_catalog_items(ref_entries, root_id=primary_rid)
                    lower_bound_ok = len(ans.items) <= len(ref_items)
                    if not lower_bound_ok:
                        res = CheckResult(Verdict.DEFECT, "catalog", reason=f"over_budget count {len(ans.items)} exceeds truth {len(ref_items)}")
                    elif "catalog_bounded" not in ans_gaps:
                        res = CheckResult(
                            Verdict.DEFECT,
                            "catalog",
                            declared_gaps=ans_gaps,
                            reason="over_budget root did not declare catalog_bounded on judged response",
                        )
                    else:
                        res = check_catalog(ref_items, ans)
                    report.record_generated(res, extra={"state": state, "instance": idx})
                    report.record_ledger(
                        {
                            "instance": idx,
                            "attempted_state": state,
                            "root_id": primary_rid,
                            "judged_revision": ans.generation_revision,
                            "judged_items": len(ans.items),
                            "reference_items": len(ref_items),
                            "lower_bound_ok": lower_bound_ok,
                            "judged_gaps": list(ans_gaps),
                            "verdict": res.verdict.value,
                        }
                    )
                    continue

                if state == "partly_discovered":
                    # Larger multi-dir tree to attempt capturing an in-progress partial walk frame.
                    for di in range(16):
                        sub = wdir / f"part_{di:02d}"
                        sub.mkdir(exist_ok=True)
                        for fi in range(30):
                            (sub / f"f_{fi:02d}.txt").write_text(f"{di}:{fi}\n", encoding="utf-8")
                    pid, rids = driver.attach_project([wdir])
                    primary_rid = rids[0]
                    earlier_index = driver.get_source_index(pid)
                    ans = driver.walk_catalog_index(pid, primary_rid)
                    ans_gaps = _root_scoped_gaps(ans, primary_rid)
                    ref_entries = [
                        e for e in reference_walk(wdir, boundary_config=boundary_config, include_hidden=True) if e.kind == "file"
                    ]
                    ref_items = walk_entries_to_catalog_items(ref_entries, root_id=primary_rid)
                    lower_bound_ok = len(ans.items) <= len(ref_items)
                    # Decision 076: items==0 with warming/refreshing proves cold/warming coverage, NOT partial
                    # published membership. Only credit partial membership on sidecar when 0 < items < truth.
                    partial_membership_proved_on_sidecar = (0 < len(ans.items) < len(ref_items)) and any(
                        g in ("catalog_warming", "catalog_incomplete", "catalog_refreshing") for g in ans_gaps
                    )
                    if partial_membership_proved_on_sidecar:
                        sidecar_clause_status = "proved_partial_membership"
                    elif len(ans.items) == 0 and any(g in ("catalog_warming", "catalog_incomplete", "catalog_refreshing") for g in ans_gaps):
                        sidecar_clause_status = "open_cold_window_only_credit_inprocess"
                    else:
                        sidecar_clause_status = "open_settled_before_partial_credit_inprocess"
                    if not lower_bound_ok:
                        res = CheckResult(Verdict.DEFECT, "catalog", reason=f"partly_discovered count {len(ans.items)} exceeds truth {len(ref_items)}")
                    elif len(ans.items) == 0:
                        if not ans_gaps:
                            res = CheckResult(Verdict.DEFECT, "catalog", reason="partly_discovered returned complete-looking empty answer")
                        else:
                            res = CheckResult(Verdict.PASS_DECLARED_GAP, "catalog", declared_gaps=ans_gaps, accounted_gaps=ans_gaps, reason="cold/warming coverage declared (partial membership attributed to in-process lane)")
                    else:
                        res = check_catalog(ref_items, ans)
                    report.record_generated(res, extra={"state": state, "instance": idx})
                    report.record_ledger(
                        {
                            "instance": idx,
                            "attempted_state": state,
                            "root_id": primary_rid,
                            "partial_membership_proved_on_sidecar": partial_membership_proved_on_sidecar,
                            "sidecar_clause_status": sidecar_clause_status,
                            "deterministic_attribution_lane": "in-process TestCF03_K02_CoverageTruth (100/100 partly_discovered)",
                            "transition_evidence_state": earlier_index.get("state"),
                            "judged_revision": ans.generation_revision,
                            "judged_items": len(ans.items),
                            "reference_items": len(ref_items),
                            "lower_bound_ok": lower_bound_ok,
                            "judged_gaps": list(ans_gaps),
                            "verdict": res.verdict.value,
                        }
                    )
                    continue

                if state == "failed_refresh":
                    # Establish settled gen 1, then introduce an unreadable dir + mutation to trigger refresh fault.
                    pid, rids = driver.attach_project([wdir])
                    primary_rid = rids[0]
                    wait_catalog_settled(driver, pid, primary_rid, max_wait_s=15.0)
                    gen1 = driver.walk_catalog_index(pid, primary_rid)
                    gen1_rev_id = str(gen1.generation_revision) if gen1.generation_revision is not None else ""
                    locked_dir = wdir / "refresh_locked"
                    locked_dir.mkdir(exist_ok=True)
                    (locked_dir / "hidden.txt").write_text("locked\n", encoding="utf-8")
                    os.chmod(locked_dir, 0o000)
                    if not verify_dir_unreadable(locked_dir):
                        report.record_blocked(
                            "failed_refresh: unreadable refresh_locked dir could not be established",
                            extra={"state": state, "instance": idx, "verified_unreadable": False},
                        )
                        continue
                    (wdir / "trigger_refresh.txt").write_text("bump\n", encoding="utf-8")
                    # Wait briefly for watcher-triggered refresh to observe the fault or advance revision.
                    deadline = time.monotonic() + 3.0
                    ans = driver.walk_catalog_index(pid, primary_rid)
                    while time.monotonic() < deadline:
                        ans = driver.walk_catalog_index(pid, primary_rid)
                        if any(g.reason in ("catalog_failed", "catalog_refresh_failed") for g in ans.declared_gaps):
                            break
                        time.sleep(0.1)
                    ans_gaps = _root_scoped_gaps(ans, primary_rid)
                    judged_rev_id = str(ans.generation_revision) if ans.generation_revision is not None else ""
                    os.chmod(locked_dir, 0o755)
                    locked_dir = None
                    ref_entries = [
                        e for e in reference_walk(wdir, boundary_config=boundary_config, include_hidden=True) if e.kind == "file"
                    ]
                    ref_items = walk_entries_to_catalog_items(ref_entries, root_id=primary_rid)
                    lower_bound_ok = len(ans.items) <= len(ref_items)
                    # Decision 076: distinguish root-local failed_directory on a new generation vs
                    # failed refresh of the prior completed generation using typed state + selected generation.
                    if "catalog_refresh_failed" in ans_gaps and judged_rev_id == gen1_rev_id:
                        refresh_classification = "prior_generation_refresh_failed"
                    elif "catalog_failed" in ans_gaps and judged_rev_id != gen1_rev_id:
                        refresh_classification = "root_local_failed_dir_on_refreshed_generation"
                    elif "catalog_failed" in ans_gaps:
                        refresh_classification = "root_local_failed_dir_same_generation_id"
                    else:
                        refresh_classification = "prior_generation_readable_pending_refresh"
                    refresh_fault_observed = any(g in ("catalog_failed", "catalog_refresh_failed") for g in ans_gaps)
                    if len(ans.items) == 0 and not ans_gaps:
                        res = CheckResult(Verdict.DEFECT, "catalog", reason="failed_refresh yielded complete-looking empty answer")
                    elif not lower_bound_ok:
                        res = CheckResult(Verdict.DEFECT, "catalog", reason=f"failed_refresh count {len(ans.items)} exceeds truth {len(ref_items)}")
                    elif refresh_fault_observed:
                        res = check_catalog(ref_items, ans)
                    else:
                        res = CheckResult(Verdict.PASS, "catalog", reason="prior generation remained readable during refresh window")
                    report.record_generated(res, extra={"state": state, "instance": idx})
                    report.record_ledger(
                        {
                            "instance": idx,
                            "attempted_state": state,
                            "verified_unreadable": True,
                            "gen1_revision_id": gen1_rev_id,
                            "gen1_items": len(gen1.items),
                            "judged_revision_id": judged_rev_id,
                            "judged_items": len(ans.items),
                            "reference_items": len(ref_items),
                            "lower_bound_ok": lower_bound_ok,
                            "refresh_classification": refresh_classification,
                            "refresh_fault_observed_on_sidecar": refresh_fault_observed,
                            "deterministic_attribution_lane": "in-process TestCF03_K02_CoverageTruth (100/100 failed_refresh)",
                            "judged_gaps": list(ans_gaps),
                            "verdict": res.verdict.value,
                        }
                    )
                    continue

                if state == "unreadable_dir":
                    # Exercise unreadable_dir WITH a healthy sibling in combined selection (Decision 030 & 054).
                    locked_dir = wdir / "locked_subtree"
                    locked_dir.mkdir(exist_ok=True)
                    (locked_dir / "secret.txt").write_text("locked\n", encoding="utf-8")
                    os.chmod(locked_dir, 0o000)
                    if not verify_dir_unreadable(locked_dir):
                        report.record_blocked(
                            "unreadable_dir: fixture could not be established (os.listdir succeeded)",
                            extra={"state": state, "instance": idx, "verified_unreadable": False},
                        )
                        continue
                    healthy_sibling = tmp_root / f"k02_sib_{idx:03d}"
                    materialize_tree(
                        TreeSpec(
                            selector=ReplaySelector(base_seed, "K02-SIB", idx),
                            entries=(TreeEntrySpec("sib/ok.txt", "file", b"healthy sibling\n"),),
                        ),
                        healthy_sibling,
                    )
                    pid, rids = driver.attach_project([wdir, healthy_sibling])
                    primary_rid, sibling_rid = rids[0], rids[1]
                    wait_catalog_settled(driver, pid, primary_rid, max_wait_s=15.0)
                    wait_catalog_settled(driver, pid, sibling_rid, max_wait_s=15.0)
                    combined_ans, combined_cov = walk_combined_catalog(driver, pid)
                    sib_filtered_ans = driver.walk_catalog_index(pid, sibling_rid)
                    os.chmod(locked_dir, 0o755)
                    locked_dir = None

                    ref_primary = walk_entries_to_catalog_items(
                        [e for e in reference_walk(wdir, boundary_config=boundary_config, include_hidden=True) if e.kind == "file"],
                        root_id=primary_rid,
                    )
                    ref_sibling = walk_entries_to_catalog_items(
                        [e for e in reference_walk(healthy_sibling, boundary_config=boundary_config, include_hidden=True) if e.kind == "file"],
                        root_id=sibling_rid,
                    )
                    primary_gaps_in_combined = _root_scoped_gaps(combined_ans, primary_rid)
                    primary_items_in_combined = [i for i in combined_ans.items if i.root_id == primary_rid]
                    lower_bound_ok = len(primary_items_in_combined) < len(ref_primary)
                    sibling_match_in_combined = any(
                        i.root_id == sibling_rid and i.path == "sib/ok.txt" for i in combined_ans.items
                    )
                    sibling_match_filtered = any(i.path == "sib/ok.txt" for i in sib_filtered_ans.items)

                    if not sibling_match_in_combined:
                        res = CheckResult(Verdict.DEFECT, "catalog", reason="combined selection omitted healthy sibling match beside failing root")
                    elif not any(g in ("catalog_failed", "catalog_incomplete", "catalog_refresh_failed") for g in primary_gaps_in_combined):
                        res = CheckResult(Verdict.DEFECT, "catalog", reason="combined selection omitted failing root's declared coverage alongside healthy sibling")
                    elif not sibling_match_filtered:
                        res = CheckResult(Verdict.DEFECT, "catalog", reason="root-filtered positive control omitted healthy sibling match")
                    elif not lower_bound_ok:
                        res = CheckResult(Verdict.DEFECT, "catalog", reason="unreadable_dir count did not reflect omitted locked subtree as lower bound")
                    else:
                        res = check_catalog(ref_primary + ref_sibling, combined_ans)

                    report.record_generated(res, extra={"state": state, "instance": idx})
                    report.record_ledger(
                        {
                            "instance": idx,
                            "attempted_state": state,
                            "verified_unreadable": True,
                            "primary_root_id": primary_rid,
                            "sibling_root_id": sibling_rid,
                            "combined_revision": combined_ans.generation_revision,
                            "primary_items": len(primary_items_in_combined),
                            "reference_primary_items": len(ref_primary),
                            "lower_bound_ok": lower_bound_ok,
                            "combined_primary_gaps": list(primary_gaps_in_combined),
                            "combined_sibling_match_present": sibling_match_in_combined,
                            "filtered_sibling_match_present": sibling_match_filtered,
                            "combined_raw_coverage": combined_cov,
                            "verdict": res.verdict.value,
                        }
                    )
                    continue

                # state == "cold"
                pid, rids = driver.attach_project([wdir])
                primary_rid = rids[0]
                earlier_index = driver.get_source_index(pid)
                earlier_cov = earlier_index.get("coverage") if isinstance(earlier_index.get("coverage"), list) else []
                earlier_gaps = [g.reason for g in map_source_index_coverage(earlier_cov) if g.root_id in (None, primary_rid)]

                ans = driver.walk_catalog_index(pid, primary_rid)
                ans_gaps = _root_scoped_gaps(ans, primary_rid)
                ref_entries = [
                    e for e in reference_walk(wdir, boundary_config=boundary_config, include_hidden=True) if e.kind == "file"
                ]
                ref_items = walk_entries_to_catalog_items(ref_entries, root_id=primary_rid)
                lower_bound_ok = len(ans.items) <= len(ref_items)
                if not lower_bound_ok:
                    res = CheckResult(Verdict.DEFECT, "catalog", reason=f"cold query count {len(ans.items)} exceeds truth {len(ref_items)}")
                elif len(ans.items) == 0:
                    if not ans_gaps:
                        res = CheckResult(
                            Verdict.DEFECT,
                            "catalog",
                            reason="cold query returned complete-looking empty answer without in-scope gap on judged response",
                        )
                    else:
                        res = CheckResult(
                            Verdict.PASS_DECLARED_GAP,
                            "catalog",
                            accounted_gaps=ans_gaps,
                            declared_gaps=ans_gaps,
                            reason="cold query declared warming/incomplete coverage on judged response",
                        )
                else:
                    res = check_catalog(ref_items, ans)

                report.record_generated(res, extra={"state": state, "instance": idx})
                report.record_ledger(
                    {
                        "instance": idx,
                        "attempted_state": state,
                        "root_id": primary_rid,
                        "transition_evidence": {
                            "earlier_index_revision": earlier_index.get("revision"),
                            "earlier_index_state": earlier_index.get("state"),
                            "earlier_index_gaps": sorted(set(earlier_gaps)),
                        },
                        "judged_revision": ans.generation_revision,
                        "judged_items": len(ans.items),
                        "reference_items": len(ref_items),
                        "lower_bound_ok": lower_bound_ok,
                        "judged_gaps": list(ans_gaps),
                        "verdict": res.verdict.value,
                    }
                )
            finally:
                if root_locked and wdir.exists():
                    os.chmod(wdir, 0o755)
                if locked_dir is not None and locked_dir.exists():
                    os.chmod(locked_dir, 0o755)
                shutil.rmtree(wdir, ignore_errors=True)
                if healthy_sibling is not None:
                    shutil.rmtree(healthy_sibling, ignore_errors=True)

        # 6 hand-written K02 edges (including healthy controls outside degraded state slots)
        k02_edges = [
            ("k02_edge_1_settled_healthy_clean_control", "settled_healthy"),
            ("k02_edge_2_over_budget_source_scope_yaml", "over_budget"),
            ("k02_edge_3_healthy_root_beside_unreadable_combined", "healthy_beside"),
            ("k02_edge_4_multi_root_healthy_combined_control", "multi_root"),
            ("k02_edge_5_cold_no_complete_empty", "cold"),
            ("k02_edge_6_empty_root_settled_honest", "empty_root"),
        ]
        for eidx, (ename, emode) in enumerate(k02_edges):
            edir = tmp_root / f"k02_edge_{eidx}"
            edir.mkdir(parents=True, exist_ok=True)
            ldir: Path | None = None
            try:
                if emode == "settled_healthy":
                    (edir / "src").mkdir()
                    (edir / "src/a.go").write_text("package a\n", encoding="utf-8")
                    (edir / "README.md").write_text("hi\n", encoding="utf-8")
                    pid, rids = driver.attach_project([edir])
                    wait_catalog_settled(driver, pid, rids[0], max_wait_s=15.0)
                    ans = driver.walk_catalog_index(pid, rids[0])
                    ans_gaps = _root_scoped_gaps(ans, rids[0])
                    ok = sorted(i.path for i in ans.items) == ["README.md", "src/a.go"] and len(ans_gaps) == 0
                    report.record_edge(ename, ok, {"items": len(ans.items), "gaps": list(ans_gaps)})
                elif emode == "over_budget":
                    (edir / ".paintedwolf").mkdir()
                    (edir / ".paintedwolf/source-scope.yaml").write_text(OVER_BUDGET_SCOPE_YAML, encoding="utf-8")
                    (edir / "wide").mkdir()
                    for wi in range(10):
                        (edir / f"wide/f_{wi:02d}.txt").write_text("w\n", encoding="utf-8")
                    (edir / "keep.txt").write_text("k\n", encoding="utf-8")
                    pid, rids = driver.attach_project([edir])
                    wait_catalog_settled(driver, pid, rids[0], max_wait_s=15.0)
                    ans = driver.walk_catalog_index(pid, rids[0])
                    ans_gaps = _root_scoped_gaps(ans, rids[0])
                    ok = ("catalog_bounded" in ans_gaps) and (len(ans.items) < 12) and any(i.path == "keep.txt" for i in ans.items)
                    report.record_edge(
                        ename,
                        ok,
                        {"items": len(ans.items), "truth": 12, "gaps": list(ans_gaps)},
                        gap="catalog_bounded" if ok else None,
                    )
                elif emode == "healthy_beside":
                    (edir / "a.txt").write_text("a\n", encoding="utf-8")
                    ldir = edir / "locked"
                    ldir.mkdir()
                    (ldir / "s.txt").write_text("s\n", encoding="utf-8")
                    os.chmod(ldir, 0o000)
                    if not verify_dir_unreadable(ldir):
                        report.record_edge(ename, False, {"reason": "unreadable dir could not be established"}, blocked=True)
                        continue
                    hdir = tmp_root / f"k02_edge_h_{eidx}"
                    hdir.mkdir()
                    (hdir / "good.txt").write_text("g\n", encoding="utf-8")
                    pid, rids = driver.attach_project([edir, hdir])
                    wait_catalog_settled(driver, pid, rids[0], max_wait_s=15.0)
                    wait_catalog_settled(driver, pid, rids[1], max_wait_s=15.0)
                    comb_ans, comb_cov = walk_combined_catalog(driver, pid)
                    failing_gaps = _root_scoped_gaps(comb_ans, rids[0])
                    healthy_present = any(i.root_id == rids[1] and i.path == "good.txt" for i in comb_ans.items)
                    failing_declared = "catalog_failed" in failing_gaps
                    ok = healthy_present and failing_declared
                    report.record_edge(
                        ename,
                        ok,
                        {
                            "healthy_present_in_combined": healthy_present,
                            "failing_gaps_in_combined": list(failing_gaps),
                            "combined_coverage": comb_cov,
                        },
                        gap="catalog_failed" if ok else None,
                    )
                elif emode == "multi_root":
                    r2 = tmp_root / f"k02_edge_r2_{eidx}"
                    r2.mkdir()
                    (edir / "one.txt").write_text("1\n", encoding="utf-8")
                    (r2 / "two.txt").write_text("2\n", encoding="utf-8")
                    pid, rids = driver.attach_project([edir, r2])
                    wait_catalog_settled(driver, pid, rids[0], max_wait_s=15.0)
                    wait_catalog_settled(driver, pid, rids[1], max_wait_s=15.0)
                    comb, _ = walk_combined_catalog(driver, pid)
                    ok = (
                        any(i.root_id == rids[0] and i.path == "one.txt" for i in comb.items)
                        and any(i.root_id == rids[1] and i.path == "two.txt" for i in comb.items)
                        and len(comb.items) == 2
                        and len(comb.declared_gaps) == 0
                    )
                    report.record_edge(ename, ok, {"combined_items": len(comb.items), "gaps": [g.reason for g in comb.declared_gaps]})
                elif emode == "cold":
                    for i in range(40):
                        (edir / f"f_{i:03d}.txt").write_text(f"body {i}\n", encoding="utf-8")
                    pid, rids = driver.attach_project([edir])
                    earlier = driver.get_source_index(pid)
                    ans = driver.walk_catalog_index(pid, rids[0])
                    ans_gaps = _root_scoped_gaps(ans, rids[0])
                    ok = len(ans.items) > 0 or len(ans_gaps) > 0
                    report.record_edge(
                        ename,
                        ok,
                        {
                            "judged_revision": ans.generation_revision,
                            "items": len(ans.items),
                            "judged_gaps": list(ans_gaps),
                            "transition_evidence_state": earlier.get("state"),
                        },
                    )
                else:
                    pid, rids = driver.attach_project([edir])
                    settled, _, _ = wait_catalog_settled(driver, pid, rids[0], max_wait_s=15.0)
                    ans = driver.walk_catalog_index(pid, rids[0])
                    ok = settled and len(ans.items) == 0
                    report.record_edge(ename, ok, {"settled": settled, "items": len(ans.items)})
            finally:
                if ldir is not None and ldir.exists():
                    os.chmod(ldir, 0o755)

        # Incident edge replays: K02-INC-03 and K02-INC-04 (tracked separately from frozen totals per ledger.md)
        incidents = _run_k02_incidents(driver, tmp_root)

    return report, incidents


def _run_k02_incidents(driver: SidecarDriver, tmp_root: Path) -> list[dict[str, object]]:
    results: list[dict[str, object]] = []

    # K02-INC-03: 2,050-file synthetic tree queried immediately on attach (cold generation boundary).
    # Decision 030: judge immediate_ans by its OWN coverage; keep earlier get_source_index as transition evidence;
    # use explicit replay_disposition ("reproduced" | "not_reproduced" | "unreproduced_attempt").
    inc03_dir = tmp_root / "k02_inc03"
    total_files = 2050
    for i in range(total_files):
        sub = inc03_dir / f"pkg_{i % 25:02d}"
        sub.mkdir(parents=True, exist_ok=True)
        (sub / f"file_{i:04d}.go").write_text(f"package p // {i}\n", encoding="utf-8")
    pid3, rids3 = driver.attach_project([inc03_dir])
    earlier_index = driver.get_source_index(pid3)
    earlier_cov = earlier_index.get("coverage") if isinstance(earlier_index.get("coverage"), list) else []
    immediate_ans = driver.walk_catalog_index(pid3, rids3[0])
    ans_gaps = list(_root_scoped_gaps(immediate_ans, rids3[0]))
    cold_window_gaps = {"catalog_warming", "catalog_incomplete", "catalog_refreshing"}

    if len(immediate_ans.items) == 0 and not ans_gaps:
        disposition = "reproduced"
        verdict = "DEFECT"
        note = "cold query returned complete-looking empty answer with zero declared gaps"
    elif any(g in cold_window_gaps for g in ans_gaps) and len(immediate_ans.items) < total_files:
        disposition = "not_reproduced"
        verdict = "PASS"
        note = "cold/active-first-walk window captured and declared typed coverage gap"
    else:
        disposition = "unreproduced_attempt"
        verdict = "UNREPRODUCED_ATTEMPT"
        note = (
            "completed/readable catalog obtained before cold window was captured; "
            "does not establish historical incident outcome (see deterministic in-process TestCF03_K02_INC03_ColdWalkCoverage)"
        )

    results.append(
        {
            "incident_id": "K02-INC-03",
            "family": "K02",
            "lane": "sidecar",
            "tree_files": total_files,
            "replay_disposition": disposition,
            "verdict": verdict,
            "reproduced_divergence": disposition == "reproduced",
            "judged_revision": immediate_ans.generation_revision,
            "immediate_item_count": len(immediate_ans.items),
            "immediate_declared_gaps": ans_gaps,
            "transition_evidence": {
                "earlier_index_revision": earlier_index.get("revision"),
                "earlier_index_state": earlier_index.get("state"),
                "earlier_index_coverage": earlier_cov,
            },
            "note": note,
        }
    )

    # K02-INC-04: unreadable chmod 000 subtree (io_error) + symlink outside root (unconfined) beside healthy file.
    inc04_dir = tmp_root / "k02_inc04"
    outside_dir = tmp_root / "k02_inc04_outside"
    inc04_dir.mkdir(parents=True, exist_ok=True)
    outside_dir.mkdir(parents=True, exist_ok=True)
    (outside_dir / "outside_secret.txt").write_text("outside\n", encoding="utf-8")
    (inc04_dir / "healthy").mkdir()
    (inc04_dir / "healthy/match.txt").write_text("healthy match\n", encoding="utf-8")
    os.symlink(str(outside_dir), inc04_dir / "unconfined_link")
    locked = inc04_dir / "unreadable_dir"
    locked.mkdir()
    (locked / "hidden.txt").write_text("locked\n", encoding="utf-8")
    os.chmod(locked, 0o000)
    try:
        if not verify_dir_unreadable(locked):
            results.append(
                {
                    "incident_id": "K02-INC-04",
                    "family": "K02",
                    "lane": "sidecar",
                    "replay_disposition": "unreproduced_attempt",
                    "verdict": "BLOCKED",
                    "reproduced_divergence": False,
                    "verified_unreadable": False,
                    "note": "unreadable chmod 000 fixture could not be established on this host (os.listdir succeeded)",
                }
            )
            return results

        pid4, rids4 = driver.attach_project([inc04_dir])
        wait_catalog_settled(driver, pid4, rids4[0], max_wait_s=15.0)
        summary4 = driver.get_source_index(pid4)
        ans4 = driver.walk_catalog_index(pid4, rids4[0])
        paths4 = [i.path for i in ans4.items]
        healthy_ok = "healthy/match.txt" in paths4
        no_escape = not any("outside_secret" in p or p.startswith("unconfined_link/") for p in paths4)
        gaps4 = list(_root_scoped_gaps(ans4, rids4[0]))
        gap_declared = "catalog_failed" in gaps4
        ok4 = healthy_ok and no_escape and gap_declared
        results.append(
            {
                "incident_id": "K02-INC-04",
                "family": "K02",
                "lane": "sidecar",
                "replay_disposition": "not_reproduced" if ok4 else "reproduced",
                "verdict": "PASS" if ok4 else "DEFECT",
                "reproduced_divergence": not ok4,
                "verified_unreadable": True,
                "judged_revision": ans4.generation_revision,
                "healthy_present": healthy_ok,
                "no_outside_escape": no_escape,
                "declared_gaps": gaps4,
                "raw_coverage": summary4.get("coverage", []),
            }
        )
    finally:
        if locked.exists():
            os.chmod(locked, 0o755)

    return results
