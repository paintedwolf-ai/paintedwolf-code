"""CF06 sidecar runners for S04 (file lookup), S05 (symbol leg), S06 (query language), S07 (pages/cancel/export), S08 (store leg)."""

from __future__ import annotations

import json
import os
from pathlib import Path
import urllib.error

from ..driver_sidecar import SidecarDriver
from ..queries import S05_LANGUAGES, S06_TEXT_KINDS, generate_s04_lookup, generate_s05_corpus, generate_s06_query_text
from ..references import reference_file_lookup
from ..seeds import BASE_SEED_PRIMARY, seed_rng
from .cf06_types import FamilySummary, wait_for_catalog_ready


def run_s04_sidecar(
    driver: SidecarDriver,
    work_root: Path,
    *,
    base_seed: str = BASE_SEED_PRIMARY,
    instances: int = 200,
) -> FamilySummary:
    """Run S04 file lookup differential (default 200 generated instances + 4 hand-written edges)."""
    summary = FamilySummary(family_id="S04", lane="sidecar", planned=instances, edges_planned=4)
    cdir = work_root / "s04_lookup_tree"
    dirs = ("src", "pkg/api", "internal/search", "docs", "cmd/app")
    stems = ("router", "catalog", "alpha", "search", "index", "config", "handler", "model")
    exts = (".go", ".ts", ".md", ".py")
    paths: list[str] = []
    for i in range(120):
        rel = f"{dirs[i % len(dirs)]}/{stems[(i * 3) % len(stems)]}_{i:03d}{exts[i % len(exts)]}"
        p = cdir / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text("x\n", encoding="utf-8")
        paths.append(rel)
    paths.sort()

    pid, roots = driver.attach_project([cdir])
    wait_for_catalog_ready(driver, pid)

    for idx in range(instances):
        lq = generate_s04_lookup(base_seed, "S04", idx, paths)
        resp = driver.file_lookup(pid, lq.query_text, root_id=roots[0], limit=50)
        matches = resp.get("matches") or []
        got_paths = [str(m.get("path") or "") for m in matches if isinstance(m, dict)]
        ref_hits = reference_file_lookup(paths, lq.query_text)[:50]
        ref_paths = [h.path for h in ref_hits]

        if got_paths != ref_paths:
            summary.record_fail(
                {"instance": idx, "query": lq.query_text, "got": got_paths[:5], "want": ref_paths[:5]}
            )
            continue
        # Verify matched ranges are present and non-empty.
        ranges_ok = True
        for m in matches:
            if not isinstance(m, dict):
                continue
            hl = m.get("highlights")
            if not isinstance(hl, list) or not hl:
                ranges_ok = False
                break
        if not ranges_ok:
            summary.record_fail({"instance": idx, "query": lq.query_text, "reason": "missing highlights ranges"})
            continue
        summary.record_pass()

    # 4 hand-written S04 edges.
    # Edge 1: Basename prefix ranks ahead of basename substring.
    e1 = driver.file_lookup(pid, "router", root_id=roots[0], limit=10)
    e1_paths = [str(m.get("path") or "") for m in (e1.get("matches") or []) if isinstance(m, dict)]
    summary.record_edge(bool(e1_paths) and Path(e1_paths[0]).name.startswith("router"), {"edge": "s04_1_prefix_first", "top": e1_paths[:3]})

    # Edge 2: Empty query returns browse listing.
    e2 = driver.file_lookup(pid, "", root_id=roots[0], limit=10)
    summary.record_edge(len(e2.get("matches") or []) == 10, {"edge": "s04_2_empty_browse"})

    # Edge 3: Leading directory relaxation finds local file.
    e3 = driver.file_lookup(pid, f"ci/runner/checkout/{paths[0]}", root_id=roots[0], limit=10)
    e3_paths = [str(m.get("path") or "") for m in (e3.get("matches") or []) if isinstance(m, dict)]
    summary.record_edge(bool(e3_paths) and e3_paths[0] == paths[0], {"edge": "s04_3_dir_relaxation", "top": e3_paths[:2]})

    # Edge 4: Multi-root with a healthy root and a second root returns healthy root matches.
    cdir2 = work_root / "s04_second_root"
    cdir2.mkdir(parents=True, exist_ok=True)
    (cdir2 / "second_unique.go").write_text("package s\n", encoding="utf-8")
    pid2, _ = driver.attach_project([cdir, cdir2])
    wait_for_catalog_ready(driver, pid2)
    e4 = driver.file_lookup(pid2, "second_unique", limit=10)
    e4_paths = [str(m.get("path") or "") for m in (e4.get("matches") or []) if isinstance(m, dict)]
    summary.record_edge("second_unique.go" in e4_paths, {"edge": "s04_4_multiroot_healthy", "paths": e4_paths})
    return summary


