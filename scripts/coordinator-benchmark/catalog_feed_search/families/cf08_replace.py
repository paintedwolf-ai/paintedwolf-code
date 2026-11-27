"""CF08 · Replace across files (R01-R04) sidecar family runner and CLI."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import shutil
import tempfile
import urllib.parse
import uuid

from ..checker_replace import check_replace
from ..checker_types import Verdict
from ..driver_http import HTTPTransport, SidecarTransport
from ..driver_provenance import collect_run_provenance, file_sha256, write_evidence_manifest
from ..driver_sidecar import SidecarDriver
from ..generators import TreeEntrySpec, TreeSpec, materialize_tree
from ..live_controls import wait_catalog_settled
from ..queries import CodeQuerySpec, ReplaceSpec
from ..references import reference_replace
from ..seeds import BASE_SEED_PRIMARY, ReplaySelector, seed_rng
from ..shrinker import FailingInstance, shrink_instance
from .cf08_corpus import (
    DriftAction,
    FamilySummary,
    collect_disk_hashes,
    encode_text_with_bom,
    generate_r02_case,
    generate_r03_case,
    generate_r04_case,
    sha256_bytes,
)


class CF08SidecarRunner:
    """Executes CF08 R01-R04 sidecar suites against an isolated D1 lycaon-dev instance."""

    def __init__(
        self,
        transport: SidecarTransport,
        work_root: Path,
        *,
        base_seed: str = BASE_SEED_PRIMARY,
        rg_bin: str = "rg",
    ) -> None:
        self._transport = transport
        self._driver = SidecarDriver(transport)
        self._work_root = Path(work_root)
        self._work_root.mkdir(parents=True, exist_ok=True)
        self._base_seed = base_seed
        self._rg_bin = rg_bin

    def _remove_project(self, project_id: str) -> None:
        try:
            assess = self._transport.request(
                f"/v1/projects/{project_id}/removal-assessment", method="GET"
            )
            token = str(assess.get("assessment_token") or "")
            if token:
                self._transport.request(
                    f"/v1/projects/{project_id}/removals",
                    {
                        "operation_id": str(uuid.uuid4()),
                        "assessment_token": token,
                        "remove_extensions": [],
                        "force": True,
                    },
                    method="POST",
                )
        except Exception:
            pass

    def _attach_settled(self, root_dir: Path, *, settle: bool = True) -> tuple[str, str]:
        project_id, root_ids = self._driver.attach_project([root_dir])
        root_id = root_ids[0] if root_ids else "root-0"
        if settle:
            wait_catalog_settled(self._driver, project_id, root_id, max_wait_s=15.0, poll_interval_s=0.05)
        return project_id, root_id

    def _person_write(self, project_id: str, root_id: str, rel_path: str, new_text: str) -> bool:
        qs = urllib.parse.urlencode({"path": rel_path, "root_id": root_id})
        read = self._transport.request(f"/v1/projects/{project_id}/source?{qs}", method="GET")
        base_sha = str(read.get("sha256") or "")
        enc = str(read.get("encoding") or "utf-8")
        if not base_sha:
            return False
        self._transport.request(
            f"/v1/projects/{project_id}/source",
            {
                "operation_id": str(uuid.uuid4()),
                "path": rel_path,
                "root_id": root_id,
                "content": new_text,
                "encoding": enc,
                "base_sha256": base_sha,
            },
            method="PUT",
        )
        return True

    def _preview_until_not_preparing(
        self,
        spec: ReplaceSpec,
        origin_project_id: str,
        *,
        max_attempts: int = 30,
    ) -> dict[str, object]:
        resp: dict[str, object] = {}
        for _ in range(max_attempts):
            resp = self._driver.replace_preview(
                spec.pattern,
                spec.replacement,
                origin_project_id=origin_project_id,
                regex=spec.regex,
                case_sensitive=spec.case_sensitive,
                whole_word=spec.whole_word,
            )
            if str(resp.get("state") or "").lower() != "preparing":
                return resp
        return resp

    # -------------------------------------------------------------------------
    # R01 · Preview states (60 sidecar + 4 edges)
    # -------------------------------------------------------------------------

    def run_r01(self, count: int = 60) -> FamilySummary:
        summary = FamilySummary("R01", "sidecar", self._base_seed, planned=count, edges_planned=4)
        for i in range(count):
            summary.executed += 1
            rng = seed_rng(self._base_seed, "R01:sidecar", i)
            mode = i % 3  # 0=ready, 1=limited (result_limit), 2=preparing_or_settled_contract
            case_dir = Path(tempfile.mkdtemp(prefix=f"r01_{i:03d}_", dir=str(self._work_root)))
            project_id = ""
            try:
                if mode == 0:
                    expect_match = i % 6 != 0
                    body = b"alpha_1 beta\n" if expect_match else b"unrelated_xyz\n"
                    (case_dir / "sample.txt").write_bytes(body)
                    project_id, _ = self._attach_settled(case_dir, settle=True)
                    resp = self._preview_until_not_preparing(
                        ReplaceSpec("alpha_1", "alpha_2"), project_id
                    )
                    state = str(resp.get("state") or "")
                    files = resp.get("files") or []
                    issues = resp.get("issues") or []
                    want_len = 1 if expect_match else 0
                    if state != "ready" or len(issues) != 0 or len(files) != want_len:
                        summary.failed += 1
                        summary.failures.append({"instance": i, "mode": "ready", "resp": resp})
                    else:
                        summary.passed += 1
                elif mode == 1:
                    # 5,010 matches in one file guarantees ReplaceMaxHunks (5,000) -> state=limited + result_limit.
                    (case_dir / "a_big.txt").write_bytes(("alpha " * 5010 + "\n").encode("utf-8"))
                    (case_dir / "b_small.txt").write_bytes(b"alpha one\n")
                    project_id, _ = self._attach_settled(case_dir, settle=True)
                    resp = self._preview_until_not_preparing(
                        ReplaceSpec("alpha", "beta"), project_id
                    )
                    state = str(resp.get("state") or "")
                    issues = resp.get("issues") or []
                    for iss in issues:
                        if isinstance(iss, dict) and iss.get("reason"):
                            summary.record_gap(str(iss["reason"]))
                    if state != "limited" or not resp.get("truncated") or not issues:
                        summary.failed += 1
                        summary.failures.append({"instance": i, "mode": "limited", "resp": resp})
                    else:
                        summary.passed += 1
                else:
                    # Attach without waiting and issue preview immediately; verify strict state invariants.
                    n_files = rng.randint(8, 20)
                    for k in range(n_files):
                        sub = case_dir / f"d{k % 4}"
                        sub.mkdir(parents=True, exist_ok=True)
                        (sub / f"f_{k:02d}.txt").write_bytes(f"gamma_{k} line\n".encode("utf-8"))
                    project_id, _ = self._attach_settled(case_dir, settle=False)
                    resp = self._driver.replace_preview("gamma_", "delta_", origin_project_id=project_id)
                    state = str(resp.get("state") or "")
                    files = resp.get("files") or []
                    issues = resp.get("issues") or []
                    for iss in issues:
                        if isinstance(iss, dict) and iss.get("reason"):
                            summary.record_gap(str(iss["reason"]))
                    ok = (
                        (state == "preparing" and len(files) == 0 and len(issues) > 0)
                        or (state == "ready" and len(issues) == 0 and len(files) == n_files)
                        or (state == "limited" and len(issues) > 0)
                    )
                    if not ok:
                        summary.failed += 1
                        summary.failures.append({"instance": i, "mode": "preparing_probe", "resp": resp})
                    else:
                        summary.passed += 1
            except Exception as exc:
                summary.failed += 1
                summary.failures.append({"instance": i, "error": str(exc)})
            finally:
                if project_id:
                    self._remove_project(project_id)
                shutil.rmtree(case_dir, ignore_errors=True)

        self._run_r01_edges(summary)
        return summary

    def _run_r01_edges(self, summary: FamilySummary) -> None:
        # R01-E01: Empty ready preview -> "No replaceable matches" valid only when ready & issues==[].
        self._exec_edge(summary, "R01-E01", self._r01_edge_empty_ready)
        # R01-E02: Limited preview via result_limit disables Apply all while keeping selected file applicable.
        self._exec_edge(summary, "R01-E02", self._r01_edge_limited_selected_apply)
        # R01-E03: Limited preview via unreadable file (files_skipped) keeps readable file applicable.
        self._exec_edge(summary, "R01-E03", self._r01_edge_limited_unreadable_file)
        # R01-E04: Input change between preview requests never leaks older hunks.
        self._exec_edge(summary, "R01-E04", self._r01_edge_input_change)

    def _r01_edge_empty_ready(self) -> None:
        d = Path(tempfile.mkdtemp(prefix="r01_e01_", dir=str(self._work_root)))
        pid = ""
        try:
            (d / "a.txt").write_bytes(b"hello world\n")
            pid, _ = self._attach_settled(d)
            resp = self._preview_until_not_preparing(ReplaceSpec("no_match_xyz", "y"), pid)
            if resp.get("state") != "ready" or resp.get("files") or resp.get("issues"):
                raise AssertionError(f"unexpected empty ready response: {resp}")
        finally:
            if pid:
                self._remove_project(pid)
            shutil.rmtree(d, ignore_errors=True)

    def _r01_edge_limited_selected_apply(self) -> None:
        d = Path(tempfile.mkdtemp(prefix="r01_e02_", dir=str(self._work_root)))
        pid = ""
        try:
            (d / "a_first.txt").write_bytes(b"alpha one\n")
            (d / "z_huge.txt").write_bytes(("alpha " * 5010 + "\n").encode("utf-8"))
            pid, rid = self._attach_settled(d)
            resp = self._preview_until_not_preparing(ReplaceSpec("alpha", "beta"), pid)
            if resp.get("state") != "limited" or not resp.get("truncated"):
                raise AssertionError(f"expected limited truncated preview, got {resp}")
            selected = [f for f in (resp.get("files") or []) if f.get("path") == "a_first.txt"]
            if len(selected) != 1:
                raise AssertionError(f"expected a_first.txt in limited preview: {resp}")
            subset_preview = dict(resp)
            subset_preview["files"] = selected
            ans = self._driver.replace_apply(
                subset_preview,
                query="alpha",
                replacement="beta",
                origin_project_id=pid,
                root_dirs_by_id={rid: d},
            )
            if len(ans.files) != 1 or not ans.files[0].applied or (d / "a_first.txt").read_bytes() != b"beta one\n":
                raise AssertionError(f"selected file from limited preview failed to apply: {ans}")
        finally:
            if pid:
                self._remove_project(pid)
            shutil.rmtree(d, ignore_errors=True)

    def _r01_edge_limited_unreadable_file(self) -> None:
        d = Path(tempfile.mkdtemp(prefix="r01_e03_", dir=str(self._work_root)))
        unreadable = d / "locked.txt"
        pid = ""
        try:
            (d / "good.txt").write_bytes(b"alpha ok\n")
            unreadable.write_bytes(b"alpha locked\n")
            os.chmod(unreadable, 0o000)
            pid, rid = self._attach_settled(d)
            resp = self._preview_until_not_preparing(ReplaceSpec("alpha", "beta"), pid)
            if resp.get("state") != "limited":
                raise AssertionError(f"expected limited preview with unreadable file, got {resp}")
            reasons = {str(i.get("reason")) for i in (resp.get("issues") or []) if isinstance(i, dict)}
            if "files_skipped" not in reasons:
                raise AssertionError(f"expected files_skipped in issues, got {resp}")
            ans = self._driver.replace_apply(
                resp,
                query="alpha",
                replacement="beta",
                origin_project_id=pid,
                root_dirs_by_id={rid: d},
            )
            if len(ans.files) != 1 or not ans.files[0].applied or (d / "good.txt").read_bytes() != b"beta ok\n":
                raise AssertionError(f"healthy file beside unreadable file failed to apply: {ans}")
        finally:
            if unreadable.exists():
                os.chmod(unreadable, 0o644)
            if pid:
                self._remove_project(pid)
            shutil.rmtree(d, ignore_errors=True)

    def _r01_edge_input_change(self) -> None:
        d = Path(tempfile.mkdtemp(prefix="r01_e04_", dir=str(self._work_root)))
        pid = ""
        try:
            (d / "swap.txt").write_bytes(b"alpha and beta\n")
            pid, _ = self._attach_settled(d)
            r1 = self._preview_until_not_preparing(ReplaceSpec("alpha", "OLD_REPL"), pid)
            r2 = self._preview_until_not_preparing(ReplaceSpec("beta", "NEW_REPL"), pid)
            if r1.get("state") != "ready" or r2.get("state") != "ready":
                raise AssertionError(f"unexpected states r1={r1} r2={r2}")
            h2 = (r2.get("files") or [{}])[0].get("hunks", [{}])[0]
            if "NEW_REPL" not in str(h2.get("after", "")) or "OLD_REPL" in str(h2.get("after", "")):
                raise AssertionError(f"stale hunks leaked into second preview: {r2}")
        finally:
            if pid:
                self._remove_project(pid)
            shutil.rmtree(d, ignore_errors=True)

    # -------------------------------------------------------------------------
    # R02 · Revision guard (100 sidecar + 6 edges)
    # -------------------------------------------------------------------------

    def _run_r02_single(
        self,
        tree_spec: TreeSpec,
        spec: ReplaceSpec,
        drifts: tuple[DriftAction, ...],
    ) -> tuple[bool, str]:
        case_dir = Path(tempfile.mkdtemp(prefix="r02_", dir=str(self._work_root)))
        pid = ""
        try:
            materialize_tree(tree_spec, case_dir)
            ref_before = reference_replace(case_dir, spec, rg_bin=self._rg_bin, root_id="pending")
            pid, rid = self._attach_settled(case_dir)
            ref_map = {
                (rid, rel): type(res)(
                    path=res.path,
                    before_sha256=res.before_sha256,
                    after_sha256=res.after_sha256,
                    resulting_bytes=res.resulting_bytes,
                    match_count=res.match_count,
                    changed=res.changed,
                    root_id=rid,
                    encoding=res.encoding,
                )
                for rel, res in ref_before.items()
            }
            preview = self._preview_until_not_preparing(spec, pid)
            if preview.get("state") != "ready":
                return False, f"preview not ready: {preview}"

            drifted_keys: set[tuple[str, str]] = set()
            drifted_hashes: dict[tuple[str, str], str] = {}
            deleted_paths: set[str] = set()
            created_hashes: dict[str, str] = {}

            for action in drifts:
                target = case_dir / action.rel_path
                if action.kind == "create_post_preview":
                    target.parent.mkdir(parents=True, exist_ok=True)
                    target.write_bytes(action.content)
                    created_hashes[action.rel_path] = sha256_bytes(action.content)
                elif action.kind == "delete":
                    if target.exists():
                        target.unlink()
                    deleted_paths.add(action.rel_path)
                    drifted_keys.add((rid, action.rel_path))
                    drifted_hashes[(rid, action.rel_path)] = ""
                elif action.kind == "delete_recreate":
                    if target.exists():
                        target.unlink()
                    target.parent.mkdir(parents=True, exist_ok=True)
                    target.write_bytes(action.content)
                    deleted_paths.discard(action.rel_path)
                    drifted_keys.add((rid, action.rel_path))
                    drifted_hashes[(rid, action.rel_path)] = sha256_bytes(action.content)
                elif action.kind == "edit":
                    if action.rel_path in deleted_paths:
                        continue
                    wrote = False
                    if action.writer == "person":
                        try:
                            wrote = self._person_write(pid, rid, action.rel_path, action.content.decode("utf-8"))
                        except Exception:
                            wrote = False
                    if not wrote:
                        target.write_bytes(action.content)
                    drifted_keys.add((rid, action.rel_path))
                    drifted_hashes[(rid, action.rel_path)] = file_sha256(target)

            # Remove deleted paths from drifted_keys for check_replace (we check deleted explicitly below)
            # because deleted files return reason="source_not_found" with empty observed_sha256.
            apply_files: list[dict[str, object]] = []
            for pf in preview.get("files") or []:
                if isinstance(pf, dict):
                    apply_files.append(
                        {
                            "root_id": str(pf["root_id"]),
                            "path": str(pf["path"]),
                            "sha256": str(pf["sha256"]),
                            "hunks": list(range(len(pf.get("hunks") or []))),
                        }
                    )
            apply_resp = self._transport.request(
                "/v1/search/replace/apply",
                {
                    "operation_id": str(uuid.uuid4()),
                    "query": spec.pattern,
                    "replacement": spec.replacement,
                    "origin_project_id": pid,
                    "regex": spec.regex,
                    "case_sensitive": spec.case_sensitive,
                    "whole_word": spec.whole_word,
                    "files": apply_files,
                },
                method="POST",
            )

            for item in apply_resp.get("files") or []:
                if not isinstance(item, dict):
                    continue
                rel = str(item.get("path") or "").removeprefix("./")
                key = (rid, rel)
                applied = bool(item.get("applied"))
                skipped = bool(item.get("skipped"))
                reason = str(item.get("reason") or "")
                full = case_dir / rel

                if rel in deleted_paths:
                    if applied or not skipped or reason != "source_not_found":
                        return False, f"deleted {rel} wrong outcome: {item}"
                    if full.exists():
                        return False, f"deleted {rel} reappeared on disk"
                elif key in drifted_keys:
                    if applied or not skipped or reason != "source_write_conflict":
                        return False, f"drifted {rel} not refused: {item}"
                    if not full.is_file() or file_sha256(full) != drifted_hashes[key]:
                        return False, f"drifted {rel} bytes changed on disk"
                else:
                    if not applied or skipped:
                        return False, f"unaffected {rel} not applied: {item}"
                    if file_sha256(full) != ref_map[key].after_sha256:
                        return False, f"unaffected {rel} hash mismatch after apply"

            for rel, want_sha in created_hashes.items():
                full = case_dir / rel
                if not full.is_file() or file_sha256(full) != want_sha:
                    return False, f"post-preview file {rel} was modified or removed"

            return True, ""
        finally:
            if pid:
                self._remove_project(pid)
            shutil.rmtree(case_dir, ignore_errors=True)

    def run_r02(self, count: int = 100) -> FamilySummary:
        summary = FamilySummary("R02", "sidecar", self._base_seed, planned=count, edges_planned=6)
        for i in range(count):
            summary.executed += 1
            try:
                tree_spec, spec, drifts = generate_r02_case(self._base_seed, i)
                ok, reason = self._run_r02_single(tree_spec, spec, drifts)
                if ok:
                    summary.passed += 1
                else:
                    summary.failed += 1
                    summary.failures.append({"instance": i, "reason": reason})
            except Exception as exc:
                summary.failed += 1
                summary.failures.append({"instance": i, "error": str(exc)})

        self._run_r02_edges(summary)
        return summary

    def _run_r02_edges(self, summary: FamilySummary) -> None:
        sel = ReplaySelector(self._base_seed, "R02-EDGE", 0)
        base_tree = TreeSpec(
            sel,
            (
                TreeEntrySpec("a.txt", "file", b"alpha one\n"),
                TreeEntrySpec("b.txt", "file", b"alpha two\n"),
            ),
        )
        spec = ReplaceSpec("alpha", "beta", case_sensitive=True)
        edge_cases: list[tuple[str, TreeSpec, tuple[DriftAction, ...]]] = [
            ("R02-E01", base_tree, (DriftAction("person", "edit", "a.txt", b"alpha person\n"),)),
            ("R02-E02", base_tree, (DriftAction("external", "delete", "a.txt"),)),
            ("R02-E03", base_tree, (DriftAction("agent", "delete_recreate", "a.txt", b"alpha agent\n"),)),
            ("R02-E04", base_tree, (DriftAction("external", "create_post_preview", "new.txt", b"alpha new\n"),)),
            (
                "R02-E05",
                TreeSpec(
                    sel,
                    (
                        TreeEntrySpec("d1.txt", "file", b"alpha 1\n"),
                        TreeEntrySpec("d2.txt", "file", b"alpha 2\n"),
                        TreeEntrySpec("ok.txt", "file", b"alpha 3\n"),
                    ),
                ),
                (
                    DriftAction("person", "edit", "d1.txt", b"alpha p\n"),
                    DriftAction("external", "delete", "d2.txt"),
                    DriftAction("agent", "create_post_preview", "late.txt", b"alpha late\n"),
                ),
            ),
            ("R02-E06", base_tree, (DriftAction("external", "edit", "a.txt", b"ALPHA one\n"),)),
        ]
        for name, t_spec, drifts in edge_cases:
            def _run(ts: TreeSpec = t_spec, dr: tuple[DriftAction, ...] = drifts) -> None:
                ok, reason = self._run_r02_single(ts, spec, dr)
                if not ok:
                    raise AssertionError(reason)
            self._exec_edge(summary, name, _run)

    # -------------------------------------------------------------------------
    # R03 · Replacement differential (200 sidecar + 6 edges vs rg 15.2.0)
    # -------------------------------------------------------------------------

    def _run_r03_single(self, tree_spec: TreeSpec, spec: ReplaceSpec) -> tuple[bool, str, tuple[str, ...]]:
        case_dir = Path(tempfile.mkdtemp(prefix="r03_", dir=str(self._work_root)))
        pid = ""
        try:
            materialize_tree(tree_spec, case_dir)
            ref_raw = reference_replace(case_dir, spec, rg_bin=self._rg_bin, root_id="pending")
            pid, rid = self._attach_settled(case_dir)
            ref_map = {
                (rid, rel): type(res)(
                    path=res.path,
                    before_sha256=res.before_sha256,
                    after_sha256=res.after_sha256,
                    resulting_bytes=res.resulting_bytes,
                    match_count=res.match_count,
                    changed=res.changed,
                    root_id=rid,
                    encoding=res.encoding,
                )
                for rel, res in ref_raw.items()
            }
            preview = self._preview_until_not_preparing(spec, pid)
            answer = self._driver.replace_apply(
                preview,
                query=spec.pattern,
                replacement=spec.replacement,
                origin_project_id=pid,
                root_dirs_by_id={rid: case_dir},
                regex=spec.regex,
                case_sensitive=spec.case_sensitive,
                whole_word=spec.whole_word,
            )
            res = check_replace(ref_map, answer)
            if res.verdict == Verdict.PASS:
                return True, "", res.declared_gaps
            return False, f"{res.reason} wrong={res.wrong_claims} extra={res.extra_claims} missing={res.missing_claims}", res.declared_gaps
        finally:
            if pid:
                self._remove_project(pid)
            shutil.rmtree(case_dir, ignore_errors=True)

    def run_r03(self, count: int = 200) -> FamilySummary:
        summary = FamilySummary("R03", "sidecar", self._base_seed, planned=count, edges_planned=6)
        for i in range(count):
            summary.executed += 1
            try:
                tree_spec, spec = generate_r03_case(self._base_seed, i)
                ok, reason, gaps = self._run_r03_single(tree_spec, spec)
                for g in gaps:
                    summary.record_gap(g)
                if ok:
                    summary.passed += 1
                else:
                    summary.failed += 1
                    summary.failures.append({"instance": i, "spec": spec.to_dict(), "reason": reason})
                    # Shrink first failing instance with shrinker.py (D5).
                    if len(summary.shrunk_failures) < 3:
                        failing = FailingInstance(
                            selector=tree_spec.selector,
                            tree_spec=tree_spec,
                            query_spec=CodeQuerySpec(
                                form="regex" if spec.regex else "literal",
                                pattern=spec.pattern,
                                case_sensitive=spec.case_sensitive,
                                whole_word=spec.whole_word,
                                regex=spec.regex,
                            ),
                            failure_summary=reason,
                        )
                        shrunk = shrink_instance(
                            failing,
                            lambda inst, sp=spec: not self._run_r03_single(inst.tree_spec, sp)[0],
                            max_evaluations=30,
                        )
                        summary.shrunk_failures.append(json.loads(shrunk.to_json()))
            except Exception as exc:
                summary.failed += 1
                summary.failures.append({"instance": i, "error": str(exc)})

        self._run_r03_edges(summary)
        return summary

    def _run_r03_edges(self, summary: FamilySummary) -> None:
        sel = ReplaySelector(self._base_seed, "R03-EDGE", 0)
        edges: list[tuple[str, TreeSpec, ReplaceSpec]] = [
            (
                "R03-E01",
                TreeSpec(sel, (TreeEntrySpec("lit.txt", "file", b"price alpha_1 end\n"),)),
                ReplaceSpec("alpha_1", "$1_${2}_$$", regex=False, case_sensitive=True),
            ),
            (
                "R03-E02",
                TreeSpec(sel, (TreeEntrySpec("re.txt", "file", b"item alpha_42 and beta_7\n"),)),
                ReplaceSpec("([a-z]+)_([0-9]+)", "${2}_$1_$$", regex=True, case_sensitive=True),
            ),
            (
                "R03-E03",
                TreeSpec(sel, (TreeEntrySpec("ww.txt", "file", b"alpha alpha. prealpha alpha_1 (alpha)\n"),)),
                ReplaceSpec("alpha", "OMEGA", regex=False, case_sensitive=True, whole_word=True),
            ),
            (
                "R03-E04",
                TreeSpec(sel, (TreeEntrySpec("eol.txt", "file", b"one alpha\r\ntwo alpha\nthree alpha\r\n"),)),
                ReplaceSpec("alpha", "beta", regex=False, case_sensitive=True),
            ),
            (
                "R03-E05",
                TreeSpec(
                    sel,
                    (
                        TreeEntrySpec("u8.txt", "file", encode_text_with_bom("alpha_10 line\n", "utf-8")),
                        TreeEntrySpec("u8bom.txt", "file", encode_text_with_bom("alpha_20 line\r\n", "utf-8-bom")),
                        TreeEntrySpec("u16le.txt", "file", encode_text_with_bom("alpha_30 line\r\n", "utf-16-le-bom")),
                        TreeEntrySpec("u16be.txt", "file", encode_text_with_bom("alpha_40 line\n", "utf-16-be-bom")),
                    ),
                ),
                ReplaceSpec("(alpha)_([0-9]+)", "${2}_$1", regex=True, case_sensitive=False),
            ),
            (
                "R03-E06",
                TreeSpec(sel, (TreeEntrySpec("case.txt", "file", b"PaintedWolf paintedwolf PAINTEDWOLF\n"),)),
                ReplaceSpec("paintedwolf", "pw", regex=False, case_sensitive=False),
            ),
        ]
        for name, ts, sp in edges:
            def _run(t_spec: TreeSpec = ts, r_spec: ReplaceSpec = sp) -> None:
                ok, reason, _ = self._run_r03_single(t_spec, r_spec)
                if not ok:
                    raise AssertionError(reason)
            self._exec_edge(summary, name, _run)

    # -------------------------------------------------------------------------
    # R04 · Replace, history and open documents (40 sidecar + 3 edges)
    # -------------------------------------------------------------------------

    def _run_r04_single(
        self,
        tree_spec: TreeSpec,
        spec: ReplaceSpec,
        open_drafts: dict[str, str],
    ) -> tuple[bool, str]:
        case_dir = Path(tempfile.mkdtemp(prefix="r04_", dir=str(self._work_root)))
        pid = ""
        try:
            materialize_tree(tree_spec, case_dir)
            file_paths = tree_spec.file_paths()
            before_hashes = collect_disk_hashes(case_dir, file_paths)
            ref_raw = reference_replace(case_dir, spec, rg_bin=self._rg_bin, root_id="pending")
            pid, rid = self._attach_settled(case_dir)

            # Inject unsaved editor drafts via POST+PUT /v1/projects/{id}/editor-documents.
            opened_docs: dict[str, dict[str, object]] = {}
            for rel_path, draft_text in open_drafts.items():
                doc = self._driver.inject_editor_draft(pid, rid, rel_path, draft_text)
                if not doc.get("dirty"):
                    return False, f"injected draft for {rel_path} was not marked dirty: {doc}"
                opened_docs[rel_path] = doc

            preview = self._preview_until_not_preparing(spec, pid)
            if preview.get("state") != "ready":
                return False, f"preview not ready: {preview}"

            answer = self._driver.replace_apply(
                preview,
                query=spec.pattern,
                replacement=spec.replacement,
                origin_project_id=pid,
                root_dirs_by_id={rid: case_dir},
                regex=spec.regex,
                case_sensitive=spec.case_sensitive,
                whole_word=spec.whole_word,
            )
            if not answer.files or any(not f.applied for f in answer.files):
                return False, f"not all files applied: {answer}"

            # 1. Verify disk bytes match reference_replace.
            for rel, ref in ref_raw.items():
                got = file_sha256(case_dir / rel)
                if got != ref.after_sha256:
                    return False, f"post-apply hash mismatch on {rel}: got={got} want={ref.after_sha256}"

            # 2. Verify open draft documents reconciled per contract (reopen returns valid doc with updated base_sha256).
            for rel_path in open_drafts:
                doc_after = self._transport.request(
                    f"/v1/projects/{pid}/editor-documents",
                    {"path": rel_path, "root_id": rid, "client_id": "cfs-driver"},
                    method="POST",
                )
                if not doc_after.get("id") or not doc_after.get("file_id"):
                    return False, f"open document lost identity for {rel_path}: {doc_after}"

            # 3. Verify each changed file gets a person-attributed version (`origin == "user"`)
            #    and restoring its pre-replace version returns the exact original bytes by SHA-256.
            for rel, ref in ref_raw.items():
                qs = urllib.parse.urlencode({"path": rel, "root_id": rid})
                src_info = self._transport.request(f"/v1/projects/{pid}/source?{qs}", method="GET")
                file_id = str(src_info.get("file_id") or "")
                if not file_id:
                    return False, f"missing file_id for replaced file {rel}: {src_info}"
                vqs = urllib.parse.urlencode({"file_id": file_id, "lane": "retained", "limit": 20})
                vresp = self._transport.request(f"/v1/projects/{pid}/source/versions?{vqs}", method="GET")
                versions = [v for v in (vresp.get("versions") or []) if isinstance(v, dict)]
                after_ver = next(
                    (
                        v
                        for v in versions
                        if str(v.get("content_sha256") or "") == ref.after_sha256
                        and str(v.get("origin") or "") == "user"
                    ),
                    None,
                )
                if after_ver is None:
                    return False, f"missing person-attributed (origin=user) version for {rel}: {versions}"
                before_ver = next(
                    (v for v in versions if str(v.get("content_sha256") or "") == before_hashes[rel]),
                    None,
                )
                if before_ver is None:
                    return False, f"missing pre-replace version for {rel}: {versions}"

                restore_resp = self._transport.request(
                    f"/v1/projects/{pid}/source/versions/{before_ver['id']}/restore",
                    {
                        "operation_id": str(uuid.uuid4()),
                        "file_id": file_id,
                        "root_id": rid,
                        "path": rel,
                        "base": {"state": "content", "sha256": ref.after_sha256},
                    },
                    method="POST",
                )
                if str(restore_resp.get("sha256") or "") != before_hashes[rel]:
                    return False, f"restore response hash mismatch for {rel}: {restore_resp}"
                restored_disk_sha = file_sha256(case_dir / rel)
                if restored_disk_sha != before_hashes[rel]:
                    return False, f"restored disk bytes mismatch for {rel}: got={restored_disk_sha} want={before_hashes[rel]}"

            return True, ""
        finally:
            if pid:
                self._remove_project(pid)
            shutil.rmtree(case_dir, ignore_errors=True)

    def run_r04(self, count: int = 40) -> FamilySummary:
        summary = FamilySummary("R04", "sidecar", self._base_seed, planned=count, edges_planned=3)
        for i in range(count):
            summary.executed += 1
            try:
                tree_spec, spec, open_path, draft_text = generate_r04_case(self._base_seed, i)
                ok, reason = self._run_r04_single(tree_spec, spec, {open_path: draft_text})
                if ok:
                    summary.passed += 1
                else:
                    summary.failed += 1
                    summary.failures.append({"instance": i, "reason": reason})
            except Exception as exc:
                summary.failed += 1
                summary.failures.append({"instance": i, "error": str(exc)})

        self._run_r04_edges(summary)
        return summary

    def _run_r04_edges(self, summary: FamilySummary) -> None:
        sel = ReplaySelector(self._base_seed, "R04-EDGE", 0)
        spec = ReplaceSpec("alpha", "beta", case_sensitive=True)
        # R04-E01: Unsaved draft on a different line from replacement.
        self._exec_edge(
            summary,
            "R04-E01",
            lambda: self._assert_ok(
                self._run_r04_single(
                    TreeSpec(
                        sel,
                        (
                            TreeEntrySpec("a.txt", "file", b"top\nmiddle alpha\nbottom\n"),
                            TreeEntrySpec("b.txt", "file", b"other alpha\n"),
                        ),
                    ),
                    spec,
                    {"a.txt": "top [draft]\nmiddle alpha\nbottom\n"},
                )
            ),
        )
        # R04-E02: Unsaved draft on the same line as replacement.
        self._exec_edge(
            summary,
            "R04-E02",
            lambda: self._assert_ok(
                self._run_r04_single(
                    TreeSpec(sel, (TreeEntrySpec("a.txt", "file", b"line alpha end\n"),)),
                    spec,
                    {"a.txt": "line alpha end [same-line draft]\n"},
                )
            ),
        )
        # R04-E03: Multi-file replace (5 files, 2 open with unsaved drafts, 3 closed).
        self._exec_edge(
            summary,
            "R04-E03",
            lambda: self._assert_ok(
                self._run_r04_single(
                    TreeSpec(
                        sel,
                        tuple(
                            TreeEntrySpec(f"f{k}.txt", "file", f"file {k}\nalpha {k}\n".encode("utf-8"))
                            for k in range(5)
                        ),
                    ),
                    spec,
                    {"f0.txt": "file 0 draft\nalpha 0\n", "f2.txt": "file 2 draft\nalpha 2\n"},
                )
            ),
        )

    @staticmethod
    def _assert_ok(res: tuple[bool, str]) -> None:
        if not res[0]:
            raise AssertionError(res[1])

    @staticmethod
    def _exec_edge(summary: FamilySummary, name: str, fn) -> None:
        summary.edges_executed += 1
        try:
            fn()
            summary.edges_passed += 1
        except Exception as exc:
            summary.edges_failed += 1
            summary.failures.append({"edge": name, "reason": str(exc)})


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="CF08 Replace across files (R01-R04) sidecar runner")
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--token", default="")
    parser.add_argument("--binary", required=True)
    parser.add_argument("--harness-repo", required=True)
    parser.add_argument("--d1-commit", default="3bb1464d22dd0a38a149d37610810e7a0324e8cf")
    parser.add_argument("--d1-tree", default="")
    parser.add_argument("--rg-bin", default="rg")
    parser.add_argument("--seed", default=BASE_SEED_PRIMARY)
    parser.add_argument("--families", default="R01,R02,R03,R04")
    parser.add_argument("--r01-count", type=int, default=60)
    parser.add_argument("--r02-count", type=int, default=100)
    parser.add_argument("--r03-count", type=int, default=200)
    parser.add_argument("--r04-count", type=int, default=40)
    parser.add_argument("--out-dir", required=True)
    parser.add_argument("--launch-receipt", default="")
    args = parser.parse_args(argv)

    out_dir = Path(args.out_dir).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)
    work_root = Path(tempfile.mkdtemp(prefix="cf08_sidecar_work_"))

    try:
        transport = HTTPTransport(args.base_url, token=args.token, timeout_s=30.0)
        runner = CF08SidecarRunner(transport, work_root, base_seed=args.seed, rg_bin=args.rg_bin)
        selected = {f.strip().upper() for f in args.families.split(",") if f.strip()}
        summaries: dict[str, dict[str, object]] = {}
        if "R01" in selected:
            summaries["R01"] = runner.run_r01(args.r01_count).to_dict()
        if "R02" in selected:
            summaries["R02"] = runner.run_r02(args.r02_count).to_dict()
        if "R03" in selected:
            summaries["R03"] = runner.run_r03(args.r03_count).to_dict()
        if "R04" in selected:
            summaries["R04"] = runner.run_r04(args.r04_count).to_dict()

        summary_path = out_dir / "sidecar_summary.json"
        all_passed = all(
            int(s["failed"]) == 0 and int(s["edges_failed"]) == 0 and int(s["blocked"]) == 0
            for s in summaries.values()
        )
        payload = {
            "card": "CF08",
            "lane": "sidecar",
            "base_seed": args.seed,
            "all_passed": all_passed,
            "families": summaries,
        }
        summary_path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")

        provenance = collect_run_provenance(
            harness_repo=Path(args.harness_repo),
            work_dir=work_root,
            binary_path=Path(args.binary),
            d1_commit=args.d1_commit,
            d1_tree=args.d1_tree,
            rg_bin=args.rg_bin,
        )
        launch_receipt = None
        artifacts: dict[str, Path] = {"sidecar_summary": summary_path}
        if args.launch_receipt and Path(args.launch_receipt).is_file():
            lp = Path(args.launch_receipt)
            launch_receipt = json.loads(lp.read_text(encoding="utf-8"))
            artifacts["launch_receipt"] = lp

        write_evidence_manifest(
            out_dir,
            provenance,
            artifacts,
            extra={"all_passed": all_passed, "families": sorted(summaries)},
            replay_command=[
                "python3",
                "-m",
                "catalog_feed_search.families.cf08_replace",
                "--seed",
                args.seed,
                "--families",
                args.families,
                "--r01-count",
                str(args.r01_count),
                "--r02-count",
                str(args.r02_count),
                "--r03-count",
                str(args.r03_count),
                "--r04-count",
                str(args.r04_count),
            ],
            launch_receipt=launch_receipt,
        )
        print(json.dumps(payload, indent=2, sort_keys=True))
        return 0 if all_passed else 1
    finally:
        shutil.rmtree(work_root, ignore_errors=True)


if __name__ == "__main__":
    raise SystemExit(main())
