"""CF05 (F01–F06) real-sidecar family runners, D4 checkers, shrinker, and evidence writer."""

from __future__ import annotations

import argparse
from collections import Counter
from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import urllib.error
import uuid

from ..change_runner import ChangeOp, ChangeRunner, ChangeScript, WriteLog, WriteLogEntry
from ..checker import check_catalog, walk_entries_to_catalog_items
from ..checker_feed import check_change_feed
from ..checker_types import CheckResult, DeclaredGap, Verdict
from ..driver_adapters import map_sse_source_changed_to_answer
from ..driver_provenance import collect_run_provenance, file_sha256, write_evidence_manifest
from ..driver_sse import LiveSSESubscriber
from ..generators import BoundaryConfig, TreeEntrySpec, TreeSpec, load_repo_boundary_config, materialize_tree
from ..live_controls import wait_catalog_settled
from ..live_controls_feed import DRAIN_SENTINEL_REL_PATH, _has_matching_change, _is_initial_inventory_frame, _retag_write_log
from ..references import reference_walk
from ..seeds import BASE_SEED_PRIMARY, ReplaySelector
from ..shrinker import FailingInstance, shrink_instance
from .cf05_generators import (
    CF05_LEDGER_SIDECAR_COUNTS,
    F01Case,
    F02Case,
    F03Case,
    F04Case,
    F05Case,
    F06Case,
    f01_edge_cases,
    f02_edge_cases,
    f03_edge_cases,
    f04_edge_cases,
    f05_edge_cases,
    f06_edge_cases,
    generate_f01_case,
    generate_f02_case,
    generate_f03_case,
    generate_f04_case,
    generate_f05_case,
    generate_f06_case,
)
from .cf05_sidecar_harness import CF05SidecarClient, CursorSSESubscriber, start_isolated_sidecar


@dataclass
class CaseOutcome:
    case_id: str
    family: str
    is_edge: bool
    verdict: str  # PASS | PASS_DECLARED_GAP | DEFECT | UNKNOWN
    reason: str
    declared_gaps: list[str]
    elapsed_ms: int
    details: dict[str, object]

    def to_dict(self) -> dict[str, object]:
        return {
            "case_id": self.case_id,
            "family": self.family,
            "is_edge": self.is_edge,
            "verdict": self.verdict,
            "reason": self.reason,
            "declared_gaps": self.declared_gaps,
            "elapsed_ms": self.elapsed_ms,
            "details": self.details,
        }


def _reconcile_catalog_against_reference(
    client: CF05SidecarClient,
    root_dir: Path,
    project_id: str,
    root_id: str,
    boundary_config: BoundaryConfig | None,
    *,
    settle_wait_s: float = 30.0,
) -> tuple[bool, CheckResult, dict[str, object]]:
    settled, polls, summary = wait_catalog_settled(
        client.driver,
        project_id,
        root_id,
        max_wait_s=settle_wait_s,
    )
    if not settled:
        return (
            False,
            CheckResult(Verdict.UNKNOWN, "catalog", reason="catalog did not settle before deadline"),
            {"settled": False, "polls": polls, "summary": summary},
        )
    ref_entries = [
        e
        for e in reference_walk(root_dir, boundary_config=boundary_config, include_hidden=True)
        if e.kind == "file"
    ]
    ref_items = walk_entries_to_catalog_items(ref_entries, root_id=root_id)
    prod = client.driver.walk_catalog_index(project_id, root_id)
    res = check_catalog(ref_items, prod)
    return (
        res.verdict == Verdict.PASS,
        res,
        {
            "settled": True,
            "polls": polls,
            "reference_count": len(ref_items),
            "product_count": len(prod.items),
            "missing_claims": list(res.missing_claims[:20]),
            "extra_claims": list(res.extra_claims[:20]),
        },
    )