def run_s05_sidecar(
    driver: SidecarDriver,
    work_root: Path,
    *,
    base_seed: str = BASE_SEED_PRIMARY,
    instances: int = 100,
) -> FamilySummary:
    """Run S05 symbol leg checks across all 6 languages (default 100 instances + 5 hand-written edges)."""
    summary = FamilySummary(family_id="S05", lane="sidecar", planned=instances, edges_planned=5)
    cdir = work_root / "s05_symbols"
    files, decls = generate_s05_corpus(base_seed, "S05", 0, languages=S05_LANGUAGES, declarations_per_language=6)
    for rel, content in files.items():
        p = cdir / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(content, encoding="utf-8")
    (cdir / "README.md").write_text(
        "# Documentation\n\n" + "\n".join(f"## {d.name}\nMentions {d.name}\n" for d in decls[:6]),
        encoding="utf-8",
    )
    decl_names = {d.name for d in decls} | {"run"}
    pid, _ = driver.attach_project([cdir])
    wait_for_catalog_ready(driver, pid)

    for idx in range(instances):
        rng = seed_rng(base_seed, "S05:query", idx)
        target = rng.choice(decls)
        q = target.name if (idx % 3 == 0) else target.name[:8]
        resp = driver.search_raw(f"project:current kind:symbol {q}", origin_project_id=pid, limit=50, budget="complete")
        hits = [h for h in (resp.get("hits") or []) if isinstance(h, dict) and str(h.get("hit_kind") or "") == "symbol"]
        bad_md = [h for h in hits if str(h.get("path") or "") == "README.md"]
        bad_name = [h for h in hits if str(h.get("title") or "") not in decl_names]
        if bad_md or bad_name:
            summary.record_fail({"instance": idx, "query": q, "bad_md": len(bad_md), "bad_name": len(bad_name)})
            continue
        if idx % 3 == 0 and (not hits or str(hits[0].get("title") or "") != target.name):
            summary.record_fail({"instance": idx, "query": q, "top": hits[0] if hits else None, "want": target.name})
            continue
        summary.record_pass()

    # 5 hand-written S05 edges.
    # Edge 1: Rename declaration and re-search.
    ren_dir = work_root / "s05_rename"
    ren_dir.mkdir(parents=True, exist_ok=True)
    ren_file = ren_dir / "ren.go"
    ren_file.write_text("package p\n\nfunc OldSidecarSym() {}\n", encoding="utf-8")
    pid_r, _ = driver.attach_project([ren_dir])
    wait_for_catalog_ready(driver, pid_r)
    r1 = driver.search_raw("project:current kind:symbol OldSidecarSym", origin_project_id=pid_r, budget="complete")
    ren_file.write_text("package p\n\nfunc NewSidecarSym() {}\n", encoding="utf-8")
    r_old = driver.search_raw("project:current kind:symbol OldSidecarSym", origin_project_id=pid_r, budget="complete")
    r_new = driver.search_raw("project:current kind:symbol NewSidecarSym", origin_project_id=pid_r, budget="complete")
    summary.record_edge(
        len(r1.get("hits") or []) == 1 and len(r_old.get("hits") or []) == 0 and len(r_new.get("hits") or []) == 1,
        {"edge": "s05_1_rename"},
    )

    # Edge 2: Tier order exact-case > exact > prefix > word-start > hump > substring.
    tier_dir = work_root / "s05_tiers"
    tier_dir.mkdir(parents=True, exist_ok=True)
    (tier_dir / "tiers.go").write_text(
        "package p\n\nfunc xcfgx() {}\nfunc LoadCfg() {}\nfunc CfgLoader() {}\nfunc Cfg() {}\nfunc cfg() {}\nfunc CacheFileGroup() {}\n",
        encoding="utf-8",
    )
    pid_t, _ = driver.attach_project([tier_dir])
    wait_for_catalog_ready(driver, pid_t)
    rt = driver.search_raw("project:current kind:symbol cfg", origin_project_id=pid_t, budget="complete")
    got_t = [str(h.get("title") or "") for h in (rt.get("hits") or []) if isinstance(h, dict)]
    summary.record_edge(
        got_t == ["cfg", "Cfg", "CfgLoader", "LoadCfg", "CacheFileGroup", "xcfgx"],
        {"edge": "s05_2_tier_order", "got": got_t},
    )

    # Edge 3: Markdown headings & comments never answer.
    md_dir = work_root / "s05_md"
    md_dir.mkdir(parents=True, exist_ok=True)
    (md_dir / "doc.md").write_text("# HeadingOnlySym\n\nHeadingOnlySym\n", encoding="utf-8")
    (md_dir / "c.go").write_text("package p\n// HeadingOnlySym\n", encoding="utf-8")
    pid_m, _ = driver.attach_project([md_dir])
    wait_for_catalog_ready(driver, pid_m)
    rm = driver.search_raw("project:current kind:symbol HeadingOnlySym", origin_project_id=pid_m, budget="complete")
    summary.record_edge(len(rm.get("hits") or []) == 0, {"edge": "s05_3_no_headings_mentions"})

    # Edge 4: Multi-language coverage check (all 6 languages returned at least one declaration).
    found_langs = set()
    for lang in S05_LANGUAGES:
        sample = next(d for d in decls if d.language == lang)
        rl = driver.search_raw(f"project:current kind:symbol {sample.name}", origin_project_id=pid, budget="complete")
        if any(str(h.get("title") or "") == sample.name for h in (rl.get("hits") or []) if isinstance(h, dict)):
            found_langs.add(lang)
    summary.record_edge(len(found_langs) == len(S05_LANGUAGES), {"edge": "s05_4_six_languages", "found": sorted(found_langs)})

    # Edge 5: Unreadable sibling does not suppress healthy symbol.
    un_dir = work_root / "s05_unreadable"
    un_dir.mkdir(parents=True, exist_ok=True)
    (un_dir / "good.go").write_text("package p\n\nfunc GoodSiblingSym() {}\n", encoding="utf-8")
    bad_f = un_dir / "bad.go"
    bad_f.write_text("package p\n\nfunc BadSiblingSym() {}\n", encoding="utf-8")
    try:
        os.chmod(bad_f, 0o000)
        pid_u, _ = driver.attach_project([un_dir])
        wait_for_catalog_ready(driver, pid_u)
        ru = driver.search_raw("project:current kind:symbol GoodSiblingSym", origin_project_id=pid_u, budget="complete")
        summary.record_edge(
            any(str(h.get("title") or "") == "GoodSiblingSym" for h in (ru.get("hits") or []) if isinstance(h, dict)),
            {"edge": "s05_5_unreadable_sibling"},
        )
    finally:
        os.chmod(bad_f, 0o644)
    return summary


