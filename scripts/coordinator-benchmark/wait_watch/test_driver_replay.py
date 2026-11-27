"""Unit tests for fixture barriers, timer guards, storage observer, and replay round-trip."""
from __future__ import annotations

import json
import pathlib
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wait_watch.barriers import (  # noqa: E402
    BarrierTimeoutError,
    GitRefWriterFixture,
    HTTPReadinessFixture,
    ProcessBarrier,
    TimerGuard,
)
from wait_watch.driver import (  # noqa: E402
    WaitWatchApplication,
    _make_wait_watch_app_factory,
    _observe_report_publication,
    replay_bundle,
    run_case,
)
from wait_watch.observers import StorageObserver  # noqa: E402


class _FakeSSE:
    def __init__(self, url: str, token: str, out_path: pathlib.Path) -> None:
        self.out_path = pathlib.Path(out_path)

    def start(self) -> None:
        self.out_path.write_text(
            json.dumps({"type": "wait_lease.updated", "session_id": "sess-fake", "active": True}) + "\n",
            encoding="utf-8",
        )

    def close(self) -> None:
        return


class _FakeApp:
    """In-memory fake Application exercising run_case and replay_bundle without spawning lycaon."""

    def __init__(self, directory: pathlib.Path, **kwargs) -> None:
        self.directory = pathlib.Path(directory)
        self.directory.mkdir(parents=True, exist_ok=True)
        self.url = "http://127.0.0.1:0"
        self.token = "fake-token"
        self.manual = True
        self.process = None
        self._db_path = self.directory / "store.db"
        self._init_db()
        self._pending: dict | None = None
        self._child: subprocess.Popen | None = None
        self._child_thread: threading.Thread | None = None
        self._handle = "command-1"
        self._parked = False
        self._exit_code: int | None = None
        self._stdout = ""

    def _init_db(self) -> None:
        with sqlite3.connect(self._db_path) as db:
            db.executescript(
                """
                CREATE TABLE wait_leases (id TEXT PRIMARY KEY, session_id TEXT, tool_call_id TEXT, status TEXT);
                CREATE TABLE watch_registrations (watch_id TEXT PRIMARY KEY, session_id TEXT, state TEXT);
                CREATE TABLE watch_matches (match_id TEXT PRIMARY KEY, watch_id TEXT, source_occurrence_id TEXT, outcome TEXT);
                CREATE TABLE watch_deliveries (delivery_id TEXT PRIMARY KEY, session_id TEXT, state TEXT);
                CREATE TABLE watch_wait_selections (lease_id TEXT, watch_id TEXT);
                CREATE TABLE watch_session_state (session_id TEXT PRIMARY KEY, next_seq INTEGER);
                CREATE TABLE watch_presentation_claims (claim_id TEXT PRIMARY KEY, session_id TEXT, state TEXT);
                CREATE TABLE watch_runs (run_id TEXT PRIMARY KEY, session_id TEXT, status TEXT);
                CREATE TABLE prompt_submissions (id TEXT PRIMARY KEY, session_id TEXT, status TEXT, error_code TEXT, admission_seq INTEGER);
                CREATE TABLE messages (id TEXT PRIMARY KEY, session_id TEXT, ord INTEGER, role TEXT, tool_result_call_id TEXT, tool_result_json TEXT);
                CREATE TABLE invocation_receipts (id TEXT PRIMARY KEY, session_id TEXT, tool_call_id TEXT, tool_name TEXT, status TEXT);
                CREATE TABLE invocation_programs (id TEXT PRIMARY KEY, parent_receipt_id TEXT, session_id TEXT, state TEXT, status_code INTEGER, published_at_ns INTEGER);
                CREATE TABLE invocation_program_outputs (id TEXT PRIMARY KEY, program_id TEXT, stream TEXT, preview TEXT);
                """
            )

    def ready(self) -> None:
        return

    def wait_for(self, observe, phase, session=None, timeout=15):
        guard = TimerGuard(timeout, phase=phase)
        return guard.poll(observe)

    def _record_tool_result(self, call_id: str, result: dict) -> None:
        with sqlite3.connect(self._db_path) as db:
            db.execute(
                "INSERT INTO messages(id, session_id, ord, role, tool_result_call_id, tool_result_json) VALUES (?, ?, ?, 'tool', ?, ?)",
                (f"msg-{call_id}", "sess-fake", 1, call_id, json.dumps(result)),
            )

    def _run_child(self, cmd: str, receipt_id: str) -> None:
        # Strip trailing background '&' in the fake runner so Popen tracks the actual child process directly.
        clean_cmd = cmd.rstrip()
        if clean_cmd.endswith("&"):
            clean_cmd = clean_cmd[:-1].rstrip()
        proc = subprocess.Popen(
            ["/bin/sh", "-c", clean_cmd],
            cwd=self.directory.parent / "project",
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        self._child = proc
        out, _ = proc.communicate(timeout=15)
        self._stdout = out
        self._exit_code = proc.returncode
        from datetime import datetime, timezone
        pub_dt = datetime.now(timezone.utc)
        pub_ns = int(pub_dt.timestamp() * 1_000_000_000)
        with sqlite3.connect(self._db_path) as db:
            db.execute(
                "INSERT INTO invocation_programs(id, parent_receipt_id, session_id, state, status_code, published_at_ns) VALUES (?, ?, ?, 'completed', ?, ?)",
                ("prog-1", receipt_id, "sess-fake", proc.returncode, pub_ns),
            )
            db.execute(
                "UPDATE wait_leases SET status='resolved' WHERE session_id='sess-fake' AND status='armed'"
            )
        # Emit authoritative slog debug line matching await.EmitPhaseRecord(phase=process_report_published).
        rec_ts = pub_dt.isoformat().replace("+00:00", "Z")
        with (self.directory / "sidecar.log").open("a", encoding="utf-8") as log_fh:
            log_fh.write(
                f'time={rec_ts} level=DEBUG msg="wait delivery phase" session_id=sess-fake '
                f'tool_call_id=call-1 lease_id=lease-1 handle={self._handle} tool=command '
                f'phase=process_report_published outcome=satisfied status=exited duration_ms=1 '
                f'recorded_at={rec_ts} failed=false\n'
            )
        if self._parked:
            report = f"handle={self._handle} termination=exited exit_code={proc.returncode}\n{out}"
            self._pending = {
                "pending": True,
                "id": "req-resumed",
                "session_id": "sess-fake",
                "tools": ["command", "wait", "watch", "watch_control"],
                "messages": [{"role": "user", "content": report}],
            }

    def request(self, path: str, body=None, missing: bool = False, method=None):
        if path == "/v1/projects":
            return {"id": "proj-fake", "roots": [{"id": "root-1"}]}
        if path == "/v1/sessions":
            return {"id": "sess-fake", "posture": "build", "status": "idle"}
        if path == "/v1/sessions/sess-fake":
            return {"id": "sess-fake", "posture": "build", "status": "idle"}
        if path == "/harness/model-limit":
            return {"ok": True}
        if path == "/v1/sessions/sess-fake/prompts":
            self._pending = {
                "pending": True,
                "id": "req-1",
                "session_id": "sess-fake",
                "tools": ["command", "wait", "watch", "watch_control"],
                "messages": [{"role": "user", "content": body["text"]}],
            }
            return {"operation_id": body["operation_id"]}
        if path == "/harness/llm/pending":
            return self._pending or {"pending": False}
        if path == "/harness/llm/respond":
            calls = (body or {}).get("tool_calls") or []
            if not calls:
                self._pending = None
                return {"ok": True}
            call = calls[0]
            cid, name, args = call["id"], call["name"], call.get("args") or {}
            if name == "command":
                with sqlite3.connect(self._db_path) as db:
                    db.execute(
                        "INSERT INTO invocation_receipts(id, session_id, tool_call_id, tool_name, status) VALUES (?, 'sess-fake', ?, 'command', 'running')",
                        ("rcpt-1", cid),
                    )
                self._child_thread = threading.Thread(
                    target=self._run_child, args=(args["command"], "rcpt-1"), daemon=True
                )
                self._child_thread.start()
                self._record_tool_result(
                    cid,
                    {
                        "outcome": "completed",
                        "content": json.dumps({
                            "state": "completed",
                            "background_jobs": [{"handle": self._handle, "output_cursor": 0}],
                        }),
                    },
                )
                self._pending = {
                    "pending": True,
                    "id": "req-2",
                    "session_id": "sess-fake",
                    "tools": ["command", "wait", "watch", "watch_control"],
                    "messages": [{"role": "tool", "content": f"started {self._handle}"}],
                }
            elif name == "wait":
                if self._exit_code is not None:
                    report = f"handle={self._handle} termination=exited exit_code={self._exit_code}\n{self._stdout}"
                    self._record_tool_result(
                        cid,
                        {
                            "outcome": "completed",
                            "content": json.dumps({
                                "status": "process_done",
                                "conditions": [{"kind": "process_done", "handles": [self._handle], "outcome": "satisfied", "report": report}],
                            }),
                        },
                    )
                    self._pending = {
                        "pending": True,
                        "id": "req-3",
                        "session_id": "sess-fake",
                        "tools": ["command", "wait", "watch", "watch_control"],
                        "messages": [{"role": "tool", "content": report}],
                    }
                else:
                    self._parked = True
                    with sqlite3.connect(self._db_path) as db:
                        db.execute("INSERT INTO wait_leases(id, session_id, tool_call_id, status) VALUES ('lease-1', 'sess-fake', ?, 'armed')", (cid,))
                    self._record_tool_result(
                        cid,
                        {"outcome": "completed", "content": json.dumps({"status": "parked"})},
                    )
                    self._pending = None
            return {"ok": True}
        if path.startswith("/harness/execution/"):
            return {"session_id": "sess-fake", "submission_id": path.rsplit("/", 1)[-1], "settled": True}
        if path.endswith("/watches"):
            return {"watches": []}
        if path.endswith("/abort"):
            return {"ok": True}
        return {}

    def close(self, cleanup: bool = True) -> None:
        if self._child_thread is not None:
            self._child_thread.join(timeout=5)


class TestBarriersAndReplay(unittest.TestCase):
    def test_process_barrier_reached_release_and_exit_17(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            barrier = ProcessBarrier(pathlib.Path(td) / "b", exit_code=17, marker="unit-c01")
            proc = subprocess.Popen(
                ["/bin/sh", "-c", barrier.command_string()],
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
            )
            sentinel = barrier.wait_reached(timeout_s=5.0)
            self.assertEqual(sentinel.get("marker"), "unit-c01")
            self.assertIsNone(proc.poll(), "process must remain blocked until release gate is written")
            barrier.release()
            stdout, stderr = proc.communicate(timeout=5.0)
            self.assertEqual(proc.returncode, 17, stderr)
            self.assertEqual(stdout, barrier.payload)
            facts = barrier.source_facts()
            self.assertTrue(facts["reached"])
            self.assertTrue(facts["released"])
            self.assertEqual(facts["expected_exit_code"], 17)

    def test_process_barrier_timeout_aborts_with_incomplete_diagnostics(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            barrier = ProcessBarrier(pathlib.Path(td) / "b-timeout")
            with self.assertRaises(BarrierTimeoutError) as ctx:
                barrier.wait_reached(timeout_s=0.1, dump_state=lambda: {"pending": False, "armed_leases": 1})
            err = ctx.exception
            self.assertEqual(err.verdict, "incomplete")
            self.assertEqual(err.diagnostics.get("armed_leases"), 1)

    def test_http_readiness_fixture_503_then_200(self) -> None:
        fixture = HTTPReadinessFixture()
        url = fixture.start()
        try:
            with self.assertRaises(urllib.error.HTTPError) as err:
                urllib.request.urlopen(url, timeout=2)
            self.assertEqual(err.exception.code, 503)
            err.exception.close()
            fixture.release()
            with urllib.request.urlopen(url, timeout=2) as resp:
                self.assertEqual(resp.status, 200)
                self.assertEqual(resp.read().decode("utf-8"), "ready\n")
            self.assertEqual(fixture.source_facts()["probe_count"], 2)
        finally:
            fixture.close()

    def test_git_ref_writer_fixture_advances_head(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            git_fix = GitRefWriterFixture(pathlib.Path(td) / "repo")
            first = git_fix.init()
            rec = git_fix.release(message="bump")
            self.assertNotEqual(first, rec["oid"])
            self.assertEqual(git_fix.source_facts()["commit_count"], 2)

    def test_observe_report_publication_requires_authoritative_phase_record(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            log_path = pathlib.Path(td) / "sidecar.log"
            # Generic log lines without phase=process_report_published must NOT satisfy report publication.
            log_path.write_text(
                'time=2026-11-27T17:00:00Z level=DEBUG msg="wait delivery phase" session_id=s1 handle=command-1 phase=wait_handler_return recorded_at=2026-11-27T17:00:00Z\n',
                encoding="utf-8",
            )
            self.assertIsNone(_observe_report_publication(log_path, session_id="s1", handle="command-1"))

            # Matching session_id + handle + phase=process_report_published with recorded_at succeeds.
            with log_path.open("a", encoding="utf-8") as fh:
                fh.write(
                    'time=2026-11-27T17:00:01Z level=DEBUG msg="wait delivery phase" session_id=s1 handle=command-1 phase=process_report_published recorded_at=2026-11-27T17:00:01.123456Z\n'
                )
            pub = _observe_report_publication(log_path, session_id="s1", handle="command-1")
            self.assertIsNotNone(pub)
            self.assertEqual(pub["phase"], "process_report_published")
            self.assertEqual(pub["evidence_ref"], "sidecar.log:L2:phase=process_report_published")
            self.assertIsInstance(pub["published_at_ns"], int)
            # Wrong handle must still return None.
            self.assertIsNone(_observe_report_publication(log_path, session_id="s1", handle="command-2"))

    def test_engine_bin_honored_for_wait_watch_application_subclass(self) -> None:
        import smoke

        seen_paths: list[pathlib.Path] = []

        class _ProbeApp(WaitWatchApplication):
            def __init__(self, directory: pathlib.Path) -> None:
                seen_paths.append(pathlib.Path(smoke.engine_path(directory)))

        with tempfile.TemporaryDirectory() as td:
            custom_bin = pathlib.Path(td) / "custom-lycaon-dev"
            custom_bin.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
            factory = _make_wait_watch_app_factory(custom_bin, _ProbeApp)
            factory(pathlib.Path(td) / "app")
            self.assertEqual(seen_paths, [custom_bin.resolve()])

    def test_sidecar_fast_exit_and_parked_exit_replay_round_trip(self) -> None:
        for case_id in ("c01-fast-exit", "c01-parked-exit"):
            with self.subTest(case_id=case_id), tempfile.TemporaryDirectory() as td:
                base = pathlib.Path(td)
                exported = base / "exported-bundle.json"
                bundle = run_case(
                    case_id,
                    base / "run1",
                    seed=7,
                    timeout_s=10.0,
                    export_path=exported,
                    app_factory=_FakeApp,
                    sse_factory=_FakeSSE,
                )
                self.assertEqual(bundle["checker_verdict"]["verdict"], "pass", json.dumps(bundle["checker_verdict"], indent=2))
                self.assertTrue(exported.is_file())

                replayed = replay_bundle(
                    exported,
                    base / "run2",
                    app_factory=_FakeApp,
                    sse_factory=_FakeSSE,
                )
                self.assertEqual(replayed["checker_verdict"]["verdict"], "pass", json.dumps(replayed["checker_verdict"], indent=2))
                self.assertEqual(replayed["replayed_from"]["source_run_id"], bundle["run_id"])
                self.assertEqual(replayed["case_id"], case_id)
                self.assertEqual(replayed["seed"], 7)

                # Verify storage observer reads SQLite read-only cleanly.
                snap = StorageObserver(base / "run2" / "app" / "store.db").snapshot("sess-fake")
                self.assertTrue(snap["available"])
                self.assertEqual(snap["armed_lease_count"], 0)


if __name__ == "__main__":
    unittest.main()