def run_f01_instance(
    client: CF05SidecarClient,
    case: F01Case,
    work_dir: Path,
    boundary_config: BoundaryConfig | None,
) -> CaseOutcome:
    t0 = time.monotonic()
    root_dir = work_dir / case.case_id
    materialize_tree(case.tree_spec, root_dir)
    sentinel_file = root_dir / DRAIN_SENTINEL_REL_PATH
    sentinel_file.parent.mkdir(parents=True, exist_ok=True)
    sentinel_file.write_bytes(b"f01 drain sentinel initial\n")

    project_id, root_ids = client.driver.attach_project([root_dir])
    root_id = root_ids[0]
    try:
        settled, _, _ = wait_catalog_settled(client.driver, project_id, root_id, max_wait_s=15.0)
        if not settled:
            return CaseOutcome(case.case_id, "F01", case.is_edge, "UNKNOWN", "initial catalog did not settle", [], int((time.monotonic() - t0) * 1000), {})

        sub = LiveSSESubscriber(client.base_url, client.token, project_id=project_id, client_id=f"f01-{case.case_id}")
        sub.start()
        try:
            if not sub.wait_ready(max_wait_s=10.0):
                return CaseOutcome(case.case_id, "F01", case.is_edge, "UNKNOWN", "SSE readiness failed", [], int((time.monotonic() - t0) * 1000), {})

            init_frames, init_seen = sub.wait_for_frames(
                lambda fs: any(_is_initial_inventory_frame(f, project_id) for f in fs),
                max_wait_s=3.0,
            )
            baseline_idx = len(init_frames) if init_seen else len(sub.frames())

            runner = ChangeRunner()
            existing_dirs_before = {
                p.relative_to(root_dir).as_posix()
                for p in root_dir.rglob("*")
                if p.is_dir()
            }
            primary_log = runner.run(root_dir, case.change_script)
            logged_entries = _retag_write_log(primary_log, root_id, seq_offset=0)
            # Record exact auxiliary paths ChangeRunner actually wrote on disk:
            # 1. atomic_replace temp siblings (.tmp.<name>.<seq>)
            # 2. newly created parent directories from mkdir(parents=True)
            next_seq = len(logged_entries) + 1
            for entry in primary_log.entries:
                if entry.op == "atomic_replace":
                    p_obj = Path(entry.path)
                    tmp_rel = (p_obj.parent / f".tmp.{p_obj.name}.{entry.seq}").as_posix().removeprefix("./")
                    logged_entries.append(
                        WriteLogEntry(
                            seq=next_seq,
                            clock_domain=entry.clock_domain,
                            timestamp_ns=entry.timestamp_ns,
                            op="delete",
                            path=tmp_rel,
                            new_path=None,
                            before_sha256=None,
                            after_sha256=None,
                            root_id=root_id,
                        )
                    )
                    next_seq += 1
            existing_dirs_after = {
                p.relative_to(root_dir).as_posix()
                for p in root_dir.rglob("*")
                if p.is_dir()
            }
            for new_dir in sorted(existing_dirs_after - existing_dirs_before):
                logged_entries.append(
                    WriteLogEntry(
                        seq=next_seq,
                        clock_domain="monotonic_ns",
                        timestamp_ns=time.monotonic_ns(),
                        op="create",
                        path=new_dir,
                        new_path=None,
                        before_sha256=None,
                        after_sha256=None,
                        root_id=root_id,
                    )
                )
                next_seq += 1

            # Write sentinel after change script so we have a causal drain boundary
            sentinel_log = runner.run(
                root_dir,
                ChangeScript(
                    selector=ReplaySelector(case.selector.base_seed, "F01_SENTINEL", case.selector.instance_index),
                    ops=(ChangeOp("edit", DRAIN_SENTINEL_REL_PATH, content=f"drain {case.case_id}\n".encode("utf-8")),),
                ),
            )
            logged_entries.extend(_retag_write_log(sentinel_log, root_id, seq_offset=len(logged_entries)))

            frames, drained = sub.wait_for_frames(
                lambda fs: _has_matching_change(fs[baseline_idx:], project_id, root_id, DRAIN_SENTINEL_REL_PATH),
                max_wait_s=15.0,
            )
        finally:
            sub.close()

        post_frames = frames[baseline_idx:]
        # External watcher (watcher.go) emits only op="write" (exists) or "delete" (absent),
        # never "create"/"rename"/"chmod". Normalize external "write" -> "change" for D4 op compatibility
        # while preserving "delete" strictly so false deletes are still flagged as defects.
        normalized_frames: list[dict[str, object]] = []
        for f in post_frames:
            if f.get("topic") == "source_changed" and isinstance(f.get("data"), dict):
                data_copy = dict(f["data"])
                raw_changes = data_copy.get("changes")
                if isinstance(raw_changes, list):
                    norm_changes = []
                    for ch in raw_changes:
                        if isinstance(ch, dict):
                            ch_copy = dict(ch)
                            if str(ch_copy.get("origin") or "") == "external" and str(ch_copy.get("op") or "") == "write":
                                ch_copy["op"] = "change"
                            norm_changes.append(ch_copy)
                    data_copy["changes"] = norm_changes
                f_copy = dict(f)
                f_copy["data"] = data_copy
                normalized_frames.append(f_copy)
            else:
                normalized_frames.append(f)

        write_log = WriteLog(clock_domain="monotonic_ns", entries=tuple(logged_entries))

        answer = map_sse_source_changed_to_answer(
            normalized_frames,
            project_id=project_id,
            default_root_id=root_id,
        )
        if not drained:
            return CaseOutcome(
                case.case_id,
                "F01",
                case.is_edge,
                "UNKNOWN",
                "drain sentinel did not arrive within 15s",
                [g.reason for g in answer.declared_gaps],
                int((time.monotonic() - t0) * 1000),
                {"observed_events": len(answer.events), "logged_ops": len(logged_entries)},
            )

        reconciled_walk = None
        ref_post_walk = None
        if any(g.reason == "resync" for g in answer.declared_gaps):
            wait_catalog_settled(client.driver, project_id, root_id, max_wait_s=20.0)
            reconciled = client.driver.walk_catalog_index(project_id, root_id)
            ref_entries = [
                e for e in reference_walk(root_dir, boundary_config=boundary_config, include_hidden=True) if e.kind == "file"
            ]
            reconciled_walk = reconciled.items
            ref_post_walk = walk_entries_to_catalog_items(ref_entries, root_id=root_id)
            answer = map_sse_source_changed_to_answer(
                normalized_frames,
                project_id=project_id,
                default_root_id=root_id,
                reconciled_walk=reconciled_walk,
                reference_post_walk=ref_post_walk,
            )

        res = check_change_feed(write_log, answer)
        walk_resp = client.get_source_walk_all(project_id)
        batch_ids = sorted({e.batch_id for e in answer.events})
        return CaseOutcome(
            case_id=case.case_id,
            family="F01",
            is_edge=case.is_edge,
            verdict=res.verdict.value,
            reason=res.reason,
            declared_gaps=list(res.declared_gaps),
            elapsed_ms=int((time.monotonic() - t0) * 1000),
            details={
                "op_count": len(case.change_script.ops),
                "observed_events": len(answer.events),
                "batch_ids": batch_ids[:10],
                "walk_files_count": len(walk_resp.get("files") or []),
                "missing_claims": list(res.missing_claims[:20]),
                "extra_claims": list(res.extra_claims[:20]),
                "wrong_claims": list(res.wrong_claims[:20]),
            },
        )
    finally:
        client.delete_project(project_id)


def _git(cwd: Path, *args: str) -> str:
    env = {
        **os.environ,
        "GIT_AUTHOR_NAME": "CF05 Runner",
        "GIT_AUTHOR_EMAIL": "cf05@example.com",
        "GIT_COMMITTER_NAME": "CF05 Runner",
        "GIT_COMMITTER_EMAIL": "cf05@example.com",
    }
    proc = subprocess.run(
        ["git", *args],
        cwd=str(cwd),
        env=env,
        capture_output=True,
        text=True,
        check=True,
    )
    return proc.stdout.strip()