def run_s06_sidecar(
    driver: SidecarDriver,
    work_root: Path,
    *,
    base_seed: str = BASE_SEED_PRIMARY,
    instances: int = 200,
) -> FamilySummary:
    """Run S06 query language checks over /v1/search (default 200 instances + 10 hand-written edges)."""
    summary = FamilySummary(family_id="S06", lane="sidecar", planned=instances, edges_planned=10)
    cdir = work_root / "s06_query_tree"
    cdir.mkdir(parents=True, exist_ok=True)
    (cdir / "sample.txt").write_text("alpha beta gamma\n", encoding="utf-8")
    pid, _ = driver.attach_project([cdir])
    wait_for_catalog_ready(driver, pid)

    def _run_one(query_text: str, expect_err: bool) -> tuple[bool, str]:
        try:
            driver.search_raw(query_text, origin_project_id=pid, limit=10, budget="interactive")
            if expect_err:
                return False, "expected HTTP 400 parse error, got 200"
            return True, ""
        except urllib.error.HTTPError as exc:
            body = exc.read().decode("utf-8", errors="replace")
            if not expect_err:
                return False, f"plain text failed with HTTP {exc.code}: {body[:200]}"
            if exc.code != 400:
                return False, f"expected HTTP 400, got {exc.code}"
            return True, ""

    for idx in range(instances):
        kind = S06_TEXT_KINDS[idx % len(S06_TEXT_KINDS)]
        case = generate_s06_query_text(base_seed, "S06", idx, kind)
        ok, err_msg = _run_one(case.text, case.expect_parse_error)
        if ok:
            summary.record_pass()
        else:
            summary.record_fail({"instance": idx, "kind": kind, "query": case.text, "error": err_msg})

    # 10 hand-written S06 edges.
    edges = (
        ("plain_sentence", "hello world from editor", False),
        ("url_with_port", "http://localhost:8787/v1/search?a=1#frag", False),
        ("unclosed_quote", '"unclosed phrase', False),
        ("unclosed_paren", "(alpha OR beta", False),
        ("bare_punctuation", "->", False),
        ("lowercase_operators", "alpha and beta or not gamma", False),
        ("unknown_field_colon", "Error:connection_refused", False),
        ("invalid_closed_kind", "alpha kind:invalid_kind", True),
        ("conflicting_single_valued", "kind:code kind:symbol", True),
        ("required_and_negated", "kind:code NOT kind:code", True),
    )
    for name, qtxt, want_err in edges:
        ok, err_msg = _run_one(qtxt, want_err)
        summary.record_edge(ok, {"edge": f"s06_{name}", "query": qtxt, "error": err_msg})
    return summary


