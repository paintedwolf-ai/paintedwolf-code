"""Terminal settlement at real batch and source capture boundaries."""

import json
import os
from pathlib import Path
import signal
import sys
import unittest
from unittest.mock import patch

import verification_batch as batch
import verification_cancel as cancel
import verification_custody as custody
from verification_completion import Publisher, Witness, sealed_receipt
from verification_tests import support
from verification_tests.scheduler.test_terminal_settlement_execution import TerminalFixture


class CaptureBoundaryTests(TerminalFixture):
    def test_capture_descendant_is_settled_before_the_commit_is_used(self):
        cap = self.capability("lease")
        source = self.root / "source"
        scripts = source / "scripts"
        scripts.mkdir(parents=True)
        ready, stop = self.root / "capture-holder", self.root / "capture-stop"
        self.addCleanup(stop.touch)
        descendant = ("import os,time\nfrom pathlib import Path\n"
                      f"Path({str(ready)!r}).write_text(str(os.getpid()))\n"
                      f"while not Path({str(stop)!r}).exists(): time.sleep(.01)\n")
        capture = ("import os,subprocess,sys,time\nfrom pathlib import Path\n"
                   "fd=int(os.environ['PW_TEST_EXECUTION_FD'])\n"
                   f"subprocess.Popen([sys.executable,'-c',{descendant!r}],pass_fds=(fd,),"
                   "start_new_session=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)\n"
                   f"while not Path({str(ready)!r}).exists(): time.sleep(.01)\n"
                   f"print({'b' * 40!r})\n")
        (scripts / "capture.py").write_text(capture)
        (scripts / "repo-snapshot-lock.sh").write_text('#!/bin/bash\nshift 2\nexec "$@"\n')
        (scripts / "test-source-snapshot.sh").write_text(f'#!/bin/bash\nexec {sys.executable} scripts/capture.py\n')
        cap.entry.update(source=str(source), members=[cap.entry["ticket"]], phase="capture")
        cap.write()
        directory = self.root / "capture-output"
        directory.mkdir()
        # FD200 belongs to the outer managed test. Only the separately spawned
        # real batch supervisor owns/restores that reserved number.
        with patch.dict(os.environ, PW_TEST_EXECUTION_FD=str(cap.file.fileno())), \
                patch.object(batch, "child_options", return_value={"start_new_session": True, "pass_fds": (cap.file.fileno(),)}), \
                patch.object(batch, "restore_batch_descriptor"):
            code, commit = batch.capture_source(self.queue, directory, cap)
        self.assertEqual((code, commit), (0, "b" * 40))
        pid = int(ready.read_text())
        info = cap.path.stat()
        self.assertFalse(custody.holds(pid, cap.path, (info.st_dev, info.st_ino)))
        self.assertEqual(cap.entry["terminal_settlement"]["state"], "settled")

    def test_cleanup_preserves_only_exact_sealed_pass_and_actual_failure_evidence(self):
        directory = self.root / "receipts"
        directory.mkdir()
        requests = [{"ticket": name} for name in ["sealed", "forged", "modified", "failed", "cancelled"]]
        witness = Witness(directory)
        self.addCleanup(witness.close)
        # The executor receives the write capability; result files do not possess it.
        descriptor = os.dup(witness.writer)
        with patch.dict(os.environ, PW_TEST_COMPLETION_FD=str(descriptor)):
            publisher = Publisher("source")
        witness.launched("source")
        evidence = [{"results": {"fixture": {"status": "passed"}}}]
        for request in requests:
            receipt = {"request": request["ticket"], "source_commit": "source", "exit_code": 0,
                       "status": request["ticket"] if request["ticket"] in {"failed", "cancelled"} else "passed",
                       "evidence": evidence}
            if request["ticket"] in {"sealed", "modified"}:
                publisher.receipt(receipt)
            if request["ticket"] == "modified":
                receipt["source_tree"] = "replacement"
            (directory / (request["ticket"] + ".json")).write_text(json.dumps(receipt))
        publisher.file.close()  # Simulate executor death before its final completion fact.
        witness.drain()
        self.assertFalse(witness.completed(0), "wrapper success fabricated executor completion")
        for request in requests:
            receipt = json.loads((directory / (request["ticket"] + ".json")).read_text())
            self.assertEqual(sealed_receipt(directory, receipt), request["ticket"] == "sealed")
        # Crash recovery uses the supervisor's durable seals, not an in-memory executor.
        batch.unverify_cleanup(directory, requests)
        for name in ["forged", "modified"]:
            changed = json.loads((directory / (name + ".json")).read_text())
            self.assertEqual((changed["status"], changed["exit_code"]), ("unverified", 2))
            self.assertEqual(changed["evidence"], evidence)
        self.assertEqual(json.loads((directory / "sealed.json").read_text())["status"], "passed")
        self.assertEqual(json.loads((directory / "failed.json").read_text())["status"], "failed")
        self.assertEqual(json.loads((directory / "cancelled.json").read_text())["status"], "cancelled")

    def test_executor_failure_can_complete_but_stage_children_do_not_inherit_the_witness(self):
        witness = Witness()
        self.addCleanup(witness.close)
        with patch.dict(os.environ, PW_TEST_COMPLETION_FD=str(os.dup(witness.writer))):
            publisher = Publisher("source")
            self.assertNotIn("PW_TEST_COMPLETION_FD", os.environ)
            self.assertFalse(os.get_inheritable(publisher.file.fileno()))
            self.assertNotIn(publisher.file.fileno(), batch.child_options().get("pass_fds", ()))
        witness.launched("source")
        publisher.finish(1)
        witness.drain()
        self.assertTrue(witness.completed(1))
        self.assertFalse(witness.completed(0))

    def test_cleanup_diagnostic_does_not_add_public_cancel_permission(self):
        entry = {"ticket": "fixture", "kind": "invocation", "state": "running", "name": "fixture",
                 "health": {"state": "cleanup_blocked"}}
        status = {"runs": [entry], "operations": []}
        self.assertIsNotNone(cancel.refusal(status, entry, False))
        self.assertIsNone(cancel.refusal(status, entry, True))


