"""Focused unit tests exercising error paths, transport, CLI, and edge branches across all modules."""

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
from catalog_feed_search.change_runner import ChangeOp, WriteLog, WriteLogEntry
from catalog_feed_search.checker import (
    CatalogItem,
    ProductCatalogAnswer,
    ProductSearchAnswer,
    ScopedSearchHit,
    check_catalog,
    check_search,
    walk_entries_to_catalog_items,
)
from catalog_feed_search.checker_feed import (
    FeedEvent,
    ProductFeedAnswer,
    check_change_feed,
    check_feed_batch_effects,
)
from catalog_feed_search.checker_replace import (
    ProductReplaceAnswer,
    ReplaceFileObservation,
    check_replace,
)
from catalog_feed_search.checker_types import DeclaredGap, Verdict
from catalog_feed_search.driver_http import HTTPTransport
from catalog_feed_search.driver_inprocess import (
    build_task_digest_argv,
    extract_receipt_path,
    parse_receipt_and_go_json,
)
from catalog_feed_search.driver_provenance import (
    detect_filesystem_type,
    file_sha256,
    inspect_product_binary,
    parse_darwin_mount_fs,
    parse_proc_mounts_fs,
)
from catalog_feed_search.driver_sidecar import SidecarDriver
from catalog_feed_search.driver_sse import LiveSSESubscriber
from catalog_feed_search.generators import (
    BoundaryConfig,
    TreeEntrySpec,
    TreeSpec,
    filter_entry_paths,
    materialize_tree,
)
from catalog_feed_search.live_controls import (
    DRAIN_SENTINEL_REL_PATH,
    run_live_control_change_feed,
    wait_catalog_settled,
)
from catalog_feed_search.queries import (
    generate_replace_spec,
    generate_s01_query,
    generate_s04_lookup,
    generate_s05_corpus,
    generate_s06_query_text,
    selector_for,
)
from catalog_feed_search.references import ReplaceFileResult, SearchHit, WalkEntry
from catalog_feed_search.seeds import BASE_SEED_PRIMARY, ReplaySelector, derive_seed, seed_rng


class HTTPTransportAndSSEErrorPathTests(unittest.TestCase):
    def test_http_transport_get_post_and_raw_headers(self) -> None:
        received: list[tuple[str, str, str, bytes]] = []

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self) -> None:
                received.append(("GET", self.path, self.headers.get("Authorization", ""), b""))
                if self.path == "/v1/empty":
                    self.send_response(200)
                    self.end_headers()
                    return
                body = json.dumps({"ok": True, "path": self.path}).encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("X-Export-Truncated", "true")
                self.end_headers()
                self.wfile.write(body)

            def do_POST(self) -> None:
                length = int(self.headers.get("Content-Length", "0"))
                raw = self.rfile.read(length)
                received.append(("POST", self.path, self.headers.get("Authorization", ""), raw))
                body = json.dumps({"echo": json.loads(raw.decode("utf-8"))}).encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, format: str, *args) -> None:
                return

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        port = server.server_address[1]
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            tr = HTTPTransport(f"http://127.0.0.1:{port}", "secret-tok")
            self.assertEqual(tr.request("/v1/ping"), {"ok": True, "path": "/v1/ping"})
            self.assertEqual(tr.request("/v1/empty"), {})
            self.assertEqual(tr.request("/v1/post", {"k": "v"}), {"echo": {"k": "v"}})
            raw, headers = tr.request_raw("/v1/ping")
            self.assertIn(b'"ok": true', raw)
            self.assertEqual(headers.get("x-export-truncated"), "true")
            self.assertTrue(all(auth == "Bearer secret-tok" for _, _, auth, _ in received))
        finally:
            server.shutdown()
            server.server_close()

    def test_sse_subscriber_validation_non_200_malformed_and_https(self) -> None:
        with self.assertRaises(ValueError):
            LiveSSESubscriber("http://127.0.0.1:9999", "tok", project_id="")

        https_sub = LiveSSESubscriber("https://127.0.0.1:9999", "tok", project_id="p1")
        self.assertIn("project_id=p1", https_sub.request_target)
        conn = https_sub._open_connection()
        conn.close()

        class MixedSSEHandler(BaseHTTPRequestHandler):
            def do_GET(self) -> None:
                if "project_id=bad" in self.path:
                    self.send_response(401)
                    self.end_headers()
                    return
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self.wfile.write(b": comment\ndata:\ndata: not-json\ndata: {\"id\":\"ok-1\"}\n\n")
                self.wfile.flush()

            def log_message(self, format: str, *args) -> None:
                return

        server = ThreadingHTTPServer(("127.0.0.1", 0), MixedSSEHandler)
        port = server.server_address[1]
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            bad = LiveSSESubscriber(f"http://127.0.0.1:{port}", "tok", project_id="bad")
            bad.start()
            self.assertFalse(bad.wait_ready())
            self.assertIsNotNone(bad.error)
            bad.close()

            good = LiveSSESubscriber(f"http://127.0.0.1:{port}", "tok", project_id="good")
            good.start()
            self.assertTrue(good.wait_ready())
            frames, matched = good.wait_for_frames(lambda fs: len(fs) == 1)
            self.assertTrue(matched)
            self.assertEqual(len(good.frames()), 1)
            self.assertEqual(frames[0]["id"], "ok-1")
            good.close()
        finally:
            server.shutdown()
            server.server_close()


