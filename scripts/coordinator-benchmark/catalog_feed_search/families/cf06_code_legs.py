"""CF06 sidecar runners for S01 (code leg differential), S02 (literal acceleration), S03 (search after change)."""

from __future__ import annotations

from pathlib import Path
import threading
import time

from ..checker import ProductSearchAnswer, ScopedSearchHit, check_search
from ..checker_types import Verdict
from ..driver_sidecar import SidecarDriver
from ..generators import TreeEntrySpec, TreeGenOptions, TreeSpec, generate_tree_spec, materialize_tree
from ..queries import S01_QUERY_FORMS, CodeQuerySpec, generate_s01_query
from ..references import reference_search
from ..seeds import BASE_SEED_PRIMARY, ReplaySelector, seed_rng
from ..shrinker import FailingInstance, shrink_instance
from .cf06_types import FamilySummary, build_code_dsl, wait_for_catalog_ready


def _dedup_ref_hits_by_line(hits: tuple[ScopedSearchHit, ...]) -> tuple[ScopedSearchHit, ...]:
    """Deduplicate ripgrep submatch hits to 1 per (root_id, leg, path, line_number) to match /v1/search line projection."""
    seen: set[tuple[str, str, str, int]] = set()
    out: list[ScopedSearchHit] = []
    for h in hits:
        k = (h.root_id, h.leg, h.hit.path, h.hit.line_number)
        if k not in seen:
            seen.add(k)
            out.append(h)
    return tuple(out)


def _filter_code_hits(answer: ProductSearchAnswer) -> ProductSearchAnswer:
    """Project a federated /v1/search response down to code-line hits only."""
    code_hits = tuple(h for h in answer.hits if h.leg == "code")
    return ProductSearchAnswer(
        hits=code_hits,
        declared_gaps=answer.declared_gaps,
        instrumentation_complete=answer.instrumentation_complete,
        projection=answer.projection,
    )


def _make_s01_corpus(base_seed: str, family_id: str, corpus_idx: int, entry_count: int) -> TreeSpec:
    opts = TreeGenOptions(
        entry_count=entry_count,
        max_depth=4,
        include_hidden=True,
        include_ignored=False,
        include_empty_dirs=True,
        include_vcs=False,
        include_boundaries=False,
        include_nested_checkouts=False,
        include_paintedwolf=False,
        include_symlinks=False,
        include_binary=True,
        include_mixed_line_endings=True,
    )
    return generate_tree_spec(base_seed, f"{family_id}:corpus", corpus_idx, options=opts)


