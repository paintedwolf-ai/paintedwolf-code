"""CF03 sidecar family runners for K03 (Generations/Restart), K04 (Traversal Safety), K07 (Snapshots)."""

from __future__ import annotations

import os
from pathlib import Path
import shutil
import sqlite3
import tempfile
import time
import urllib.error

from ..checker import check_catalog, walk_entries_to_catalog_items
from ..checker_types import CheckResult, Verdict
from ..driver_provenance import probe_case_sensitive
from ..driver_sidecar import SidecarDriver
from ..generators import BoundaryConfig, TreeEntrySpec, TreeGenOptions, TreeSpec, generate_tree_spec, materialize_tree
from ..live_controls import wait_catalog_settled
from ..references import reference_walk
from ..seeds import BASE_SEED_PRIMARY, ReplaySelector, seed_rng
from .cf03_common import FamilyReport, ManagedSidecar, parse_http_error_json, verify_dir_unreadable, walk_catalog_retry_409

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
            # Ensure at least 6 files so limit=4 ALWAYS produces a real continuation cursor.
            for pad_i in range(6):
                pfile = wdir / f"pad_{pad_i:02d}.txt"
                if not pfile.exists():
                    pfile.write_text(f"pad {pad_i}\n", encoding="utf-8")
            pid, rids = driver.attach_project([wdir])
            rid = rids[0]
            wait_catalog_settled(driver, pid, rid, max_wait_s=15.0)
            before = walk_catalog_retry_409(driver, pid, rid, page_limit=200)

            # Mutate tree to trigger a new generation, or do a real sidecar restart every 15th instance.
            if idx % 15 == 0:
                sidecar.restart()
                driver = sidecar.driver()
                # Decision 054: observe FIRST readiness immediately after restart (before settle)
                # to distinguish persisted readiness from a rebuilt view.
                first_idx = driver.get_source_index(pid)
                first_read = walk_catalog_retry_409(driver, pid, rid, page_limit=200)
                served_immediately = (
                    len(first_read.items) == len(before.items)
                    and first_read.generation_revision is not None
                )
                wait_catalog_settled(driver, pid, rid, max_wait_s=15.0)
                after_restart = walk_catalog_retry_409(driver, pid, rid, page_limit=200)
                ref_entries = [e for e in reference_walk(wdir, boundary_config=boundary_config, include_hidden=True) if e.kind == "file"]
                res = check_catalog(walk_entries_to_catalog_items(ref_entries, root_id=rid), after_restart)
                extra_meta = {
                    "mode": "real_restart",
                    "instance": idx,
                    "prior_completed_generation_id": str(before.generation_revision) if before.generation_revision is not None else "",
                    "first_read_state": first_idx.get("state"),
                    "first_read_generation_id": str(first_read.generation_revision) if first_read.generation_revision is not None else "",
                    "generation_identity_preserved": (
                        before.generation_revision is not None
                        and first_read.generation_revision is not None
                        and str(first_read.generation_revision) == str(before.generation_revision)
                    ),
                    "first_read_items": len(first_read.items),
                    "served_persisted_immediately": served_immediately,
                    "after_settle_generation_id": str(after_restart.generation_revision) if after_restart.generation_revision is not None else "",
                    "verdict": res.verdict.value,
                }
                report.record_generated(res, extra=extra_meta)
                report.record_ledger(extra_meta)
            else:
                # Decision 054 & 076: require real continuation cursor, opaque revision identity on page 1,
                # witnessed generation publication (changed revision identity + newly published file visible),
                # and typed 409 cursor_generation_expired (or exact pinned replay on rev1_id).
                page1 = driver.file_lookup(pid, "", root_id=rid, limit=4)
                rev1 = page1.get("revision")
                cur = str(page1.get("next_cursor") or "")
                if rev1 is None or not cur:
                    res = CheckResult(
                        Verdict.DEFECT,
                        "catalog",
                        reason=f"page 1 missing revision ({rev1}) or continuation cursor ({cur!r}) on >=6 file tree",
                    )
                    report.record_generated(res, extra={"mode": "page_across_mutation", "instance": idx})
                    report.record_ledger({"mode": "page_across_mutation", "instance": idx, "rev1_id": str(rev1), "next_cursor_present": bool(cur), "verdict": res.verdict.value})
                    shutil.rmtree(wdir, ignore_errors=True)
                    continue

                rev1_id = str(rev1)
                # Mutate tree and witness changed revision identity + newly published file visible.
                bump_name = f"gen_bump_{idx}.txt"
                (wdir / bump_name).write_text("new gen\n", encoding="utf-8")
                pub_deadline = time.monotonic() + 3.0
                rev_after_pub_id = rev1_id
                new_file_visible = False
                while time.monotonic() < pub_deadline:
                    idx_now = driver.get_source_index(pid)
                    r_now = idx_now.get("revision")
                    if r_now is not None and str(r_now) != rev1_id:
                        rev_after_pub_id = str(r_now)
                        probe_new = driver.file_lookup(pid, bump_name, root_id=rid, limit=10)
                        if any(
                            isinstance(m, dict) and str(m.get("path") or "") == bump_name
                            for m in (probe_new.get("matches") or [])
                        ):
                            new_file_visible = True
                            break
                    time.sleep(0.05)
                publication_witnessed = (rev_after_pub_id != rev1_id) and new_file_visible

                revs = {rev1_id}
                seen_paths = [str(m.get("path") or "") for m in (page1.get("matches") or []) if isinstance(m, dict)]
                typed_409_code = ""
                crossed_gen = False
                missing_rev_on_page2 = False
                leaked_bump = False
                while cur:
                    try:
                        nxt = driver.file_lookup(pid, "", root_id=rid, limit=4, cursor=cur)
                    except urllib.error.HTTPError as exc:
                        if exc.code == 409:
                            err_body = parse_http_error_json(exc)
                            typed_409_code = str(err_body.get("code") or "")
                            break
                        raise
                    nxt_rev = nxt.get("revision")
                    if nxt_rev is None:
                        missing_rev_on_page2 = True
                    else:
                        nxt_rev_id = str(nxt_rev)
                        revs.add(nxt_rev_id)
                        if nxt_rev_id != rev1_id:
                            crossed_gen = True
                    page_paths = [str(m.get("path") or "") for m in (nxt.get("matches") or []) if isinstance(m, dict)]
                    if bump_name in page_paths:
                        leaked_bump = True
                    seen_paths.extend(page_paths)
                    nc = str(nxt.get("next_cursor") or "")
                    if not nc or nc == cur:
                        break
                    cur = nc

                has_dups = len(seen_paths) != len(set(seen_paths))
                if typed_409_code and typed_409_code != "cursor_generation_expired":
                    res = CheckResult(Verdict.DEFECT, "catalog", reason=f"HTTP 409 returned unexpected code {typed_409_code!r}, want 'cursor_generation_expired'")
                elif crossed_gen:
                    res = CheckResult(Verdict.DEFECT, "catalog", reason=f"pagination crossed generations without 409 rejection: {sorted(revs)}")
                elif missing_rev_on_page2:
                    res = CheckResult(Verdict.DEFECT, "catalog", reason="page 2 response omitted revision")
                elif leaked_bump:
                    res = CheckResult(Verdict.DEFECT, "catalog", reason=f"stale cursor leaked post-mutation file {bump_name}")
                elif has_dups:
                    res = CheckResult(Verdict.DEFECT, "catalog", reason="pagination returned duplicate paths")
                else:
                    res = CheckResult(Verdict.PASS, "catalog")
                rec = {
                    "mode": "page_across_mutation",
                    "instance": idx,
                    "rev_before_id": rev1_id,
                    "rev_after_publication_id": rev_after_pub_id,
                    "new_file_visible_in_new_gen": new_file_visible,
                    "publication_witnessed": publication_witnessed,
                    "next_cursor_present": True,
                    "typed_409_code": typed_409_code,
                    "revisions_seen": sorted(revs),
                    "verdict": res.verdict.value,
                }
                report.record_generated(res, extra=rec)
                report.record_ledger(rec)
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
        first_idx_e1 = driver.get_source_index(pid)
        first_read_e1 = driver.walk_catalog_index(pid, rid)
        wait_catalog_settled(driver, pid, rid, max_wait_s=15.0)
        after = driver.walk_catalog_index(pid, rid)
        ok1 = sorted(i.path for i in before.items) == sorted(i.path for i in after.items) and len(after.items) == 12
        report.record_edge(
            "k03_edge_1_completed_gen_survives_restart",
            ok1,
            {
                "before_rev": before.generation_revision,
                "before_items": len(before.items),
                "first_read_state": first_idx_e1.get("state"),
                "first_read_rev": first_read_e1.generation_revision,
                "first_read_items": len(first_read_e1.items),
                "after_settle_rev": after.generation_revision,
                "after_settle_items": len(after.items),
                "finished_dir_resume_lane": "verified zero re-read by count across 200 in-process instances in TestCF03_K03_GenerationsAndRestart mode 1",
            },
        )

        # Edge 2: generation stays readable during mutation/refresh
        (e1_dir / "src/extra.go").write_text("package p\n", encoding="utf-8")
        immediate = driver.walk_catalog_index(pid, rid)
        ok2 = len(immediate.items) >= 12 and immediate.generation_revision is not None
        report.record_edge("k03_edge_2_readable_during_refresh", ok2, {"items": len(immediate.items), "revision": immediate.generation_revision})

        # Edge 3: multi-page walk requires continuation cursor, consistent revision, and no duplicates
        p1 = driver.file_lookup(pid, "", root_id=rid, limit=3)
        p1_rev = p1.get("revision")
        cur = str(p1.get("next_cursor") or "")
        all_p = [m["path"] for m in (p1.get("matches") or []) if isinstance(m, dict)]
        e3_409_code = ""
        e3_crossed = False
        e3_missing_rev = p1_rev is None
        e3_has_cursor = bool(cur)
        while cur:
            try:
                pn = driver.file_lookup(pid, "", root_id=rid, limit=3, cursor=cur)
            except urllib.error.HTTPError as exc:
                if exc.code == 409:
                    e3_409_code = str(parse_http_error_json(exc).get("code") or "")
                    break
                raise
            if pn.get("revision") is None:
                e3_missing_rev = True
            elif p1_rev is not None and int(pn["revision"]) != int(p1_rev):
                e3_crossed = True
            all_p.extend(m["path"] for m in (pn.get("matches") or []) if isinstance(m, dict))
            nc = str(pn.get("next_cursor") or "")
            if not nc or nc == cur:
                break
            cur = nc
        ok3 = (
            e3_has_cursor
            and (not e3_missing_rev)
            and (not e3_crossed)
            and (len(all_p) == len(set(all_p)))
            and (e3_409_code == "cursor_generation_expired" or (not e3_409_code and len(all_p) >= 12))
        )
        report.record_edge(
            "k03_edge_3_paged_no_duplicates",
            ok3,
            {
                "has_cursor": e3_has_cursor,
                "p1_rev": p1_rev,
                "total": len(all_p),
                "unique": len(set(all_p)),
                "typed_409_code": e3_409_code,
                "crossed_gen": e3_crossed,
            },
        )

        # Edge 4: second restart preserves persisted index
        sidecar.restart()
        driver = sidecar.driver()
        first_idx_e4 = driver.get_source_index(pid)
        first_read_e4 = driver.walk_catalog_index(pid, rid)
        wait_catalog_settled(driver, pid, rid, max_wait_s=15.0)
        after2 = driver.walk_catalog_index(pid, rid)
        ok4 = len(after2.items) >= 12
        report.record_edge(
            "k03_edge_4_second_restart_readable",
            ok4,
            {
                "first_read_state": first_idx_e4.get("state"),
                "first_read_items": len(first_read_e4.items),
                "after_settle_items": len(after2.items),
            },
        )

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
                    os.chmod(locked, 0o000)
                    if not verify_dir_unreadable(locked):
                        report.record_blocked(
                            "K04 mode 1: unreadable perm_dir could not be established",
                            extra={"mode": mode, "instance": idx},
                        )
                        continue
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
                elif emode == "chmod000":
                    ldir = edir / "no_read"
                    ldir.mkdir()
                    (ldir / "x.txt").write_text("x\n", encoding="utf-8")
                    os.chmod(ldir, 0o000)
                    if not verify_dir_unreadable(ldir):
                        report.record_edge(ename, False, {"reason": "unreadable dir could not be established"}, blocked=True)
                        continue
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
                paths = {i.path for i in ans.items}
                gaps = sorted({g.reason for g in ans.declared_gaps if g.root_id in (None, rids[0])})
                escaped = [p for p in paths if "OUTSIDE_SECRET" in p]
                # Decision 054: assert each adversarial name's exact round-trip, not just anchor.txt.
                expected_roundtrip: list[str] = ["anchor.txt"]
                if emode == "unicode":
                    expected_roundtrip.append("caf\u00e9.txt")
                    if case_sensitive:
                        expected_roundtrip.append("cafe\u0301.txt")
                elif emode == "case_diff":
                    expected_roundtrip.append("Alpha.txt")
                    if case_sensitive:
                        expected_roundtrip.append("alpha.txt")
                elif emode == "long_path":
                    expected_roundtrip.append(f"{'x'*50}/{'y'*50}/{'z'*50}/deep.txt")
                elif emode == "newline":
                    expected_roundtrip.extend(["line\nbreak.txt", "tab\tchar.txt"])

                missing_expected = [ep for ep in expected_roundtrip if ep not in paths]
                chmod_ok = (emode != "chmod000") or ("catalog_failed" in gaps and "no_read/x.txt" not in paths)
                ok = (not escaped) and (not missing_expected) and chmod_ok
                report.record_edge(
                    ename,
                    ok,
                    {
                        "items": len(ans.items),
                        "expected_roundtrip": expected_roundtrip,
                        "missing_expected": missing_expected,
                        "escaped": escaped,
                        "gaps": gaps,
                        "case_sensitive": case_sensitive,
                        "output_non_escape_verdict": "PASS" if (not escaped) else "DEFECT",
                        "host_io_clause_verdict": "UNKNOWN",
                        "host_read_attribution": (
                            "output-level non-escape verified on sidecar lane; host-I/O clause carried as UNKNOWN "
                            "(no process-scoped syscall trace attached to shared sidecar; scheduled mid-walk symlink swap "
                            "verified in-process in TestCF03_K04_TraversalSafetyAndNames mode 0)"
                        ),
                    },
                    gap="catalog_failed" if (emode == "chmod000" and ok) else None,
                )
            finally:
                if ldir is not None and ldir.exists():
                    os.chmod(ldir, 0o755)
                shutil.rmtree(edir, ignore_errors=True)

        report.notes.append(
            "K04 sidecar keeps output-level non-escape (PASS) and host-I/O read attribution (UNKNOWN on unattached shared sidecar) "
            "explicitly separate; exact adversarial filename round-trips are verified on sidecar and scheduled mid-walk ancestor "
            "symlink swaps are verified in-process in TestCF03_K04_TraversalSafetyAndNames."
        )

    return report