class CLILiveControlsEndToEndLoopbackTests(unittest.TestCase):
    def test_cli_live_controls_and_replay_with_out_dir(self) -> None:
        from unittest.mock import patch
        from catalog_feed_search.change_runner import ChangeRunner

        primary_written = threading.Event()
        sentinel_written = threading.Event()
        orig_run = ChangeRunner.run

        def signaling_run(self_runner, root, script):
            res = orig_run(self_runner, root, script)
            for op in script.ops:
                if op.path == "src/control_write.txt":
                    primary_written.set()
                elif op.path == DRAIN_SENTINEL_REL_PATH:
                    sentinel_written.set()
            return res

        class FakeD1Handler(BaseHTTPRequestHandler):
            def do_GET(self) -> None:
                if self.path.startswith("/v1/events"):
                    self.send_response(200)
                    self.send_header("Content-Type", "text/event-stream")
                    self.end_headers()
                    e0 = {"id": "e0", "topic": "source_changed", "data": {"project_id": "p1", "resync": False, "changes": []}}
                    self.wfile.write(f"data: {json.dumps(e0)}\n\n".encode("utf-8"))
                    self.wfile.flush()
                    primary_written.wait()
                    e1 = {"id": "e1", "topic": "source_changed", "data": {"project_id": "p1", "resync": False, "changes": [{"root_id": "r1", "path": "src/control_write.txt", "op": "write"}]}}
                    self.wfile.write(f"data: {json.dumps(e1)}\n\n".encode("utf-8"))
                    self.wfile.flush()
                    sentinel_written.wait()
                    e2 = {"id": "e2", "topic": "source_changed", "data": {"project_id": "p1", "resync": False, "changes": [{"root_id": "r1", "path": DRAIN_SENTINEL_REL_PATH, "op": "write"}]}}
                    self.wfile.write(f"data: {json.dumps(e2)}\n\n".encode("utf-8"))
                    self.wfile.flush()
                    return
                if self.path == "/v1/projects/p1/source/index":
                    payload = {
                        "state": "ready",
                        "revision": 2,
                        "refreshing": False,
                        "coverage": [{"root_id": "r1", "state": "ready", "discovery_complete": True, "refreshing": False, "bounded_directories": 0, "failed_directories": 0}],
                    }
                elif self.path.startswith("/v1/projects/p1/source/search"):
                    payload = {
                        "state": "ready",
                        "revision": 2,
                        "refreshing": False,
                        "coverage": [{"root_id": "r1", "state": "ready", "discovery_complete": True, "refreshing": False, "bounded_directories": 0, "failed_directories": 0}],
                        "matches": [
                            {"root_id": "r1", "path": "docs/readme.md", "highlights": []},
                            {"root_id": "r1", "path": DRAIN_SENTINEL_REL_PATH, "highlights": []},
                            {"root_id": "r1", "path": "src/alpha.txt", "highlights": []},
                            {"root_id": "r1", "path": "src/control_write.txt", "highlights": []},
                        ],
                    }
                else:
                    payload = {}
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps(payload).encode("utf-8"))

            def do_POST(self) -> None:
                length = int(self.headers.get("Content-Length", "0"))
                _ = self.rfile.read(length)
                if self.path == "/v1/projects":
                    payload = {"id": "p1", "roots": [{"id": "r1"}]}
                elif self.path == "/v1/search":
                    payload = {
                        "status": "complete",
                        "exhaustive": True,
                        "total_hits": 1,
                        "count_relation": "exact",
                        "facets_exhaustive": True,
                        "hits": [{"hit_id": "h1", "hit_kind": "code", "source": "code", "project_id": "p1", "root_id": "r1", "title": "src/alpha.txt:1", "path": "src/alpha.txt", "line": 1, "snippet": "PaintedWolfControlToken"}],
                        "issues": [],
                    }
                else:
                    payload = {}
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps(payload).encode("utf-8"))

            def log_message(self, format: str, *args) -> None:
                return

        server = ThreadingHTTPServer(("127.0.0.1", 0), FakeD1Handler)
        port = server.server_address[1]
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            with tempfile.TemporaryDirectory() as tmp:
                tmp_path = Path(tmp)
                out_dir = tmp_path / "out"
                replay_dir = tmp_path / "replay_tree"
                lr_file = tmp_path / "launch.json"
                lr_file.write_text(json.dumps({"pid": 42, "url": f"http://127.0.0.1:{port}"}), encoding="utf-8")

                buf = io.StringIO()
                with patch.object(ChangeRunner, "run", signaling_run), redirect_stdout(buf):
                    rc = cli.main(
                        [
                            "live-controls",
                            "--url",
                            f"http://127.0.0.1:{port}",
                            "--token",
                            "tok",
                            "--d1-commit",
                            "3bb1464d",
                            "--d1-tree",
                            "tree123",
                            "--out-dir",
                            str(out_dir),
                            "--launch-receipt",
                            str(lr_file),
                        ]
                    )
                self.assertEqual(rc, 0, buf.getvalue())
                manifest = json.loads((out_dir / "manifest.json").read_text(encoding="utf-8"))
                self.assertEqual(manifest["launch_receipt"]["pid"], 42)
                self.assertIn("sse_events", manifest["artifacts"])

                buf_rep = io.StringIO()
                with redirect_stdout(buf_rep):
                    self.assertEqual(
                        cli.main(["replay", "--seed", BASE_SEED_PRIMARY, "--family", "K01", "--instance", "1", "--out-dir", str(replay_dir)]),
                        0,
                    )
                self.assertTrue(replay_dir.is_dir())
        finally:
            server.shutdown()
            server.server_close()


