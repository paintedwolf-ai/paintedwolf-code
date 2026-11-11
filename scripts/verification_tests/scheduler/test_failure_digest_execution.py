import json
import os
from pathlib import Path
import signal
import tempfile
import unittest

from verification_lifecycle import digest
from verification_tests import support

LINKER = "ld: warning: ignoring duplicate libraries: '-lobjc'"


def lines_of(block):
    return block.rstrip("\n").split("\n")


def block_of(stdout):
    """The digest and everything after it; delivery requires that to be the digest alone."""
    return stdout[stdout.index(digest.MARKER):]


class DigestBounds(unittest.TestCase):
    def assert_bounded(self, block, max_bytes=None):
        self.assertLessEqual(len(block.encode()), max_bytes or digest.budget())
        self.assertLessEqual(len(lines_of(block)), digest.MAX_LINES)

    def target(self, name, lines=(), lead=(), status="failed", code=1):
        return {"name": name, "package": name, "status": status, "exit_code": code, "result": {},
                "lead": list(lead), "lines": list(lines)}

    def test_ac_ver_03_caps_hold_for_any_target_count_and_budget(self):
        unicode_line = "    a_test.go:12: want " + "é界🐺" * 500
        for count in (1, 2, 11, 12, 40, 500):
            for max_bytes in (digest.MIN_BYTES, 2000, digest.budget()):
                with self.subTest(count=count, max_bytes=max_bytes):
                    found = [self.target(f"test:short example/{i}", [unicode_line] * 80, ["Tests: TestOne"])
                             for i in range(count)]
                    references = ["Receipt: /receipts/r.json", "Full log: /logs/stage.log"]
                    block = digest.compose(digest.status_head(["check-fast"], "failed", 1), found, references, max_bytes)
                    self.assert_bounded(block, max_bytes)
                    block.encode().decode()
                    self.assertEqual(lines_of(block)[-2:], references)
                    headers = sum(line.startswith("- ") for line in lines_of(block))
                    self.assertGreaterEqual(headers, 1)
                    omitted = [line for line in lines_of(block) if line.startswith("Omitted: ")]
                    self.assertEqual(bool(omitted), headers < count)
                    if omitted:
                        self.assertEqual(omitted[0].split()[1:4], [str(count - headers), "of", str(count)])

    def test_ac_ver_03_targets_share_deterministically_and_unused_room_rolls_on(self):
        found = [self.target("short", ["one error"]), self.target("long", [f"{i} error" for i in range(200)])]
        head = digest.status_head(["unit"], "failed", 1)
        first = digest.compose(head, found, ["Full log: /l"], digest.budget())
        self.assertEqual(first, digest.compose(head, found, ["Full log: /l"], digest.budget()))
        self.assertEqual(sum(line.startswith("  ") for line in lines_of(first)), digest.MAX_LINES - 6)

    def test_ac_ver_03_long_names_and_references_stay_bounded(self):
        block = digest.compose(digest.status_head(["n" * 900], "failed", 1, reason="r" * 900, not_verified=["s" * 900] * 12),
                               [self.target("start" + "m" * 900 + "end", ["x"])],
                               ["Receipt: /" + "p" * 900], digest.MIN_BYTES)
        self.assert_bounded(block, digest.MIN_BYTES)
        header = next(line for line in lines_of(block) if line.startswith("- "))
        self.assertLessEqual(len(header.encode()), digest.NAME_BYTES + 32)
        self.assertIn("start", header)
        self.assertIn("end:", header)

    def test_budget_fits_the_smallest_tail_and_late_output(self):
        self.assertEqual(digest.budget(), digest.STREAM_TAIL_BYTES)
        self.assertEqual(digest.budget("x" * 3000), digest.MERGED_TAIL_BYTES - 3000 - digest.STREAM_LABEL_BYTES)
        self.assertEqual(digest.budget("x" * 9000), digest.MIN_BYTES)

    def test_excerpts_select_diagnostics_without_control_bytes_or_noise(self):
        with tempfile.TemporaryDirectory() as temp:
            log = Path(temp) / "stage.log"
            log.write_bytes("\n".join(["[PASS] build", LINKER, "\x1b[31mstep\x1b[0m", "error: real cause", "after",
                                       "task: Failed to run task", "\xff"]).encode("latin-1", "replace"))
            shown = digest.log_lines(log)
            self.assertNotIn(LINKER, shown)
            self.assertFalse(any(line.startswith(("[PASS]", "task: ")) for line in shown))
            self.assertEqual(digest.representative(shown, 3), ["step", "error: real cause", "after"])
            self.assertEqual(digest.representative(shown, 1), ["error: real cause"])
            self.assertEqual(digest.log_lines(Path(temp) / "missing"), [])
            self.assertEqual(digest.representative(["plain", "text"], 1), ["text"])
            self.assertEqual(digest.representative(["x"], 0), [])
            self.assertEqual(digest.log_lines(None), [])

    def test_ac_ver_01_early_diagnostics_survive_any_amount_of_later_log(self):
        with tempfile.TemporaryDirectory() as temp:
            log = Path(temp) / "stage.log"
            passes = "".join(f"step {i}\n" for i in range(200000))
            log.write_text(f"before\nfoo_test.go:12: want 1 got 2\nafter\n{passes}last\n")
            shown = digest.log_lines(log)
            self.assertEqual(shown[:3], ["before", "foo_test.go:12: want 1 got 2", "after"])
            self.assertEqual(shown[-1], "last")
            self.assertEqual(len(shown), 3 + digest.LOG_NEWEST_LINES)
            self.assertEqual(digest.representative(shown, 3)[1], "foo_test.go:12: want 1 got 2")

    def test_go_events_yield_leaf_assertions_panic_cause_and_build_output(self):
        events = [
            {"Action": "output", "Package": "p/a", "Test": "TestA/sub", "Output": "=== RUN   TestA/sub\n"},
            {"Action": "output", "Package": "p/a", "Test": "TestA/sub", "Output": "--- FAIL: TestA/sub (0.00s)\n"},
            {"Action": "output", "Package": "p/a", "Test": "TestA/sub", "Output": "    a_test.go:9: want 1 got 2\n"},
            {"Action": "fail", "Package": "p/a", "Test": "TestA/sub"},
            {"Action": "fail", "Package": "p/a", "Test": "TestA"},
            {"Action": "output", "Package": "p/a", "Output": "panic: boom\n"},
            {"Action": "output", "Package": "p/a", "Output": "goroutine 1 [running]:\n"},
            {"Action": "fail", "Package": "p/a"},
            {"Action": "output", "Package": "p/other", "Test": "TestOther", "Output": "unrelated error\n"},
            {"ImportPath": "p/b [p/b.test]", "Action": "build-output", "Output": "b.go:1:1: undefined: y\n"},
            {"ImportPath": "p/unrelated [p/unrelated.test]", "Action": "build-output", "Output": "p/a noise\n"},
            {"Action": "fail", "Package": "p/quiet"},
            ["not", "an", "event"],
        ]
        with tempfile.TemporaryDirectory() as temp:
            raw = Path(temp) / "stage-001-go.json"
            raw.write_text("\n".join(json.dumps(event) for event in events) + "\np/a not json\n")
            log = Path(temp) / "stage-001.log"
            log.write_text("FAIL p/quiet unknown shape\n")
            result = lambda: {"status": "failed", "exit_code": 1, "stage_id": "stage-001", "log": str(log)}
            receipt = {"evidence": [{"stage": "test:short", "results": {
                "p/a": result(), "p/b": result(), "p/quiet": result(), "p/fine": {"status": "passed"}}}]}
            found = {target["package"]: target for target in digest.targets(receipt, temp)}
            self.assertNotIn("p/fine", found)
            self.assertEqual(found["p/a"]["lead"], ["Tests: TestA/sub"])
            self.assertEqual(found["p/a"]["lines"], ["    a_test.go:9: want 1 got 2", "panic: boom", "goroutine 1 [running]:"])
            self.assertEqual(found["p/b"]["lines"], ["b.go:1:1: undefined: y"])
            self.assertEqual(found["p/quiet"]["lines"], ["FAIL p/quiet unknown shape"])

    def test_many_failed_test_names_report_their_overflow(self):
        record = {"failed": [f"Test{i}" for i in range(5)], "tests": {}, "package": [], "build": []}
        self.assertEqual(digest.go_diagnostics(record)[0], ["Tests: Test0, Test1, Test2 (+2 more)"])

    def test_unreadable_events_fall_back_to_the_recorded_output(self):
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / "failure.txt"
            output.write_text("boom error\n")
            result = {"status": "failed", "exit_code": 1, "raw": str(Path(temp) / "missing.json"), "output": str(output)}
            found = digest.targets({"evidence": [{"stage": "test:short", "results": {"p": result}}]}, temp)
            self.assertEqual(found[0]["lines"], ["boom error"])
            self.assertEqual(digest.go_records(None, ["p"])["p"]["failed"], [])
            self.assertEqual(digest.go_records(temp, ["p"])["p"]["failed"], [])

    def test_failures_lead_unverified_results(self):
        results = {"a": {"status": "unverified", "exit_code": 2}, "b": {"status": "failed", "exit_code": 1}}
        with tempfile.TemporaryDirectory() as temp:
            found = digest.targets({"evidence": [{"stage": "unit", "results": results}]}, temp)
        self.assertEqual([target["package"] for target in found], ["b", "a"])

    def test_preview_filter_drops_only_exact_standalone_warnings(self):
        kept = [f"# example [example.test]\n", f"  {LINKER}\n", f"{LINKER} extra\n", "ld: warning: other\n", f"{LINKER[:-1]}\n"]
        stream = b"".join(line.encode() for pair in zip(kept, [LINKER + "\n"] * len(kept)) for line in pair)
        for chunk in (1, 7, len(stream)):
            with self.subTest(chunk=chunk):
                preview = digest.PreviewFilter()
                out = b"".join(preview.feed(stream[i:i + chunk]) for i in range(0, len(stream), chunk)) + preview.finish()
                self.assertEqual(out.decode(), "".join(kept))
        preview = digest.PreviewFilter()
        self.assertEqual(preview.feed(LINKER.encode() + b"\r\nld:"), b"")
        self.assertEqual(preview.finish(), b"ld:")
        preview = digest.PreviewFilter()
        self.assertEqual(preview.feed(LINKER.encode()), b"")
        self.assertEqual(preview.finish(), b"")
        preview = digest.PreviewFilter()
        self.assertEqual(preview.feed(b"progress"), b"progress")

    def test_delivery_failure_does_not_replace_the_result(self):
        class Closed:
            class buffer:
                @staticmethod
                def write(_):
                    raise BrokenPipeError()
        digest.deliver("block", Closed)


