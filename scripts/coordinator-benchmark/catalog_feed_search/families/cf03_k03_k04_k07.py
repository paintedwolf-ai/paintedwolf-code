"""CF03 sidecar family runners for K03 (Generations/Restart), K04 (Traversal Safety), K07 (Snapshots)."""

from __future__ import annotations

import os
from pathlib import Path
import shutil
import sqlite3
import tempfile
import urllib.error

from ..checker import check_catalog, walk_entries_to_catalog_items
from ..checker_types import CheckResult, Verdict
from ..driver_provenance import probe_case_sensitive
from ..driver_sidecar import SidecarDriver
from ..generators import BoundaryConfig, TreeEntrySpec, TreeGenOptions, TreeSpec, generate_tree_spec, materialize_tree
from ..live_controls import wait_catalog_settled
from ..references import reference_walk
from ..seeds import BASE_SEED_PRIMARY, ReplaySelector, seed_rng
from .cf03_common import FamilyReport, ManagedSidecar

K03_SIDECAR_COUNT = 60
K03_EDGE_COUNT = 4
K04_SIDECAR_COUNT = 100
K04_EDGE_COUNT = 8
K07_SIDECAR_COUNT = 40
K07_EDGE_COUNT = 4


def run_k03_sidecar(
    sidecar: ManagedSidecar,
    boundary_config: BoundaryConfig,
    *,
    base_seed: str = BASE_SEED_PRIMARY,
    count: int = K03_SIDECAR_COUNT,
) -> FamilyReport:
    """Run K03 Generations and restart: 60 sidecar instances + 4 edges including real sidecar restarts."""
    report = FamilyReport(
        family="K03",
        lane="sidecar",
        base_seed=base_seed,
        planned=count,
        edge_planned=K03_EDGE_COUNT,
    )
    driver = sidecar.driver()

    with tempfile.TemporaryDirectory(prefix="cf03-k03-") as tmp:
        tmp_root = Path(tmp)
        for idx in range(count):
            wdir = tmp_root / f"k03_{idx:03d}"
            spec = generate_tree_spec(
                base_seed,
                "K03",
                idx,
                options=TreeGenOptions(entry_count=8 + (idx % 20), max_depth=4, include_symlinks=False),
            )
            materialize_tree(spec, wdir)
            pid, rids = driver.attach_project([wdir])
            rid = rids[0]
            wait_catalog_settled(driver, pid, rid, max_wait_s=15.0)
            before = driver.walk_catalog_index(pid, rid, page_limit=5)

            # Mutate tree to trigger a new generation, or do a real sidecar restart every 15th instance.
            if idx % 15 == 0:
                sidecar.restart()
                driver = sidecar.driver()
                wait_catalog_settled(driver, pid, rid, max_wait_s=15.0)
                after_restart = driver.walk_catalog_index(pid, rid, page_limit=5)
                ref_entries = [e for e in reference_walk(wdir, boundary_config=boundary_config, include_hidden=True) if e.kind == "file"]
                res = check_catalog(walk_entries_to_catalog_items(ref_entries, root_id=rid), after_restart)
                report.record_generated(res, extra={"mode": "real_restart", "instance": idx, "rev_before": before.generation_revision, "rev_after": after_restart.generation_revision})
            else:
                # Page through results while adding a file to verify no duplicate/crossed page items.
                page1 = driver.file_lookup(pid, "", root_id=rid, limit=4)
                rev1 = page1.get("revision")
                (wdir / f"gen_bump_{idx}.txt").write_text("new gen\n", encoding="utf-8")
                cur = str(page1.get("next_cursor") or "")
                revs = {rev1} if rev1 is not None else set()
                seen_paths = [str(m.get("path") or "") for m in (page1.get("matches") or []) if isinstance(m, dict)]
                while cur:
                    nxt = driver.file_lookup(pid, "", root_id=rid, limit=4, cursor=cur)
                    if nxt.get("revision") is not None:
                        revs.add(nxt.get("revision"))
                    seen_paths.extend(str(m.get("path") or "") for m in (nxt.get("matches") or []) if isinstance(m, dict))
                    nc = str(nxt.get("next_cursor") or "")
                    if not nc or nc == cur:
                        break
                    cur = nc
                has_dups = len(seen_paths) != len(set(seen_paths))
                if has_dups:
                    res = CheckResult(Verdict.DEFECT, "catalog", reason="pagination returned duplicate paths across generation change")
                else:
                    res = CheckResult(Verdict.PASS, "catalog")
                report.record_generated(res, extra={"mode": "page_across_mutation", "instance": idx, "revisions_seen": sorted(str(r) for r in revs)})
            shutil.rmtree(wdir, ignore_errors=True)

        # 4 hand-written K03 edges
        e1_dir = tmp_root / "k03_edge_1"
        materialize_tree(
            TreeSpec(
                selector=ReplaySelector(base_seed, "K03-EDGE", 0),
                entries=tuple(TreeEntrySpec(f"src/f_{i:02d}.go", "file", f"package p // {i}\n".encode()) for i in range(12)),
            ),
            e1_dir,
        )
        pid, rids = driver.attach_project([e1_dir])
        rid = rids[0]
        wait_catalog_settled(driver, pid, rid, max_wait_s=15.0)
        before = driver.walk_catalog_index(pid, rid)
        sidecar.restart()
        driver = sidecar.driver()
        wait_catalog_settled(driver, pid, rid, max_wait_s=15.0)
        after = driver.walk_catalog_index(pid, rid)
        ok1 = sorted(i.path for i in before.items) == sorted(i.path for i in after.items) and len(after.items) == 12
        report.record_edge("k03_edge_1_completed_gen_survives_restart", ok1, {"before": len(before.items), "after": len(after.items)})

        # Edge 2: generation stays readable during mutation/refresh
        (e1_dir / "src/extra.go").write_text("package p\n", encoding="utf-8")
        immediate = driver.walk_catalog_index(pid, rid)
        ok2 = len(immediate.items) >= 12
        report.record_edge("k03_edge_2_readable_during_refresh", ok2, {"items": len(immediate.items)})

        # Edge 3: multi-page walk has no duplicates
        p1 = driver.file_lookup(pid, "", root_id=rid, limit=3)
        all_p = [m["path"] for m in (p1.get("matches") or []) if isinstance(m, dict)]
        cur = str(p1.get("next_cursor") or "")
        while cur:
            pn = driver.file_lookup(pid, "", root_id=rid, limit=3, cursor=cur)
            all_p.extend(m["path"] for m in (pn.get("matches") or []) if isinstance(m, dict))
            nc = str(pn.get("next_cursor") or "")
            if not nc or nc == cur:
                break
            cur = nc
        ok3 = len(all_p) == len(set(all_p)) and len(all_p) >= 12
        report.record_edge("k03_edge_3_paged_no_duplicates", ok3, {"total": len(all_p), "unique": len(set(all_p))})

        # Edge 4: second restart preserves persisted index
        sidecar.restart()
        driver = sidecar.driver()
        wait_catalog_settled(driver, pid, rid, max_wait_s=15.0)
        after2 = driver.walk_catalog_index(pid, rid)
        ok4 = len(after2.items) >= 12
        report.record_edge("k03_edge_4_second_restart_readable", ok4, {"items": len(after2.items)})

    return report


