"""Unit tests for Decision 128 CF05 project removal cleanup guard.

Verifies that CF05SidecarClient.delete_project:
1. Uses POST /v1/projects/{id}/removals -> HTTP 202 -> typed project_state=="deleted"
   -> separate GET /v1/projects/{id} == 404.
2. Never accepts a 404 on the removal status endpoint as deleted.
3. Fails loudly with ProjectCleanupError on never-completing removal or lingering GET 200.
4. Marks the sidecar client contaminated and blocks any subsequent case or family reuse.
5. _run_guarded propagates ProjectCleanupError instead of swallowing it as UNKNOWN.
"""

from __future__ import annotations

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import tempfile
import threading
import unittest

from catalog_feed_search.families.cf05_change_feed import _run_guarded, run_family_suite
from catalog_feed_search.families.cf05_sidecar_harness import CF05SidecarClient, ProjectCleanupError


class _StubState:
    def __init__(self, mode: str) -> None:
        self.mode = mode
        self.status_polls = 0
        self.attach_calls = 0
        self.requests: list[tuple[str, str]] = []


def _start_stub_server(mode: str) -> tuple[ThreadingHTTPServer, _StubState, str]:
    state = _StubState(mode)

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, format: str, *args) -> None:  # noqa: A002
            return

        def _send_json(self, code: int, payload: dict[str, object]) -> None:
            raw = json.dumps(payload).encode("utf-8")
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def do_POST(self) -> None:  # noqa: N802
            length = int(self.headers.get("Content-Length", "0"))
            body = json.loads(self.rfile.read(length).decode("utf-8")) if length else {}
            state.requests.append(("POST", self.path))
            if self.path == "/v1/projects":
                state.attach_calls += 1
                self._send_json(201, {"id": "proj-new", "roots": [{"id": "root-1"}]})
                return
            if self.path.endswith("/removals"):
                op_id = body.get("operation_id", "")
                if state.mode == "post_405":
                    self._send_json(405, {"error": "method_not_allowed"})
                    return
                self._send_json(202, {"operation_id": op_id, "project_state": "removing"})
                return
            self._send_json(404, {"error": "not_found"})

        def do_GET(self) -> None:  # noqa: N802
            state.requests.append(("GET", self.path))
            if "/removals/" in self.path:
                state.status_polls += 1
                op_id = self.path.rsplit("/", 1)[-1]
                if state.mode == "false_status_404":
                    self._send_json(404, {"error": "operation_not_found"})
                    return
                if state.mode == "never_completes":
                    self._send_json(200, {"operation_id": op_id, "project_state": "removing"})
                    return
                if state.status_polls == 1:
                    self._send_json(200, {"operation_id": op_id, "project_state": "removing"})
                    return
                self._send_json(
                    200,
                    {
                        "operation_id": op_id,
                        "project_state": "deleted",
                        "cleanup_state": "complete",
                    },
                )
                return
            if self.path.startswith("/v1/projects/"):
                if state.mode == "final_get_drop":
                    self.connection.close()
                    return
                if state.mode == "lingering_200":
                    self._send_json(200, {"id": "proj-1", "state": "active"})
                    return
                self._send_json(404, {"error": "project_not_found"})
                return
            self._send_json(404, {"error": "not_found"})

    srv = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    t = threading.Thread(target=srv.serve_forever, daemon=True)
    t.start()
    host, port = srv.server_address
    return srv, state, f"http://{host}:{port}"


class TestCF05CleanupGuard(unittest.TestCase):
    def test_happy_path_202_deleted_then_404(self) -> None:
        srv, state, url = _start_stub_server("happy")
        try:
            client = CF05SidecarClient(url, "tok")
            res = client.delete_project("proj-1", timeout_s=2.0)
            self.assertEqual(res["cleanup_status"], "deleted_verified_404")
            self.assertIsNone(client.contaminated)
            self.assertGreaterEqual(state.status_polls, 2)
            self.assertIn(("POST", "/v1/projects/proj-1/removals"), state.requests)
            self.assertIn(("GET", "/v1/projects/proj-1"), state.requests)
            client.ensure_clean()
        finally:
            srv.shutdown()
            srv.server_close()

    def test_false_status_404_rejected_and_blocks_following_family(self) -> None:
        srv, state, url = _start_stub_server("false_status_404")
        try:
            client = CF05SidecarClient(url, "tok")
            with self.assertRaises(ProjectCleanupError):
                client.delete_project("proj-1", timeout_s=1.0)
            self.assertIsNotNone(client.contaminated)
            with self.assertRaises(ProjectCleanupError):
                client.driver.attach_project([Path("/tmp")])
            with tempfile.TemporaryDirectory() as tmp:
                with self.assertRaises(ProjectCleanupError):
                    run_family_suite("F06", client, Path(tmp), None, max_instances=1)
            self.assertEqual(state.attach_calls, 0)
        finally:
            srv.shutdown()
            srv.server_close()

    def test_never_completing_removal_fails_and_blocks_reuse(self) -> None:
        srv, state, url = _start_stub_server("never_completes")
        try:
            client = CF05SidecarClient(url, "tok")
            with self.assertRaises(ProjectCleanupError):
                client.delete_project("proj-1", timeout_s=0.35)
            self.assertIsNotNone(client.contaminated)
            with self.assertRaises(ProjectCleanupError):
                client.ensure_clean()
            self.assertEqual(state.attach_calls, 0)
        finally:
            srv.shutdown()
            srv.server_close()

    def test_lingering_project_200_after_deleted_fails_and_blocks_reuse(self) -> None:
        srv, _, url = _start_stub_server("lingering_200")
        try:
            client = CF05SidecarClient(url, "tok")
            with self.assertRaises(ProjectCleanupError):
                client.delete_project("proj-1", timeout_s=2.0)
            self.assertIsNotNone(client.contaminated)
            with self.assertRaises(ProjectCleanupError):
                client.ensure_clean()
        finally:
            srv.shutdown()
            srv.server_close()

    def test_post_deleted_get_network_error_taints_and_blocks_reuse(self) -> None:
        srv, state, url = _start_stub_server("final_get_drop")
        try:
            client = CF05SidecarClient(url, "tok")
            with self.assertRaises(ProjectCleanupError) as ctx:
                client.delete_project("proj-1", timeout_s=2.0)
            self.assertIsNotNone(ctx.exception.__cause__)
            self.assertIsNotNone(client.contaminated)
            with self.assertRaises(ProjectCleanupError):
                client.driver.attach_project([Path("/tmp")])
            with tempfile.TemporaryDirectory() as tmp:
                with self.assertRaises(ProjectCleanupError):
                    run_family_suite("F06", client, Path(tmp), None, max_instances=1)
            self.assertEqual(state.attach_calls, 0)
        finally:
            srv.shutdown()
            srv.server_close()

    def test_run_guarded_reraises_project_cleanup_error(self) -> None:
        def _boom():
            raise ProjectCleanupError("simulated cleanup failure")

        with self.assertRaises(ProjectCleanupError):
            _run_guarded("F04", "F04-0001", False, _boom)


if __name__ == "__main__":
    unittest.main()
