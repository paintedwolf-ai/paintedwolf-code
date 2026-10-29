"""Manual completions traverse the ordinary model and session APIs."""

import contextlib
import importlib.util
import io
import json
from pathlib import Path
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
import unittest
from unittest.mock import Mock, patch
import urllib.error

SPEC = importlib.util.spec_from_file_location("manual_harness", Path(__file__).resolve().parents[2] / "harness/manual.py")
manual = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(manual)

SESSION = "6ea1f0b4-b33c-4ad2-9872-fc3793a7e801"
PROJECT = "463384c6-4eee-492e-b175-e6756e6b3ef6"


class ManualClientTests(unittest.TestCase):
    def test_only_explicit_loopback_origins(self):
        for url in ("https://localhost", "http://example.org", "http://user@localhost", "http://localhost/path", "http://localhost?q=1", "http://localhost#x"):
            with self.subTest(url=url), self.assertRaises(ValueError):
                manual.Client(url, "secret")
        for url in ("http://localhost:2345", "http://127.0.0.1", "http://[::1]:1234/"):
            self.assertEqual(manual.Client(url, "secret").url, url.rstrip("/"))

    def test_transport_authenticates_and_preserves_structured_response(self):
        seen = []

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def do_POST(self):
                seen.append((self.path, self.headers["Authorization"], json.loads(self.rfile.read(int(self.headers["Content-Length"])))))
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b'{"ok":true}')

            def do_GET(self):
                self.send_response(404)
                self.end_headers()
                self.wfile.write(b'manual endpoint missing')

        with HTTPServer(("127.0.0.1", 0), Handler) as server:
            thread = threading.Thread(target=server.serve_forever)
            thread.start()
            try:
                client = manual.Client(f"http://127.0.0.1:{server.server_port}", "fixture-token")
                self.assertEqual(client.respond({"id": "pending-1"}, tool_calls=[{"name": "command", "args": {"command": "printf 'λ'"}}]), {"ok": True})
                with self.assertRaisesRegex(RuntimeError, "HTTP 404.*manual endpoint missing"):
                    client.pending(SESSION)
                client.abort(SESSION)
            finally:
                server.shutdown()
                thread.join()
        self.assertEqual(seen[0][0:2], ("/harness/llm/respond", "Bearer fixture-token"))
        self.assertEqual(seen[0][2]["tool_calls"][0]["args"], {"command": "printf 'λ'"})
        self.assertEqual(seen[1][0], f"/v1/sessions/{SESSION}/abort")

    def test_bootstrap_and_reuse_do_not_seed_authority(self):
        client = Mock()
        client.request.side_effect = [{"id": PROJECT}, {"id": SESSION, "status": "preparing"}, {"id": SESSION, "status": "idle"}]
        with patch.object(manual.time, "sleep"):
            self.assertEqual(manual.open_session(client)["id"], SESSION)
        self.assertEqual(client.request.call_args_list[0].args, ("/v1/projects", {"name": "Manual command testing", "draft": True}))
        self.assertEqual(client.request.call_args_list[1].args, ("/v1/sessions", {"project_id": PROJECT, "posture": "build"}))
        client.reset_mock(side_effect=True)
        client.request.return_value = {"id": SESSION, "status": "idle"}
        self.assertEqual(manual.open_session(client, session=SESSION)["id"], SESSION)
        client.request.assert_called_once_with(f"/v1/sessions/{SESSION}")
        client.request.return_value = {"status": "running"}
        with self.assertRaisesRegex(ValueError, "must be idle"):
            manual.open_session(client, session=SESSION)
        with self.assertRaises(ValueError):
            manual.open_session(client, project="bad")
        client.request.return_value = {"status": "error"}
        with self.assertRaisesRegex(RuntimeError, "preparation failed"):
            manual.open_session(client, project=PROJECT)


