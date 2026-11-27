"""Isolated unit tests for CF02 sidecar driver, D1 wire adapters, SSE, in-process lane, and CLI."""

from __future__ import annotations

from contextlib import redirect_stdout
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import io
import json
from pathlib import Path
import tempfile
import threading
import unittest

from catalog_feed_search import cli
from catalog_feed_search.checker import ScopedSearchHit, check_search
from catalog_feed_search.checker_replace import check_replace
from catalog_feed_search.checker_types import Verdict
from catalog_feed_search.driver_adapters import (
    map_agent_tool_result,
    map_replace_apply_to_answer,
    map_search_response_to_answer,
    map_source_index_coverage,
    map_sse_source_changed_to_answer,
)
from catalog_feed_search.driver_inprocess import build_task_digest_argv, parse_digest_output, run_inprocess_suite
from catalog_feed_search.driver_provenance import collect_run_provenance, file_sha256
from catalog_feed_search.driver_sidecar import SidecarDriver
from catalog_feed_search.driver_sse import LiveSSESubscriber
from catalog_feed_search.live_controls import run_three_live_controls
from catalog_feed_search.references import ReplaceFileResult, SearchHit

REPO_ROOT = Path(__file__).resolve().parents[4]


class StubTransport:
    """In-memory SidecarTransport recording all requests and returning pinned D1 OpenAPI payloads."""

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


class StubSSESubscription:
    """Deterministic SSESubscription verifying readiness barrier ordering before writes."""

    def __init__(self, frames_after_write: list[dict[str, object]]) -> None:
        self._frames = frames_after_write
        self.started = False
        self.ready_checked = False
        self.closed = False

    def start(self) -> None:
        self.started = True

    def wait_ready(self) -> bool:
        self.ready_checked = True
        return True

    def wait_for_frames(self, predicate) -> list[dict[str, object]]:
        return list(self._frames)

    def frames(self) -> list[dict[str, object]]:
        return list(self._frames)

    def close(self) -> None:
        self.closed = True


