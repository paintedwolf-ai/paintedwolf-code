"""Isolated unit tests for CF02 sidecar driver, D1 wire adapters, SSE, in-process lane, and CLI."""

from __future__ import annotations

from contextlib import redirect_stdout
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import io
import json
from pathlib import Path
import sys
import tempfile
import threading
import unittest

_BENCHMARK_DIR = str(Path(__file__).resolve().parents[2])
if _BENCHMARK_DIR not in sys.path:
    sys.path.insert(0, _BENCHMARK_DIR)

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
from catalog_feed_search.driver_inprocess import (
    build_task_digest_argv,
    extract_receipt_path,
    parse_receipt_and_go_json,
    resolve_last_run_go_json,
    run_inprocess_suite,
)
from catalog_feed_search.driver_provenance import (
    collect_run_provenance,
    file_sha256,
    parse_darwin_mount_fs,
    parse_proc_mounts_fs,
)
from catalog_feed_search.driver_sidecar import SidecarDriver
from catalog_feed_search.driver_sse import LiveSSESubscriber
from catalog_feed_search.live_controls import (
    DRAIN_SENTINEL_REL_PATH,
    run_live_control_catalog,
    run_live_control_change_feed,
    run_three_live_controls,
    wait_catalog_settled,
)
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
    """Deterministic SSESubscription supporting staged frame arrival across waits."""

    def __init__(self, project_id: str, stages: list[list[dict[str, object]]]) -> None:
        self.project_id = project_id
        self._stages = list(stages)
        self._frames: list[dict[str, object]] = []
        self._stage_idx = 0
        self.started = False
        self.ready_checked = False
        self.closed = False

    def start(self) -> None:
        self.started = True

    def wait_ready(self, max_wait_s: float = 10.0) -> bool:
        self.ready_checked = True
        return True

    def wait_for_frames(self, predicate, max_wait_s: float = 10.0) -> tuple[list[dict[str, object]], bool]:
        if self._stage_idx < len(self._stages):
            self._frames.extend(self._stages[self._stage_idx])
            self._stage_idx += 1
        return list(self._frames), bool(predicate(self._frames))

    def frames(self) -> list[dict[str, object]]:
        return list(self._frames)

    def close(self, max_wait_s: float = 3.0) -> None:
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
    def test_real_live_sse_subscriber_project_scope_readiness_and_frame_drain(self) -> None:
        release_event = threading.Event()
        seen_paths: list[str] = []

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self) -> None:
                seen_paths.append(self.path)
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self.wfile.flush()
                release_event.wait()
                # Only deliver project-scoped events matching the requested project_id.
                if "project_id=proj-1" in self.path:
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
                sub = LiveSSESubscriber(
                    f"http://127.0.0.1:{port}",
                    "tok",
                    project_id="proj-1",
                    out_jsonl=jsonl,
                )
                sub.start()
                self.assertTrue(sub.wait_ready())
                release_event.set()
                frames, matched = sub.wait_for_frames(lambda fs: len(fs) >= 1)
                sub.close()
                self.assertTrue(matched)
                self.assertEqual(len(frames), 1)
                self.assertEqual(frames[0]["id"], "env-live-1")
                self.assertTrue(any("project_id=proj-1" in p for p in seen_paths), seen_paths)
                self.assertTrue(jsonl.is_file())
        finally:
            server.shutdown()
            server.server_close()

    def test_live_sse_missing_event_exits_cleanly_as_unknown(self) -> None:
        stop_server_stream = threading.Event()

        class SilentKeepaliveHandler(BaseHTTPRequestHandler):
            def do_GET(self) -> None:
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self.wfile.write(b": keepalive\n\n")
                self.wfile.flush()
                stop_server_stream.wait()

            def log_message(self, format: str, *args) -> None:
                return

        server = ThreadingHTTPServer(("127.0.0.1", 0), SilentKeepaliveHandler)
        port = server.server_address[1]
        srv_thread = threading.Thread(target=server.serve_forever, daemon=True)
        srv_thread.start()
        try:
            with tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                (root / "src").mkdir()
                (root / "src/control_write.txt").write_bytes(b"init\n")
                sub = LiveSSESubscriber(
                    f"http://127.0.0.1:{port}",
                    "tok",
                    project_id="proj-silent",
                )
                ticks = iter([10, 20])
                res = run_live_control_change_feed(
                    root,
                    "proj-silent",
                    "root-1",
                    sub,
                    ready_wait_s=2.0,
                    init_wait_s=0.0,
                    observe_wait_s=0.1,
                    drain_wait_s=0.1,
                    clock_fn=lambda: next(ticks),
                    clock_domain="manual",
                )
                self.assertFalse(res.passed)
                self.assertEqual(res.check.verdict, Verdict.UNKNOWN)
                self.assertFalse(res.evidence["primary_arrived"])
        finally:
            stop_server_stream.set()
            server.shutdown()
            server.server_close()

    def test_sentinel_drain_boundary_catches_duplicate_in_later_batch(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "src").mkdir()
            (root / "src/control_write.txt").write_bytes(b"init\n")
            sub = StubSSESubscription(
                "proj-1",
                [
                    [],  # no baseline frame
                    [
                        {
                            "id": "batch-1",
                            "topic": "source_changed",
                            "data": {
                                "project_id": "proj-1",
                                "workspace_id": "ws_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                                "resync": False,
                                "changes": [{"root_id": "root-1", "path": "src/control_write.txt", "op": "write"}],
                            },
                        },
                        {
                            "id": "batch-2",
                            "topic": "source_changed",
                            "data": {
                                "project_id": "proj-1",
                                "workspace_id": "ws_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                                "resync": False,
                                "changes": [{"root_id": "root-1", "path": "src/control_write.txt", "op": "write"}],
                            },
                        },
                    ],
                    [
                        {
                            "id": "batch-3",
                            "topic": "source_changed",
                            "data": {
                                "project_id": "proj-1",
                                "workspace_id": "ws_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                                "resync": False,
                                "changes": [{"root_id": "root-1", "path": DRAIN_SENTINEL_REL_PATH, "op": "write"}],
                            },
                        },
                    ],
                ],
            )
            ticks = iter([10, 20, 30])
            res = run_live_control_change_feed(
                root,
                "proj-1",
                "root-1",
                sub,
                clock_fn=lambda: next(ticks),
                clock_domain="manual",
            )
            self.assertFalse(res.passed)
            self.assertEqual(res.check.verdict, Verdict.DEFECT)
            self.assertEqual(res.evidence["drain_boundary"], "sentinel_write")

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
                            {"root_id": "root-1", "path": DRAIN_SENTINEL_REL_PATH, "highlights": []},
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
            created_subs: list[StubSSESubscription] = []

            def sse_factory(pid: str) -> StubSSESubscription:
                sub = StubSSESubscription(
                    pid,
                    [
                        [
                            {
                                "id": "env-init",
                                "topic": "source_changed",
                                "data": {
                                    "project_id": pid,
                                    "workspace_id": "ws_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                                    "workspace_kind": "primary",
                                    "resync": False,
                                    "changes": [],
                                },
                            }
                        ],
                        [
                            {
                                "id": "env-1",
                                "topic": "source_changed",
                                "data": {
                                    "project_id": pid,
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
                        ],
                        [
                            {
                                "id": "env-2",
                                "topic": "source_changed",
                                "data": {
                                    "project_id": pid,
                                    "workspace_id": "ws_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                                    "workspace_kind": "primary",
                                    "resync": False,
                                    "changes": [
                                        {
                                            "root_id": "root-1",
                                            "path": DRAIN_SENTINEL_REL_PATH,
                                            "op": "write",
                                            "origin": "external",
                                            "changed_at": "2026-11-27T19:00:01Z",
                                        }
                                    ],
                                },
                            }
                        ],
                    ],
                )
                created_subs.append(sub)
                return sub

            ticks = iter([100, 200, 300])
            report = run_three_live_controls(
                driver,
                fixture_dir,
                sse_factory,
                provenance=provenance,
                out_dir=out_dir,
                replay_command=["python3", "-m", "catalog_feed_search.cli", "live-controls", "--out-dir", "."],
                launch_receipt={"pid": 12345, "url": "http://127.0.0.1:9999", "binary_sha256": "abc123"},
                clock_fn=lambda: next(ticks),
                clock_domain="manual",
            )
            self.assertTrue(report.all_passed, report.to_dict())
            self.assertEqual(len(created_subs), 1)
            self.assertEqual(created_subs[0].project_id, "proj-1")
            self.assertTrue(created_subs[0].started and created_subs[0].ready_checked and created_subs[0].closed)
            self.assertEqual(report.controls[0].evidence["projection"], "catalog")
            self.assertEqual(report.controls[0].evidence["generation_revision"], 7)
            self.assertIn("raw_index_summary", report.controls[0].evidence)
            self.assertIn("raw_search_response", report.controls[2].evidence)
            self.assertEqual(
                report.controls[2].evidence["projected_product_hits"],
                report.controls[2].evidence["projected_reference_hits"],
            )

            # Verify manifest uses locally-resolvable relative paths + replay_command + launch_receipt.
            manifest = json.loads(report.manifest_path.read_text(encoding="utf-8"))  # type: ignore[union-attr]
            self.assertEqual(manifest["artifacts"]["live_controls"]["path"], "live_controls.json")
            self.assertEqual(manifest["replay_command"][0], "python3")
            self.assertEqual(manifest["launch_receipt"]["pid"], 12345)

            # Verify browse projection descends `is_dir: True` and labels projection="browse".
            browse_ans = driver.walk_browse("proj-1", ["root-1"])
            self.assertEqual(browse_ans.projection, "browse")
            self.assertIn("src/alpha.txt", {i.path for i in browse_ans.items})

            # Verify D1 editor-document PUT includes mixed_eol, history_vector, session_id.
            draft = driver.inject_editor_draft("proj-1", "root-1", "src/alpha.txt", "draft\n")
            self.assertEqual(draft["revision"], 2)

    def test_wait_catalog_settled_transitions_and_deadline_unknown(self) -> None:
        polls = {"n": 0}

        def warming_then_ready(method: str, path: str, body):
            polls["n"] += 1
            if polls["n"] == 1:
                return {
                    "state": "warming",
                    "revision": 0,
                    "refreshing": True,
                    "coverage": [
                        {
                            "root_id": "root-1",
                            "state": "warming",
                            "discovery_complete": False,
                            "refreshing": True,
                            "bounded_directories": 0,
                            "failed_directories": 0,
                        }
                    ],
                }
            return {
                "state": "ready",
                "revision": 3,
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
            }

        driver = SidecarDriver(StubTransport(warming_then_ready))
        settled, count, summary = wait_catalog_settled(driver, "proj-1", "root-1", max_wait_s=2.0, poll_interval_s=0.01)
        self.assertTrue(settled)
        self.assertEqual(count, 2)
        self.assertEqual(summary["revision"], 3)

        # Always-warming root hits deadline -> Control 1 returns Verdict.UNKNOWN.
        stuck_driver = SidecarDriver(
            StubTransport(
                lambda _m, _p, _b: {
                    "state": "warming",
                    "revision": 0,
                    "refreshing": True,
                    "coverage": [
                        {
                            "root_id": "root-1",
                            "state": "warming",
                            "discovery_complete": False,
                            "refreshing": True,
                            "bounded_directories": 0,
                            "failed_directories": 0,
                        }
                    ],
                }
            )
        )
        with tempfile.TemporaryDirectory() as tmp:
            res = run_live_control_catalog(
                stuck_driver,
                Path(tmp),
                "proj-1",
                "root-1",
                settle_wait_s=0.03,
                settle_poll_s=0.01,
            )
            self.assertFalse(res.passed)
            self.assertEqual(res.check.verdict, Verdict.UNKNOWN)
            self.assertFalse(res.evidence["settled"])

    def test_proc_mounts_linux_ext4_detection(self) -> None:
        sample = (
            "/dev/sda1 / ext4 rw,relatime 0 0\n"
            "tmpfs /tmp tmpfs rw,nosuid 0 0\n"
            "/dev/nvme0n1p2 /home ext4 rw,relatime 0 0\n"
        )
        self.assertEqual(parse_proc_mounts_fs(Path("/home/user/work/repo"), sample), "ext4")
        self.assertEqual(parse_proc_mounts_fs(Path("/tmp/cfs_pilot"), sample), "tmpfs")

        darwin_sample = (
            "/dev/disk3s1s1 on / (apfs, sealed, local, read-only, journaled)\n"
            "/dev/disk3s5 on /System/Volumes/Data (apfs, local, journaled, nobrowse)\n"
        )
        self.assertEqual(parse_darwin_mount_fs(Path("/Users/test/work"), darwin_sample), "apfs")
        resolved = resolve_last_run_go_json(REPO_ROOT)
        self.assertIsNotNone(resolved)
        self.assertTrue(str(resolved).endswith("last-run/go-test.json"))

    def test_baseline_slicing_post_sentinel_slicing_and_resync_reconciliation(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "src").mkdir()
            (root / "src/control_write.txt").write_bytes(b"init\n")

            # Stage 0: initial baseline frame; Stage 1: resync=True; Stage 2: sentinel arrives AFTER resync.
            sub = StubSSESubscription(
                "proj-1",
                [
                    [
                        {
                            "id": "env-init",
                            "topic": "source_changed",
                            "data": {"project_id": "proj-1", "resync": False, "changes": []},
                        }
                    ],
                    [
                        {
                            "id": "env-resync",
                            "topic": "source_changed",
                            "data": {"project_id": "proj-1", "resync": True, "changes": []},
                        }
                    ],
                    [
                        {
                            "id": "env-sentinel",
                            "topic": "source_changed",
                            "data": {
                                "project_id": "proj-1",
                                "resync": False,
                                "changes": [{"root_id": "root-1", "path": DRAIN_SENTINEL_REL_PATH, "op": "write"}],
                            },
                        }
                    ],
                ],
            )

            def reconcile_handler(method: str, path: str, body):
                if path == "/v1/projects/proj-1/source/index":
                    return {
                        "state": "ready",
                        "revision": 9,
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
                    }
                if path.startswith("/v1/projects/proj-1/source/search"):
                    return {
                        "state": "ready",
                        "revision": 9,
                        "refreshing": False,
                        "coverage": [],
                        "matches": [
                            {"root_id": "root-1", "path": DRAIN_SENTINEL_REL_PATH, "highlights": []},
                            {"root_id": "root-1", "path": "src/control_write.txt", "highlights": []},
                        ],
                    }
                raise AssertionError(f"unexpected call: {method} {path}")

            driver = SidecarDriver(StubTransport(reconcile_handler))
            ticks = iter([10, 20, 30])
            res = run_live_control_change_feed(
                root,
                "proj-1",
                "root-1",
                sub,
                driver=driver,
                clock_fn=lambda: next(ticks),
                clock_domain="manual",
            )
            self.assertTrue(res.passed, res.to_dict())
            self.assertEqual(res.check.verdict, Verdict.PASS_DECLARED_GAP)
            self.assertTrue(res.evidence["init_baseline_seen"])
            self.assertEqual(res.evidence["baseline_frame_count"], 1)


class InProcessAndCLITests(unittest.TestCase):
    def test_inprocess_runner_requires_receipt_and_go_json_pilot_run_and_pass(self) -> None:
        argv = build_task_digest_argv(REPO_ROOT, "./internal/search/...", pilot_test="TestCodeBudget")
        self.assertTrue(argv[0].endswith("/task"))
        self.assertIn("^TestCodeBudget$", argv)

        # 1. Exit 0 with missing receipt -> UNKNOWN, NOT qualified.
        missing_res = parse_receipt_and_go_json(
            argv,
            0,
            "Request test:digest: passed, exit 0.\n",
            "",
            pilot_test="TestCodeBudget",
        )
        self.assertFalse(missing_res.qualified)
        self.assertEqual(missing_res.verdict, "UNKNOWN")
        self.assertEqual(missing_res.executed_cases, 0)

        with tempfile.TemporaryDirectory() as tmp:
            artifact_dir = Path(tmp)
            go_json = artifact_dir / "stage-001-go.json"
            receipt_file = artifact_dir / "req-001.json"
            receipt_payload = {
                "request": "req-001",
                "status": "passed",
                "source_commit": "3bb1464d22dd0a38a149d37610810e7a0324e8cf",
                "evidence": [
                    {
                        "stage": "test:digest",
                        "results": {
                            "github.com/lycaon/lycaon/internal/search": {
                                "status": "passed",
                                "stage_id": "stage-001",
                                "raw": str(go_json),
                            }
                        },
                    }
                ],
            }
            receipt_file.write_text(json.dumps(receipt_payload), encoding="utf-8")

            # 2. Valid receipt whose Go JSON lacks the selected pilot -> UNKNOWN.
            go_json.write_text(
                "\n".join(
                    [
                        json.dumps({"Action": "run", "Package": "p", "Test": "TestOther"}),
                        json.dumps({"Action": "pass", "Package": "p", "Test": "TestOther"}),
                    ]
                )
                + "\n",
                encoding="utf-8",
            )
            no_pilot = parse_receipt_and_go_json(
                argv,
                0,
                f"Receipt: {receipt_file}\n",
                "",
                pilot_test="TestCodeBudget",
            )
            self.assertFalse(no_pilot.qualified)
            self.assertEqual(no_pilot.verdict, "UNKNOWN")
            self.assertFalse(no_pilot.pilot_matched)

            # 3. Valid receipt + Go JSON with pilot run + pass -> PASS & qualified!
            go_json.write_text(
                "\n".join(
                    [
                        json.dumps({"Action": "run", "Package": "p", "Test": "TestCodeBudget"}),
                        json.dumps({"Action": "pass", "Package": "p", "Test": "TestCodeBudget"}),
                        json.dumps({"Action": "pass", "Package": "p"}),
                    ]
                )
                + "\n",
                encoding="utf-8",
            )
            ok_res = run_inprocess_suite(
                REPO_ROOT,
                "./internal/search/...",
                pilot_test="TestCodeBudget",
                executor=lambda _argv, _cwd: (0, f"Request test:digest: passed, exit 0.\nReceipt: {receipt_file}\n", ""),
            )
            self.assertTrue(ok_res.qualified)
            self.assertEqual(ok_res.verdict, "PASS")
            self.assertEqual(ok_res.executed_cases, 1)
            self.assertTrue(ok_res.pilot_matched)
            self.assertEqual(ok_res.source_digest, "3bb1464d22dd0a38a149d37610810e7a0324e8cf")
            self.assertEqual(ok_res.receipt_path, str(receipt_file))
            self.assertEqual(ok_res.receipt_mode, "shared_batch_receipt")

            # 4. Inline shared-batch receipt extraction (`passed; receipt /path/...json`).
            inline_out = f"test:digest: passed; receipt {receipt_file}\n"
            self.assertEqual(extract_receipt_path(inline_out), str(receipt_file))

            # 5. Scoped non-batch run: freshly written last-run/go-test.json -> PASS; stale -> UNKNOWN.
            last_run = artifact_dir / "last-run" / "go-test.json"
            last_run.parent.mkdir(parents=True, exist_ok=True)
            go_payload = "\n".join(
                [
                    json.dumps({"Action": "run", "Package": "p", "Test": "TestCodeBudget"}),
                    json.dumps({"Action": "pass", "Package": "p", "Test": "TestCodeBudget"}),
                    json.dumps({"Action": "pass", "Package": "p"}),
                ]
            ) + "\n"

            def write_fresh(_argv, _cwd):
                last_run.write_text(go_payload, encoding="utf-8")
                return (0, "no receipt is written outside a shared batch.\n", "")

            fresh_res = run_inprocess_suite(
                REPO_ROOT,
                "./internal/search",
                pilot_test="TestCodeBudget",
                last_run_go_json=last_run,
                executor=write_fresh,
            )
            self.assertTrue(fresh_res.qualified)
            self.assertEqual(fresh_res.verdict, "PASS")
            self.assertEqual(fresh_res.receipt_mode, "last_run_go_json")
            self.assertEqual(fresh_res.go_json_paths, (str(last_run),))

            # Stale (unmodified) last-run/go-test.json MUST return UNKNOWN!
            stale_res = run_inprocess_suite(
                REPO_ROOT,
                "./internal/search",
                pilot_test="TestCodeBudget",
                last_run_go_json=last_run,
                executor=lambda _a, _c: (0, "no receipt is written outside a shared batch.\n", ""),
            )
            self.assertFalse(stale_res.qualified)
            self.assertEqual(stale_res.verdict, "UNKNOWN")
            self.assertEqual(stale_res.receipt_mode, "missing")

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