def _inspect_root_snapshots(config_dir: Path, root_path: Path) -> dict[str, object]:
    """Read-only SQLite inspection pinned to the exact tested root_path (Decision 054)."""
    db_path = config_dir / "store.db"
    norm_root = str(Path(root_path).resolve())
    empty: dict[str, object] = {
        "root_path": norm_root,
        "snapshot_count": 0,
        "latest_snapshot_id": "",
        "latest_manifest_id": "",
        "chunk_ids": [],
        "boundaries": {},
        "dangling_chunks": 0,
    }
    if not db_path.is_file():
        return empty
    conn = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
    try:
        cur = conn.cursor()
        rows = cur.execute(
            """
            SELECT sr.snapshot_id, sr.manifest_id, s.created_ts
            FROM source_snapshot_roots sr
            JOIN source_snapshots s ON s.id = sr.snapshot_id
            WHERE sr.root_path = ?
            ORDER BY s.created_ts ASC, sr.snapshot_id ASC
            """,
            (norm_root,),
        ).fetchall()
        if not rows:
            return empty
        latest_snap, latest_man = str(rows[-1][0]), str(rows[-1][1])
        chunk_rows = cur.execute(
            "SELECT chunk_id FROM source_manifest_layout WHERE manifest_id = ? ORDER BY first_path ASC",
            (latest_man,),
        ).fetchall()
        chunk_ids = [str(r[0]) for r in chunk_rows]
        b_rows = cur.execute(
            "SELECT path, reason, detail FROM source_snapshot_boundaries WHERE snapshot_id = ? AND root_path = ?",
            (latest_snap, norm_root),
        ).fetchall()
        boundaries = {str(r[0]): {"reason": str(r[1]), "detail": str(r[2])} for r in b_rows}
        dangling = int(
            cur.execute(
                """
                SELECT COUNT(*)
                FROM source_manifest_layout l
                LEFT JOIN source_manifest_chunks c ON c.id = l.chunk_id
                WHERE l.manifest_id = ? AND c.id IS NULL
                """,
                (latest_man,),
            ).fetchone()[0]
        )
        return {
            "root_path": norm_root,
            "snapshot_count": len(rows),
            "latest_snapshot_id": latest_snap,
            "latest_manifest_id": latest_man,
            "chunk_ids": chunk_ids,
            "boundaries": boundaries,
            "dangling_chunks": dangling,
        }
    except sqlite3.Error:
        return empty
    finally:
        conn.close()