class D1WireContractAdapterTests(unittest.TestCase):
    def test_exact_d1_source_index_root_coverage_never_hides_partial_root(self) -> None:
        # Pinned D1 SourceIndexRootCoverage shape: state="ready" with discovery_complete=False,
        # bounded_directories, failed_directories, refreshing, and error.
        gaps = map_source_index_coverage(
            [
                {
                    "root_id": "r-partial",
                    "state": "ready",
                    "discovery_complete": False,
                    "refreshing": False,
                    "bounded_directories": 2,
                    "failed_directories": 1,
                    "error": "refresh permission denied",
                },
                {
                    "root_id": "r-warming",
                    "state": "warming",
                    "discovery_complete": False,
                    "refreshing": True,
                    "bounded_directories": 0,
                    "failed_directories": 0,
                },
            ]
        )
        by_root = {(g.root_id, g.reason) for g in gaps}
        self.assertIn(("r-partial", "catalog_incomplete"), by_root)
        self.assertIn(("r-partial", "catalog_bounded"), by_root)
        self.assertIn(("r-partial", "catalog_failed"), by_root)
        self.assertIn(("r-partial", "catalog_refresh_failed"), by_root)
        self.assertIn(("r-warming", "catalog_warming"), by_root)
        self.assertIn(("r-warming", "catalog_refreshing"), by_root)

    def test_exact_d1_search_hit_path_line_projection_and_exact_span_unknown(self) -> None:
        # Pinned D1 SearchResponse / SearchHit payload (NO byte_offset/byte_end/match_text).
        d1_payload = {
            "status": "complete",
            "exhaustive": True,
            "total_hits": 1,
            "count_relation": "exact",
            "facets_exhaustive": True,
            "hits": [
                {
                    "hit_id": "11111111-1111-1111-1111-111111111111",
                    "hit_kind": "code",
                    "source": "code",
                    "project_id": "proj-1",
                    "root_id": "root-1",
                    "title": "src/alpha.txt:1",
                    "path": "src/alpha.txt",
                    "line": 1,
                    "snippet": "PaintedWolfControlToken line one",
                }
            ],
            "issues": [],
        }
        ans = map_search_response_to_answer(d1_payload)
        self.assertEqual(ans.projection, "path_line")
        self.assertFalse(ans.hits[0].span_available)

        ref_hit = ScopedSearchHit(
            SearchHit("src/alpha.txt", 1, 1, 0, 23, "PaintedWolfControlToken", "PaintedWolfControlToken line one\n"),
            root_id="root-1",
            leg="code",
        )
        # path_line projection passes when (root_id, leg, path, line) matches.
        self.assertEqual(check_search((ref_hit,), ans, projection="path_line").verdict, Verdict.PASS)
        # exact_span comparison on spanless wire hits returns UNKNOWN (never invents offsets).
        self.assertEqual(check_search((ref_hit,), ans, projection="exact_span").verdict, Verdict.UNKNOWN)
        # Duplicate hit on same line is still caught as DEFECT under path_line.
        dup_ans = map_search_response_to_answer(
            {**d1_payload, "hits": [d1_payload["hits"][0], d1_payload["hits"][0]]}
        )
        self.assertEqual(check_search((ref_hit,), dup_ans, projection="path_line").verdict, Verdict.DEFECT)

    def test_exact_d1_sse_source_changed_replace_and_agent_mappers(self) -> None:
        # Pinned D1 EventEnvelopeSourceChanged + SourceChangesEvent + SourceChange.
        feed = map_sse_source_changed_to_answer(
            [
                {
                    "id": "env-42",
                    "topic": "source_changed",
                    "data": {
                        "project_id": "proj-1",
                        "workspace_id": "ws_0123456789abcdef0123456789abcdef",
                        "workspace_kind": "primary",
                        "resync": False,
                        "changes": [
                            {
                                "root_id": "root-1",
                                "path": "src/new.txt",
                                "from_path": "src/old.txt",
                                "op": "rename",
                                "origin": "external",
                                "changed_at": "2026-11-27T19:00:00Z",
                            }
                        ],
                    },
                }
            ],
            project_id="proj-1",
            workspace_id="ws_0123456789abcdef0123456789abcdef",
            default_root_id="root-1",
        )
        self.assertEqual([e.path for e in feed.events], ["src/new.txt", "src/old.txt"])
        self.assertTrue(all(e.batch_id == "ws_0123456789abcdef0123456789abcdef:env-42" for e in feed.events))

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


