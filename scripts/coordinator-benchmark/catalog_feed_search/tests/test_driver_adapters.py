"""Isolated unit tests for CF02 sidecar driver, adapters, provenance, in-process lane, live controls, and CLI."""

from __future__ import annotations

from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import tempfile
import unittest

from catalog_feed_search import cli
from catalog_feed_search.checker import check_catalog, check_search
from catalog_feed_search.checker_replace import check_replace
from catalog_feed_search.checker_types import Verdict
from catalog_feed_search.driver_adapters import (
    map_agent_tool_result,
    map_replace_apply_to_answer,
    map_search_response_to_answer,
    map_source_index_coverage,
    map_sse_source_changed_to_answer,
)
from catalog_feed_search.driver_inprocess import build_task_digest_argv, run_inprocess_suite
from catalog_feed_search.driver_provenance import (
    collect_run_provenance,
    file_sha256,
    write_evidence_manifest,
)
from catalog_feed_search.driver_sidecar import SidecarDriver
from catalog_feed_search.live_controls import run_three_live_controls
from catalog_feed_search.references import ReplaceFileResult

REPO_ROOT = Path(__file__).resolve().parents[4]


class StubTransport:
    """In-memory SidecarTransport recording all requests and returning scripted D1 responses."""

    def __init__(self, handler) -> None:
        self._handler = handler
        self.calls: list[tuple[str, str, dict[str, object] | None]] = []

    def request(
        self,
        path: str,
        body: dict[str, object] | None = None,
        *,
        method: str | None = None,
    ) -> dict[str, object]:
        eff_method = method or ("POST" if body is not None else "GET")
        self.calls.append((eff_method, path, body))
        return self._handler(eff_method, path, body)

    def request_raw(
        self,
        path: str,
        body: dict[str, object] | None = None,
        *,
        method: str | None = None,
    ) -> tuple[bytes, dict[str, str]]:
        eff_method = method or ("POST" if body is not None else "GET")
        self.calls.append((eff_method, path, body))
        resp = self._handler(eff_method, path, body)
        return resp["raw"], resp.get("headers", {})


class DriverAdapterMapperTests(unittest.TestCase):
    def test_coverage_and_search_issue_mappers(self) -> None:
        gaps = map_source_index_coverage(
            [
                {"root_id": "r1", "state": "warming"},
                {"root_id": "r2", "state": "bounded", "refreshing": True},
                {"root_id": "r3", "state": "ready", "unreadable_dirs": 2},
            ]
        )
        reasons = [(g.reason, g.root_id) for g in gaps]
        self.assertIn(("catalog_warming", "r1"), reasons)
        self.assertIn(("catalog_bounded", "r2"), reasons)
        self.assertIn(("catalog_refreshing", "r2"), reasons)
        self.assertIn(("catalog_failed", "r3"), reasons)

        search_ans = map_search_response_to_answer(
            {
                "status": "limited",
                "next_cursor": "cur-2",
                "hits": [
                    {
                        "hit_id": "h1",
                        "hit_kind": "code",
                        "root_id": "r1",
                        "path": "src/a.txt",
                        "line": 1,
                        "column": 1,
                        "byte_offset": 0,
                        "byte_end": 5,
                        "match_text": "alpha",
                        "line_text": "alpha\n",
                    }
                ],
                "issues": [
                    {"executor": "code", "reason": "result_limit", "limit": 1},
                    {"executor": "symbol", "reason": "symbol_budget", "count": 1},
                ],
            },
            continuation_required=True,
        )
        self.assertEqual(len(search_ans.hits), 1)
        self.assertEqual(search_ans.hits[0].leg, "code")
        self.assertEqual({g.reason for g in search_ans.declared_gaps}, {"result_limit", "symbol_budget"})
        limit_gap = next(g for g in search_ans.declared_gaps if g.reason == "result_limit")
        self.assertTrue(limit_gap.truncated)
        self.assertEqual(limit_gap.continuation, "cur-2")

    def test_sse_replace_and_agent_mappers(self) -> None:
        feed = map_sse_source_changed_to_answer(
            [
                {"topic": "unrelated", "data": {}},
                {
                    "id": "ev-1",
                    "topic": "source_changed",
                    "data": {
                        "project_id": "p1",
                        "resync": False,
                        "changes": [{"path": "src/a.txt", "kind": "edit", "root_id": "r1"}],
                    },
                },
                {
                    "id": "ev-2",
                    "topic": "source_changed",
                    "data": {"project_id": "other-proj", "resync": True, "changes": []},
                },
            ],
            project_id="p1",
            default_root_id="r1",
        )
        self.assertEqual(len(feed.events), 1)
        self.assertEqual(feed.events[0].path, "src/a.txt")
        self.assertEqual(feed.events[0].op, "edit")
        self.assertEqual(feed.declared_gaps, ())

        rep = map_replace_apply_to_answer(
            {"files": [{"root_id": "r1", "path": "src/a.txt", "applied": False, "skipped": True, "reason": "sha256 drift"}]},
            {"state": "limited", "files": [{"root_id": "r1", "path": "src/a.txt", "sha256": "orig", "hunks": [{}]}], "issues": []},
            disk_hashes_by_key={("r1", "src/a.txt"): "drifted_sha"},
            drifted_keys={("r1", "src/a.txt")},
            drifted_hashes={("r1", "src/a.txt"): "drifted_sha"},
        )
        ref = {("r1", "src/a.txt"): ReplaceFileResult("src/a.txt", "orig", "after", b"after", 1, True, root_id="r1")}
        self.assertEqual(check_replace(ref, rep).verdict, Verdict.PASS)

        obs = map_agent_tool_result(
            "survey_repo",
            {"ok": False, "rejection_code": "SURVEY_INVENTORY_WARMING", "fresh": False, "generation_id": "g-4"},
        )
        self.assertFalse(obs.ok)
        self.assertEqual(obs.rejection_code, "SURVEY_INVENTORY_WARMING")
        self.assertIn("stale_inventory", obs.declared_gaps)