class DigestDelivery(support.BatchFixture):
    """AC-VER-01 to AC-VER-03 through the real subscriber, batch, stage, and job paths."""

    def noisy(self, name, body):
        directory = self.root / "fixture-output"
        directory.mkdir(exist_ok=True)
        (directory / name).write_text(body)
        return {**self.env, "FIXTURE_OUTPUT": str(directory)}

    def noise(self):
        passes = "".join(f"[PASS] target-{i}\n{LINKER}\n{LINKER}\n" for i in range(600))
        return f"internal/foo/foo_test.go:12: want 1 got 2\nFAIL example/foo\n{passes}"

    def finish(self, process, code):
        stdout, stderr = process.communicate(timeout=self.state_timeout + 5)
        self.assertEqual(process.returncode, code, stdout + stderr)
        return stdout, stderr

    def assert_digest_ends(self, stdout):
        block = block_of(stdout)
        self.assertEqual(stdout.count(digest.MARKER), 1)
        self.assertLessEqual(len(block.encode()), digest.budget())
        self.assertLessEqual(len(lines_of(block)), digest.MAX_LINES)
        self.assertTrue(stdout.encode()[-digest.STREAM_TAIL_BYTES:].decode("utf-8", "ignore").startswith(block)
                        or block in stdout[-digest.STREAM_TAIL_BYTES:])
        return lines_of(block)

    def reference(self, lines, label):
        return Path(next(line for line in lines if line.startswith(label + ": ")).split(": ", 1)[1])

    def test_ac_ver_01_early_failure_survives_later_passes_and_linker_noise(self):
        env = {**self.noisy("build", self.noise()), "FIXTURE_FAIL": "build"}
        process = self.start("build", env=env)
        self.queued(1)
        self.queue.resume()
        stdout, stderr = self.finish(process, 1)
        self.assertNotIn(LINKER, stdout)
        lines = self.assert_digest_ends(stdout)
        self.assertIn("- build: failed, exit 1", lines)
        self.assertIn("  internal/foo/foo_test.go:12: want 1 got 2", lines)
        receipt = json.loads(self.reference(lines, "Receipt").read_text())
        self.assertEqual((receipt["status"], receipt["exit_code"]), ("failed", 1))
        log = self.reference(lines, "Full log")
        self.assertEqual(log.read_text().count(LINKER), 1200)
        self.assertEqual((log.parent / "batch.log").read_text().count(LINKER), 1200)
        self.assertIn(f"{receipt['names'][0]}: failed", stderr)

    def test_ac_ver_02_passing_requests_have_no_digest_and_keep_their_receipt(self):
        process = self.start("build", env=self.noisy("build", self.noise()))
        self.queued(1)
        self.queue.resume()
        stdout, stderr = self.finish(process, 0)
        self.assertNotIn(digest.MARKER, stdout + stderr)
        self.assertIn("[PASS] target-599", stdout)
        self.assertNotIn(LINKER, stdout)
        self.assertEqual(self.receipts()[0]["status"], "passed")
        self.assertIn("receipt ", stderr)

    def test_ac_ver_03_gate_failure_names_stages_it_did_not_verify(self):
        process = self.start("check-fast", env={**self.env, "FIXTURE_FAIL": "build"})
        self.queued(1)
        self.queue.resume()
        stdout, _ = self.finish(process, 1)
        lines = self.assert_digest_ends(stdout)
        self.assertEqual(sum(line.startswith("- ") for line in lines), 1)
        not_verified = next(line for line in lines if line.startswith("Not verified: "))
        self.assertEqual(not_verified.removeprefix("Not verified: ").split(", "), ["lint", "unit"])

    def test_ac_ver_03_early_package_failure_reports_before_its_stage_ends(self):
        self.hold.touch()
        process = self.start("test:digest", "--", "./bad", "./good", env={**self.env, "FIXTURE_EARLY": "1"})
        self.queued(1)
        self.queue.resume()
        stdout, _ = self.finish(process, 1)
        lines = self.assert_digest_ends(stdout)
        self.assertIn("- test:digest fixture/bad: failed, exit 1", lines)
        self.assertTrue(any(line.startswith("Reason: fixture/bad failed") for line in lines))

    def test_ac_ver_03_incomplete_evidence_is_unverified_not_failed(self):
        process = self.start("test:digest", "--", "./good", env={**self.env, "FIXTURE_MISSING": "1"})
        self.queued(1)
        self.queue.resume()
        stdout, _ = self.finish(process, 2)
        lines = self.assert_digest_ends(stdout)
        self.assertEqual(lines[1], "Request test:digest: unverified, exit 2.")
        self.assertIn("- test:digest fixture/good: unverified, exit 2", lines)

    def test_ac_ver_03_cancellation_keeps_its_own_status(self):
        self.hold.touch()
        process = self.start("build")
        self.queued(1)
        self.queue.resume()
        self.await_condition(lambda: bool(self.records()))
        ticket = next(entry["ticket"] for entry in self.queue.status()["runs"] if entry.get("kind") == "request")
        cancel = support.subprocess.run(
            [support.sys.executable, str(self.scripts / "test-execution.py"), "cancel", "--", ticket,
             "--force", "--reason", "fixture"], cwd=self.repo, env=self.env, capture_output=True, text=True)
        self.assertEqual(cancel.returncode, 0, cancel.stdout + cancel.stderr)
        stdout, _ = self.finish(process, 130)
        lines = self.assert_digest_ends(stdout)
        self.assertEqual(lines[1], "Request build: cancelled, exit 130.")
        self.assertIn("Reason: fixture", lines)

    def test_ac_ver_03_interrupted_subscriber_reports_unverified(self):
        process = self.start("build")
        self.queued(1)
        os.kill(process.pid, signal.SIGTERM)
        stdout, _ = self.finish(process, 130)
        lines = self.assert_digest_ends(stdout)
        self.assertEqual(lines[1], "Request build: unverified, exit 130.")

    def test_ac_ver_03_interrupted_shared_subscriber_keeps_its_receipt_reference(self):
        self.hold.touch()
        process = self.start("build")
        self.queued(1)
        self.queue.resume()
        while "joined batch" not in process.stderr.readline():
            pass
        os.kill(process.pid, signal.SIGTERM)
        stdout, _ = self.finish(process, 130)
        lines = self.assert_digest_ends(stdout)
        self.assertEqual(self.reference(lines, "Full log").name, "batch.log")
        self.assertTrue(any(line.startswith("Receipt: ") for line in lines))
        self.assertTrue(any(line.startswith("Not verified: build") for line in lines))

    def test_ac_ver_01_waiter_gets_the_jobs_digest_and_json_stays_on_stdout(self):
        submitted = json.loads(self.collect(self.start(
            "--background", "build", env={**self.noisy("build", self.noise()), "FIXTURE_FAIL": "build"})))
        self.queued(1)
        self.queue.resume()
        stdout, stderr = self.finish(self.start("--wait", submitted["job_id"]), 1)
        self.assertEqual(json.loads(stdout)["exit_code"], 1)
        lines = lines_of(block_of(stderr))
        self.assertIn("- build: failed, exit 1", lines)
        self.assertIn("  internal/foo/foo_test.go:12: want 1 got 2", lines)
        self.assertEqual(stderr, stderr[stderr.index(digest.MARKER):])

    def test_ac_ver_03_lost_job_reports_from_its_result_alone(self):
        job_id = "1" * 32
        directory = self.artifacts / "jobs" / job_id
        directory.mkdir(parents=True)
        (directory / "job.json").write_text("{}")
        (directory / "completion.lock").touch()
        stdout, stderr = self.finish(self.start("--wait", job_id), 2)
        self.assertEqual(json.loads(stdout)["state"], "unverified")
        lines = lines_of(stderr)
        self.assertEqual(lines[0], digest.MARKER)
        self.assertIn("unverified, exit 2", lines[1])
        self.assertIn("Reason: Verification supervisor exited without a result", lines)
        self.assertTrue(lines[-1].endswith("output.log"))

    def test_ac_ver_03_job_digest_status_comes_from_the_result(self):
        with tempfile.TemporaryDirectory() as temp:
            result = {"state": "completed", "exit_code": 1, "task_exit_code": 201}
            self.assertIn(": failed, exit 1 (tool exit 201).", digest.job_digest(temp, result))
            (Path(temp) / "output.log").write_text(f"earlier\n{digest.MARKER}\nold\n{digest.MARKER}\nnew\n")
            self.assertEqual(digest.job_digest(temp, result), f"{digest.MARKER}\nnew\n")
            (Path(temp) / "output.log").write_text(f"{digest.MARKER}\nonly\n")
            self.assertEqual(digest.job_digest(temp, result), f"{digest.MARKER}\nonly\n")

    def test_ac_ver_03_invocation_outside_a_batch_reports_structured_status(self):
        process = self.start("test:exclusive", env={**self.env, "FIXTURE_FAIL": "test:exclusive"})
        self.await_condition(lambda: bool(self.queue.status()["runs"]))
        self.queue.resume()
        stdout, _ = self.finish(process, 1)
        lines = self.assert_digest_ends(stdout)
        self.assertEqual(lines[1], "Request test:exclusive: failed, exit 1.")
        self.assertIn("no receipt", lines[-1])

    def test_ac_ver_03_interrupted_invocation_outside_a_batch_is_unverified(self):
        process = self.start("test:exclusive")
        self.await_condition(lambda: bool(self.queue.status()["runs"]))
        os.kill(process.pid, signal.SIGTERM)
        stdout, _ = self.finish(process, 130)
        self.assertEqual(self.assert_digest_ends(stdout)[1], "Request test:exclusive: unverified, exit 130.")


if __name__ == "__main__":
    unittest.main()