class SidecarLiveSSESubscriberAndControlsTests(unittest.TestCase):
    def test_real_live_sse_subscriber_readiness_barrier_and_frame_drain(self) -> None:
        release_event = threading.Event()

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self) -> None:
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self.wfile.flush()
                release_event.wait()
                frame = json.dumps(
                    {
                        "id": "env-live-1",
                        "topic": "source_changed",
                        "data": {
                            "project_id": "proj-1",
                            "workspace_id": "ws_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                            "workspace_kind": "primary",
                            "resync": False,
                            "changes": [
                                {
                                    "root_id": "root-1",
                                    "path": "src/control_write.txt",
                                    "op": "write",
                                    "origin": "external",
                                    "changed_at": "2026-11-27T19:00:00Z",
                                }
                            ],
                        },
                    }
                )
                self.wfile.write(f"data: {frame}\n\n".encode("utf-8"))
                self.wfile.flush()

            def log_message(self, format: str, *args) -> None:
                return

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        port = server.server_address[1]
        srv_thread = threading.Thread(target=server.serve_forever, daemon=True)
        srv_thread.start()
        try:
            with tempfile.TemporaryDirectory() as tmp:
                jsonl = Path(tmp) / "events.jsonl"
                sub = LiveSSESubscriber(f"http://127.0.0.1:{port}", "tok", out_jsonl=jsonl)
                sub.start()
                self.assertTrue(sub.wait_ready())
                # Release frame ONLY AFTER readiness barrier succeeded.
                release_event.set()
                frames = sub.wait_for_frames(lambda fs: len(fs) >= 1)
                sub.close()
                self.assertEqual(len(frames), 1)
                self.assertEqual(frames[0]["id"], "env-live-1")
                self.assertTrue(jsonl.is_file())
        finally:
            server.shutdown()
            server.server_close()

    def test_sidecar_browse_is_dir_catalog_index_draft_put_and_three_live_controls(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            fixture_dir = tmp_path / "fixture"
            out_dir = tmp_path / "evidence"

            def handler(method: str, path: str, body):
                if method == "POST" and path == "/v1/projects":
                    return {"id": "proj-1", "roots": [{"id": "root-1"}]}
                if method == "GET" and path == "/v1/projects/proj-1/source/index":
                    return {
                        "state": "ready",
                        "revision": 7,
                        "refreshing": False,
                        "file_count": 3,
                        "roots": [],
                        "coverage": [
                            {
                                "root_id": "root-1",
                                "state": "ready",
                                "discovery_complete": True,
                                "refreshing": False,
                                "bounded_directories": 0,
                                "failed_directories": 0,
                            }
                        ],
                    }
                if method == "GET" and path.startswith("/v1/projects/proj-1/source/browse"):
                    # Exact D1 SourceDirListing + SourceDirEntry (`name`, `is_dir`).
                    if "dir=src" in path:
                        return {
                            "workspace_id": "ws_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                            "root_id": "root-1",
                            "dir": "src",
                            "watch_complete": True,
                            "entries": [
                                {"name": "alpha.txt", "is_dir": False},
                                {"name": "control_write.txt", "is_dir": False},
                            ],
                        }
                    if "dir=docs" in path:
                        return {
                            "workspace_id": "ws_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                            "root_id": "root-1",
                            "dir": "docs",
                            "watch_complete": True,
                            "entries": [{"name": "readme.md", "is_dir": False}],
                        }
                    return {
                        "workspace_id": "ws_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                        "root_id": "root-1",
                        "dir": ".",
                        "watch_complete": True,
                        "entries": [
                            {"name": "docs", "is_dir": True},
                            {"name": "src", "is_dir": True},
                        ],
                    }
                if method == "GET" and path.startswith("/v1/projects/proj-1/source/search"):
                    return {
                        "state": "ready",
                        "revision": 7,
                        "refreshing": False,
                        "coverage": [
                            {
                                "root_id": "root-1",
                                "state": "ready",
                                "discovery_complete": True,
                                "refreshing": False,
                                "bounded_directories": 0,
                                "failed_directories": 0,
                            }
                        ],
                        "matches": [
                            {"root_id": "root-1", "path": "docs/readme.md", "highlights": []},
                            {"root_id": "root-1", "path": "src/alpha.txt", "highlights": []},
                            {"root_id": "root-1", "path": "src/control_write.txt", "highlights": []},
                        ],
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
                                "hit_id": "11111111-1111-1111-1111-111111111111",
                                "hit_kind": "code",
                                "source": "code",
                                "project_id": "proj-1",
                                "root_id": "root-1",
                                "title": "src/alpha.txt:1",
                                "path": "src/alpha.txt",
                                "line": 1,
                                "snippet": "PaintedWolfControlToken line one",
                            }
                        ],
                        "issues": [],
                    }
                if method == "POST" and path == "/v1/projects/proj-1/editor-documents":
                    return {"id": "doc-1", "revision": 1}
                if method == "PUT" and path == "/v1/projects/proj-1/editor-documents/doc-1":
                    # Verify all required D1 PUT fields are present.
                    for required_key in ("mixed_eol", "history_vector", "session_id", "operation_id", "expected_revision"):
                        self.assertIn(required_key, body)
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
            sse_sub = StubSSESubscription(
                [
                    {
                        "id": "env-1",
                        "topic": "source_changed",
                        "data": {
                            "project_id": "proj-1",
                            "workspace_id": "ws_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                            "workspace_kind": "primary",
                            "resync": False,
                            "changes": [
                                {
                                    "root_id": "root-1",
                                    "path": "src/control_write.txt",
                                    "op": "write",
                                    "origin": "external",
                                    "changed_at": "2026-11-27T19:00:00Z",
                                }
                            ],
                        },
                    }
                ]
            )
            ticks = iter([100, 200])
            report = run_three_live_controls(
                driver,
                fixture_dir,
                sse_sub,
                provenance=provenance,
                out_dir=out_dir,
                clock_fn=lambda: next(ticks),
                clock_domain="manual",
            )
            self.assertTrue(report.all_passed, report.to_dict())
            self.assertTrue(sse_sub.started and sse_sub.ready_checked and sse_sub.closed)
            self.assertEqual(report.controls[0].evidence["projection"], "catalog")
            self.assertEqual(report.controls[0].evidence["generation_revision"], 7)

            # Verify browse projection descends `is_dir: True` and labels projection="browse".
            browse_ans = driver.walk_browse("proj-1", ["root-1"])
            self.assertEqual(browse_ans.projection, "browse")
            self.assertIn("src/alpha.txt", {i.path for i in browse_ans.items})

            # Verify D1 editor-document PUT includes mixed_eol, history_vector, session_id.
            draft = driver.inject_editor_draft("proj-1", "root-1", "src/alpha.txt", "draft\n")
            self.assertEqual(draft["revision"], 2)


