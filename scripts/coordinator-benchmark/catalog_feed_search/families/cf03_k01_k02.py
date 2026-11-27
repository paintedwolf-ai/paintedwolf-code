"""CF03 sidecar family runners for K01 (Membership) and K02 (Coverage truth + INC-03/04)."""

from __future__ import annotations

import os
from pathlib import Path
import shutil
import tempfile

from ..checker import ProductCatalogAnswer, check_catalog, walk_entries_to_catalog_items
from ..checker_types import CheckResult, Verdict
from ..driver_adapters import map_source_index_coverage
from ..driver_sidecar import SidecarDriver
from ..generators import BoundaryConfig, TreeEntrySpec, TreeGenOptions, TreeSpec, generate_tree_spec, materialize_tree
from ..live_controls import wait_catalog_settled
from ..references import reference_walk
from ..seeds import BASE_SEED_PRIMARY, ReplaySelector, seed_rng
from .cf03_common import FamilyReport, evaluate_tree_against_sidecar, shrink_catalog_failure

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
            eres, _eans, emeta = evaluate_tree_against_sidecar(driver, espec, edir, boundary_config)
            report.record_edge(
                ename,
                eres.verdict in (Verdict.PASS, Verdict.PASS_DECLARED_GAP),
                {
                    "verdict": eres.verdict.value,
                    "reason": eres.reason,
                    "reference_count": emeta["reference_count"],
                    "product_count": emeta["product_count"],
                },
            )
            shutil.rmtree(edir, ignore_errors=True)

    return report