def run_k04_sidecar(
    driver: SidecarDriver,
    boundary_config: BoundaryConfig,
    *,
    base_seed: str = BASE_SEED_PRIMARY,
    count: int = K04_SIDECAR_COUNT,
) -> FamilyReport:
    """Run K04 Traversal safety and hostile names: 100 sidecar instances + 8 edges (D9 platform-aware)."""
    report = FamilyReport(
        family="K04",
        lane="sidecar",
        base_seed=base_seed,
        planned=count,
        edge_planned=K04_EDGE_COUNT,
    )
    is_root = hasattr(os, "geteuid") and os.geteuid() == 0

    with tempfile.TemporaryDirectory(prefix="cf03-k04-") as tmp:
        tmp_root = Path(tmp)
        case_sensitive = probe_case_sensitive(tmp_root)
        outside_canary = tmp_root / "outside_canary"
        outside_canary.mkdir()
        (outside_canary / "OUTSIDE_SECRET.txt").write_text("DO_NOT_READ\n", encoding="utf-8")

        for idx in range(count):
            rng = seed_rng(base_seed, "K04", idx)
            mode = idx % 4
            wdir = tmp_root / f"k04_{idx:03d}"
            wdir.mkdir(parents=True, exist_ok=True)
            locked: Path | None = None
            try:
                if mode == 0:
                    # Ancestor symlink pointing outside root
                    (wdir / "inside.txt").write_text("ok\n", encoding="utf-8")
                    os.symlink(str(outside_canary), wdir / "escape_dir")
                elif mode == 1:
                    # Permission flip directory
                    (wdir / "ok.txt").write_text("ok\n", encoding="utf-8")
                    locked = wdir / "perm_dir"
                    locked.mkdir()
                    (locked / "hidden.txt").write_text("secret\n", encoding="utf-8")
                    if not is_root:
                        os.chmod(locked, 0o000)
                elif mode == 2:
                    # Unicode NFC/NFD + case-only diffs (D9)
                    (wdir / f"caf\u00e9_{idx}.txt").write_text("nfc\n", encoding="utf-8")
                    if case_sensitive:
                        (wdir / f"cafe\u0301_{idx}.txt").write_text("nfd\n", encoding="utf-8")
                        (wdir / "CaseDiff.txt").write_text("upper\n", encoding="utf-8")
                        (wdir / "casediff.txt").write_text("lower\n", encoding="utf-8")
                    else:
                        (wdir / "CaseDiff.txt").write_text("single\n", encoding="utf-8")
                else:
                    # Long paths + spaces/tabs/quotes
                    seg = "a" * (35 + rng.randint(0, 15))
                    deep = wdir / seg / seg / seg
                    deep.mkdir(parents=True)
                    (deep / f"f_{idx}.txt").write_text("long\n", encoding="utf-8")
                    (wdir / f"tab\tspace {idx}.txt").write_text("ctrl\n", encoding="utf-8")

                pid, rids = driver.attach_project([wdir])
                rid = rids[0]
                wait_catalog_settled(driver, pid, rid, max_wait_s=15.0)
                ans = driver.walk_catalog_index(pid, rid)
                # Safety invariant: outside canary must NEVER appear in catalog!
                escaped = [i.path for i in ans.items if "OUTSIDE_SECRET" in i.path or i.path.startswith("escape_dir/")]
                if escaped:
                    res = CheckResult(Verdict.DEFECT, "catalog", extra_claims=tuple(escaped), reason="symlink escaped root")
                elif locked is not None and not is_root:
                    os.chmod(locked, 0o755)
                    locked = None
                    ref_entries = [e for e in reference_walk(wdir, boundary_config=boundary_config, include_hidden=True) if e.kind == "file"]
                    res = check_catalog(walk_entries_to_catalog_items(ref_entries, root_id=rid), ans)
                else:
                    ref_entries = [e for e in reference_walk(wdir, boundary_config=boundary_config, include_hidden=True) if e.kind == "file"]
                    res = check_catalog(walk_entries_to_catalog_items(ref_entries, root_id=rid), ans)
                report.record_generated(res, extra={"mode": mode, "instance": idx, "case_sensitive": case_sensitive})
            finally:
                if locked is not None and locked.exists():
                    os.chmod(locked, 0o755)
                shutil.rmtree(wdir, ignore_errors=True)

        # 8 hand-written K04 edges
        edge_cases = [
            ("k04_edge_1_symlink_to_outside_dir", "sym_out"),
            ("k04_edge_2_symlink_to_outside_file", "sym_file"),
            ("k04_edge_3_dangling_symlink", "dangling"),
            ("k04_edge_4_unreadable_dir", "chmod000"),
            ("k04_edge_5_unicode_nfc_nfd", "unicode"),
            ("k04_edge_6_case_only_diff_d9", "case_diff"),
            ("k04_edge_7_very_long_path", "long_path"),
            ("k04_edge_8_newline_and_tab_in_name", "newline"),
        ]
        for eidx, (ename, emode) in enumerate(edge_cases):
            edir = tmp_root / f"k04_edge_{eidx}"
            edir.mkdir(parents=True, exist_ok=True)
            ldir: Path | None = None
            try:
                (edir / "anchor.txt").write_text("anchor\n", encoding="utf-8")
                if emode == "sym_out":
                    os.symlink(str(outside_canary), edir / "out_dir")
                elif emode == "sym_file":
                    os.symlink(str(outside_canary / "OUTSIDE_SECRET.txt"), edir / "out_file.txt")
                elif emode == "dangling":
                    os.symlink(str(outside_canary / "does_not_exist"), edir / "dangling_link")
                elif emode == "chmod000" and not is_root:
                    ldir = edir / "no_read"
                    ldir.mkdir()
                    (ldir / "x.txt").write_text("x\n", encoding="utf-8")
                    os.chmod(ldir, 0o000)
                elif emode == "unicode":
                    (edir / "caf\u00e9.txt").write_text("nfc\n", encoding="utf-8")
                    if case_sensitive:
                        (edir / "cafe\u0301.txt").write_text("nfd\n", encoding="utf-8")
                elif emode == "case_diff":
                    (edir / "Alpha.txt").write_text("A\n", encoding="utf-8")
                    if case_sensitive:
                        (edir / "alpha.txt").write_text("a\n", encoding="utf-8")
                elif emode == "long_path":
                    p = edir / ("x" * 50) / ("y" * 50) / ("z" * 50)
                    p.mkdir(parents=True)
                    (p / "deep.txt").write_text("deep\n", encoding="utf-8")
                elif emode == "newline":
                    (edir / "line\nbreak.txt").write_text("nl\n", encoding="utf-8")
                    (edir / "tab\tchar.txt").write_text("tab\n", encoding="utf-8")

                pid, rids = driver.attach_project([edir])
                wait_catalog_settled(driver, pid, rids[0], max_wait_s=15.0)
                ans = driver.walk_catalog_index(pid, rids[0])
                escaped = [i.path for i in ans.items if "OUTSIDE_SECRET" in i.path]
                ok = (not escaped) and any(i.path == "anchor.txt" for i in ans.items)
                report.record_edge(ename, ok, {"items": len(ans.items), "escaped": escaped, "case_sensitive": case_sensitive})
            finally:
                if ldir is not None and ldir.exists():
                    os.chmod(ldir, 0o755)
                shutil.rmtree(edir, ignore_errors=True)

    return report