class SidecarMethodsProvenanceAndEdgeCoverageTests(unittest.TestCase):
    def test_sidecar_driver_remaining_routes_and_provenance(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            target = root / "src" / "a.txt"
            target.parent.mkdir(parents=True)
            target.write_bytes(b"replaced\n")
            expected_sha = file_sha256(target)

            class Stub:
                def __init__(self) -> None:
                    self.page = 0

                def request(self, path: str, body=None, *, method=None):
                    if path.startswith("/v1/projects/p1/source/search"):
                        self.page += 1
                        if self.page == 1:
                            return {"revision": 4, "coverage": [], "matches": [{"root_id": "r1", "path": "src/a.txt"}], "next_cursor": "c2"}
                        return {"revision": 4, "coverage": [], "matches": [{"root_id": "r1", "path": "src/b.txt"}]}
                    if path.startswith("/v1/projects/p1/source/walk"):
                        return {"rows": []}
                    if path == "/v1/search/replace/preview":
                        return {"state": "ready", "files": [{"root_id": "r1", "path": "src/a.txt", "sha256": "orig", "hunks": [{}]}], "truncated": False, "issues": []}
                    if path == "/v1/search/replace/apply":
                        return {"batch_id": "b1", "files": [{"root_id": "r1", "path": "src/a.txt", "applied": True, "skipped": False}]}
                    if path == "/v1/search":
                        return {"status": "complete", "hits": [], "issues": []}
                    return {}

                def request_raw(self, path: str, body=None, *, method=None):
                    return b"line1\n", {"x-export-truncated": "true"}

            driver = SidecarDriver(Stub())
            cat = driver.walk_catalog_index("p1", "r1")
            self.assertEqual(len(cat.items), 2)
            self.assertEqual(driver.list_source_walk("p1", cursor="c1"), {"rows": []})
            driver.search_raw("q", include=("*.go",), exclude=("*.bin",), cursor="c1", budget="complete")
            text, trunc = driver.export_search("q", origin_project_id="p1")
            self.assertTrue(trunc and text == "line1\n")
            prev = driver.replace_preview("a", "b", origin_project_id="p1", include=("*.txt",), exclude=("*.bin",))
            rep_ans = driver.replace_apply(prev, query="a", replacement="b", origin_project_id="p1", root_dirs_by_id={"r1": root})
            self.assertEqual(rep_ans.files[0].observed_sha256, expected_sha)
            obs = driver.run_agent_discovery(lambda name, args: {"ok": True, "issues": [{"reason": "time_budget"}]}, "command", {"argv": ["wc"]})
            self.assertTrue(obs.ok)
            self.assertIn("time_budget", obs.declared_gaps)

            # Provenance binary inspection on existing + missing paths & filesystem detection.
            dummy_bin = root / "fake_lycaon"
            dummy_bin.write_bytes(b"#!/bin/sh\necho v1\n")
            dummy_bin.chmod(0o755)
            bp = inspect_product_binary(dummy_bin, source_commit="abc", source_tree="def")
            self.assertEqual(bp.binary_sha256, file_sha256(dummy_bin))
            self.assertEqual(bp.to_dict()["source_commit"], "abc")
            self.assertTrue(detect_filesystem_type(root))
            self.assertEqual(parse_proc_mounts_fs(Path("/nonmatch"), "badline\n"), "")
            self.assertEqual(parse_darwin_mount_fs(Path("/nonmatch"), "badline\n"), "")

    def test_seeds_generators_queries_inprocess_and_checker_edge_branches(self) -> None:
        # Seeds
        self.assertIsNotNone(seed_rng(BASE_SEED_PRIMARY, "K01", 0))
        sel = ReplaySelector(BASE_SEED_PRIMARY, "K01", 0)
        self.assertEqual(sel.derived_seed, derive_seed(BASE_SEED_PRIMARY, "K01", 0))
        self.assertEqual(selector_for(BASE_SEED_PRIMARY, "K01", 0), sel)

        # Generators
        self.assertFalse(BoundaryConfig(("build",)).is_boundary_dir(""))
        spec = TreeSpec(sel, (TreeEntrySpec("a.txt", "file", b"hi\n", mode=0o644),))
        self.assertEqual(len(filter_entry_paths(spec.entries, lambda e: e.kind == "file")), 1)
        with tempfile.TemporaryDirectory() as tmp:
            man = materialize_tree(spec, Path(tmp))
            self.assertEqual(man.to_dict()["manifest_digest"], man.digest())
            with self.assertRaises(ValueError):
                materialize_tree(TreeSpec(sel, (TreeEntrySpec("bad", "symlink", link_target=None),)), Path(tmp))
            with self.assertRaises(ValueError):
                materialize_tree(TreeSpec(sel, (TreeEntrySpec("bad", "fifo"),)), Path(tmp))

        # Queries
        self.assertTrue(generate_replace_spec(BASE_SEED_PRIMARY, "R01", 0).to_dict()["pattern"])
        self.assertEqual(generate_s04_lookup(BASE_SEED_PRIMARY, "S04", 0, []).expected_kind, "basename_prefix")
        for idx in range(6):
            self.assertTrue(generate_s04_lookup(BASE_SEED_PRIMARY, "S04", idx, ["src/catalog/index.go"]).query_text)
        with self.assertRaises(ValueError):
            generate_s01_query(BASE_SEED_PRIMARY, "S01", 0, "bogus")
        with self.assertRaises(ValueError):
            generate_s05_corpus(BASE_SEED_PRIMARY, "S05", 0, ("cobol",))
        with self.assertRaises(ValueError):
            generate_s06_query_text(BASE_SEED_PRIMARY, "S06", 0, "bogus")

        # In-process error branches
        with self.assertRaises(ValueError):
            build_task_digest_argv(Path("."), "./...", pilot_test="")
        self.assertEqual(extract_receipt_path("nothing here"), "")
        with tempfile.TemporaryDirectory() as tmp:
            rfile = Path(tmp) / "r.json"
            rfile.write_text(json.dumps({"evidence": [{"results": {"pkg": {"stage_id": "s1"}}}]}), encoding="utf-8")
            missing_log = parse_receipt_and_go_json(("t",), 0, f"Receipt: {rfile}\n", "", pilot_test="TestX")
            self.assertEqual(missing_log.verdict, "UNKNOWN")
            self.assertIn("receipt_mode", missing_log.to_dict())
            go_log = Path(tmp) / "s1-go.json"
            go_log.write_text('not-json\n{"Action":"run","Package":"p","Test":"TestX"}\n{"Action":"fail","Package":"p","Test":"TestX"}\n{"Action":"fail","Package":"p"}\n', encoding="utf-8")
            failed = parse_receipt_and_go_json(("t",), 1, f"Receipt: {rfile}\n", "", pilot_test="TestX")
            self.assertEqual(failed.verdict, "FAIL")

        # Checker edge branches (catalog, search, feed, replace)
        cat_items = walk_entries_to_catalog_items([WalkEntry("a.txt", "file", 2, "sha1")])
        self.assertEqual(check_catalog(cat_items, ProductCatalogAnswer((CatalogItem("a.txt", "dir"),))).verdict, Verdict.DEFECT)
        self.assertEqual(check_catalog(cat_items, ProductCatalogAnswer((CatalogItem("a.txt", "file", sha256="sha2"),))).verdict, Verdict.DEFECT)

        h = ScopedSearchHit(SearchHit("a.txt", 1, 1, 0, 1, "a", "a\n"))
        with self.assertRaises(ValueError):
            check_search((h,), ProductSearchAnswer((h,)), projection="bogus")
        self.assertEqual(check_search((h,), ProductSearchAnswer((), instrumentation_complete=False)).verdict, Verdict.UNKNOWN)

        self.assertEqual(check_feed_batch_effects(ProductFeedAnswer((), instrumentation_complete=False)).verdict, Verdict.UNKNOWN)
        self.assertEqual(check_feed_batch_effects(ProductFeedAnswer((FeedEvent("a.txt", "", effect_id="e1"), FeedEvent("b.txt", "b1", effect_id="e1")))).verdict, Verdict.DEFECT)
        self.assertEqual(check_feed_batch_effects(ProductFeedAnswer((FeedEvent("a.txt", "b1", effect_id="e1"),))).verdict, Verdict.PASS)

        wlog = WriteLog("manual", (WriteLogEntry(1, "manual", 1, "rename", "a.txt", "b.txt", "s1", "s1"),))
        self.assertEqual(check_change_feed(wlog, ProductFeedAnswer((), instrumentation_complete=False)).verdict, Verdict.UNKNOWN)
        self.assertEqual(
            check_change_feed(
                wlog,
                ProductFeedAnswer(
                    (FeedEvent("a.txt", "", op="rename", effect_id="e1"), FeedEvent("b.txt", "b1", op="rename", effect_id="e1")),
                    claims_batch_effect=True,
                ),
            ).verdict,
            Verdict.DEFECT,
        )

        ref_rep = {"a.txt": ReplaceFileResult("a.txt", "orig", "after", b"after", 1, True)}
        self.assertEqual(check_replace(ref_rep, ProductReplaceAnswer((), instrumentation_complete=False)).verdict, Verdict.UNKNOWN)
        self.assertEqual(check_replace(ref_rep, ProductReplaceAnswer((ReplaceFileObservation("extra.txt", applied=True, observed_sha256="x"),))).verdict, Verdict.DEFECT)
        self.assertEqual(check_replace(ref_rep, ProductReplaceAnswer((ReplaceFileObservation("a.txt", applied=True, observed_sha256="wrong"),))).verdict, Verdict.DEFECT)
        self.assertEqual(check_replace(ref_rep, ProductReplaceAnswer((ReplaceFileObservation("a.txt", applied=False, refused_drift=True, drifted_before_apply=True, drifted_sha256="d1", observed_sha256="d2"),))).verdict, Verdict.DEFECT)
        self.assertEqual(check_replace(ref_rep, ProductReplaceAnswer(())).verdict, Verdict.DEFECT)

        # Live controls feed error branches (not ready + sentinel deadline expired)
        class NotReadySub:
            def start(self): pass
            def wait_ready(self, max_wait_s=10.0): return False
            def wait_for_frames(self, pred, max_wait_s=10.0): return [], False
            def frames(self): return []
            def close(self, max_wait_s=3.0): pass

        with tempfile.TemporaryDirectory() as tmp:
            r = Path(tmp)
            (r / "src").mkdir()
            (r / "src/control_write.txt").write_bytes(b"a\n")
            (r / DRAIN_SENTINEL_REL_PATH).write_bytes(b"s\n")
            not_ready = run_live_control_change_feed(r, "p1", "r1", NotReadySub())
            self.assertEqual(not_ready.check.verdict, Verdict.UNKNOWN)

        class EmptyCovDriver:
            def get_source_index(self, pid: str):
                return {"state": "ready", "refreshing": False, "coverage": []}

        settled, polls, _ = wait_catalog_settled(EmptyCovDriver(), "p1", "r1")  # type: ignore[arg-type]
        self.assertTrue(settled and polls == 1)


if __name__ == "__main__":
    unittest.main()