class SidecarDriverAndLiveControlsTests(unittest.TestCase):
    def test_sidecar_driver_routes_and_three_live_controls(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            fixture_dir = tmp_path / "fixture"
            out_dir = tmp_path / "evidence"

            def handler(method: str, path: str, body):
                if method == "POST" and path == "/v1/projects":
                    return {"id": "proj-1", "roots": [{"id": "root-1"}]}
                if method == "GET" and path == "/v1/projects/proj-1/source/index":
                    return {"state": "ready", "revision": 1, "refreshing": False, "file_count": 3, "roots": [], "coverage": []}
                if method == "GET" and path.startswith("/v1/projects/proj-1/source/browse"):
                    if "dir=src" in path:
                        return {
                            "entries": [
                                {"name": "alpha.txt", "path": "src/alpha.txt", "kind": "file"},
                                {"name": "control_write.txt", "path": "src/control_write.txt", "kind": "file"},
                            ]
                        }
                    if "dir=docs" in path:
                        return {"entries": [{"name": "readme.md", "path": "docs/readme.md", "kind": "file"}]}
                    return {
                        "entries": [
                            {"name": "docs", "path": "docs", "kind": "dir"},
                            {"name": "src", "path": "src", "kind": "dir"},
                        ]
                    }
                if method == "POST" and path == "/v1/search":
                    return {
                        "status": "complete",
                        "exhaustive": True,
                        "total_hits": 1,
                        "count_relation": "exact",
                        "facets_exhaustive": True,
                        "hits": [
                            {
                                "hit_id": "h-1",
                                "hit_kind": "code",
                                "root_id": "root-1",
                                "path": "src/alpha.txt",
                                "line": 1,
                                "column": 1,
                                "byte_offset": 0,
                                "byte_end": 23,
                                "match_text": "PaintedWolfControlToken",
                                "line_text": "PaintedWolfControlToken line one\n",
                            }
                        ],
                        "issues": [],
                    }
                if method == "POST" and path == "/v1/search/export":
                    return {"raw": b'{"path":"src/alpha.txt"}\n', "headers": {"x-export-truncated": "false"}}
                if method == "GET" and path.startswith("/v1/projects/proj-1/source/search"):
                    return {"state": "ready", "revision": 1, "refreshing": False, "matches": [{"root_id": "root-1", "path": "src/alpha.txt", "highlights": []}], "coverage": []}
                if method == "POST" and path == "/v1/projects/proj-1/editor-documents":
                    return {"id": "doc-1", "revision": 1}
                if method == "PUT" and path == "/v1/projects/proj-1/editor-documents/doc-1":
                    return {"id": "doc-1", "revision": 2}
                raise AssertionError(f"unexpected call: {method} {path}")

            transport = StubTransport(handler)
            driver = SidecarDriver(transport)
            provenance = collect_run_provenance(
                harness_repo=REPO_ROOT,
                work_dir=tmp_path,
                binary_path=None,
                d1_commit="3bb1464d22dd0a38a149d37610810e7a0324e8cf",
                clock_domain="manual",
            )
            sse_frames = [
                {
                    "id": "batch-1",
                    "topic": "source_changed",
                    "data": {
                        "project_id": "proj-1",
                        "resync": False,
                        "changes": [{"path": "src/control_write.txt", "kind": "edit", "root_id": "root-1"}],
                    },
                }
            ]
            ticks = iter([100, 200])
            report = run_three_live_controls(
                driver,
                fixture_dir,
                lambda: sse_frames,
                provenance=provenance,
                out_dir=out_dir,
                clock_fn=lambda: next(ticks),
                clock_domain="manual",
            )
            self.assertTrue(report.all_passed, report.to_dict())
            self.assertEqual(len(report.controls), 3)
            self.assertIsNotNone(report.manifest_path)
            manifest = json.loads(report.manifest_path.read_text(encoding="utf-8"))  # type: ignore[union-attr]
            self.assertEqual(manifest["provenance"]["product"]["source_commit"], "3bb1464d22dd0a38a149d37610810e7a0324e8cf")
            self.assertTrue(manifest["provenance"]["rg_version"].startswith("ripgrep 15."))
            self.assertEqual(
                manifest["artifacts"]["live_controls"]["sha256"],
                file_sha256(out_dir / "live_controls.json"),
            )

            # Exercise export, file_lookup, and draft injection routes.
            exported, truncated = driver.export_search("PaintedWolfControlToken", origin_project_id="proj-1")
            self.assertFalse(truncated)
            self.assertIn("src/alpha.txt", exported)
            lookup = driver.file_lookup("proj-1", "alpha")
            self.assertEqual(lookup["matches"][0]["path"], "src/alpha.txt")  # type: ignore[index]
            draft = driver.inject_editor_draft("proj-1", "root-1", "src/alpha.txt", "draft text\n")
            self.assertEqual(draft["revision"], 2)


class InProcessAndCLITests(unittest.TestCase):
    def test_inprocess_lane_uses_task_digest_and_parses_results(self) -> None:
        argv = build_task_digest_argv(REPO_ROOT, "./internal/search/...", run_pattern="TestCodeBudget")
        self.assertTrue(argv[0].endswith("/task"))
        self.assertEqual(argv[1:4], ("test:digest", "--", "./internal/search/..."))

        fake_out = "--- PASS: TestCodeBudget (0.01s)\nok  \tgithub.com/lycaon/lycaon/internal/search\t0.02s\n"
        res = run_inprocess_suite(
            REPO_ROOT,
            "./internal/search/...",
            run_pattern="TestCodeBudget",
            executor=lambda _argv, _cwd: (0, fake_out, ""),
        )
        self.assertTrue(res.passed)
        self.assertEqual(res.passed_tests, ("TestCodeBudget",))
        self.assertEqual(res.failed_tests, ())

    def test_cli_controls_and_replay_subcommands(self) -> None:
        buf_ctrl = io.StringIO()
        with tempfile.TemporaryDirectory() as tmp, redirect_stdout(buf_ctrl):
            code = cli.main(["controls", "--out-dir", tmp])
            self.assertEqual(code, 0)
            self.assertTrue((Path(tmp) / "checker_controls.json").is_file())
        parsed_ctrl = json.loads(buf_ctrl.getvalue())
        self.assertTrue(parsed_ctrl["all_passed"])
        self.assertEqual(parsed_ctrl["controls"]["passed"], 20)

        buf_r1 = io.StringIO()
        buf_r2 = io.StringIO()
        with redirect_stdout(buf_r1):
            self.assertEqual(cli.main(["replay", "--seed", "20261127", "--family", "S01", "--instance", "3"]), 0)
        with redirect_stdout(buf_r2):
            self.assertEqual(cli.main(["replay", "--seed", "20261127", "--family", "S01", "--instance", "3"]), 0)
        self.assertEqual(json.loads(buf_r1.getvalue()), json.loads(buf_r2.getvalue()))


if __name__ == "__main__":
    unittest.main()