def run_s01_sidecar(
    driver: SidecarDriver,
    work_root: Path,
    *,
    base_seed: str = BASE_SEED_PRIMARY,
    instances: int = 300,
    rg_bin: str = "rg",
    shrink_dir: Path | None = None,
) -> FamilySummary:
    """Run S01 code leg differential (default 300 generated instances + 8 hand-written edges)."""
    summary = FamilySummary(family_id="S01", lane="sidecar", planned=instances, edges_planned=8)
    corpus_count = max(1, min(15, instances))
    queries_per_corpus = max(1, instances // corpus_count)

    corpora: list[tuple[TreeSpec, Path, str, str]] = []
    for c in range(corpus_count):
        file_count = 12 if c < corpus_count - 1 else 60
        if c == 0:
            file_count = 5
        spec = _make_s01_corpus(base_seed, "S01", c, file_count)
        cdir = work_root / f"s01_corpus_{c:02d}"
        materialize_tree(spec, cdir)
        pid, roots = driver.attach_project([cdir])
        wait_for_catalog_ready(driver, pid)
        corpora.append((spec, cdir, pid, roots[0]))

    shrunk_done = False
    for idx in range(instances):
        c = min(idx // queries_per_corpus, corpus_count - 1)
        spec, cdir, pid, rid = corpora[c]
        form = S01_QUERY_FORMS[idx % len(S01_QUERY_FORMS)]
        q = generate_s01_query(base_seed, "S01", idx, form, paths=spec.file_paths())
        dsl = f"project:current kind:code {build_code_dsl(q.pattern, regex=q.regex, path_filters=q.path_filters)}"

        ref = reference_search(cdir, q, rg_bin=rg_bin)
        ref_scoped = _dedup_ref_hits_by_line(tuple(ScopedSearchHit(hit=h, root_id=rid, leg="code") for h in ref.hits))
        prod_raw = driver.search(
            dsl,
            default_root_id=rid,
            default_leg="code",
            origin_project_id=pid,
            regex=q.regex,
            case_sensitive=q.case_sensitive,
            whole_word=q.whole_word,
            include=q.include_globs,
            exclude=q.exclude_globs,
            limit=500,
            budget="complete",
        )
        prod = _filter_code_hits(prod_raw)
        chk = check_search(ref_scoped, prod, projection="path_line")
        summary.record_verdict(chk.verdict, chk.accounted_gaps)
        if chk.verdict == Verdict.DEFECT:
            detail: dict[str, object] = {
                "instance": idx,
                "form": form,
                "dsl": dsl,
                "reason": chk.reason,
                "extra_claims": list(chk.extra_claims[:5]),
                "missing_claims": list(chk.missing_claims[:5]),
            }
            if shrink_dir is not None and not shrunk_done:
                shrunk_done = True
                fail_inst = FailingInstance(
                    selector=ReplaySelector(base_seed, "S01", idx),
                    tree_spec=spec,
                    query_spec=q,
                    failure_summary=chk.reason,
                )

                def _pred(trial: FailingInstance) -> bool:
                    tdir = work_root / f"s01_shrink_{time.monotonic_ns()}"
                    materialize_tree(trial.tree_spec, tdir)
                    tpid, troots = driver.attach_project([tdir])
                    wait_for_catalog_ready(driver, tpid)
                    tq = trial.query_spec or q
                    tdsl = f"project:current kind:code {build_code_dsl(tq.pattern, regex=tq.regex, path_filters=tq.path_filters)}"
                    tref = reference_search(tdir, tq, rg_bin=rg_bin)
                    tref_s = _dedup_ref_hits_by_line(tuple(ScopedSearchHit(hit=h, root_id=troots[0], leg="code") for h in tref.hits))
                    tprod = _filter_code_hits(
                        driver.search(
                            tdsl,
                            default_root_id=troots[0],
                            default_leg="code",
                            origin_project_id=tpid,
                            regex=tq.regex,
                            case_sensitive=tq.case_sensitive,
                            whole_word=tq.whole_word,
                            include=tq.include_globs,
                            exclude=tq.exclude_globs,
                            limit=500,
                            budget="complete",
                        )
                    )
                    return check_search(tref_s, tprod, projection="path_line").verdict == Verdict.DEFECT

                try:
                    shrunk = shrink_instance(fail_inst, _pred, max_evaluations=25)
                    shrink_dir.mkdir(parents=True, exist_ok=True)
                    out_p = shrink_dir / f"S01_instance_{idx}_shrunk.json"
                    shrunk.write_json(out_p)
                    summary.shrunk_files.append(out_p.name)
                except ValueError:
                    pass
            summary.failures.append(detail)

    # 8 hand-written S01 edges.
    _run_s01_edges(driver, work_root, summary)
    return summary


def _run_s01_edges(driver: SidecarDriver, work_root: Path, summary: FamilySummary) -> None:
    edir1 = work_root / "s01_edges_r1"
    edir2 = work_root / "s01_edges_r2"
    edge_spec = TreeSpec(
        selector=ReplaySelector(BASE_SEED_PRIMARY, "S01-edge", 0),
        entries=(
            TreeEntrySpec("visible.txt", "file", b"s01_edge_needle\n"),
            TreeEntrySpec(".hidden/secret.txt", "file", b"s01_edge_needle\n"),
            TreeEntrySpec("crlf.txt", "file", b"line1\r\ns01_edge_needle\r\n"),
            TreeEntrySpec("utf8bom.txt", "file", b"\xef\xbb\xbfs01_edge_needle\n"),
            TreeEntrySpec("bin.bin", "file", b"\x00\x01\x02s01_edge_needle"),
            TreeEntrySpec("src/case.txt", "file", b"S01_EDGE_NEEDLE\ns01_edge_needle\n"),
            TreeEntrySpec("src/word.txt", "file", b"s01_edge_needle_ext\ns01_edge_needle\n"),
        ),
    )
    materialize_tree(edge_spec, edir1)
    edir2.mkdir(parents=True, exist_ok=True)
    (edir2 / "visible.txt").write_bytes(b"s01_edge_needle\n")

    pid1, roots1 = driver.attach_project([edir1])
    wait_for_catalog_ready(driver, pid1)
    pid_multi, roots_multi = driver.attach_project([edir1, edir2])
    wait_for_catalog_ready(driver, pid_multi)

    def _paths(dsl: str, **kwargs: object) -> list[str]:
        ans = _filter_code_hits(driver.search(dsl, default_root_id=roots1[0], origin_project_id=pid1, budget="complete", **kwargs))
        return [h.hit.path for h in ans.hits]

    p1 = _paths("project:current kind:code s01_edge_needle")
    summary.record_edge(".hidden/secret.txt" not in p1 and "visible.txt" in p1, {"edge": "s01_1_hidden_default", "paths": p1})
    p2 = _paths("project:current kind:code s01_edge_needle path:.hidden/")
    summary.record_edge(".hidden/secret.txt" in p2, {"edge": "s01_2_hidden_path_filter", "paths": p2})
    p3 = _paths("project:current kind:code s01_edge_needle", include=(".hidden/*.txt",))
    summary.record_edge(".hidden/secret.txt" in p3, {"edge": "s01_3_hidden_include_glob", "paths": p3})
    p4 = _paths("project:current kind:code s01_edge_needle NOT path:src/")
    summary.record_edge(".hidden/secret.txt" not in p4, {"edge": "s01_4_negative_no_widen", "paths": p4})
    ans5 = _filter_code_hits(driver.search("project:current kind:code s01_edge_needle path:crlf.txt", default_root_id=roots1[0], origin_project_id=pid1, budget="complete"))
    summary.record_edge(len(ans5.hits) == 1 and ans5.hits[0].hit.line_number == 2, {"edge": "s01_5_crlf", "hits": len(ans5.hits)})
    ans6 = _filter_code_hits(driver.search("project:current kind:code s01_edge_needle path:utf8bom.txt", default_root_id=roots1[0], origin_project_id=pid1, budget="complete"))
    summary.record_edge(len(ans6.hits) == 1 and ans6.hits[0].hit.line_number == 1, {"edge": "s01_6_utf8bom", "hits": len(ans6.hits)})
    summary.record_edge("bin.bin" not in p1, {"edge": "s01_7_binary_skipped", "paths": p1})
    ans8 = _filter_code_hits(driver.search("project:current kind:code s01_edge_needle path:visible.txt", default_root_id=roots_multi[0], origin_project_id=pid_multi, budget="complete"))
    root_ids = {h.root_id for h in ans8.hits}
    summary.record_edge(len(ans8.hits) == 2 and len(root_ids) == 2, {"edge": "s01_8_multiroot_ids", "roots": sorted(root_ids)})


def run_s02_sidecar(
    driver: SidecarDriver,
    work_root: Path,
    *,
    base_seed: str = BASE_SEED_PRIMARY,
    instances: int = 200,
    rg_bin: str = "rg",
) -> FamilySummary:
    """Run S02 literal acceleration checks (default 200 generated instances + 3 hand-written edges)."""
    summary = FamilySummary(family_id="S02", lane="sidecar", planned=instances, edges_planned=3)
    corpus_count = max(1, min(10, instances))
    queries_per_corpus = max(1, instances // corpus_count)

    corpora: list[tuple[Path, str, str]] = []
    for c in range(corpus_count):
        spec = _make_s01_corpus(base_seed, "S02", c, 20)
        cdir = work_root / f"s02_corpus_{c:02d}"
        materialize_tree(spec, cdir)
        pid, roots = driver.attach_project([cdir])
        wait_for_catalog_ready(driver, pid)
        corpora.append((cdir, pid, roots[0]))

    for idx in range(instances):
        c = min(idx // queries_per_corpus, corpus_count - 1)
        cdir, pid, rid = corpora[c]
        q = generate_s01_query(base_seed, "S02", idx, "literal")
        ref = reference_search(cdir, q, rg_bin=rg_bin)
        ref_files = ref.matched_files()
        prod = _filter_code_hits(
            driver.search(
                f"project:current kind:code {q.pattern}",
                default_root_id=rid,
                origin_project_id=pid,
                limit=500,
                budget="complete",
            )
        )
        prod_files = {h.hit.path for h in prod.hits}
        skipped = sorted(ref_files - prod_files)
        if not skipped:
            summary.record_pass()
        elif prod.declared_gaps:
            summary.record_verdict(Verdict.PASS_DECLARED_GAP, tuple(g.reason for g in prod.declared_gaps))
        else:
            summary.record_fail({"instance": idx, "pattern": q.pattern, "skipped_files": skipped[:5]})

    # 3 hand-written S02 edges.
    edir = work_root / "s02_edges"
    edir.mkdir(parents=True, exist_ok=True)
    target = edir / "live.txt"
    target.write_text("before_edit_token\n", encoding="utf-8")
    pid, roots = driver.attach_project([edir])
    wait_for_catalog_ready(driver, pid)
    # Prime bloom filter with a first search.
    driver.search("project:current kind:code before_edit_token", default_root_id=roots[0], origin_project_id=pid, budget="complete")

    # Edge 1: Edit file on disk and search immediately before re-index.
    target.write_text("after_edit_s02_needle\n", encoding="utf-8")
    ans1 = _filter_code_hits(driver.search("project:current kind:code after_edit_s02_needle", default_root_id=roots[0], origin_project_id=pid, budget="complete"))
    summary.record_edge(any(h.hit.path == "live.txt" for h in ans1.hits), {"edge": "s02_1_edit_before_reindex", "hits": len(ans1.hits)})

    # Edge 2 (Decision 057): Unsaved editor draft with authoritative open/update/reread, unchanged disk bytes, and saved-file positive.
    try:
        saved_file = edir / "saved_control.txt"
        saved_file.write_text("saved_s02_positive_control\n", encoding="utf-8")
        disk_before_bytes = target.read_bytes()
        opened_resp = driver._transport.request(  # noqa: SLF001
            f"/v1/projects/{pid}/editor-documents",
            {"path": "live.txt", "root_id": roots[0], "client_id": "cfs-driver"},
            method="POST",
        )
        doc_id = str(opened_resp.get("id") or "")
        updated_resp = driver._transport.request(  # noqa: SLF001
            f"/v1/projects/{pid}/editor-documents/{doc_id}",
            {
                "operation_id": f"s02-draft-{int(time.time() * 1000)}",
                "client_id": "cfs-driver",
                "expected_revision": opened_resp.get("revision"),
                "content": "unsaved_draft_s02_token\n",
                "eol": "lf",
                "mixed_eol": False,
                "history_vector": None,
                "session_id": None,
            },
            method="PUT",
        )
        reread_resp: dict[str, object] = {}
        try:
            reread_resp = driver._transport.request(  # noqa: SLF001
                f"/v1/projects/{pid}/editor-documents/{doc_id}",
                None,
                method="GET",
            )
        except Exception as reread_exc:
            reread_resp = {"reread_error": str(reread_exc)}
        disk_after_bytes = target.read_bytes()
        saved_pos = _filter_code_hits(
            driver.search("project:current kind:code saved_s02_positive_control", default_root_id=roots[0], origin_project_id=pid, budget="complete")
        )
        draft_dsl = "project:current kind:code unsaved_draft_s02_token"
        ans2 = _filter_code_hits(driver.search(draft_dsl, default_root_id=roots[0], origin_project_id=pid, budget="complete"))
        summary.record_edge(
            any(h.hit.path == "live.txt" for h in ans2.hits),
            {
                "edge": "s02_2_unsaved_editor_draft",
                "hits": len(ans2.hits),
                "request_scope": {"origin_project_id": pid, "root_id": roots[0], "dsl": draft_dsl},
                "draft_open": {"id": doc_id, "revision": opened_resp.get("revision")},
                "draft_update": {"id": updated_resp.get("id"), "revision": updated_resp.get("revision"), "dirty": updated_resp.get("dirty")},
                "draft_reread": {
                    "id": reread_resp.get("id"),
                    "revision": reread_resp.get("revision"),
                    "dirty": reread_resp.get("dirty"),
                    "content": reread_resp.get("content"),
                    "reread_error": reread_resp.get("reread_error"),
                },
                "disk_unchanged": disk_before_bytes == disk_after_bytes,
                "disk_bytes_len": len(disk_after_bytes),
                "saved_control_hits": len(saved_pos.hits),
            },
        )
    except Exception as exc:
        summary.record_edge(False, {"edge": "s02_2_unsaved_editor_draft", "error": str(exc)})

    # Edge 3: Short (<3 byte) literal never skipped by 3-gram bloom.
    target.write_text("short_xy_boundary\n", encoding="utf-8")
    ans3 = _filter_code_hits(driver.search("project:current kind:code xy", default_root_id=roots[0], origin_project_id=pid, budget="complete"))
    summary.record_edge(any(h.hit.path == "live.txt" for h in ans3.hits), {"edge": "s02_3_short_literal", "hits": len(ans3.hits)})
    return summary


def run_s03_sidecar(
    driver: SidecarDriver,
    work_root: Path,
    *,
    base_seed: str = BASE_SEED_PRIMARY,
    instances: int = 100,
    searches_per_instance: int = 20,
    rg_bin: str = "rg",
) -> FamilySummary:
    """Run S03 search-after-change under a continuous atomic writer (100 instances x 20 searches + 2 edges).

    Addresses Decision 033:
    - Keeps the stable-token control (`base_alpha`) AND adds moving-result comparisons (`moving_token`).
    - Logs every atomic version transition with identifiable sequence numbers.
    - Applies strict D4 attribution in BOTH directions:
      * Extra/stale hit (path not holding the queried token in ANY version during [t_start, t_end]) -> DEFECT.
      * Missing hit (path holding the queried token in EVERY version during [t_start, t_end]) ->
        PASS_DECLARED_GAP only if an accounting freshness/coverage gap is declared, else DEFECT.
    - Deletion edge enforces D4 strictness: a stale hit for a deleted file is ALWAYS a DEFECT.
    """
    summary = FamilySummary(family_id="S03", lane="sidecar", planned=instances, edges_planned=2)
    cdir = work_root / "s03_live_tree"
    cdir.mkdir(parents=True, exist_ok=True)
    file_count = 12
    # Track exact per-file moving_token membership across atomic versions.
    state_lock = threading.Lock()
    current_moving: set[str] = {f"doc_{i:02d}.txt" for i in range(0, file_count, 2)}
    version_history: list[frozenset[str]] = [frozenset(current_moving)]
    for i in range(file_count):
        fname = f"doc_{i:02d}.txt"
        tok = "moving_token" if fname in current_moving else "other_token"
        (cdir / fname).write_text(f"base_alpha {tok} rev_0\n", encoding="utf-8")

    pid, roots = driver.attach_project([cdir])
    wait_for_catalog_ready(driver, pid)

    stop_event = threading.Event()
    write_counter = [0]

    stage_dir = work_root / "s03_stage"
    stage_dir.mkdir(parents=True, exist_ok=True)

    def _atomic_write(path: Path, text: str) -> None:
        tmp = stage_dir / f"{path.name}.tmp"
        tmp.write_text(text, encoding="utf-8")
        tmp.replace(path)

    def _writer() -> None:
        idx = 1
        while not stop_event.is_set():
            fidx = idx % file_count
            fname = f"doc_{fidx:02d}.txt"
            has_moving = (idx % 2 == 0)
            tok = "moving_token" if has_moving else "other_token"
            try:
                _atomic_write(cdir / fname, f"base_alpha {tok} rev_{idx}\n")
                with state_lock:
                    if has_moving:
                        current_moving.add(fname)
                    else:
                        current_moving.discard(fname)
                    version_history.append(frozenset(current_moving))
                    write_counter[0] = idx
            except OSError:
                pass
            idx += 1
            time.sleep(0.015)

    writer_thread = threading.Thread(target=_writer, daemon=True)
    writer_thread.start()
    freshness_reasons = frozenset(
        {"catalog_refreshing", "catalog_incomplete", "catalog_warming", "stale_inventory", "time_budget", "index_warming"}
    )
    try:
        q_stable = CodeQuerySpec(form="literal", pattern="base_alpha")
        q_moving = CodeQuerySpec(form="literal", pattern="moving_token")
        for inst in range(instances):
            inst_ok = True
            inst_gaps: list[str] = []
            for s_idx in range(searches_per_instance):
                use_moving = (s_idx % 2 == 1)
                q_spec = q_moving if use_moving else q_stable
                dsl = f"project:current kind:code {q_spec.pattern}"

                with state_lock:
                    ver_start = len(version_history) - 1
                before_ref = reference_search(cdir, q_spec, rg_bin=rg_bin).matched_files()
                prod = _filter_code_hits(
                    driver.search(
                        dsl,
                        default_root_id=roots[0],
                        origin_project_id=pid,
                        limit=200,
                        budget="interactive",
                    )
                )
                after_ref = reference_search(cdir, q_spec, rg_bin=rg_bin).matched_files()
                with state_lock:
                    ver_end = len(version_history) - 1
                    window_snaps = list(version_history[ver_start : ver_end + 1])

                if use_moving:
                    window_possible = set(before_ref) | set(after_ref)
                    window_guaranteed = set(before_ref) & set(after_ref)
                    for snap in window_snaps:
                        window_possible |= set(snap)
                        window_guaranteed &= set(snap)
                else:
                    window_possible = set(before_ref) | set(after_ref)
                    window_guaranteed = set(before_ref) & set(after_ref)

                prod_files = {h.hit.path for h in prod.hits}
                # D4 Direction 1: Extra / stale claim is ALWAYS a DEFECT (never excused by gaps).
                extra_stale = sorted(prod_files - window_possible)
                if extra_stale:
                    inst_ok = False
                    summary.record_fail(
                        {
                            "instance": inst,
                            "search": s_idx,
                            "query": q_spec.pattern,
                            "extra_stale_files": extra_stale[:5],
                            "ver_window": [ver_start, ver_end],
                            "declared_gaps": [g.reason for g in prod.declared_gaps],
                        }
                    )
                    break

                # D4 Direction 2: Missing guaranteed file requires an accounting freshness/coverage gap.
                missing = sorted(window_guaranteed - prod_files)
                if missing:
                    valid_gaps = [g.reason for g in prod.declared_gaps if g.reason in freshness_reasons]
                    if valid_gaps:
                        inst_gaps.extend(valid_gaps)
                    else:
                        inst_ok = False
                        summary.record_fail(
                            {
                                "instance": inst,
                                "search": s_idx,
                                "query": q_spec.pattern,
                                "missing_guaranteed_files": missing[:5],
                                "ver_window": [ver_start, ver_end],
                                "declared_gaps": [g.reason for g in prod.declared_gaps],
                            }
                        )
                        break
            if inst_ok:
                if inst_gaps:
                    summary.record_verdict(Verdict.PASS_DECLARED_GAP, tuple(dict.fromkeys(inst_gaps)))
                else:
                    summary.record_pass()
    finally:
        stop_event.set()
        writer_thread.join(timeout=2.0)

    # 2 hand-written S03 edges.
    e_file = cdir / "s03_edge_new.txt"
    _atomic_write(e_file, "s03_brand_new_burst_token\n")
    ans_new = _filter_code_hits(driver.search("project:current kind:code s03_brand_new_burst_token", default_root_id=roots[0], origin_project_id=pid, budget="complete"))
    valid_new_gaps = [g.reason for g in ans_new.declared_gaps if g.reason in freshness_reasons]
    edge1_ok = any(h.hit.path == "s03_edge_new.txt" for h in ans_new.hits) or bool(valid_new_gaps)
    summary.record_edge(edge1_ok, {"edge": "s03_1_create_and_search", "hits": len(ans_new.hits), "gaps": valid_new_gaps})

    # Edge 2 (D4 strictness): Deleted file MUST NOT appear as a stale hit (never excused by a gap).
    e_file.unlink(missing_ok=True)
    ans_del = _filter_code_hits(driver.search("project:current kind:code s03_brand_new_burst_token", default_root_id=roots[0], origin_project_id=pid, budget="complete"))
    edge2_ok = not any(h.hit.path == "s03_edge_new.txt" for h in ans_del.hits)
    summary.record_edge(edge2_ok, {"edge": "s03_2_delete_and_search", "stale_hits": [h.hit.path for h in ans_del.hits], "gaps": [g.reason for g in ans_del.declared_gaps]})
    return summary