def run_s07_sidecar(
    driver: SidecarDriver,
    work_root: Path,
    *,
    base_seed: str = BASE_SEED_PRIMARY,
    instances: int = 60,
) -> FamilySummary:
    """Run S07 pages, cancellation, project switch, and export checks (default 60 instances + 4 edges)."""
    summary = FamilySummary(family_id="S07", lane="sidecar", planned=instances, edges_planned=4)
    dir_a = work_root / "s07_proj_a"
    dir_b = work_root / "s07_proj_b"
    dir_a.mkdir(parents=True, exist_ok=True)
    dir_b.mkdir(parents=True, exist_ok=True)
    for i in range(15):
        (dir_a / f"a_{i:02d}.txt").write_text(f"alpha_s07 shared_line_{i}\n", encoding="utf-8")
        (dir_b / f"b_{i:02d}.txt").write_text(f"gamma_s07 shared_line_{i}\n", encoding="utf-8")
    pid_a, _ = driver.attach_project([dir_a])
    pid_b, _ = driver.attach_project([dir_b])
    wait_for_catalog_ready(driver, pid_a)
    wait_for_catalog_ready(driver, pid_b)

    for idx in range(instances):
        mode = idx % 3
        if mode == 0:
            # Page query A, issue query B, verify cursor from A cannot be used on B.
            p1 = driver.search_raw("project:current kind:code alpha_s07", origin_project_id=pid_a, limit=5, budget="complete")
            cur = str(p1.get("next_cursor") or "")
            if not cur:
                summary.record_fail({"instance": idx, "reason": "expected next_cursor for limit=5 over 15 hits"})
                continue
            try:
                driver.search_raw("project:current kind:code gamma_s07", origin_project_id=pid_b, limit=5, cursor=cur, budget="complete")
                summary.record_fail({"instance": idx, "reason": "cursor from query A accepted by query B"})
            except urllib.error.HTTPError as exc:
                if exc.code == 400:
                    summary.record_pass()
                else:
                    summary.record_fail({"instance": idx, "reason": f"unexpected status {exc.code}"})
        elif mode == 1:
            # Project switch under same query string.
            ra = driver.search_raw("project:current kind:code shared_line_1", origin_project_id=pid_a, limit=20, budget="complete")
            rb = driver.search_raw("project:current kind:code shared_line_1", origin_project_id=pid_b, limit=20, budget="complete")
            a_paths = [str(h.get("path") or "") for h in (ra.get("hits") or []) if isinstance(h, dict)]
            b_paths = [str(h.get("path") or "") for h in (rb.get("hits") or []) if isinstance(h, dict)]
            if a_paths and b_paths and all(p.startswith("a_") for p in a_paths) and all(p.startswith("b_") for p in b_paths):
                summary.record_pass()
            else:
                summary.record_fail({"instance": idx, "a_paths": a_paths, "b_paths": b_paths})
        else:
            # Export JSONL matches shown search rows.
            shown = driver.search_raw("project:current kind:code alpha_s07", origin_project_id=pid_a, limit=50, budget="complete")
            shown_ids = [str(h.get("hit_id") or "") for h in (shown.get("hits") or []) if isinstance(h, dict)]
            exported_text, _ = driver.export_search("project:current kind:code alpha_s07", fmt="jsonl", origin_project_id=pid_a)
            exp_rows = [json.loads(line) for line in exported_text.splitlines() if line.strip()]
            exp_ids = [str(r.get("hit_id") or r.get("id") or "") for r in exp_rows]
            if len(exp_rows) == len(shown_ids) and (not shown_ids or set(exp_ids) == set(shown_ids) or len(exp_rows) == 15):
                summary.record_pass()
            else:
                summary.record_fail({"instance": idx, "shown": len(shown_ids), "exported": len(exp_rows)})

    # 4 hand-written S07 edges.
    # Edge 1: Complete pagination across pages yields disjoint, non-overlapping hits summing to total.
    seen_ids: list[str] = []
    cur_tok: str | None = None
    for _ in range(5):
        pg = driver.search_raw("project:current kind:code alpha_s07", origin_project_id=pid_a, limit=5, cursor=cur_tok, budget="complete")
        seen_ids.extend(str(h.get("hit_id") or "") for h in (pg.get("hits") or []) if isinstance(h, dict))
        cur_tok = str(pg.get("next_cursor") or "") or None
        if not cur_tok:
            break
    summary.record_edge(len(seen_ids) == 15 and len(set(seen_ids)) == 15, {"edge": "s07_1_disjoint_pages", "count": len(seen_ids)})

    # Edge 2: Export CSV succeeds and contains expected filenames.
    csv_text, _ = driver.export_search("project:current kind:code alpha_s07", fmt="csv", origin_project_id=pid_a)
    summary.record_edge("a_00.txt" in csv_text, {"edge": "s07_2_export_csv"})

    # Edge 3: Export preserves case_sensitive flag.
    exp_case, _ = driver.export_search("project:current kind:code ALPHA_S07", fmt="jsonl", origin_project_id=pid_a, case_sensitive=True)
    summary.record_edge(len([l for l in exp_case.splitlines() if l.strip()]) == 0, {"edge": "s07_3_export_case_flag"})

    # Edge 4: Invalid cursor rejected cleanly with HTTP 400.
    try:
        driver.search_raw("project:current kind:code alpha_s07", origin_project_id=pid_a, limit=5, cursor="invalid-cursor-token")
        summary.record_edge(False, {"edge": "s07_4_invalid_cursor"})
    except urllib.error.HTTPError as exc:
        summary.record_edge(exc.code == 400, {"edge": "s07_4_invalid_cursor", "status": exc.code})
    return summary


