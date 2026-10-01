import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time
from unittest.mock import patch

import verification_batch as batch
import verification_resources as resources
from verification_tests import support


class BatchLifecycleTests(support.BatchFixture):
    def test_unknown_flags_reject_before_queue_or_background_job_creation(self):
        for prefix in ([], ["--background"]):
            with self.subTest(prefix=prefix):
                process = self.start(*prefix, "den:harness:test", "--", "e2e/fixture.spec.ts", "--workers=1")
                output = self.collect(process, code=2)
                self.assertIn("unsupported flag '--workers=1'", output)
                self.assertEqual(self.queue.status()["runs"], [])
                self.assertEqual(self.records(), [])
                self.assertFalse((self.artifacts / "jobs").exists())

    def test_requests_task_would_reject_or_narrow_refuse_before_queue_or_background_job_creation(self):
        for args, message in ((["test:missing"], 'Task "test:missing" does not exist'),
                              (["test:drops", "--", "./good"], "Task would drop ./good"),
                              (["test:forwards", "test:drops", "--", "-x"], "test:drops passes nothing after --"),
                              (["test:digest", "--", "./missing"], "no package directory"),
                              (["den:harness:test", "--", "missing.spec.ts"], "no web-project spec")):
            for prefix in ([], ["--background"]):
                with self.subTest(args=args, prefix=prefix):
                    output = self.collect(self.start(*prefix, *args), code=2)
                    self.assertIn(message, output)
                    self.assertIn("No verification was queued", output)
                    self.assertEqual(self.queue.status()["runs"], [])
                    self.assertFalse((self.artifacts / "jobs").exists())
        self.start("test:forwards", "--", "--anything")
        self.await_condition(lambda: len(self.queue.status()["runs"]) == 1)

    def test_background_job_waits_through_admission_and_replays_completion(self):
        submitted = json.loads(self.collect(self.start("--background", "build")))
        job_id = submitted["job_id"]
        self.queued(1)
        first = self.start("--wait", job_id)
        second = self.start("--wait", job_id)
        with self.assertRaises(subprocess.TimeoutExpired):
            first.communicate(timeout=.1)
        self.assertEqual(self.records(), [])
        self.queue.resume()
        first_result = json.loads(self.collect(first))
        self.assertEqual(first_result, json.loads(self.collect(second)))
        self.assertEqual(first_result, json.loads(self.collect(self.start("--wait", job_id))))
        self.assertEqual(first_result["state"], "completed")
        self.assertEqual(first_result["exit_code"], 0)
        self.assertIn("receipt", Path(first_result["log"]).read_text())
        self.assertEqual(len(self.records()), 1)

    def test_cancelling_a_background_job_releases_admission_and_completes_its_wait(self):
        submitted = json.loads(self.collect(self.start("--background", "build")))
        self.queued(1)
        ticket = next(e["ticket"] for e in self.queue.status()["runs"] if e.get("kind") == "request")
        cancelled = subprocess.run([sys.executable, str(self.scripts / "test-execution.py"), "cancel", "--",
                                    ticket, "--reason", "superseded by a later edit"],
                                   cwd=self.repo, env=self.env, capture_output=True, text=True,
                                   timeout=self.state_timeout + 5)
        self.assertEqual(cancelled.returncode, 0, cancelled.stdout + cancelled.stderr)
        self.assertTrue(json.loads(cancelled.stdout)["released"])
        result = json.loads(self.collect(self.start("--wait", submitted["job_id"]), code=2))
        self.assertEqual(result["exit_code"], 2, "a cancelled run establishes nothing")
        self.assertEqual(result["task_exit_code"], 130, "the tool's own code stays visible")
        self.assertEqual(self.queue.status()["runs"], [])
        self.assertEqual(self.records(), [])
        self.queue.resume()
        self.collect(self.start("build"))
        self.assertEqual(len(self.records()), 1)

    def test_cancelled_waiter_leaves_background_job_and_failure_result_intact(self):
        submitted = json.loads(self.collect(self.start("--background", "build",
                                                       env={**self.env, "FIXTURE_FAIL": "build"})))
        self.queued(1)
        waiter = self.start("--wait", submitted["job_id"])
        with self.assertRaises(subprocess.TimeoutExpired):
            waiter.communicate(timeout=.1)
        waiter.terminate()
        waiter.communicate(timeout=5)
        self.assertEqual(len(self.queue.status()["runs"]), 1)
        self.queue.resume()
        result = json.loads(self.collect(self.start("--wait", submitted["job_id"]), code=1))
        self.assertEqual(result["exit_code"], 1)
        self.assertEqual(len(self.records()), 1)

    def test_background_job_controls_validate_handles_and_reject_non_checks(self):
        for arguments in (("--wait", "../outside"), ("--wait", "0" * 32),
                          ("--wait",), ("--background", "test:status"), ("--background", "--dry", "build"),
                          ("--background", "--on-complete", "[]", "build"),
                          ("--background", "--on-complete", '["bad\\u0000command"]', "build"),
                          ("--background", "--on-complete", '"shell command"', "build")):
            with self.subTest(arguments=arguments):
                self.collect(self.start(*arguments), code=2)
        self.assertEqual(self.queue.status()["runs"], [])

    def test_completion_callback_receives_one_result_without_shell_expansion(self):
        delivered = self.root / "delivered.jsonl"
        literal = "$(touch must-not-exist); quoted argument"
        callback = [sys.executable, "-c",
                    "import json,pathlib,sys; "
                    "p=pathlib.Path(sys.argv[1]); "
                    "p.open('a').write(json.dumps(sys.argv[2:])+'\\n')",
                    str(delivered), literal]
        submitted = json.loads(self.collect(self.start("--background", "--on-complete", json.dumps(callback), "build")))
        self.queued(1)
        self.assertFalse(delivered.exists())
        self.queue.resume()
        result = json.loads(self.collect(self.start("--wait", submitted["job_id"])))
        notification = Path(result["log"]).with_name("notification.json")
        self.await_condition(notification.exists)
        self.collect(self.start("--wait", submitted["job_id"]))
        deliveries = delivered.read_text().splitlines()
        self.assertEqual(len(deliveries), 1)
        arguments = json.loads(deliveries[0])
        self.assertEqual(arguments[0], literal)
        self.assertEqual(json.loads(arguments[1]), {k: v for k, v in result.items() if k != "task_exit_code"})
        self.assertEqual(json.loads(notification.read_text())["exit_code"], 0)

    def test_callback_failure_does_not_replace_verification_result(self):
        callback = [sys.executable, "-c", "raise SystemExit(7)"]
        submitted = json.loads(self.collect(self.start("--background", "--on-complete", json.dumps(callback), "build")))
        self.queued(1)
        self.queue.resume()
        result = json.loads(self.collect(self.start("--wait", submitted["job_id"])))
        notification = Path(result["log"]).with_name("notification.json")
        self.await_condition(notification.exists)
        self.assertEqual(json.loads(notification.read_text())["exit_code"], 7)
        self.assertEqual(result["exit_code"], 0)

    def test_missing_background_result_is_unverified(self):
        job_id = "1" * 32
        directory = self.artifacts / "jobs" / job_id
        directory.mkdir(parents=True)
        (directory / "job.json").write_text("{}")
        (directory / "completion.lock").touch()
        result = json.loads(self.collect(self.start("--wait", job_id), code=2))
        self.assertEqual(result["state"], "unverified")

    def test_malformed_background_result_is_unverified(self):
        job_id = "2" * 32
        directory = self.artifacts.resolve() / "jobs" / job_id
        directory.mkdir(parents=True)
        (directory / "job.json").write_text("{}")
        (directory / "completion.lock").touch()
        valid = {"job_id": job_id, "state": "completed", "exit_code": 0,
                 "finished_at": 123, "log": str(directory / "output.log")}
        malformed = ["{", "[]", "null", "{}"] + [json.dumps({**valid, **change}) for change in (
            {"state": []}, {"exit_code": True}, {"exit_code": "0"}, {"job_id": "3" * 32},
            {"finished_at": float("nan")}, {"finished_at": 0}, {"state": "unverified"}, {"log": "other"})]
        for body in malformed:
            with self.subTest(body=body):
                (directory / "result.json").write_text(body)
                result = json.loads(self.collect(self.start("--wait", job_id), code=2))
                self.assertEqual(result["state"], "unverified")
        (directory / "result.json").write_text(json.dumps(valid))
        self.assertEqual(json.loads(self.collect(self.start("--wait", job_id))),
                         {**valid, "task_exit_code": 0})
        (directory / "completion.lock").unlink()
        self.assertEqual(json.loads(self.collect(self.start("--wait", job_id), code=2))["state"], "unverified")

    def test_snapshot_toolchain_mutation_does_not_change_shared_runtime(self):
        self.hold.touch()
        toolchain = Path("lycaon-den/src-tauri/engine-root/gitengine")
        original = self.repo / toolchain
        original.mkdir(parents=True)
        (original / "binary").write_text("shared runtime")
        with (self.repo / ".gitignore").open("a") as ignored:
            ignored.write(str(toolchain) + "/\n")
        first = self.start("build")
        self.queued(1)
        self.queue.resume()
        self.await_condition(lambda: len(self.records()) == 1)
        captured = Path(self.records()[0]["cwd"]) / toolchain
        self.assertFalse(captured.is_symlink())
        (captured / "binary").write_text("snapshot mutation")
        self.assertEqual((original / "binary").read_text(), "shared runtime")
        self.release.touch()
        self.collect(first)
        self.await_condition(lambda: not self.queue.status()["runs"])
        (original / "binary").write_text("new shared runtime")
        self.release.unlink()
        second = self.start("build")
        self.await_condition(lambda: len(self.records()) == 2)
        self.assertEqual((Path(self.records()[-1]["cwd"]) / toolchain / "binary").read_text(), "new shared runtime")
        self.release.touch()
        self.collect(second)

    def test_harness_overlaps_go_keeps_frontend_exclusive_and_retains_artifacts(self):
        if resources.capacity() < 3:
            self.skipTest("harness overlap fixture requires three worker slots")
        self.hold.touch()
        self.catalog["tasks"].update({name: [name] for name in ("den:harness:test", "den:typecheck")})
        self.catalog["resources"].update({
            "den:harness:test": {"locks": ["harness", "frontend"], "workers": 1},
            "den:typecheck": {"locks": ["frontend"], "workers": 1},
            "go": {"locks": ["go"], "shared_locks": ["go"], "workers": 1}})
        self.save_catalog()
        task = self.repo / "task"
        task.write_text(task.read_text().replace("print('fixture '+name, flush=True)", '''
if name == 'den:harness:test':
 artifact=pathlib.Path(os.environ['LYCAON_E2E_OUTPUT_DIR'])/'failure.txt'
 artifact.parent.mkdir(parents=True,exist_ok=True)
 artifact.write_text('browser failure evidence')
print('fixture '+name, flush=True)'''))
        env = {**self.env, "FIXTURE_FAIL": "den:harness:test"}
        first = self.start("den:harness:test", "--", "fixture.spec.ts", env=env)
        self.queued(1)
        second = self.start("test:digest", "--", "./good", env=env)
        self.queued(2)
        third = self.start("den:typecheck", env=env)
        self.queued(3)
        self.queue.resume()
        self.await_condition(lambda: len(self.records()) == 2)
        self.assertEqual({r.get("name", "go") for r in self.records()}, {"den:harness:test", "go"})
        self.release.touch()
        self.collect(first, 1)
        self.collect(second)
        self.collect(third)
        self.assertEqual(self.records()[-1]["name"], "den:typecheck")
        receipt = next(r for r in self.receipts() if r["status"] == "failed")
        artifacts = Path(receipt["evidence"][0]["results"]["task"]["artifacts"])
        self.assertEqual((artifacts / "failure.txt").read_text(), "browser failure evidence")
        self.assertTrue(artifacts.resolve().is_relative_to((self.artifacts / "verification").resolve()))

    def parallel_profiles(self):
        if resources.capacity() < 3:
            self.skipTest("overlap fixture requires at least three worker slots")
        self.catalog["resources"] = {
            "build": {"locks": ["rust"], "workers": 1},
            "lint": {"locks": ["frontend"], "workers": 1},
            "unit": {"locks": ["rust"], "workers": 1}}
        self.save_catalog()
        self.hold.touch()

    def test_independent_stages_overlap_and_conflicting_stages_wait(self):
        self.parallel_profiles()
        first = self.start("build")
        self.queued(1)
        second = self.start("lint")
        self.queued(2)
        third = self.start("unit")
        self.queued(3)
        self.queue.resume()
        self.await_condition(lambda: len(self.records()) == 2)
        self.assertEqual({r["name"] for r in self.records()}, {"build", "lint"})
        status = self.queue.status()
        self.assertEqual(status["capacity"]["reserved"], 2)
        self.assertTrue(any(r["state"] == "queued" and r["locks"] == ["rust"] for r in status["operations"]))
        self.release.touch()
        for process in (first, second, third):
            self.collect(process)
        self.assertEqual(len({r["batch_id"] for r in self.receipts()}), 1)

    def test_late_incompatible_batch_overlaps_with_its_own_source(self):
        self.parallel_profiles()
        first = self.start("build")
        self.queued(1)
        self.queue.resume()
        self.await_condition(lambda: len(self.records()) == 1)
        (self.repo / "source.txt").write_text("later source")
        second = self.start("lint", env={**self.env, "FIXTURE_INPUT": "different"})
        self.await_condition(lambda: len(self.records()) == 2)
        self.assertEqual([r["source"] for r in self.records()], ["initial", "later source"])
        self.assertEqual(len([e for e in self.queue.status()["runs"] if e.get("kind") == "batch"]), 2)
        self.release.touch()
        self.collect(first)
        self.collect(second)
        self.assertEqual(len({r["batch_id"] for r in self.receipts()}), 2)

    def test_same_resource_waiters_do_not_fill_batch_slots_ahead_of_independent_work(self):
        self.parallel_profiles()
        first = self.start("lint")
        self.queued(1)
        self.queue.resume()
        self.await_condition(lambda: len(self.records()) == 1)
        second = self.start("lint", env={**self.env, "FIXTURE_INPUT": "different"})
        self.queued(2)
        third = self.start("build")
        self.await_condition(lambda: len(self.records()) == 2)
        self.assertEqual([r["name"] for r in self.records()], ["lint", "build"])
        active = [e for e in self.queue.status()["runs"] if e.get("kind") == "batch"]
        self.assertEqual(len(active), 2)
        self.release.touch()
        for process in (first, second, third):
            self.collect(process)
        self.assertEqual([r["name"] for r in self.records()], ["lint", "build", "lint"])

    def test_cancellation_stops_only_its_parallel_stage(self):
        self.parallel_profiles()
        first = self.start("build")
        self.queued(1)
        second = self.start("lint")
        self.queued(2)
        self.queue.resume()
        self.await_condition(lambda: len(self.records()) == 2)
        first.terminate()
        self.collect(first, 130)
        self.await_condition(lambda: self.queue.status()["capacity"]["reserved"] == 1)
        self.assertIsNone(second.poll())
        self.release.touch()
        self.collect(second)

    def test_parallel_operations_cannot_exceed_global_worker_capacity(self):
        self.parallel_profiles()
        limit = resources.capacity()
        self.env["PW_TEST_WORKERS"] = str(limit)
        for index, name in enumerate(("build", "lint", "unit")):
            self.catalog["resources"][name] = {"locks": [str(index)], "workers": limit // 2 + 1}
        self.save_catalog()
        processes = []
        for count, name in enumerate(("build", "lint", "unit"), 1):
            processes.append(self.start(name))
            self.queued(count)
        self.queue.resume()
        # One stage runs; the batch keeps a single reservation waiting for the capacity it holds.
        self.await_condition(lambda: len(self.queue.status()["operations"]) == 2 and len(self.records()) == 1)
        status = self.queue.status()
        self.assertEqual(status["capacity"]["reserved"], limit // 2 + 1)
        self.assertEqual(sum(r["state"] == "queued" for r in status["operations"]), 1)
        self.assertEqual(self.records()[0]["workers"], str(limit // 2 + 1))
        self.release.touch()
        for process in processes:
            self.collect(process)

    def test_a_gates_independent_stages_overlap(self):
        self.parallel_profiles()
        first = self.start("check-fast")
        self.queued(1)
        self.queue.resume()
        self.await_condition(lambda: len(self.records()) == 2)
        self.assertEqual({r["name"] for r in self.records()}, {"build", "lint"})
        self.release.touch()
        self.collect(first)
        self.assertEqual(sorted(r["name"] for r in self.records()), ["build", "lint", "unit"])
        self.assertEqual([e["stage"] for e in self.receipts()[0]["evidence"]], ["build", "lint", "unit"])

    def test_a_failed_stage_ends_its_gate_while_shared_work_continues(self):
        self.parallel_profiles()
        env = {**self.env, "FIXTURE_FAIL": "build"}
        first = self.start("check-fast", env=env)
        self.queued(1)
        second = self.start("lint", env=env)
        self.queued(2)
        self.queue.resume()
        self.await_condition(lambda: len(self.records()) == 2)
        self.assertEqual({r["name"] for r in self.records()}, {"build", "lint"})
        self.release.touch()
        self.collect(first, 1)
        self.collect(second)
        # The waiting stage's only requester failed before build released the lock it needed.
        self.assertEqual(len(self.records()), 2)
        failed = next(r for r in self.receipts() if r["status"] == "failed")
        self.assertIn("unit", failed["unverified_stages"])
        self.assertNotIn("build", failed["unverified_stages"])
        self.assertEqual(failed["evidence"][0]["stage"], "build")

    def test_a_failed_package_answers_its_request_before_the_stage_finishes(self):
        self.hold.touch()
        env = {**self.env, "FIXTURE_EARLY": "1"}
        first = self.start("test:digest", "--", "./bad", "./shared", env=env)
        self.queued(1)
        second = self.start("test:digest", "--", "./good", "./shared", env=env)
        self.queued(2)
        self.queue.resume()
        self.collect(first, 1)
        self.assertIsNone(second.poll())
        failed = next(r for r in self.receipts() if r["status"] == "failed")
        self.assertEqual(failed["evidence"][0]["results"]["fixture/bad"]["tests"], ["TestBad"])
        self.assertIn("before the stage finished", failed["reason"])
        self.release.touch()
        self.collect(second)
        self.assertEqual(sorted(r["status"] for r in self.receipts()), ["failed", "passed"])

    def test_an_early_failure_stops_a_stage_no_other_request_needs(self):
        self.hold.touch()
        first = self.start("test:digest", "--", "./bad", "./good", env={**self.env, "FIXTURE_EARLY": "1"})
        self.queued(1)
        self.queue.resume()
        self.collect(first, 1)
        self.await_condition(lambda: not self.queue.status()["runs"] and not self.queue.status()["operations"])
        self.assertFalse(self.release.exists())

    def test_declared_invocation_overlaps_shared_work_and_undeclared_one_waits(self):
        self.parallel_profiles()
        self.catalog["resources"]["test:invoke"] = {"locks": ["frontend"], "workers": 1}
        self.save_catalog()
        first = self.start("build")
        self.queued(1)
        self.queue.resume()
        self.await_condition(lambda: len(self.records()) == 1)
        invocation = self.start("test:invoke")
        self.await_condition(lambda: len(self.records()) == 2)
        self.assertEqual(self.records()[1]["name"], "test:invoke")
        exclusive = self.start("test:exclusive")
        self.await_condition(lambda: any(o["state"] == "queued" and "*" in o["locks"]
                                         for o in self.queue.status()["operations"]))
        time.sleep(.5)
        self.assertEqual(len(self.records()), 2)
        self.release.touch()
        for process in (first, invocation, exclusive):
            self.collect(process)
        self.assertEqual([r["name"] for r in self.records()][-1], "test:exclusive")

    def test_executor_crash_keeps_only_surviving_child_resources_reserved(self):
        self.parallel_profiles()
        first = self.start("build")
        self.queued(1)
        self.queue.resume()
        self.await_condition(lambda: len(self.records()) == 1)
        active = next(e for e in self.queue.status()["runs"] if e.get("kind") == "batch")
        progress = json.loads((Path(active["directory"]) / "progress.json").read_text())
        os.kill(progress["executor_pid"], signal.SIGKILL)
        self.collect(first, 2)
        second = self.start("unit")
        self.queued(1)
        third = self.start("lint")
        self.await_condition(lambda: len(self.records()) == 2)
        self.assertEqual([r["name"] for r in self.records()], ["build", "lint"])
        self.assertEqual(self.queue.status()["capacity"]["reserved"], 2)
        self.release.touch()
        self.collect(second)
        self.collect(third)

    def test_duplicate_gates_and_overlapping_stages_run_once_on_one_snapshot(self):
        first = self.start("check-fast")
        self.queued(1)
        second = self.start("check-fast")
        self.queued(2)
        third = self.start("lint")
        self.queued(3)
        (self.repo / "source.txt").write_text("captured at admission")
        self.queue.resume()
        for process in (first, second, third):
            self.collect(process)
        self.assertEqual([r["name"] for r in self.records()], ["build", "lint", "unit"])
        self.assertEqual({r["source"] for r in self.records()}, {"captured at admission"})
        self.assertEqual(len({r["source_tree"] for r in self.receipts()}), 1)
        self.assertEqual(len({r["batch_id"] for r in self.receipts()}), 1)

    def test_shared_package_union_reports_each_request_independently(self):
        first = self.start("test:digest", "--", "./good", "./shared")
        self.queued(1)
        second = self.start("test:digest", "--", "./bad", "./shared")
        self.queued(2)
        self.queue.resume()
        self.collect(first)
        self.collect(second, 1)
        self.assertEqual(self.records(), [{"packages": ["fixture/bad", "fixture/good", "fixture/shared"]}])
        self.assertEqual(sorted(r["status"] for r in self.receipts()), ["failed", "passed"])

    def test_queued_gate_uses_the_definition_in_the_captured_source(self):
        first = self.start("check-fast")
        self.queued(1)
        self.catalog["groups"]["check-fast"].append("extra")
        self.catalog["tasks"]["extra"] = ["extra"]
        self.save_catalog()
        self.queue.resume()
        self.collect(first)
        self.assertEqual([r["name"] for r in self.records()], ["build", "lint", "unit", "extra"])
        self.assertEqual([e["stage"] for e in self.receipts()[0]["evidence"]], ["build", "lint", "unit", "extra"])

    def test_missing_package_terminal_event_cannot_pass(self):
        process = self.start("test:digest", "--", "./good", env={**self.env, "FIXTURE_MISSING": "1"})
        self.queued(1)
        self.queue.resume()
        self.collect(process, 2)
        self.assertEqual(self.receipts()[0]["status"], "unverified")

    def test_failure_stops_only_affected_gates(self):
        env = {**self.env, "FIXTURE_FAIL": "build"}
        first = self.start("check-fast", env=env)
        self.queued(1)
        second = self.start("lint", env=env)
        self.queued(2)
        self.queue.resume()
        self.collect(first, 1)
        self.collect(second)
        self.assertEqual([r["name"] for r in self.records()], ["build", "lint"])
        failed = next(r for r in self.receipts() if r["status"] == "failed")
        self.assertEqual(failed["unverified_stages"], ["lint", "unit"])

    def test_cancelling_first_subscriber_preserves_shared_execution(self):
        self.hold.touch()
        first = self.start("build")
        self.queued(1)
        second = self.start("build")
        self.queued(2)
        self.queue.resume()
        self.await_condition(lambda: len(self.records()) == 1)
        first.terminate()
        self.collect(first, 130)
        self.assertIsNone(second.poll())
        self.release.touch()
        self.collect(second)
        self.assertEqual(len(self.records()), 1)

    def test_killed_subscriber_does_not_own_batch_lifetime(self):
        self.hold.touch()
        first = self.start("build")
        self.queued(1)
        second = self.start("build")
        self.queued(2)
        self.queue.resume()
        self.await_condition(lambda: len(self.records()) == 1)
        first.kill()
        self.collect(first, -signal.SIGKILL)
        self.release.touch()
        self.collect(second)
        self.assertEqual(len(self.records()), 1)

    def test_killed_stage_is_unverified(self):
        self.hold.touch()
        process = self.start("build")
        self.queued(1)
        self.queue.resume()
        self.await_condition(lambda: len(self.records()) == 1)
        os.kill(self.records()[0]["pid"], signal.SIGKILL)
        self.collect(process, 2)
        self.assertEqual(self.receipts()[0]["status"], "unverified")

    def test_late_submission_gets_a_new_snapshot(self):
        self.hold.touch()
        first = self.start("build")
        self.queued(1)
        self.queue.resume()
        self.await_condition(lambda: len(self.records()) == 1)
        (self.repo / "source.txt").write_text("later")
        second = self.start("build")
        self.release.touch()
        self.collect(first)
        self.collect(second)
        self.assertEqual([r["source"] for r in self.records()], ["initial", "later"])
        self.assertEqual(len({r["batch_id"] for r in self.receipts()}), 2)

    def test_snapshot_remains_stable_while_the_checkout_changes(self):
        self.hold.touch()
        first = self.start("check-fast")
        self.queued(1)
        self.queue.resume()
        self.await_condition(lambda: len(self.records()) == 1)
        (self.repo / "source.txt").write_text("edited during build")
        self.release.touch()
        self.collect(first)
        self.assertEqual({r["source"] for r in self.records()}, {"initial"})

    def test_killed_supervisor_keeps_descendants_admitted_until_completion(self):
        self.hold.touch()
        first = self.start("check-fast")
        self.queued(1)
        self.queue.resume()
        self.await_condition(lambda: len(self.records()) == 1)
        supervisor = next(e for e in self.queue.status()["runs"] if e.get("kind") == "batch")
        os.kill(supervisor["pid"], signal.SIGKILL)
        second = self.start("build")
        self.queued(2)
        status = batch.status_details(self.queue)
        run = next(e for e in status["runs"] if e["ticket"] == supervisor["ticket"])
        self.assertEqual(run["health"]["state"], "supervision_lost")
        operation = next(e for e in status["operations"] if e.get("child_pid") == self.records()[0]["pid"])
        self.assertEqual(operation["health"]["owner"], "present")
        self.assertEqual(len(self.records()), 1)
        self.release.touch()
        self.collect(first)
        self.collect(second)
        self.assertCountEqual([r["name"] for r in self.records()], ["build", "lint", "unit", "build"])
        gate = next(r for r in self.receipts() if r["names"] == ["check-fast"])
        self.assertEqual([e["stage"] for e in gate["evidence"]], ["build", "lint", "unit"])

    def test_capture_failure_is_unverified_and_queue_can_continue(self):
        (self.scripts / "test-source-snapshot.sh").write_text("#!/bin/bash\nexit 7\n")
        process = self.start("build")
        self.queued(1)
        self.queue.resume()
        self.collect(process, 2)
        self.await_condition(lambda: not self.queue.status()["runs"])
        self.assertEqual(self.receipts()[0]["status"], "unverified")
        self.assertEqual(self.records(), [])

    def test_executor_crash_retains_source_and_admission_for_surviving_stage(self):
        self.hold.touch()
        first = self.start("build")
        self.queued(1)
        self.queue.resume()
        self.await_condition(lambda: len(self.records()) == 1)
        active = next(e for e in self.queue.status()["runs"] if e.get("kind") == "batch")
        progress = json.loads((Path(active["directory"]) / "progress.json").read_text())
        source_path = Path(self.records()[0]["cwd"]) / "source.txt"
        os.kill(progress["executor_pid"], signal.SIGKILL)
        self.collect(first, 2)
        self.assertTrue(self.receipts()[0]["source_commit"])
        self.assertTrue(source_path.is_file())
        status = batch.status_details(self.queue)
        operation = next(e for e in status["operations"] if e.get("child_pid") == self.records()[0]["pid"])
        self.assertEqual(operation["health"]["state"], "supervision_lost")
        self.assertEqual(status["capacity"]["reserved"], 1)
        second = self.start("lint")
        self.queued(1)
        self.assertEqual(len(self.records()), 1)
        self.release.touch()
        self.collect(second)
        self.assertEqual([r["name"] for r in self.records()], ["build", "lint"])

    def test_queued_cancel_does_not_leave_a_batch_member(self):
        first = self.start("build")
        self.queued(1)
        second = self.start("build")
        self.queued(2)
        first.terminate()
        self.collect(first, 130)
        self.queued(1)
        self.queue.resume()
        self.collect(second)
        self.assertEqual(len(self.records()), 1)
        self.assertEqual(len(self.receipts()), 1)

    def test_completed_package_results_satisfy_later_stages(self):
        self.catalog["groups"]["check-fast"] = ["build", "test:digest"]
        self.save_catalog()
        first = self.start("test:digest", "--", "./...")
        self.queued(1)
        second = self.start("check-fast")
        self.queued(2)
        self.queue.resume()
        self.collect(first)
        self.collect(second)
        self.assertEqual(len([r for r in self.records() if "packages" in r]), 1)

    def test_different_declared_environment_is_a_fifo_barrier(self):
        first = self.start("build")
        self.queued(1)
        second = self.start("build", env={**self.env, "FIXTURE_INPUT": "changed"})
        self.queued(2)
        third = self.start("build")
        self.queued(3)
        self.queue.resume()
        for process in (first, second, third):
            self.collect(process)
        self.assertEqual(len(self.records()), 3)
        self.assertEqual(len({r["batch_id"] for r in self.receipts()}), 3)

    def test_agent_sessions_share_a_batch_and_never_reach_its_stages(self):
        task = self.repo / "task"
        task.write_text(task.read_text().replace(
            "'pid':os.getpid()}", "'pid':os.getpid(), 'session':os.environ.get('AGENT_SESSION_TOKEN')}"))
        first = self.start("build", env={**self.env, "AGENT_SESSION_TOKEN": "one"})
        self.queued(1)
        second = self.start("build", env={**self.env, "AGENT_SESSION_TOKEN": "two"})
        self.queued(2)
        self.queue.resume()
        self.collect(first)
        self.collect(second)
        self.assertEqual(self.records()[0]["session"], None)
        self.assertEqual(len(self.records()), 1)
        self.assertEqual(len({r["batch_id"] for r in self.receipts()}), 1)

    def test_all_subscribers_cancel_and_release_admission(self):
        self.hold.touch()
        first = self.start("build")
        self.queued(1)
        self.queue.resume()
        self.await_condition(lambda: len(self.records()) == 1)
        first.terminate()
        self.collect(first, 130)
        self.await_condition(lambda: not self.queue.status()["runs"])
        self.release.touch()
        self.collect(self.start("lint"))
        self.assertEqual([r["name"] for r in self.records()], ["build", "lint"])

    def test_retention_prunes_completed_artifacts_and_source_refs_together(self):
        self.queue.resume()
        for _ in range(3):
            self.collect(self.start("build"))
        self.await_condition(lambda: not self.queue.status()["runs"])
        directory = self.artifacts / "verification"
        self.assertEqual(len(list(directory.iterdir())), 3)
        oldest = min(directory.iterdir(), key=lambda p: (p / "finished.json").stat().st_mtime)
        with patch.object(batch, "MAX_RETAINED_BYTES", 1):
            with support.execution.Lease(self.queue, "retained batch", 1, {"directory": str(oldest.resolve())}):
                batch.prune(self.queue, self.repo)
                self.assertTrue(oldest.exists())
                self.assertEqual(len(list(directory.iterdir())), 2)
            batch.prune(self.queue, self.repo)
        self.assertEqual(len(list(directory.iterdir())), 1)
        self.assertEqual(len(self.git("for-each-ref", "--format=%(refname)", "refs/verification/batches").splitlines()), 1)