class InProcessAndCLITests(unittest.TestCase):
    def test_inprocess_runner_requires_observed_pilot_cases(self) -> None:
        argv = build_task_digest_argv(REPO_ROOT, "./internal/search/...", pilot_test="TestCodeBudget")
        self.assertTrue(argv[0].endswith("/task"))
        self.assertIn("-v", argv)
        self.assertIn("^TestCodeBudget$", argv)

        # Exit 0 with 0 observed executed cases -> UNKNOWN, NOT qualified!
        empty_res = parse_digest_output(argv, 0, "ok  \tgithub.com/lycaon/lycaon/internal/search\t0.01s\n", "", pilot_test="TestCodeBudget")
        self.assertFalse(empty_res.qualified)
        self.assertEqual(empty_res.verdict, "UNKNOWN")
        self.assertEqual(empty_res.executed_cases, 0)

        # Exit 0 with a different test passing (pilot missing) -> UNKNOWN!
        other_res = parse_digest_output(argv, 0, "--- PASS: TestOther (0.01s)\n", "", pilot_test="TestCodeBudget")
        self.assertFalse(other_res.qualified)
        self.assertEqual(other_res.verdict, "UNKNOWN")
        self.assertFalse(other_res.pilot_matched)

        # Exit 0 with selected pilot passing -> PASS & qualified!
        ok_out = "--- PASS: TestCodeBudget (0.01s)\nok  \tgithub.com/lycaon/lycaon/internal/search\t0.02s\n"
        ok_res = run_inprocess_suite(
            REPO_ROOT,
            "./internal/search/...",
            pilot_test="TestCodeBudget",
            executor=lambda _argv, _cwd: (0, ok_out, ""),
        )
        self.assertTrue(ok_res.qualified)
        self.assertEqual(ok_res.verdict, "PASS")
        self.assertEqual(ok_res.executed_cases, 1)
        self.assertTrue(ok_res.pilot_matched)

    def test_cli_controls_and_replay_subcommands(self) -> None:
        buf_ctrl = io.StringIO()
        with tempfile.TemporaryDirectory() as tmp, redirect_stdout(buf_ctrl):
            self.assertEqual(cli.main(["controls", "--out-dir", tmp]), 0)
        parsed = json.loads(buf_ctrl.getvalue())
        self.assertTrue(parsed["all_passed"])

        buf_rep = io.StringIO()
        with redirect_stdout(buf_rep):
            self.assertEqual(cli.main(["replay", "--seed", "20261127", "--family", "S01", "--instance", "3"]), 0)
        self.assertEqual(json.loads(buf_rep.getvalue())["selector"]["family_id"], "S01")


if __name__ == "__main__":
    unittest.main()