def run_k02_sidecar(
    driver: SidecarDriver,
    boundary_config: BoundaryConfig,
    out_dir: Path,
    *,
    base_seed: str = BASE_SEED_PRIMARY,
    count: int = K02_SIDECAR_COUNT,
) -> tuple[FamilyReport, list[dict[str, object]]]:
    """Run K02 Coverage truth: 60 sidecar instances + 6 edges + K02-INC-03 & K02-INC-04."""
    report = FamilyReport(
        family="K02",
        lane="sidecar",
        base_seed=base_seed,
        planned=count,
        edge_planned=K02_EDGE_COUNT,
    )
    is_root = hasattr(os, "geteuid") and os.geteuid() == 0
    states = ("settled_healthy", "unreadable_dir", "healthy_beside_unreadable", "cold_query", "multi_root_healthy", "unavailable_subdir")

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
            try:
                if state in ("unreadable_dir", "healthy_beside_unreadable") and not is_root:
                    locked_dir = wdir / "locked_subtree"
                    locked_dir.mkdir(exist_ok=True)
                    (locked_dir / "secret.txt").write_text("locked\n", encoding="utf-8")
                    os.chmod(locked_dir, 0o000)

                roots = [wdir]
                healthy_sibling: Path | None = None
                if state in ("healthy_beside_unreadable", "multi_root_healthy"):
                    healthy_sibling = tmp_root / f"k02_sib_{idx:03d}"
                    materialize_tree(
                        TreeSpec(
                            selector=ReplaySelector(base_seed, "K02-SIB", idx),
                            entries=(TreeEntrySpec("sib/ok.txt", "file", b"healthy sibling\n"),),
                        ),
                        healthy_sibling,
                    )
                    roots.append(healthy_sibling)

                pid, rids = driver.attach_project(roots)
                primary_rid = rids[0]

                if state == "cold_query":
                    # Immediate query before waiting for settlement: must NOT look complete & empty.
                    idx_sum = driver.get_source_index(pid)
                    ans = driver.walk_catalog_index(pid, primary_rid)
                    gaps = map_source_index_coverage(idx_sum.get("coverage") if isinstance(idx_sum.get("coverage"), list) else [])
                    complete_empty = (len(ans.items) == 0) and (len(gaps) == 0) and (len(ans.declared_gaps) == 0)
                    if complete_empty:
                        res = CheckResult(Verdict.DEFECT, "catalog", reason="cold query returned complete-looking empty answer")
                    elif len(ans.items) == 0:
                        res = CheckResult(
                            Verdict.PASS_DECLARED_GAP,
                            "catalog",
                            accounted_gaps=tuple(dict.fromkeys(g.reason for g in (gaps or ans.declared_gaps))),
                            declared_gaps=tuple(dict.fromkeys(g.reason for g in (gaps or ans.declared_gaps))),
                            reason="cold query declared warming/incomplete coverage",
                        )
                    else:
                        # Settled fast enough to already return real entries.
                        ref_entries = [e for e in reference_walk(wdir, boundary_config=boundary_config, include_hidden=True) if e.kind == "file"]
                        res = check_catalog(walk_entries_to_catalog_items(ref_entries, root_id=primary_rid), ans)
                    report.record_generated(res, extra={"state": state, "instance": idx})
                else:
                    wait_catalog_settled(driver, pid, primary_rid, max_wait_s=15.0)
                    ans = driver.walk_catalog_index(pid, primary_rid)
                    if locked_dir is not None:
                        os.chmod(locked_dir, 0o755)
                        locked_dir = None
                    ref_entries = [e for e in reference_walk(wdir, boundary_config=boundary_config, include_hidden=True) if e.kind == "file"]
                    res = check_catalog(walk_entries_to_catalog_items(ref_entries, root_id=primary_rid), ans)
                    # Also verify healthy sibling still answers when present.
                    if healthy_sibling is not None and len(rids) > 1:
                        wait_catalog_settled(driver, pid, rids[1], max_wait_s=15.0)
                        sib_ans = driver.walk_catalog_index(pid, rids[1])
                        if not any(i.path == "sib/ok.txt" for i in sib_ans.items):
                            res = CheckResult(Verdict.DEFECT, "catalog", reason="healthy sibling suppressed beside failing root")
                    report.record_generated(res, extra={"state": state, "instance": idx})
            finally:
                if locked_dir is not None and locked_dir.exists():
                    os.chmod(locked_dir, 0o755)
                shutil.rmtree(wdir, ignore_errors=True)

        # 6 hand-written K02 edges
        k02_edges = [
            ("k02_edge_1_cold_no_complete_empty", "cold"),
            ("k02_edge_2_unreadable_dir_declares_failed", "unreadable"),
            ("k02_edge_3_healthy_root_beside_unreadable", "healthy_beside"),
            ("k02_edge_4_boundary_dir_does_not_claim_unwalked_files", "boundary"),
            ("k02_edge_5_multi_root_independent_coverage", "multi_root"),
            ("k02_edge_6_empty_root_settled_honest", "empty_root"),
        ]
        for eidx, (ename, emode) in enumerate(k02_edges):
            edir = tmp_root / f"k02_edge_{eidx}"
            edir.mkdir(parents=True, exist_ok=True)
            ldir: Path | None = None
            try:
                if emode == "cold":
                    for i in range(40):
                        (edir / f"f_{i:03d}.txt").write_text(f"body {i}\n", encoding="utf-8")
                    pid, rids = driver.attach_project([edir])
                    ans = driver.walk_catalog_index(pid, rids[0])
                    ok = len(ans.items) > 0 or len(ans.declared_gaps) > 0
                    report.record_edge(ename, ok, {"items": len(ans.items), "gaps": [g.reason for g in ans.declared_gaps]})
                elif emode == "unreadable" and not is_root:
                    (edir / "ok.txt").write_text("ok\n", encoding="utf-8")
                    ldir = edir / "locked"
                    ldir.mkdir()
                    (ldir / "secret.txt").write_text("s\n", encoding="utf-8")
                    os.chmod(ldir, 0o000)
                    pid, rids = driver.attach_project([edir])
                    wait_catalog_settled(driver, pid, rids[0], max_wait_s=15.0)
                    ans = driver.walk_catalog_index(pid, rids[0])
                    ok = any(i.path == "ok.txt" for i in ans.items) and any(g.reason == "catalog_failed" for g in ans.declared_gaps)
                    report.record_edge(ename, ok, {"gaps": [g.reason for g in ans.declared_gaps]}, gap="catalog_failed" if ok else None)
                elif emode == "healthy_beside" and not is_root:
                    (edir / "a.txt").write_text("a\n", encoding="utf-8")
                    ldir = edir / "locked"
                    ldir.mkdir()
                    (ldir / "s.txt").write_text("s\n", encoding="utf-8")
                    os.chmod(ldir, 0o000)
                    hdir = tmp_root / f"k02_edge_h_{eidx}"
                    hdir.mkdir()
                    (hdir / "good.txt").write_text("g\n", encoding="utf-8")
                    pid, rids = driver.attach_project([edir, hdir])
                    wait_catalog_settled(driver, pid, rids[1], max_wait_s=15.0)
                    hans = driver.walk_catalog_index(pid, rids[1])
                    ok = any(i.path == "good.txt" for i in hans.items)
                    report.record_edge(ename, ok, {"healthy_items": len(hans.items)})
                elif emode == "boundary":
                    (edir / "src").mkdir()
                    (edir / "src/a.go").write_text("package a\n", encoding="utf-8")
                    (edir / "node_modules/pkg").mkdir(parents=True)
                    (edir / "node_modules/pkg/b.js").write_text("x\n", encoding="utf-8")
                    pid, rids = driver.attach_project([edir])
                    wait_catalog_settled(driver, pid, rids[0], max_wait_s=15.0)
                    ans = driver.walk_catalog_index(pid, rids[0])
                    paths = [i.path for i in ans.items]
                    ok = ("src/a.go" in paths) and ("node_modules/pkg/b.js" not in paths)
                    report.record_edge(ename, ok, {"paths": paths})
                elif emode == "multi_root":
                    r2 = tmp_root / f"k02_edge_r2_{eidx}"
                    r2.mkdir()
                    (edir / "one.txt").write_text("1\n", encoding="utf-8")
                    (r2 / "two.txt").write_text("2\n", encoding="utf-8")
                    pid, rids = driver.attach_project([edir, r2])
                    wait_catalog_settled(driver, pid, rids[0], max_wait_s=15.0)
                    wait_catalog_settled(driver, pid, rids[1], max_wait_s=15.0)
                    a1 = driver.walk_catalog_index(pid, rids[0])
                    a2 = driver.walk_catalog_index(pid, rids[1])
                    ok = ([i.path for i in a1.items] == ["one.txt"]) and ([i.path for i in a2.items] == ["two.txt"])
                    report.record_edge(ename, ok, {"r1": len(a1.items), "r2": len(a2.items)})
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
        incidents = _run_k02_incidents(driver, tmp_root, is_root=is_root)

    return report, incidents