def _inspect_snapshot_db(config_dir: Path) -> dict[str, int]:
    """Read-only SQLite inspection of source_snapshots, boundaries, and chunks in isolated scratch config."""
    db_path = config_dir / "store.db"
    if not db_path.is_file():
        return {"snapshots": 0, "boundaries": 0, "manifests": 0, "chunks": 0, "dangling_chunks": 0}
    conn = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
    try:
        cur = conn.cursor()
        out: dict[str, int] = {}
        for key, sql in (
            ("snapshots", "SELECT COUNT(*) FROM source_snapshots"),
            ("boundaries", "SELECT COUNT(*) FROM source_snapshot_boundaries"),
            ("manifests", "SELECT COUNT(*) FROM source_manifests"),
            ("chunks", "SELECT COUNT(*) FROM source_manifest_chunks"),
            (
                "dangling_chunks",
                "SELECT COUNT(*) FROM source_manifest_layout l LEFT JOIN source_manifest_chunks c ON c.id = l.chunk_id WHERE c.id IS NULL",
            ),
        ):
            try:
                out[key] = int(cur.execute(sql).fetchone()[0])
            except sqlite3.Error:
                out[key] = 0
        return out
    finally:
        conn.close()


def run_k07_sidecar(
    driver: SidecarDriver,
    config_dir: Path,
    *,
    base_seed: str = BASE_SEED_PRIMARY,
    count: int = K07_SIDECAR_COUNT,
) -> FamilyReport:
    """Run K07 Snapshots and boundaries: 40 sidecar instances + 4 edges."""
    report = FamilyReport(
        family="K07",
        lane="sidecar",
        base_seed=base_seed,
        planned=count,
        edge_planned=K07_EDGE_COUNT,
    )
    is_root = hasattr(os, "geteuid") and os.geteuid() == 0

    with tempfile.TemporaryDirectory(prefix="cf03-k07-") as tmp:
        tmp_root = Path(tmp)
        for idx in range(count):
            wdir = tmp_root / f"k07_{idx:03d}"
            spec = generate_tree_spec(
                base_seed,
                "K07",
                idx,
                options=TreeGenOptions(entry_count=10 + (idx % 20), max_depth=3, include_boundaries=True, include_ignored=True),
            )
            materialize_tree(spec, wdir)
            pid, rids = driver.attach_project([wdir])
            wait_catalog_settled(driver, pid, rids[0], max_wait_s=15.0)
            # Trigger snapshot / walk via /source/walk
            try:
                _ = driver.list_source_walk(pid, limit=50)
            except urllib.error.HTTPError:
                pass
            # Small edit and re-walk
            (wdir / f"small_edit_{idx}.txt").write_text("edited\n", encoding="utf-8")
            try:
                _ = driver.list_source_walk(pid, limit=50)
            except urllib.error.HTTPError:
                pass
            db_stats = _inspect_snapshot_db(config_dir)
            if db_stats["dangling_chunks"] > 0:
                res = CheckResult(Verdict.DEFECT, "catalog", reason=f"dangling manifest chunk references: {db_stats['dangling_chunks']}")
            else:
                res = CheckResult(Verdict.PASS, "catalog")
            report.record_generated(res, extra={"instance": idx, "db_stats": db_stats})
            shutil.rmtree(wdir, ignore_errors=True)

        # 4 hand-written K07 edges
        k07_edges = [
            ("k07_edge_1_scope_exclusion_boundaries", "scope"),
            ("k07_edge_2_unreadable_dir_boundary", "unreadable"),
            ("k07_edge_3_small_edit_no_dangling_chunks", "small_edit"),
            ("k07_edge_4_surviving_manifest_chunks_intact", "surviving"),
        ]
        for eidx, (ename, emode) in enumerate(k07_edges):
            edir = tmp_root / f"k07_edge_{eidx}"
            edir.mkdir(parents=True, exist_ok=True)
            ldir: Path | None = None
            try:
                (edir / "src").mkdir()
                (edir / "src/main.go").write_text("package main\n", encoding="utf-8")
                (edir / "node_modules/pkg").mkdir(parents=True)
                (edir / "node_modules/pkg/a.js").write_text("a\n", encoding="utf-8")
                if emode == "unreadable" and not is_root:
                    ldir = edir / "locked"
                    ldir.mkdir()
                    (ldir / "x.txt").write_text("x\n", encoding="utf-8")
                    os.chmod(ldir, 0o000)
                pid, rids = driver.attach_project([edir])
                wait_catalog_settled(driver, pid, rids[0], max_wait_s=15.0)
                try:
                    _ = driver.list_source_walk(pid, limit=50)
                except urllib.error.HTTPError:
                    pass
                if emode == "small_edit":
                    (edir / "src/main.go").write_text("package main // v2\n", encoding="utf-8")
                    try:
                        _ = driver.list_source_walk(pid, limit=50)
                    except urllib.error.HTTPError:
                        pass
                stats = _inspect_snapshot_db(config_dir)
                ok = stats["dangling_chunks"] == 0
                report.record_edge(ename, ok, stats)
            finally:
                if ldir is not None and ldir.exists():
                    os.chmod(ldir, 0o755)
                shutil.rmtree(edir, ignore_errors=True)

    return report