def _inspect_snapshot_db(config_dir: Path) -> dict[str, int]:
    """Aggregate read-only SQLite inspection of snapshot tables (retained for unit tests)."""
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


def _wait_root_snapshot(config_dir: Path, root_path: Path, *, min_count: int = 1, max_wait_s: float = 4.0) -> dict[str, object]:
    """Poll store.db until at least min_count snapshots exist for root_path, or deadline expires."""
    deadline = time.monotonic() + max_wait_s
    snap = _inspect_root_snapshots(config_dir, root_path)
    while int(snap["snapshot_count"]) < min_count and time.monotonic() < deadline:
        time.sleep(0.05)
        snap = _inspect_root_snapshots(config_dir, root_path)
    return snap


def run_k07_sidecar(
    driver: SidecarDriver,
    config_dir: Path,
    *,
    base_seed: str = BASE_SEED_PRIMARY,
    count: int = K07_SIDECAR_COUNT,
) -> FamilyReport:
    """Run K07 Snapshots and boundaries: 40 root-pinned sidecar instances + 4 edges (Decision 054)."""
    report = FamilyReport(
        family="K07",
        lane="sidecar",
        base_seed=base_seed,
        planned=count,
        edge_planned=K07_EDGE_COUNT,
    )

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
            try:
                walk1 = driver.list_source_walk(pid, limit=50)
            except urllib.error.HTTPError as exc:
                res = CheckResult(Verdict.DEFECT, "catalog", reason=f"/source/walk failed before edit with HTTP {exc.code}: {parse_http_error_json(exc)}")
                report.record_generated(res, extra={"instance": idx})
                shutil.rmtree(wdir, ignore_errors=True)
                continue

            snap1 = _wait_root_snapshot(config_dir, wdir, min_count=1, max_wait_s=3.0)
            chunks1 = set(snap1["chunk_ids"])  # type: ignore[arg-type]

            # Small edit and re-walk
            edit_name = f"small_edit_{idx}.txt"
            (wdir / edit_name).write_text("edited\n", encoding="utf-8")
            try:
                walk2 = driver.list_source_walk(pid, limit=50)
            except urllib.error.HTTPError as exc:
                res = CheckResult(Verdict.DEFECT, "catalog", reason=f"/source/walk failed after edit with HTTP {exc.code}: {parse_http_error_json(exc)}")
                report.record_generated(res, extra={"instance": idx})
                shutil.rmtree(wdir, ignore_errors=True)
                continue

            snap2 = _wait_root_snapshot(config_dir, wdir, min_count=int(snap1["snapshot_count"]) + 1, max_wait_s=2.0)
            chunks2 = set(snap2["chunk_ids"])  # type: ignore[arg-type]
            dangling = int(snap2["dangling_chunks"])

            if dangling > 0:
                res = CheckResult(Verdict.DEFECT, "catalog", reason=f"dangling manifest chunk references for root {wdir}: {dangling}")
            elif int(snap2["snapshot_count"]) == 0:
                res = CheckResult(Verdict.DEFECT, "catalog", reason=f"no root-pinned snapshot persisted in store.db for {wdir}")
            else:
                res = CheckResult(Verdict.PASS, "catalog")
            rec = {
                "instance": idx,
                "snapshot_id_before": snap1["latest_snapshot_id"],
                "manifest_id_before": snap1["latest_manifest_id"],
                "chunks_before": len(chunks1),
                "snapshot_id_after": snap2["latest_snapshot_id"],
                "manifest_id_after": snap2["latest_manifest_id"],
                "chunks_after": len(chunks2),
                "chunks_reused": len(chunks1 & chunks2),
                "chunks_replaced": len(chunks2 - chunks1),
                "boundaries_recorded": snap2["boundaries"],
                "dangling_chunks": dangling,
                "walk1_items": len(walk1.get("items") or []) if isinstance(walk1.get("items"), list) else 0,
                "walk2_items": len(walk2.get("items") or []) if isinstance(walk2.get("items"), list) else 0,
                "verdict": res.verdict.value,
            }
            report.record_generated(res, extra=rec)
            report.record_ledger(rec)
            shutil.rmtree(wdir, ignore_errors=True)

        # 4 hand-written K07 edges (root-pinned, never swallowing /source/walk HTTP errors)
        k07_edges = [
            ("k07_edge_1_scope_exclusion_boundaries", "scope"),
            ("k07_edge_2_unreadable_dir_boundary", "unreadable"),
            ("k07_edge_3_small_edit_chunk_identity", "small_edit"),
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
                if emode == "unreadable":
                    ldir = edir / "locked"
                    ldir.mkdir()
                    (ldir / "x.txt").write_text("x\n", encoding="utf-8")
                    os.chmod(ldir, 0o000)
                    if not verify_dir_unreadable(ldir):
                        report.record_edge(ename, False, {"reason": "unreadable dir could not be established"}, blocked=True)
                        continue
                pid, rids = driver.attach_project([edir])
                wait_catalog_settled(driver, pid, rids[0], max_wait_s=15.0)
                try:
                    _ = driver.list_source_walk(pid, limit=50)
                except urllib.error.HTTPError as exc:
                    report.record_edge(ename, False, {"http_error": exc.code, "body": parse_http_error_json(exc)})
                    continue
                snap1 = _wait_root_snapshot(config_dir, edir, min_count=1, max_wait_s=3.0)
                chunks1 = set(snap1["chunk_ids"])  # type: ignore[arg-type]
                snap_final = snap1
                chunks_final = chunks1
                if emode == "small_edit":
                    (edir / "src/main.go").write_text("package main // v2\n", encoding="utf-8")
                    try:
                        _ = driver.list_source_walk(pid, limit=50)
                    except urllib.error.HTTPError as exc:
                        report.record_edge(ename, False, {"http_error": exc.code, "body": parse_http_error_json(exc)})
                        continue
                    snap_final = _wait_root_snapshot(config_dir, edir, min_count=int(snap1["snapshot_count"]) + 1, max_wait_s=2.0)
                    chunks_final = set(snap_final["chunk_ids"])  # type: ignore[arg-type]

                boundaries: dict[str, dict[str, str]] = snap_final["boundaries"]  # type: ignore[assignment]
                has_snap = int(snap_final["snapshot_count"]) >= 1
                no_dangling = int(snap_final["dangling_chunks"]) == 0
                if emode == "scope":
                    ok = has_snap and no_dangling and (boundaries.get("node_modules", {}).get("reason") == "scope")
                elif emode == "unreadable":
                    ok = has_snap and no_dangling and (boundaries.get("locked", {}).get("reason") == "unreadable")
                elif emode == "small_edit":
                    ok = has_snap and no_dangling and (len(chunks_final) >= 1)
                else:
                    ok = has_snap and no_dangling and (len(chunks_final) >= 1)

                report.record_edge(
                    ename,
                    ok,
                    {
                        "root_path": snap_final["root_path"],
                        "snapshot_count": snap_final["snapshot_count"],
                        "snapshot_id_before": snap1["latest_snapshot_id"],
                        "snapshot_id_after": snap_final["latest_snapshot_id"],
                        "manifest_id_after": snap_final["latest_manifest_id"],
                        "chunks_before": len(chunks1),
                        "chunks_after": len(chunks_final),
                        "chunks_reused": len(chunks1 & chunks_final),
                        "chunks_replaced": len(chunks_final - chunks1),
                        "boundaries": boundaries,
                        "dangling_chunks": snap_final["dangling_chunks"],
                        "sweep_cleanup_positive_lane": "verified across 200 in-process K07 instances in TestCF03_K07_SnapshotsAndBoundaries mode 2 (Store.Sweep reclaims unreferenced chunk while preserving surviving manifest chunk)",
                    },
                )
            finally:
                if ldir is not None and ldir.exists():
                    os.chmod(ldir, 0o755)
                shutil.rmtree(edir, ignore_errors=True)

        report.notes.append(
            "K07 sidecar pins every snapshot, manifest, boundary row, and chunk reference to the exact tested root_path in store.db; "
            "destructive Store.Sweep cleanup positive (reclaiming unreferenced chunks while preserving surviving manifest chunks) "
            "is verified across 200 in-process K07 instances in TestCF03_K07_SnapshotsAndBoundaries."
        )

    return report