class ManualDriverTests(unittest.TestCase):
    def fixture(self, offered=("command",)):
        client = Mock()
        first = {"id": "one", "pending": True, "tools": list(offered), "messages": [{"role": "system", "content": "exact prompt"}], "tool_schemas": [{"name": "command", "schema": {"type": "object"}}]}
        result = {"role": "tool", "tool_result": {"tool_call_id": "call", "content": "stdout and host guidance", "outcome": "completed"}}
        second = {**first, "id": "two", "messages": [*first["messages"], result]}
        client.pending.side_effect = [first, second]
        client.request.return_value = {"status": "idle"}
        output = []
        return manual.Driver(client, SESSION, output.append), first, second, output

    def test_calls_flow_through_model_boundary_and_return_unmodified_feedback(self):
        driver, first, second, output = self.fixture()
        driver.invoke("command", {"command": "echo hi"})
        calls = driver.client.request.call_args_list
        self.assertEqual(calls[0].args, ("/harness/llm/auto", {"enabled": False}))
        self.assertEqual(calls[1].args[0], f"/v1/sessions/{SESSION}/prompts")
        self.assertEqual(driver.client.respond.call_args_list[0].args, (first,))
        submitted = driver.client.respond.call_args_list[0].kwargs["tool_calls"][0]
        self.assertEqual(submitted["name"], "command")
        self.assertEqual(submitted["args"], {"command": "echo hi"})
        self.assertEqual(json.loads(output[0]), second["messages"])
        self.assertEqual(driver.last, second)
        driver.client.abort.assert_not_called()
        self.assertEqual(calls[-2].args, ("/harness/llm/auto", {"enabled": True}))

    def test_unoffered_tool_is_not_dispatched_and_state_is_settled(self):
        driver, _, _, output = self.fixture(offered=("request_tools",))
        driver.invoke("command", {})
        self.assertIn("not offered", output[0])
        self.assertNotIn("tool_calls", driver.client.respond.call_args.kwargs)
        driver.client.respond.assert_called_once()

    def test_approval_wait_and_abort_are_session_scoped(self):
        driver, first, _, output = self.fixture()
        driver.client.pending.side_effect = [{"pending": False}, first]
        driver.client.request.side_effect = [{"status": "waiting"}]
        self.assertEqual(driver.next_request(), first)
        self.assertIn("approval", output[0])
        driver, _, _, _ = self.fixture()
        driver.client.pending.side_effect = KeyboardInterrupt
        with self.assertRaises(KeyboardInterrupt):
            driver.invoke("command", {})
        driver.client.abort.assert_called_once_with(SESSION)
        self.assertIn(unittest.mock.call("/harness/llm/auto", {"enabled": True}), driver.client.request.call_args_list)

    def test_ended_turn_reports_error(self):
        driver, _, _, _ = self.fixture()
        driver.client.pending.side_effect = [{"pending": False}]
        with self.assertRaisesRegex(RuntimeError, "turn ended"):
            driver.next_request()

    def test_repl_preserves_programs_and_raw_tool_arguments(self):
        driver = Mock(last={"tool_schemas": [{"name": "command"}]})
        inputs = iter(["", ":help", ":prompt", ":tools", ":begin", "for i in a b; do", "echo $i", "done", ":end", ":begin", "discard", ":cancel", ':call progress_update {"items":[]}', ":call tool []", ":unknown", "echo hi", ":quit"])
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()) as errors:
            manual.repl(driver, lambda _: next(inputs))
        self.assertEqual(driver.invoke.call_args_list, [
            unittest.mock.call("command", {"command": "for i in a b; do\necho $i\ndone"}),
            unittest.mock.call("progress_update", {"items": []}),
            unittest.mock.call("command", {"command": "echo hi"}),
        ])
        self.assertIn("must be a JSON object", errors.getvalue())
        with contextlib.redirect_stdout(io.StringIO()):
            manual.repl(driver, Mock(side_effect=EOFError))
            manual.repl(driver, Mock(side_effect=[":begin", EOFError, ":quit"]))
            driver.invoke.side_effect = KeyboardInterrupt
            manual.repl(driver, Mock(side_effect=["echo hi", ":quit"]))

    def test_main_checks_manual_endpoint_before_bootstrap(self):
        client = Mock()
        with patch.object(manual, "Client", return_value=client), patch.object(manual, "open_session", return_value={"id": SESSION, "project_id": PROJECT}) as opened, patch.object(manual, "repl") as repl, contextlib.redirect_stdout(io.StringIO()):
            manual.main(["--url", "http://127.0.0.1:1234"])
            client.pending.assert_called_once_with("manual-driver-preflight", wait=0)
            opened.assert_called_once_with(client, None, None)
            repl.assert_called_once()


class HarnessPreparedStartupTests(unittest.TestCase):
    def test_reuse_flag_preserves_interactive_runtime_without_preparation(self):
        import os
        import shlex
        import shutil
        import subprocess
        import tempfile

        source = Path(__file__).resolve().parents[2]
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            scripts = root / "scripts"
            (scripts / "harness").mkdir(parents=True)
            (scripts / "e2e").mkdir()
            shutil.copy2(source / "harness/lib.sh", scripts / "harness/lib.sh")
            (scripts / "e2e/env.sh").write_text(":\n")
            log = root / "calls"
            (scripts / "e2e-sidecar-bg.sh").write_text('printf "%s\\n" "$*" >> "$PROBE_LOG"\n')
            (root / "task").write_text('#!/bin/sh\necho preparation >> "$PROBE_LOG"\n')
            (root / "task").chmod(0o700)
            env = {**os.environ, "LYCAON_E2E_ADDR": "127.0.0.1:23451", "LYCAON_E2E_VITE_PORT": "23452", "LYCAON_E2E_STATE_DIR": str(root / "state"), "PROBE_LOG": str(log)}
            script = 'source ' + shlex.quote(str(scripts / "harness/lib.sh")) + '\n' + '''
_harness_require_free_port() { :; }
harness_reap_stale_states() { :; }
harness_claim_state() { :; }
_harness_seed_project() { :; }
harness_up --interactive --reuse-built
harness_up --interactive
'''
            result = subprocess.run(["bash", "-ec", script], env=env, text=True, capture_output=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(log.read_text().splitlines(), ["--reuse-built", "preparation", "--reuse-built"])

    def test_missing_runtime_and_unknown_arguments_refuse_before_start(self):
        import os
        import subprocess

        source = Path(__file__).resolve().parents[2]
        env = {key: value for key, value in os.environ.items() if key != "LYCAON_E2E_STATE_DIR"}
        for arguments, message in ((["--reuse-built"], "prepared runtime"), (["--unknown"], "unknown harness arguments")):
            result = subprocess.run(["bash", str(source / "den-harness.sh"), *arguments], env=env, capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 2, result.stderr)
            self.assertIn(message, result.stderr)