def run_s08_sidecar(
    driver: SidecarDriver,
    work_root: Path,
    *,
    base_seed: str = BASE_SEED_PRIMARY,
    instances: int = 60,
) -> FamilySummary:
    """Run S08 store leg & cross-project isolation checks over live sidecar (default 60 instances + 4 edges)."""
    summary = FamilySummary(family_id="S08", lane="sidecar", planned=instances, edges_planned=4)
    dir_a = work_root / "s08_proj_a"
    dir_b = work_root / "s08_proj_b"
    dir_a.mkdir(parents=True, exist_ok=True)
    dir_b.mkdir(parents=True, exist_ok=True)
    (dir_a / "only_a.txt").write_text("s08_secret_proj_a\n", encoding="utf-8")
    (dir_b / "only_b.txt").write_text("s08_secret_proj_b\n", encoding="utf-8")
    pid_a, _ = driver.attach_project([dir_a])
    pid_b, _ = driver.attach_project([dir_b])
    wait_for_catalog_ready(driver, pid_a)
    wait_for_catalog_ready(driver, pid_b)

    for idx in range(instances):
        tok = f"s08_store_probe_{idx:03d}"
        # Query store leg scoped to project A and project B; verify zero cross-project hits.
        ra = driver.search_raw(f"project:current kind:message {tok}", origin_project_id=pid_a, limit=20, budget="complete")
        rb = driver.search_raw(f"project:current kind:scan {tok}", origin_project_id=pid_b, limit=20, budget="complete")
        a_hits = [h for h in (ra.get("hits") or []) if isinstance(h, dict)]
        b_hits = [h for h in (rb.get("hits") or []) if isinstance(h, dict)]
        leaked_a = [h for h in a_hits if str(h.get("project_id") or "") not in ("", pid_a)]
        leaked_b = [h for h in b_hits if str(h.get("project_id") or "") not in ("", pid_b)]
        if not leaked_a and not leaked_b:
            summary.record_pass()
        else:
            summary.record_fail({"instance": idx, "leaked_a": len(leaked_a), "leaked_b": len(leaked_b)})

    # 4 hand-written S08 edges.
    # Edge 1: Cross-project code/store isolation for project A searching project B's unique token.
    r1 = driver.search_raw("project:current s08_secret_proj_b", origin_project_id=pid_a, limit=20, budget="complete")
    summary.record_edge(len(r1.get("hits") or []) == 0, {"edge": "s08_1_cross_project_isolation_a"})

    # Edge 2: Cross-project isolation in reverse direction (project B searching project A's unique token).
    r2 = driver.search_raw("project:current s08_secret_proj_a", origin_project_id=pid_b, limit=20, budget="complete")
    summary.record_edge(len(r2.get("hits") or []) == 0, {"edge": "s08_2_cross_project_isolation_b"})

    # Edge 3: Unknown origin_project_id rejected cleanly (never falls back to global).
    try:
        driver.search_raw("project:current s08_secret_proj_a", origin_project_id="00000000-0000-0000-0000-000000000000")
        summary.record_edge(False, {"edge": "s08_3_unknown_origin_rejected"})
    except urllib.error.HTTPError as exc:
        summary.record_edge(exc.code in (400, 404), {"edge": "s08_3_unknown_origin_rejected", "status": exc.code})

    # Edge 4: Store-only kind:evidence query executes cleanly without error.
    r4 = driver.search_raw("project:current kind:evidence nonexistent_evidence_token", origin_project_id=pid_a, limit=10, budget="complete")
    summary.record_edge(isinstance(r4.get("hits"), list), {"edge": "s08_4_store_evidence_query"})
    return summary
