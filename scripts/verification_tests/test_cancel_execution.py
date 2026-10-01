"""Withdrawing a verification request, and the work that withdrawal must not destroy."""

import contextlib
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import verification_cancel as cancel
from verification_state import read_state

SCRIPT = Path(__file__).resolve().parents[1].joinpath("test-execution.py")
spec = importlib.util.spec_from_file_location("test_execution", SCRIPT)
execution = importlib.util.module_from_spec(spec)
spec.loader.exec_module(execution)

TICKET = "00000000000000000007-5da17033fb58491f99cf251"


def entry(ticket, name, state, **fields):
    value = {"ticket": ticket, "name": name, "state": state, "kind": "request",
             "pid": 4321, "owner_identity": "Thu Sep 17 14:12:24 2026", "source": "/checkout"}
    value.update(fields)
    return value


class ArgumentTests(unittest.TestCase):
    def test_a_ticket_and_a_reason_are_both_required(self):
        for arguments in ([], [TICKET], ["--reason", "why"], [TICKET, "--reason"], [TICKET, "--reason="],
                          [TICKET, "--reason", "--force"]):
            with self.subTest(arguments=arguments), self.assertRaises(ValueError) as raised:
                cancel.parse(arguments)
            self.assertIn("./task test:cancel", str(raised.exception))

    def test_options_are_parsed_in_either_spelling(self):
        self.assertEqual(cancel.parse([TICKET, "--reason", "stale capture"]), (TICKET, "stale capture", False))
        self.assertEqual(cancel.parse(["--reason=stuck", "--force", TICKET]), (TICKET, "stuck", True))

    def test_unknown_options_and_extra_tickets_are_refused(self):
        for arguments in ([TICKET, "--reason", "why", "--all"], [TICKET, "other", "--reason", "why"]):
            with self.subTest(arguments=arguments), self.assertRaises(ValueError):
                cancel.parse(arguments)


class SelectionTests(unittest.TestCase):
    def setUp(self):
        self.request = entry(TICKET, "test:integration", "running")
        self.batch = entry(TICKET + "-batch", "verification batch", "running",
                           kind="batch", members=[TICKET])
        self.entries = [self.request, self.batch]

    def test_a_prefix_addresses_the_request_not_its_batch(self):
        self.assertIs(cancel.select(self.entries, TICKET[:20]), self.request)
        self.assertIs(cancel.select(self.entries, TICKET + "-batch"), self.batch)

    def test_an_unmatched_or_ambiguous_prefix_names_the_alternatives(self):
        with self.assertRaisesRegex(ValueError, "no queued or running request"):
            cancel.select(self.entries, "0000000000000000000999")
        peers = [entry("prefix-a", "check-fast", "queued"), entry("prefix-b", "check-fast", "queued")]
        with self.assertRaisesRegex(ValueError, "matches 2 entries"):
            cancel.select(peers, "prefix-")


class RefusalTests(unittest.TestCase):
    def status(self, *runs, **fields):
        return {"runs": list(runs), "operations": [], "paused": None, **fields}

    def test_a_queued_request_costs_nothing_to_withdraw(self):
        waiting = entry(TICKET, "check-fast", "queued")
        self.assertIsNone(cancel.refusal(self.status(waiting), waiting, force=False))

    def test_the_batch_supervisor_is_not_a_request(self):
        member = entry(TICKET, "test:digest", "sharing", batch={"ticket": TICKET + "-batch", "directory": "/d"})
        supervisor = entry(TICKET + "-batch", "verification batch", "running", kind="batch",
                           members=[TICKET, "departed"])
        refused = cancel.refusal(self.status(member, supervisor), supervisor, force=False)
        self.assertIn("not a request", refused)
        self.assertIn(TICKET, refused)
        self.assertNotIn("departed", refused)

    def test_a_batch_no_request_still_wants_is_withdrawable(self):
        supervisor = entry(TICKET + "-batch", "verification batch", "running", kind="batch", members=[TICKET],
                           health={"state": "supervision_lost"})
        self.assertIsNone(cancel.refusal(self.status(supervisor), supervisor, force=False))

    def test_shared_execution_survives_withdrawing_one_subscriber(self):
        peer = entry("peer", "test:digest", "queued")
        member = entry(TICKET, "test:digest", "sharing", batch={"ticket": "b", "directory": "/d"})
        supervisor = entry("b", "verification batch", "running", kind="batch", members=[TICKET, "peer"])
        self.assertIsNone(cancel.refusal(self.status(member, peer, supervisor), member, force=False))

    def test_sole_healthy_work_is_kept_unless_the_caller_insists(self):
        member = entry(TICKET, "test:digest", "sharing", batch={"ticket": "b", "directory": "/d"},
                       health={"state": "active"})
        supervisor = entry("b", "verification batch", "running", kind="batch", members=[TICKET])
        status = self.status(member, supervisor)
        refused = cancel.refusal(status, member, force=False)
        self.assertIn("no other request shares", refused)
        self.assertIn("health is active", refused)
        self.assertIn("--force", refused)
        self.assertIsNone(cancel.refusal(status, member, force=True))

    def test_an_adverse_health_observation_is_enough_on_its_own(self):
        for state in sorted(cancel.ADVERSE_HEALTH):
            with self.subTest(state=state):
                stuck = entry(TICKET, "test:integration", "running", health={"state": state})
                self.assertIsNone(cancel.refusal(self.status(stuck), stuck, force=False))

    def test_an_unobserved_running_request_still_needs_force(self):
        running = entry(TICKET, "test:integration", "running")
        refused = cancel.refusal(self.status(running), running, force=False)
        self.assertIn("no adverse health observation", refused)


class RecordTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.queue = SimpleNamespace(root=self.root, locked=contextlib.nullcontext)

    def test_the_reason_reaches_both_the_batch_and_the_queue_log(self):
        directory = self.root / "batch"
        directory.mkdir()
        member = entry(TICKET, "test:digest", "sharing", batch={"ticket": "b", "directory": str(directory)})
        supervisor = entry("b", "verification batch", "running", kind="batch",
                           members=[TICKET], directory=str(directory))
        status = {"runs": [member, supervisor], "operations": [], "paused": None}
        cancel.record(self.queue, status, member, "captured before the fix", force=False)
        beside = read_state(directory / (TICKET + ".cancelled.json"))
        self.assertEqual(beside["reason"], "captured before the fix")
        self.assertEqual(beside["name"], "test:digest")
        logged = read_state(self.root / "cancellations.json")["cancellations"]
        self.assertEqual(logged[-1]["ticket"], TICKET)
        self.assertFalse(logged[-1]["forced"])

    def test_the_log_is_bounded(self):
        waiting = entry(TICKET, "check-fast", "queued")
        status = {"runs": [waiting], "operations": [], "paused": None}
        for index in range(cancel.MAX_RECORDS + 5):
            cancel.record(self.queue, status, waiting, f"reason {index}", force=False)
        logged = read_state(self.root / "cancellations.json")["cancellations"]
        self.assertEqual(len(logged), cancel.MAX_RECORDS)
        self.assertEqual(logged[-1]["reason"], f"reason {cancel.MAX_RECORDS + 4}")


class LifecycleTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.queue = execution.Queue(self.root / "queue")
        self.env = {**os.environ, "PW_TEST_EXECUTION_ROOT": str(self.queue.root), "PW_TEST_WORKERS": "1",
                    "GOCACHE": str(self.root / "go-cache")}
        self.env.pop("PW_TEST_EXECUTION_TICKET", None)

    def start(self, name, body):
        process = subprocess.Popen([sys.executable, str(SCRIPT), "run", "--name", name,
                                    "--", sys.executable, "-c", body], env=self.env,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)

        def cleanup():
            if process.poll() is None:
                process.terminate()
            try:
                process.communicate(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.communicate()

        self.addCleanup(cleanup)
        return process

    def await_state(self, predicate):
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if predicate():
                return
            time.sleep(0.01)
        self.fail("execution state did not settle")

    def invoke(self, *arguments):
        return subprocess.run([sys.executable, str(SCRIPT), "cancel", "--", *arguments],
                              env=self.env, capture_output=True, text=True, timeout=30)

    def test_a_waiting_request_is_withdrawn_and_the_queue_moves_on(self):
        self.queue.pause("cancel fixture")
        waiting = self.start("withdrawn", "raise AssertionError('must not run')")
        self.await_state(lambda: len(self.queue.status()["runs"]) == 1)
        ticket = self.queue.status()["runs"][0]["ticket"]
        result = self.invoke(ticket, "--reason", "superseded by a later edit")
        self.assertEqual(result.returncode, 0, result.stderr)
        record = json.loads(result.stdout)
        self.assertTrue(record["released"])
        self.assertEqual(record["reason"], "superseded by a later edit")
        self.assertEqual(waiting.wait(timeout=10), 130)
        self.assertEqual(self.queue.status()["runs"], [])
        self.queue.resume()
        following = self.start("following", "print('ok')")
        out, err = following.communicate(timeout=10)
        self.assertEqual(following.returncode, 0, err)
        self.assertEqual(out.strip(), "ok")

    @unittest.skipIf(os.name == "nt", "requires POSIX signal delivery")
    def test_running_work_is_kept_until_the_caller_forces_it(self):
        ready = self.root / "ready"
        running = self.start("active fixture",
                             "import time; from pathlib import Path\n"
                             f"Path({str(ready)!r}).touch()\n"
                             "while True: time.sleep(.01)")
        self.await_state(ready.exists)
        ticket = self.queue.status()["runs"][0]["ticket"]
        refused = self.invoke(ticket, "--reason", "changed my mind")
        self.assertEqual(refused.returncode, 2)
        self.assertIn("--force", refused.stderr)
        self.assertIsNone(running.poll())
        forced = self.invoke(ticket, "--reason", "accidental full suite", "--force")
        self.assertEqual(forced.returncode, 0, forced.stderr)
        self.assertTrue(json.loads(forced.stdout)["forced"])
        running.wait(timeout=10)
        self.assertEqual(self.queue.status()["runs"], [])

    @unittest.skipIf(os.name == "nt", "requires inherited POSIX file locks")
    def test_a_lease_held_only_by_escaped_descendants_is_released(self):
        ready = self.root / "ready"
        escapee = ("import signal, time\n"
                   "signal.signal(signal.SIGTERM, signal.SIG_IGN)\n"
                   "while True: time.sleep(.05)")
        running = self.start("lost supervisor",
                             "import os, subprocess, sys, time\n"
                             "from pathlib import Path\n"
                             "descriptor = int(os.environ['PW_TEST_EXECUTION_FD'])\n"
                             f"escapee = subprocess.Popen([sys.executable, '-c', {escapee!r}],\n"
                             "                           pass_fds=(descriptor,), start_new_session=True)\n"
                             f"Path({str(ready)!r}).write_text(f'{{os.getpid()}} {{escapee.pid}}')\n"
                             "while True: time.sleep(.01)")
        self.await_state(lambda: ready.exists() and len(ready.read_text().split()) == 2)
        child, orphan = map(int, ready.read_text().split())

        def stop_orphan():
            with contextlib.suppress(ProcessLookupError):
                os.kill(orphan, signal.SIGKILL)

        self.addCleanup(stop_orphan)
        running.kill()
        running.wait(timeout=10)
        os.kill(child, signal.SIGKILL)
        ticket = self.queue.status()["runs"][0]["ticket"]
        self.await_state(lambda: cancel.lease_holders(self.queue.root / (ticket + ".lease")) == {orphan})
        result = self.invoke(ticket, "--reason", "supervisor and child exited; an escaped descendant holds the lease")
        self.assertEqual(result.returncode, 0, result.stderr)
        record = json.loads(result.stdout)
        self.assertTrue(record["released"])
        self.assertEqual(record["owner"], "exited")
        self.assertEqual([(held["pid"], held["signal"]) for held in record["reclaimed"]], [(orphan, "SIGKILL")])
        self.assertEqual(self.queue.status()["runs"], [])

    def test_an_unknown_ticket_changes_nothing(self):
        result = self.invoke("00000000000000000999", "--reason", "nothing to cancel")
        self.assertEqual(result.returncode, 2)
        self.assertIn("no queued or running request", result.stderr)

    def test_cancellation_cannot_run_inside_an_admitted_check(self):
        with execution.Lease(self.queue, "holder", 1) as lease:
            lease.entry.update(state="running")
            lease.write()
            env = {**self.env, "PW_TEST_EXECUTION_TICKET": lease.entry["ticket"]}
            with patch.dict(os.environ, env), patch.object(execution.Queue, "inherited", return_value=True):
                with self.assertRaisesRegex(ValueError, "inside an admitted check"):
                    with patch.object(sys, "argv", ["test-execution.py", "cancel", "--", "x",
                                                    "--reason", "y"]):
                        execution.main()


if __name__ == "__main__":
    unittest.main()