def run_f02_instance(
    client: CF05SidecarClient,
    case: F02Case,
    work_dir: Path,
    boundary_config: BoundaryConfig | None,
) -> CaseOutcome:
    t0 = time.monotonic()
    root_dir = work_dir / case.case_id
    root_dir.mkdir(parents=True, exist_ok=True)

    # Populate initial tree
    init_paths: list[Path] = []
    if case.storm_kind == "branch_rewrite":
        _git(root_dir, "init", "-b", "main")
        for i in range(case.initial_files):
            p = root_dir / f"pkg_{i % 32:02d}" / f"f_{i:05d}.txt"
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_bytes(f"main content {i}\n".encode("utf-8"))
            init_paths.append(p)
        _git(root_dir, "add", ".")
        _git(root_dir, "commit", "-m", "initial main")
        _git(root_dir, "checkout", "-b", "storm-branch")
        for i in range(case.initial_files):
            if i % 3 == 0:
                init_paths[i].unlink()
            else:
                init_paths[i].write_bytes(f"storm-branch rewritten {i}\n".encode("utf-8"))
        for i in range(case.initial_files // 3):
            p = root_dir / f"storm_new_{i % 16:02d}" / f"new_{i:05d}.txt"
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_bytes(f"storm new {i}\n".encode("utf-8"))
        _git(root_dir, "add", "-A")
        _git(root_dir, "commit", "-m", "storm branch")
        _git(root_dir, "checkout", "main")
    else:
        for i in range(case.initial_files):
            p = root_dir / f"dir_{i % 32:02d}" / f"item_{i:05d}.txt"
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_bytes(f"initial {i}\n".encode("utf-8"))
            init_paths.append(p)

    sentinel = root_dir / DRAIN_SENTINEL_REL_PATH
    sentinel.parent.mkdir(parents=True, exist_ok=True)
    sentinel.write_bytes(b"f02 sentinel init\n")

    project_id, root_ids = client.driver.attach_project([root_dir])
    root_id = root_ids[0]
    try:
        settled, _, _ = wait_catalog_settled(client.driver, project_id, root_id, max_wait_s=45.0)
        if not settled:
            return CaseOutcome(case.case_id, "F02", case.is_edge, "UNKNOWN", "initial catalog did not settle", [], int((time.monotonic() - t0) * 1000), {})

        sub = LiveSSESubscriber(client.base_url, client.token, project_id=project_id, client_id=f"f02-{case.case_id}")
        sub.start()
        try:
            if not sub.wait_ready(max_wait_s=10.0):
                return CaseOutcome(case.case_id, "F02", case.is_edge, "UNKNOWN", "SSE readiness failed", [], int((time.monotonic() - t0) * 1000), {})
            baseline_idx = len(sub.frames())

            # Execute storm!
            if case.storm_kind == "mass_create":
                for i in range(case.write_count):
                    p = root_dir / f"storm_{i % 64:02d}" / f"c_{i:05d}.txt"
                    p.parent.mkdir(parents=True, exist_ok=True)
                    p.write_bytes(f"created {i}\n".encode("utf-8"))
            elif case.storm_kind == "mass_rename":
                dst_root = root_dir / "moved_storm"
                dst_root.mkdir(parents=True, exist_ok=True)
                for i, p in enumerate(init_paths[: case.write_count]):
                    dst = dst_root / f"m_{i % 64:02d}" / p.name
                    dst.parent.mkdir(parents=True, exist_ok=True)
                    os.replace(p, dst)
            elif case.storm_kind == "mass_delete":
                for p in init_paths[: case.write_count]:
                    if p.exists():
                        p.unlink()
            elif case.storm_kind == "branch_rewrite":
                _git(root_dir, "checkout", "storm-branch")
            elif case.storm_kind == "overflow_fanout":
                for i in range(case.write_count):
                    d = root_dir / f"fan_{i // 10:04d}"
                    d.mkdir(parents=True, exist_ok=True)
                    (d / f"f_{i:05d}.txt").write_bytes(f"fanout {i}\n".encode("utf-8"))

            sentinel.write_bytes(f"f02 sentinel after {case.case_id}\n".encode("utf-8"))

            frames, arrived = sub.wait_for_frames(
                lambda fs: any(
                    f.get("topic") == "source_changed"
                    and isinstance(f.get("data"), dict)
                    and str(f["data"].get("project_id") or "") == project_id
                    and (
                        f["data"].get("resync") is True
                        or _has_matching_change([f], project_id, root_id, DRAIN_SENTINEL_REL_PATH)
                    )
                    for f in fs[baseline_idx:]
                ),
                max_wait_s=60.0,
            )
        finally:
            sub.close()

        post_frames = frames[baseline_idx:]
        resync_seen = any(
            f.get("topic") == "source_changed"
            and isinstance(f.get("data"), dict)
            and f["data"].get("resync") is True
            for f in post_frames
        )

        # Wait briefly for watcher debounce + reconciliation to trigger catalog refresh
        time.sleep(0.5)
        ok, cat_res, cat_details = _reconcile_catalog_against_reference(
            client, root_dir, project_id, root_id, boundary_config, settle_wait_s=90.0
        )
        gaps = ["resync"] if resync_seen else []
        verdict = (
            ("PASS_DECLARED_GAP" if resync_seen else "PASS")
            if ok
            else cat_res.verdict.value
        )
        return CaseOutcome(
            case_id=case.case_id,
            family="F02",
            is_edge=case.is_edge,
            verdict=verdict,
            reason="" if ok else f"post-storm catalog mismatch: {cat_res.reason}",
            declared_gaps=gaps,
            elapsed_ms=int((time.monotonic() - t0) * 1000),
            details={
                "storm_kind": case.storm_kind,
                "write_count": case.write_count,
                "sse_arrived": arrived,
                "resync_seen": resync_seen,
                "post_frame_count": len(post_frames),
                "reconcile": cat_details,
            },
        )
    finally:
        client.delete_project(project_id)


def run_f03_instance(
    client: CF05SidecarClient,
    case: F03Case,
    work_dir: Path,
    boundary_config: BoundaryConfig | None,
) -> CaseOutcome:
    t0 = time.monotonic()
    root_dir = work_dir / case.case_id
    materialize_tree(case.tree_spec, root_dir)

    # Attach project and DO NOT wait for catalog settlement!
    project_id, root_ids = client.driver.attach_project([root_dir])
    root_id = root_ids[0]
    try:
        # Immediately open SSE stream and editor tab, then run outside edits during first walk
        sub = LiveSSESubscriber(client.base_url, client.token, project_id=project_id, client_id=f"f03-{case.case_id}")
        sub.start()
        try:
            sub.wait_ready(max_wait_s=5.0)
            doc = client.open_editor_document(project_id, root_id, case.open_tab_path, client_id=f"f03-{case.case_id}")
            doc_id = str(doc.get("id") or "")

            runner = ChangeRunner()
            write_log = runner.run(root_dir, case.change_script)
            expected_tab_bytes = (root_dir / case.open_tab_path).read_bytes()
            expected_tab_sha = hashlib.sha256(expected_tab_bytes).hexdigest()

            # Wait briefly for watcher debounce + catalog settle after edits
            time.sleep(0.3)
            ok_cat, cat_res, cat_details = _reconcile_catalog_against_reference(
                client, root_dir, project_id, root_id, boundary_config, settle_wait_s=25.0
            )
            # Re-observe editor document to confirm outside edit reached open tab (compare base_sha256)
            observed_doc = client.observe_editor_document(project_id, doc_id) if doc_id else {}
            doc_sha = str(observed_doc.get("base_sha256") or "")
            if not doc_sha and doc_id:
                reopened = client.open_editor_document(project_id, root_id, case.open_tab_path, client_id=f"f03-check-{case.case_id}")
                doc_sha = str(reopened.get("base_sha256") or "")

            frames = sub.frames()
        finally:
            sub.close()

        tab_matched = doc_sha == expected_tab_sha
        walk_resp = client.get_source_walk_all(project_id)
        walk_paths = {str(f.get("path") or "") for f in (walk_resp.get("files") or []) if isinstance(f, dict)}
        tab_in_history = case.open_tab_path in walk_paths

        reasons: list[str] = []
        if not ok_cat:
            reasons.append(f"settled catalog mismatch: {cat_res.reason}")
        if not tab_matched:
            reasons.append("open editor tab did not reflect outside edit during first walk")

        verdict = "PASS" if not reasons else "DEFECT"
        return CaseOutcome(
            case_id=case.case_id,
            family="F03",
            is_edge=case.is_edge,
            verdict=verdict,
            reason="; ".join(reasons),
            declared_gaps=[],
            elapsed_ms=int((time.monotonic() - t0) * 1000),
            details={
                "op_count": len(write_log.entries),
                "tab_matched": tab_matched,
                "tab_in_history": tab_in_history,
                "sse_frames": len(frames),
                "reconcile": cat_details,
            },
        )
    finally:
        client.delete_project(project_id)


def run_f04_instance(
    client: CF05SidecarClient,
    case: F04Case,
    work_dir: Path,
    boundary_config: BoundaryConfig | None,
) -> CaseOutcome:
    t0 = time.monotonic()
    root_dir = work_dir / case.case_id
    root_dir.mkdir(parents=True, exist_ok=True)
    seed_file = root_dir / "seed.txt"
    seed_bytes = f"seed content {case.case_id}\n".encode("utf-8")
    seed_file.write_bytes(seed_bytes)
    seed_sha = hashlib.sha256(seed_bytes).hexdigest()
    sentinel = root_dir / DRAIN_SENTINEL_REL_PATH
    sentinel.parent.mkdir(parents=True, exist_ok=True)
    sentinel.write_bytes(b"f04 sentinel init\n")

    project_id, root_ids = client.driver.attach_project([root_dir])
    root_id = root_ids[0]
    try:
        settled, _, _ = wait_catalog_settled(client.driver, project_id, root_id, max_wait_s=15.0)
        if not settled:
            return CaseOutcome(case.case_id, "F04", case.is_edge, "UNKNOWN", "initial catalog did not settle", [], int((time.monotonic() - t0) * 1000), {})

        sub = LiveSSESubscriber(client.base_url, client.token, project_id=project_id, client_id=f"f04-{case.case_id}")
        sub.start()
        try:
            if not sub.wait_ready(max_wait_s=10.0):
                return CaseOutcome(case.case_id, "F04", case.is_edge, "UNKNOWN", "SSE readiness failed", [], int((time.monotonic() - t0) * 1000), {})
            init_frames, init_seen = sub.wait_for_frames(
                lambda fs: any(_is_initial_inventory_frame(f, project_id) for f in fs),
                max_wait_s=3.0,
            )
            baseline_idx = len(init_frames) if init_seen else len(sub.frames())

            expected_host_events: dict[str, int] = {}
            expected_effects_per_path: dict[str, int] = {}

            if case.route == "create":
                client.create_source_entry(project_id, root_id, "created.txt", kind="file")
                expected_host_events["created.txt"] = 1
                expected_effects_per_path["created.txt"] = 1
            elif case.route == "write":
                client.write_source_file(project_id, root_id, "seed.txt", f"updated {case.case_id}\n", seed_sha)
                expected_host_events["seed.txt"] = 1
                expected_effects_per_path["seed.txt"] = 1
            elif case.route in ("rename", "rename_no_dup"):
                client.rename_source_entry(project_id, root_id, "seed.txt", "renamed.txt")
                expected_host_events["renamed.txt"] = 1
                expected_effects_per_path["renamed.txt"] = 1
            elif case.route == "copy":
                client.copy_source_entry(project_id, root_id, "seed.txt", "copied.txt")
                expected_host_events["copied.txt"] = 1
                expected_effects_per_path["copied.txt"] = 1
            elif case.route == "delete":
                client.delete_source_entry(project_id, root_id, "seed.txt")
                expected_host_events["seed.txt"] = 1
                expected_effects_per_path["seed.txt"] = 1
            elif case.route == "write_then_outside":
                client.write_source_file(project_id, root_id, "seed.txt", f"host write {case.case_id}\n", seed_sha)
                # Immediately write distinct bytes externally (<100ms) -> must NOT be suppressed as echo!
                seed_file.write_bytes(f"outside overwrite {case.case_id}\n".encode("utf-8"))
                expected_host_events["seed.txt"] = 2
                expected_effects_per_path["seed.txt"] = 2
            elif case.route == "copy_then_delete":
                client.copy_source_entry(project_id, root_id, "seed.txt", "copied.txt")
                client.delete_source_entry(project_id, root_id, "copied.txt")
                expected_host_events["copied.txt"] = 2
                expected_effects_per_path["copied.txt"] = 2

            # Wait past watcher debounce window + sentinel drain to catch any duplicate echo!
            time.sleep(0.35)
            sentinel.write_bytes(f"f04 drain {case.case_id}\n".encode("utf-8"))
            frames, drained = sub.wait_for_frames(
                lambda fs: _has_matching_change(fs[baseline_idx:], project_id, root_id, DRAIN_SENTINEL_REL_PATH),
                max_wait_s=10.0,
            )
        finally:
            sub.close()

        post_frames = frames[baseline_idx:]
        observed_counts: Counter[str] = Counter()
        for f in post_frames:
            if f.get("topic") != "source_changed":
                continue
            data = f.get("data")
            if not isinstance(data, dict) or str(data.get("project_id") or "") != project_id:
                continue
            for ch in data.get("changes") or []:
                if not isinstance(ch, dict):
                    continue
                p = str(ch.get("path") or "").removeprefix("./")
                if p and p != DRAIN_SENTINEL_REL_PATH:
                    observed_counts[p] += 1

        walk_resp = client.get_source_walk_all(project_id)
        effect_counts: Counter[str] = Counter()
        for wf in walk_resp.get("files") or []:
            if not isinstance(wf, dict):
                continue
            p = str(wf.get("path") or "").removeprefix("./")
            if p != DRAIN_SENTINEL_REL_PATH:
                effect_counts[p] += len(wf.get("effects") or [])

        defects: list[str] = []
        for path, want in expected_host_events.items():
            got = observed_counts.get(path, 0)
            if case.route == "write_then_outside":
                # Debounce may coalesce or deliver 2; must at least see >=1 and disk/effect updated
                if got < 1:
                    defects.append(f"sse missing event for {path}: got={got}")
            elif got != want:
                defects.append(f"sse count mismatch for {path}: got={got} want={want}")

        for path, want_eff in expected_effects_per_path.items():
            got_eff = effect_counts.get(path, 0)
            if got_eff > want_eff:
                defects.append(f"duplicate effect for {path}: got={got_eff} want={want_eff}")
            elif got_eff == 0:
                defects.append(f"missing effect in source/walk for {path}: got=0 want={want_eff}")

        verdict = "PASS" if not defects else "DEFECT"
        return CaseOutcome(
            case_id=case.case_id,
            family="F04",
            is_edge=case.is_edge,
            verdict=verdict,
            reason="; ".join(defects),
            declared_gaps=[],
            elapsed_ms=int((time.monotonic() - t0) * 1000),
            details={
                "route": case.route,
                "drained": drained,
                "observed_sse_counts": dict(observed_counts),
                "walk_effect_counts": dict(effect_counts),
            },
        )
    finally:
        client.delete_project(project_id)


def run_f05_instance(
    client: CF05SidecarClient,
    case: F05Case,
    work_dir: Path,
    boundary_config: BoundaryConfig | None,
) -> CaseOutcome:
    t0 = time.monotonic()
    root_dir = work_dir / case.case_id / "repo"
    remote_dir = work_dir / case.case_id / "remote.git"
    root_dir.mkdir(parents=True, exist_ok=True)
    remote_dir.mkdir(parents=True, exist_ok=True)

    _git(remote_dir, "init", "--bare", "-b", "main")
    _git(root_dir, "init", "-b", "main")
    _git(root_dir, "remote", "add", "origin", str(remote_dir))
    (root_dir / "app.txt").write_bytes(b"v0 main\n")
    (root_dir / "shared.txt").write_bytes(b"shared v0\n")
    _git(root_dir, "add", ".")
    _git(root_dir, "commit", "-m", "initial commit")
    _git(root_dir, "push", "-u", "origin", "main")

    # Create a side branch with distinct file changes for checkout/rebase
    _git(root_dir, "checkout", "-b", "feature")
    (root_dir / "app.txt").write_bytes(b"feature v1\n")
    (root_dir / "feature_only.txt").write_bytes(b"only on feature\n")
    _git(root_dir, "add", ".")
    _git(root_dir, "commit", "-m", "feature commit")
    _git(root_dir, "checkout", "main")

    project_id, root_ids = client.driver.attach_project([root_dir])
    root_id = root_ids[0]
    try:
        settled, _, _ = wait_catalog_settled(client.driver, project_id, root_id, max_wait_s=15.0)
        if not settled:
            return CaseOutcome(case.case_id, "F05", case.is_edge, "UNKNOWN", "initial catalog did not settle", [], int((time.monotonic() - t0) * 1000), {})

        sub = LiveSSESubscriber(client.base_url, client.token, project_id=project_id, client_id=f"f05-{case.case_id}")
        sub.start()
        try:
            if not sub.wait_ready(max_wait_s=10.0):
                return CaseOutcome(case.case_id, "F05", case.is_edge, "UNKNOWN", "SSE readiness failed", [], int((time.monotonic() - t0) * 1000), {})
            init_frames, init_seen = sub.wait_for_frames(
                lambda fs: any(_is_initial_inventory_frame(f, project_id) for f in fs),
                max_wait_s=4.0,
            )
            baseline_idx = len(init_frames) if init_seen else len(sub.frames())

            executed_moves = 0
            for step_idx, move in enumerate(case.movements):
                if move == "checkout":
                    cur = _git(root_dir, "rev-parse", "--abbrev-ref", "HEAD")
                    target = "feature" if cur == "main" else "main"
                    _git(root_dir, "checkout", target)
                    executed_moves += 1
                elif move == "commit":
                    (root_dir / "commit_log.txt").write_bytes(f"commit step {step_idx}\n".encode("utf-8"))
                    _git(root_dir, "add", "commit_log.txt")
                    _git(root_dir, "commit", "-m", f"commit step {step_idx}")
                    executed_moves += 1
                elif move == "empty_commit":
                    _git(root_dir, "commit", "--allow-empty", "-m", f"empty step {step_idx}")
                    executed_moves += 1
                elif move == "rebase":
                    _git(root_dir, "checkout", "main")
                    (root_dir / "shared.txt").write_bytes(f"main rebase base {step_idx}\n".encode("utf-8"))
                    _git(root_dir, "add", "shared.txt")
                    _git(root_dir, "commit", "-m", f"main base {step_idx}")
                    _git(root_dir, "checkout", "feature")
                    _git(root_dir, "rebase", "main")
                    executed_moves += 1
                elif move == "stash_cycle":
                    (root_dir / "app.txt").write_bytes(f"stashed edit {step_idx}\n".encode("utf-8"))
                    _git(root_dir, "stash", "push", "-m", f"stash {step_idx}")
                    _git(root_dir, "stash", "pop")
                    _git(root_dir, "checkout", "--", "app.txt")
                    executed_moves += 1
                elif move == "pull":
                    # Sync local main to remote first, push a new commit from a temporary clone, then ff-pull
                    _git(root_dir, "checkout", "main")
                    _git(root_dir, "push", "--force", "origin", "main")
                    pusher = work_dir / case.case_id / f"pusher_{step_idx}"
                    _git(work_dir / case.case_id, "clone", str(remote_dir), str(pusher))
                    (pusher / "remote_file.txt").write_bytes(f"from remote {step_idx}\n".encode("utf-8"))
                    _git(pusher, "add", "remote_file.txt")
                    _git(pusher, "commit", "-m", f"remote commit {step_idx}")
                    _git(pusher, "push", "origin", "main")
                    _git(root_dir, "pull", "--ff-only", "origin", "main")
                    executed_moves += 1
                elif move == "reset_hard":
                    (root_dir / "reset_tmp.txt").write_bytes(f"to be reset {step_idx}\n".encode("utf-8"))
                    _git(root_dir, "add", "reset_tmp.txt")
                    _git(root_dir, "commit", "-m", f"temp before reset {step_idx}")
                    _git(root_dir, "reset", "--hard", "HEAD~1")
                    executed_moves += 1
                # Small pause between movements so watcher/refwatch observes distinct transitions
                time.sleep(0.15)

            frames, git_event_seen = sub.wait_for_frames(
                lambda fs: any(
                    f.get("topic") == "source_changed"
                    and isinstance(f.get("data"), dict)
                    and str(f["data"].get("project_id") or "") == project_id
                    and (f["data"].get("git_changed") is True or f["data"].get("resync") is True)
                    for f in fs[baseline_idx:]
                ),
                max_wait_s=10.0,
            )
            time.sleep(0.3)
        finally:
            sub.close()

        # Verify settled catalog matches independent git / reference_walk
        ok_cat, cat_res, cat_details = _reconcile_catalog_against_reference(
            client, root_dir, project_id, root_id, boundary_config, settle_wait_s=20.0
        )
        walk_resp = client.get_source_walk_all(project_id)
        git_changes = walk_resp.get("git_changes") or []
        git_ids = {str(g.get("id") or "") for g in git_changes if isinstance(g, dict)}
        effects_with_git_ref = 0
        invalid_git_refs = 0
        for wf in walk_resp.get("files") or []:
            if not isinstance(wf, dict):
                continue
            for eff in wf.get("effects") or []:
                if not isinstance(eff, dict):
                    continue
                gcid = eff.get("git_change_id")
                if gcid:
                    effects_with_git_ref += 1
                    if str(gcid) not in git_ids:
                        invalid_git_refs += 1

        # Independent git ls-tree HEAD check
        tracked_files = sorted(p for p in _git(root_dir, "ls-tree", "-r", "--name-only", "HEAD").splitlines() if p)
        missing_on_disk = [p for p in tracked_files if not (root_dir / p).is_file()]

        defects: list[str] = []
        if not ok_cat:
            defects.append(f"settled catalog mismatch: {cat_res.reason}")
        if missing_on_disk:
            defects.append(f"tracked files missing on disk: {missing_on_disk[:5]}")
        if invalid_git_refs > 0:
            defects.append(f"effects reference unknown git_change_id count={invalid_git_refs}")
        if not git_event_seen and len(git_changes) == 0:
            defects.append("neither git_changed SSE signal nor git_changes walk entry observed")

        verdict = "PASS" if not defects else "DEFECT"
        return CaseOutcome(
            case_id=case.case_id,
            family="F05",
            is_edge=case.is_edge,
            verdict=verdict,
            reason="; ".join(defects),
            declared_gaps=[],
            elapsed_ms=int((time.monotonic() - t0) * 1000),
            details={
                "movements": list(case.movements),
                "executed_moves": executed_moves,
                "git_event_seen": git_event_seen,
                "git_changes_count": len(git_changes),
                "effects_with_git_ref": effects_with_git_ref,
                "tracked_count": len(tracked_files),
                "reconcile": cat_details,
            },
        )
    finally:
        client.delete_project(project_id)


def run_f06_instance(
    client: CF05SidecarClient,
    case: F06Case,
    work_dir: Path,
    boundary_config: BoundaryConfig | None,
) -> CaseOutcome:
    t0 = time.monotonic()
    root_dir = work_dir / case.case_id
    root_dir.mkdir(parents=True, exist_ok=True)
    target = root_dir / "burst.txt"
    target.write_bytes(b"initial burst\n")

    project_id, root_ids = client.driver.attach_project([root_dir])
    root_id = root_ids[0]
    try:
        settled, _, _ = wait_catalog_settled(client.driver, project_id, root_id, max_wait_s=15.0)
        if not settled:
            return CaseOutcome(case.case_id, "F06", case.is_edge, "UNKNOWN", "initial catalog did not settle", [], int((time.monotonic() - t0) * 1000), {})

        subs: list[CursorSSESubscriber] = []
        try:
            for cidx in range(case.client_count):
                s = CursorSSESubscriber(
                    client.base_url,
                    client.token,
                    project_id=project_id,
                    client_id=f"f06-{case.case_id}-c{cidx}",
                )
                s.start()
                if not s.wait_ready(max_wait_s=10.0):
                    return CaseOutcome(case.case_id, "F06", case.is_edge, "UNKNOWN", f"client {cidx} SSE failed", [], int((time.monotonic() - t0) * 1000), {})
                subs.append(s)

            defects: list[str] = []
            # 1. Test stale/invalid cursor continuity gap
            stale_status = None
            fresh_cursor = ""
            if case.test_stale_cursor:
                stale_status, stale_body = client.probe_sse_cursor(
                    project_id,
                    after="invalid-stale-cursor.badmac",
                    client_id=f"f06-{case.case_id}-stale",
                )
                if stale_status != 409:
                    defects.append(f"stale cursor returned HTTP {stale_status}, want 409 event_replay_unavailable")
                else:
                    err_obj = stale_body.get("error") if isinstance(stale_body.get("error"), dict) else stale_body
                    details_obj = err_obj.get("details") if isinstance(err_obj, dict) else {}
                    if isinstance(details_obj, dict):
                        fresh_cursor = str(details_obj.get("event_cursor") or "")
                    if not fresh_cursor:
                        defects.append("409 event_replay_unavailable omitted fresh event_cursor")

            # 2. Test hidden surface (unsubscribe -> 0 wakes during burst -> reconnect)
            hidden_wakes_while_closed = 0
            if case.test_hidden_surface and subs:
                hidden = subs[-1]
                before_close = len(hidden.frames())
                hidden.close()
                target.write_bytes(f"hidden burst {case.case_id}\n".encode("utf-8"))
                # Wait for remaining active subscriber (if any) to see the burst
                if len(subs) > 1:
                    subs[0].wait_for_frames(
                        lambda fs: _has_matching_change(fs, project_id, root_id, "burst.txt"),
                        max_wait_s=8.0,
                    )
                else:
                    time.sleep(0.3)
                hidden_wakes_while_closed = len(hidden.frames()) - before_close
                if hidden_wakes_while_closed != 0:
                    defects.append(f"hidden surface received {hidden_wakes_while_closed} wakes after close")
                # Re-show (reconnect)
                reopened = CursorSSESubscriber(
                    client.base_url,
                    client.token,
                    project_id=project_id,
                    client_id=f"f06-{case.case_id}-reopened",
                    after=fresh_cursor,
                )
                reopened.start()
                if not reopened.wait_ready(max_wait_s=10.0):
                    defects.append("reopened surface failed to reconnect")
                subs[-1] = reopened

            # 3. Perform drop/reconnect cycles mid-burst
            drops_done = 0
            for d in range(case.drop_count):
                victim_idx = d % len(subs)
                cursor = subs[victim_idx].last_cursor()
                subs[victim_idx].close()
                if d < case.burst_writes:
                    (root_dir / f"burst_{d:02d}.txt").write_bytes(f"burst payload {d}\n".encode("utf-8"))
                reconn = CursorSSESubscriber(
                    client.base_url,
                    client.token,
                    project_id=project_id,
                    client_id=f"f06-{case.case_id}-c{victim_idx}-r{d}",
                    after=cursor,
                )
                reconn.start()
                if not reconn.wait_ready(max_wait_s=10.0):
                    # If cursor expired (409), reconnect fresh (revalidation)
                    if reconn.http_status == 409:
                        reconn.close()
                        reconn = CursorSSESubscriber(
                            client.base_url,
                            client.token,
                            project_id=project_id,
                            client_id=f"f06-{case.case_id}-c{victim_idx}-r{d}-fresh",
                        )
                        reconn.start()
                        if not reconn.wait_ready(max_wait_s=10.0):
                            defects.append(f"reconnect fresh failed on drop {d}")
                    else:
                        defects.append(f"reconnect failed on drop {d}: status={reconn.http_status}")
                subs[victim_idx] = reconn
                drops_done += 1

            # Final sentinel write — ALL currently connected subscribers must wake!
            final_marker = root_dir / "final_wake.txt"
            final_marker.write_bytes(f"final wake {case.case_id}\n".encode("utf-8"))
            wakes_per_client: list[int] = []
            for idx_s, s in enumerate(subs):
                fs, ok_w = s.wait_for_frames(
                    lambda frames: _has_matching_change(frames, project_id, root_id, "final_wake.txt"),
                    max_wait_s=10.0,
                )
                wakes_per_client.append(len(fs))
                if not ok_w:
                    defects.append(f"subscriber {idx_s} missed final_wake.txt after reconnects")
        finally:
            for s in subs:
                s.close()

        verdict = "PASS" if not defects else "DEFECT"
        return CaseOutcome(
            case_id=case.case_id,
            family="F06",
            is_edge=case.is_edge,
            verdict=verdict,
            reason="; ".join(defects),
            declared_gaps=[],
            elapsed_ms=int((time.monotonic() - t0) * 1000),
            details={
                "client_count": case.client_count,
                "drop_count": drops_done,
                "stale_cursor_status": stale_status,
                "hidden_wakes_while_closed": hidden_wakes_while_closed,
                "wakes_per_client": wakes_per_client,
            },
        )
    finally:
        client.delete_project(project_id)


def _run_guarded(
    family: str,
    case_id: str,
    is_edge: bool,
    fn,
) -> CaseOutcome:
    t0 = time.monotonic()
    try:
        return fn()
    except urllib.error.HTTPError as err:
        body = ""
        try:
            body = err.read().decode("utf-8", errors="replace")[:500]
        except Exception:
            pass
        return CaseOutcome(
            case_id=case_id,
            family=family,
            is_edge=is_edge,
            verdict="DEFECT",
            reason=f"HTTP {err.code}: {err.reason}",
            declared_gaps=[],
            elapsed_ms=int((time.monotonic() - t0) * 1000),
            details={"http_status": err.code, "http_body": body},
        )
    except Exception as exc:
        return CaseOutcome(
            case_id=case_id,
            family=family,
            is_edge=is_edge,
            verdict="UNKNOWN",
            reason=f"harness/runtime exception: {type(exc).__name__}: {exc}",
            declared_gaps=[],
            elapsed_ms=int((time.monotonic() - t0) * 1000),
            details={"exception": repr(exc)},
        )


def run_family_suite(
    family: str,
    client: CF05SidecarClient,
    work_dir: Path,
    boundary_config: BoundaryConfig | None,
    *,
    base_seed: str = BASE_SEED_PRIMARY,
    max_instances: int | None = None,
) -> list[CaseOutcome]:
    fam_dir = work_dir / family
    fam_dir.mkdir(parents=True, exist_ok=True)
    count = CF05_LEDGER_SIDECAR_COUNTS[family]
    if max_instances is not None:
        count = min(count, max_instances)

    outcomes: list[CaseOutcome] = []
    if family == "F01":
        for case in f01_edge_cases(base_seed):
            outcomes.append(_run_guarded("F01", case.case_id, case.is_edge, lambda c=case: run_f01_instance(client, c, fam_dir, boundary_config)))
        for i in range(count):
            case = generate_f01_case(base_seed, i)
            outcomes.append(_run_guarded("F01", case.case_id, case.is_edge, lambda c=case: run_f01_instance(client, c, fam_dir, boundary_config)))
    elif family == "F02":
        for case in f02_edge_cases(base_seed):
            outcomes.append(_run_guarded("F02", case.case_id, case.is_edge, lambda c=case: run_f02_instance(client, c, fam_dir, boundary_config)))
        for i in range(count):
            case = generate_f02_case(base_seed, i)
            outcomes.append(_run_guarded("F02", case.case_id, case.is_edge, lambda c=case: run_f02_instance(client, c, fam_dir, boundary_config)))
    elif family == "F03":
        for case in f03_edge_cases(base_seed):
            outcomes.append(_run_guarded("F03", case.case_id, case.is_edge, lambda c=case: run_f03_instance(client, c, fam_dir, boundary_config)))
        for i in range(count):
            case = generate_f03_case(base_seed, i)
            outcomes.append(_run_guarded("F03", case.case_id, case.is_edge, lambda c=case: run_f03_instance(client, c, fam_dir, boundary_config)))
    elif family == "F04":
        for case in f04_edge_cases(base_seed):
            outcomes.append(_run_guarded("F04", case.case_id, case.is_edge, lambda c=case: run_f04_instance(client, c, fam_dir, boundary_config)))
        for i in range(count):
            case = generate_f04_case(base_seed, i)
            outcomes.append(_run_guarded("F04", case.case_id, case.is_edge, lambda c=case: run_f04_instance(client, c, fam_dir, boundary_config)))
    elif family == "F05":
        for case in f05_edge_cases(base_seed):
            outcomes.append(_run_guarded("F05", case.case_id, case.is_edge, lambda c=case: run_f05_instance(client, c, fam_dir, boundary_config)))
        for i in range(count):
            case = generate_f05_case(base_seed, i)
            outcomes.append(_run_guarded("F05", case.case_id, case.is_edge, lambda c=case: run_f05_instance(client, c, fam_dir, boundary_config)))
    elif family == "F06":
        for case in f06_edge_cases(base_seed):
            outcomes.append(_run_guarded("F06", case.case_id, case.is_edge, lambda c=case: run_f06_instance(client, c, fam_dir, boundary_config)))
        for i in range(count):
            case = generate_f06_case(base_seed, i)
            outcomes.append(_run_guarded("F06", case.case_id, case.is_edge, lambda c=case: run_f06_instance(client, c, fam_dir, boundary_config)))
    else:
        raise ValueError(f"unsupported CF05 family: {family}")

    return outcomes


def summarize_outcomes(outcomes: list[CaseOutcome]) -> dict[str, object]:
    by_verdict = Counter(o.verdict for o in outcomes)
    gap_counts = Counter(g for o in outcomes for g in o.declared_gaps)
    return {
        "total": len(outcomes),
        "generated": sum(1 for o in outcomes if not o.is_edge),
        "edges": sum(1 for o in outcomes if o.is_edge),
        "passed": by_verdict["PASS"] + by_verdict["PASS_DECLARED_GAP"],
        "pass_clean": by_verdict["PASS"],
        "pass_declared_gap": by_verdict["PASS_DECLARED_GAP"],
        "defects": by_verdict["DEFECT"],
        "unknown": by_verdict["UNKNOWN"],
        "declared_gap_counts": dict(sorted(gap_counts.items())),
        "defect_cases": [o.to_dict() for o in outcomes if o.verdict == "DEFECT"][:25],
        "unknown_cases": [o.to_dict() for o in outcomes if o.verdict == "UNKNOWN"][:25],
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="cf05_change_feed")
    parser.add_argument("--families", default="F01,F03,F04,F05,F06", help="Comma-separated CF05 families")
    parser.add_argument("--seed", default=BASE_SEED_PRIMARY, help="Base seed")
    parser.add_argument("--binary", required=True, help="Path to D1 lycaon-dev binary")
    parser.add_argument("--d1-worktree", required=True, help="Path to D1 worktree (cwd for launch)")
    parser.add_argument("--repo-root", default="", help="Harness repo root for git provenance")
    parser.add_argument("--out-dir", required=True, help="Output directory for evidence & manifest")
    parser.add_argument("--rg-bin", default="rg", help="Path to ripgrep 15.2.0")
    parser.add_argument("--max-instances", type=int, default=None, help="Optional cap on generated instances per family")
    args = parser.parse_args(argv)

    families = [f.strip().upper() for f in args.families.split(",") if f.strip()]
    out_dir = Path(args.out_dir).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)
    d1_worktree = Path(args.d1_worktree).resolve()
    repo_root = Path(args.repo_root).resolve() if args.repo_root else Path(__file__).resolve().parents[4]
    boundary_config = load_repo_boundary_config(d1_worktree)

    with tempfile.TemporaryDirectory(prefix="cfs-cf05-") as tmp:
        tmp_path = Path(tmp)
        provenance = collect_run_provenance(
            harness_repo=repo_root,
            work_dir=tmp_path,
            binary_path=Path(args.binary),
            d1_commit="3bb1464d22dd0a38a149d37610810e7a0324e8cf",
            d1_tree="a05c37487b1254b32be0640bcf47c0d52172f088",
            rg_bin=args.rg_bin,
        )
        sidecar = start_isolated_sidecar(
            binary_path=Path(args.binary),
            d1_worktree=d1_worktree,
            state_dir=tmp_path / "sidecar",
            harness_commit=provenance.harness_commit,
            harness_tree=provenance.harness_tree,
        )
        try:
            receipt_path = out_dir / "launch_receipt.json"
            receipt_path.write_text(
                json.dumps(sidecar.launch_receipt(), indent=2, sort_keys=True) + "\n",
                encoding="utf-8",
            )
            client = CF05SidecarClient(sidecar.url, sidecar.token)

            artifacts: dict[str, Path] = {"launch_receipt": receipt_path}
            family_summaries: dict[str, object] = {}
            any_defect = False

            for fam in families:
                print(f"[CF05] Running {fam} (seed={args.seed})...", flush=True)
                outcomes = run_family_suite(
                    fam,
                    client,
                    tmp_path / "work",
                    boundary_config,
                    base_seed=args.seed,
                    max_instances=args.max_instances,
                )
                summary = summarize_outcomes(outcomes)
                family_summaries[fam] = summary
                if summary["defects"] or summary["unknown"]:
                    any_defect = True
                fam_file = out_dir / f"{fam.lower()}_results.json"
                fam_file.write_text(
                    json.dumps(
                        {
                            "family": fam,
                            "seed": args.seed,
                            "summary": summary,
                            "outcomes": [o.to_dict() for o in outcomes],
                        },
                        indent=2,
                        sort_keys=True,
                    )
                    + "\n",
                    encoding="utf-8",
                )
                artifacts[f"{fam.lower()}_results"] = fam_file
                print(
                    f"[CF05] {fam} done: total={summary['total']} pass={summary['passed']} "
                    f"defects={summary['defects']} unknown={summary['unknown']} gaps={summary['declared_gap_counts']}",
                    flush=True,
                )
        finally:
            sidecar.close()

        summary_file = out_dir / "cf05_summary.json"
        summary_file.write_text(
            json.dumps(
                {
                    "seed": args.seed,
                    "families": family_summaries,
                    "provenance": provenance.to_dict(),
                },
                indent=2,
                sort_keys=True,
            )
            + "\n",
            encoding="utf-8",
        )
        artifacts["cf05_summary"] = summary_file

        write_evidence_manifest(
            out_dir,
            provenance,
            artifacts,
            extra={"families": family_summaries},
            replay_command=[
                "python3",
                "-m",
                "catalog_feed_search.families.cf05_change_feed",
                "--families",
                ",".join(families),
                "--seed",
                args.seed,
                "--binary",
                args.binary,
                "--d1-worktree",
                args.d1_worktree,
                "--out-dir",
                ".",
                "--rg-bin",
                args.rg_bin,
            ],
            launch_receipt=sidecar.launch_receipt(),
        )

    return 1 if any_defect else 0


if __name__ == "__main__":
    sys.exit(main())