@unittest.skipIf(os.name == "nt", "requires inherited POSIX capability locks")
class BatchTerminalIntegrationTests(support.BatchFixture):
    def test_settled_request_survives_later_executor_crash_with_orphan_custody(self):
        self.catalog["resources"].update({
            "build": {"locks": ["build"], "workers": 1},
            "lint": {"locks": ["lint"], "workers": 1}})
        self.save_catalog()
        tool = self.repo / "task"
        original = tool.read_text()
        marker = "if pathlib.Path(os.environ['FIXTURE_HOLD']).exists():"
        self.assertEqual(original.count(marker), 1)
        tool.write_text(original.replace(marker, "if name == 'lint' and pathlib.Path(os.environ['FIXTURE_HOLD']).exists():"))
        self.hold.touch()
        first, second = self.start("build"), self.start("lint")
        self.queued(2)
        self.queue.resume()
        self.await_condition(lambda: len(self.records()) == 2)
        self.collect(first)
        active = next(e for e in self.queue.status()["runs"] if e.get("kind") == "batch")
        progress = self.await_progress(active)
        os.kill(progress["executor_pid"], signal.SIGKILL)
        self.collect(second, 2)
        self.assertEqual(sorted(r["status"] for r in self.receipts()), ["passed", "unverified"])
        status = self.queue.status()
        self.assertEqual(status["capacity"]["reserved"], 1)
        self.assertTrue((Path(next(r for r in self.records() if r["name"] == "lint")["cwd"]) / "source.txt").is_file())
        self.release.touch()

    def await_progress(self, active):
        path = Path(active["directory"]) / "progress.json"
        self.await_condition(path.exists)
        return json.loads(path.read_text())

    def batch_escape_case(self, resource):
        ready, parent_exit, stop = self.root / "holder", self.root / "producer-exit", self.root / "stop"
        descendant = ("import os,time\nfrom pathlib import Path\n"
                      f"Path({str(ready)!r}).write_text(str(os.getpid()))\n"
                      f"while not Path({str(stop)!r}).exists(): time.sleep(.01)\n")
        self.addCleanup(stop.touch)
        self.addCleanup(parent_exit.touch)
        tool = self.repo / "task"
        original = tool.read_text()
        reached = "if pathlib.Path(os.environ['FIXTURE_HOLD']).exists():"
        keys = ("PW_TEST_EXECUTION_FD", "PW_TEST_RESOURCE_FD") if resource else ("PW_TEST_EXECUTION_FD",)
        fork = ("if name == 'build':\n"
                f" fds=tuple(int(os.environ[key]) for key in {keys!r})\n"
                f" subprocess.Popen([sys.executable,'-c',{descendant!r}],pass_fds=fds,start_new_session=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)\n"
                f" while not pathlib.Path({str(ready)!r}).exists(): time.sleep(.01)\n"
                f" while not pathlib.Path({str(parent_exit)!r}).exists(): time.sleep(.01)\n")
        self.assertEqual(original.count(reached), 1)
        tool.write_text(original.replace(reached, fork + reached))
        first = self.start("build")
        self.queued(1)
        self.queue.resume()
        self.await_condition(ready.exists)
        second = self.start("lint")
        self.queued(2)
        self.assertIsNone(second.poll(), "conflicting work entered before its predecessor settled")
        parent_exit.touch()
        self.collect(first)
        self.collect(second)
        self.assertCountEqual([r["name"] for r in self.records()], ["build", "lint"])
        self.assertEqual([r["status"] for r in self.receipts()], ["passed", "passed"])
        self.assertEqual(self.queue.status()["operations"], [])
        self.assertEqual(self.queue.status()["runs"], [])

    def test_finished_stage_settles_escaped_child_and_admits_conflicting_successor(self):
        self.batch_escape_case(True)

    def test_final_batch_settles_a_helper_that_retains_only_the_batch_lease(self):
        self.batch_escape_case(False)

    def invocation_case(self, code):
        ready, parent_exit, stop = self.root / "invocation-holder", self.root / "invocation-exit", self.root / "invocation-stop"
        descendant = ("import os,time\nfrom pathlib import Path\n"
                      f"Path({str(ready)!r}).write_text(str(os.getpid()))\n"
                      f"while not Path({str(stop)!r}).exists(): time.sleep(.01)\n")
        self.addCleanup(stop.touch)
        self.addCleanup(parent_exit.touch)
        tool = self.repo / "task"
        original = tool.read_text()
        reached = "if pathlib.Path(os.environ['FIXTURE_HOLD']).exists():"
        fork = ("if name == 'test:exclusive':\n"
                " fds=tuple(int(os.environ[key]) for key in ('PW_TEST_EXECUTION_FD','PW_TEST_RESOURCE_FD'))\n"
                f" subprocess.Popen([sys.executable,'-c',{descendant!r}],pass_fds=fds,start_new_session=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)\n"
                f" while not pathlib.Path({str(ready)!r}).exists(): time.sleep(.01)\n"
                f" while not pathlib.Path({str(parent_exit)!r}).exists(): time.sleep(.01)\n")
        self.assertEqual(original.count(reached), 1)
        tool.write_text(original.replace(reached, fork + reached))
        self.queue.resume()
        first = self.start("test:exclusive", env={**self.env, "FIXTURE_FAIL": "test:exclusive" if code else ""})
        self.await_condition(ready.exists)
        second = self.start("lint")
        self.queued(1)
        self.assertIsNone(second.poll(), "conflicting work overlapped the admitted invocation")
        parent_exit.touch()
        self.collect(first, code)
        self.collect(second)
        self.assertEqual([r["name"] for r in self.records()], ["test:exclusive", "lint"])
        self.assertEqual(self.queue.status()["operations"], [])
        self.assertEqual(self.queue.status()["runs"], [])

    def test_successful_invocation_settles_escaped_descendants(self):
        self.invocation_case(0)

    def test_failed_invocation_settles_escaped_descendants_without_hiding_failure(self):
        self.invocation_case(1)