def _run_k02_incidents(driver: SidecarDriver, tmp_root: Path, *, is_root: bool) -> list[dict[str, object]]:
    results: list[dict[str, object]] = []

    # K02-INC-03: 2,000+ entry synthetic tree queried immediately on attach (cold generation boundary).
    inc03_dir = tmp_root / "k02_inc03"
    for i in range(2050):
        sub = inc03_dir / f"pkg_{i % 25:02d}"
        sub.mkdir(parents=True, exist_ok=True)
        (sub / f"file_{i:04d}.go").write_text(f"package p // {i}\n", encoding="utf-8")
    pid3, rids3 = driver.attach_project([inc03_dir])
    immediate_summary = driver.get_source_index(pid3)
    immediate_ans = driver.walk_catalog_index(pid3, rids3[0])
    cov_list = immediate_summary.get("coverage") if isinstance(immediate_summary.get("coverage"), list) else []
    mapped_gaps = [g.reason for g in map_source_index_coverage(cov_list)]
    complete_empty_bug = (len(immediate_ans.items) == 0) and (len(mapped_gaps) == 0) and (len(immediate_ans.declared_gaps) == 0)
    results.append(
        {
            "incident_id": "K02-INC-03",
            "family": "K02",
            "lane": "sidecar",
            "tree_files": 2050,
            "verdict": "DEFECT" if complete_empty_bug else "PASS",
            "reproduced_divergence": complete_empty_bug,
            "immediate_item_count": len(immediate_ans.items),
            "immediate_declared_gaps": sorted(set(mapped_gaps + [g.reason for g in immediate_ans.declared_gaps])),
            "raw_coverage": cov_list,
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
    if not is_root:
        os.chmod(locked, 0o000)
    try:
        pid4, rids4 = driver.attach_project([inc04_dir])
        wait_catalog_settled(driver, pid4, rids4[0], max_wait_s=15.0)
        summary4 = driver.get_source_index(pid4)
        ans4 = driver.walk_catalog_index(pid4, rids4[0])
        paths4 = [i.path for i in ans4.items]
        healthy_ok = "healthy/match.txt" in paths4
        no_escape = not any("outside_secret" in p or p.startswith("unconfined_link/") for p in paths4)
        gaps4 = sorted({g.reason for g in ans4.declared_gaps})
        gap_declared = is_root or ("catalog_failed" in gaps4)
        ok4 = healthy_ok and no_escape and gap_declared
        results.append(
            {
                "incident_id": "K02-INC-04",
                "family": "K02",
                "lane": "sidecar",
                "verdict": "PASS" if ok4 else "DEFECT",
                "reproduced_divergence": not ok4,
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
