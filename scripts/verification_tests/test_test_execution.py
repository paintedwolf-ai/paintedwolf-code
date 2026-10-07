"""Behavioral coverage for shared verification admission."""

import importlib.util
import os
from pathlib import Path
import signal
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[1].joinpath("test-execution.py")
spec = importlib.util.spec_from_file_location("test_execution", SCRIPT)
execution = importlib.util.module_from_spec(spec)
spec.loader.exec_module(execution)
import verification_resources as resources  # importable once test-execution.py extends the path


class TaskAdmissionTests(unittest.TestCase):
    def test_task_option_values_cannot_bypass_admission(self):
        self.assertTrue(execution.scheduled_task(["--output-group-begin", "--dry", "check"]))
        self.assertTrue(execution.scheduled_task(["-n", "--dry=false", "check"]))
        self.assertFalse(execution.scheduled_task(["-vn", "check"]))
        for arguments in (["--unknown", "check"], ["--output"], ["--dry=maybe", "check"],
                          ["-Crazy", "check"]):
            with self.subTest(arguments=arguments), self.assertRaises(ValueError):
                execution.scheduled_task(arguments)

    def test_task_serialization_preserves_literal_option_values(self):
        self.assertEqual(execution.serial_task_arguments(
            ["-C8", "--output-group-begin", "-C", "--output-group-end=--dry", "check"]),
            ["--concurrency", "1", "--output-group-begin", "-C", "--output-group-end", "--dry", "check"])

    def test_controls_and_information_do_not_queue(self):
        for args in (["test:pause"], ["test:resume"], ["test:status"], ["--list"],
                     ["--summary", "check"], ["--dry", "test:full"], ["den:dev"]):
            with self.subTest(args=args):
                self.assertFalse(execution.scheduled_task(args))

    def test_selection_validation_handles_task_options_and_variables(self):
        for args in (["den:harness:test", "--", "--workers=1"],
                     ["--output", "group", "den:harness:test", "--", "--workers=1"],
                     ["den:harness:test", "MODE=fixture", "--", "--workers=1"]):
            with self.subTest(args=args), self.assertRaisesRegex(ValueError, "unsupported flag"):
                execution.validate_task_selection(args)

    def test_flag_probes_never_become_a_full_suite(self):
        for args in (["test:integration", "--", "--help"], ["check", "--", "-h"],
                     ["test:digest", "--", "--help"], ["den:harness:test", "--", "--version"],
                     ["lint:full", "--", "--help"]):
            with self.subTest(args=args), self.assertRaisesRegex(ValueError, "No verification was queued"):
                execution.validate_task_selection(args)

    def test_targets_that_would_drop_their_arguments_refuse_them(self):
        for args in (["check-fast", "--", "-run", "TestFixture"], ["check", "--", "./internal/api"],
                     ["build", "--", "./..."], ["den:test", "--", "filter"]):
            with self.subTest(args=args), self.assertRaisesRegex(ValueError, "takes no selection arguments"):
                execution.validate_task_selection(args)

    def test_a_declared_go_target_scopes_inside_its_own_packages(self):
        execution.validate_task_selection(["test:integration", "--", "./internal/api/..."])
        execution.validate_task_selection(["test:contract", "--", "-run", "TestFixture"])
        for args in (["test:integration", "--", "./test/security/..."], ["test:oar", "--", "./internal/api"]):
            with self.subTest(args=args), self.assertRaisesRegex(ValueError, "lies outside it"):
                execution.validate_task_selection(args)

    def test_declared_selection_targets_keep_their_grammar(self):
        for args in (["test:digest", "--", "./internal/db", "-run", "TestFixture"],
                     ["den:test:digest", "--", "src/shell-contract.test.ts"],
                     ["den:harness:test", "--", "--grep", "fixture"],
                     ["lint:full", "--", "--timeout=40m"],
                     ["oar:conformance", "--", "--json"],
                     ["den:bundle", "--", "--debug"]):
            with self.subTest(args=args):
                execution.validate_task_selection(args)

    def test_heavy_tasks_queue(self):
        for args in ([], ["check"], ["check-fast"], ["test:digest", "--", "./internal/db"],
                     ["den:test"], ["lint:full"], ["build"], ["perf:bench"],
                     ["--output", "group", "test:runner"], ["--dry=false", "test:full"]):
            with self.subTest(args=args):
                self.assertTrue(execution.scheduled_task(args))

    def test_release_verification_and_packaging_acquire_admission(self):
        for task in ("den:bundle", "den:stage-engine", "bundle:verify", "bundle:smoke",
                     "upgrade:corpus:seed", "upgrade:corpus:prepare", "upgrade:corpus:boot",
                     "upgrade:rehearse", "release:preflight"):
            with self.subTest(task=task):
                self.assertTrue(execution.scheduled_task([task]))
                self.assertTrue(execution.scheduled_task([task, "--", "--debug"]))
                self.assertFalse(execution.scheduled_task(["--dry", task]))
                self.assertFalse(execution.scheduled_task(["--summary", task]))

    def test_local_app_build_does_not_queue(self):
        for args in (["den:app"], ["den:app", "--", "--debug"],
                     ["den:app", "--", "--no-devtools"], ["den:app", "--", "--open"],
                     ["den:app", "--", "--debug", "--open", "--no-devtools"],
                     ["--output", "group", "den:app", "--", "--open"]):
            with self.subTest(args=args):
                self.assertFalse(execution.scheduled_task(args))

    def test_app_exception_keeps_other_checks_queued(self):
        for args in (["den:app", "check"], ["den:app", "check", "--", "--open"],
                     ["den:app", "den:test", "--", "--open"],
                     ["den:bundle", "--", "--open"]):
            with self.subTest(args=args):
                self.assertTrue(execution.scheduled_task(args))

    def test_interactive_harness_only_queues_preparation(self):
        self.assertFalse(execution.scheduled_task(["den:harness"]))
        self.assertFalse(execution.scheduled_task(["--output", "group", "den:harness"]))
        for args in (["den:harness", "--", "--prepare"], ["den:harness:test"],
                     ["den:harness:canary"], ["den:harness:test", "--", "--grep", "fixture"]):
            with self.subTest(args=args):
                self.assertTrue(execution.scheduled_task(args))
        for args in (["den:harness", "check"], ["check", "den:harness"],
                     ["den:harness", "den:app"]):
            with self.subTest(args=args), self.assertRaises(ValueError):
                execution.scheduled_task(args)

    def test_control_cannot_share_a_task_invocation(self):
        with self.assertRaises(ValueError):
            execution.scheduled_task(["test:pause", "check"])

    def test_live_evaluation_does_not_hold_verification_admission(self):
        self.assertFalse(execution.scheduled_task(["eval:tool-usage", "BENCHMARK=run"]))
        self.assertFalse(execution.scheduled_task(["eval:tool-usage", "BENCHMARK=report"]))
        self.assertTrue(execution.scheduled_task(["eval:tool-usage", "BENCHMARK=test"]))
        self.assertTrue(execution.scheduled_task(["eval:tool-usage", "BENCHMARK=prepare"]))
        self.assertTrue(execution.scheduled_task(["eval:tool-usage", "BENCHMARK=smoke"]))
        self.assertTrue(execution.scheduled_task(["eval:tool-usage", "BUILD_ONLY=true"]))
        self.assertTrue(execution.scheduled_task(["build", "eval:tool-usage", "BENCHMARK=run"]))

    def test_task_concurrency_cannot_multiply_the_budget(self):
        self.assertEqual(execution.serial_task_arguments(
            ["--concurrency=8", "--parallel", "test:digest", "--", "-C", "fixture"]),
            ["--concurrency", "1", "--parallel", "test:digest", "--", "-C", "fixture"])

    def test_worker_limits_preserve_smaller_requests_and_other_flags(self):
        env = execution.bounded_environment({"GO_TEST_P": "99", "GOMAXPROCS": "1",
                                             "GOFLAGS": "-tags=integration -p 99"}, 4)
        self.assertEqual(env["GO_TEST_P"], "4")
        self.assertEqual(env["GO_TEST_PARALLEL"], "1")
        self.assertEqual(env["GOMAXPROCS"], "1")
        self.assertEqual(env["PW_VITEST_MAX_WORKERS"], "4")
        self.assertEqual(env["GOFLAGS"], "-tags=integration -p=4")

    def test_go_budget_favors_package_fanout_and_uses_small_scope_capacity(self):
        broad = execution.bounded_environment({}, 6)
        self.assertEqual((broad["GO_TEST_P"], broad["GOMAXPROCS"]), ("6", "1"))
        scoped = execution.bounded_environment({"GO_TEST_P": "1"}, 3)
        self.assertEqual((scoped["GO_TEST_P"], scoped["GOMAXPROCS"]), ("1", "3"))
        explicit = execution.bounded_environment({"GOMAXPROCS": "2"}, 6)
        self.assertEqual((explicit["GO_TEST_P"], explicit["GOMAXPROCS"]), ("3", "2"))

    def test_default_budget_reserves_host_capacity_and_caps_large_hosts(self):
        for cpus, workers in ((None, 1), (1, 1), (2, 1), (4, 3), (8, 6), (16, 8)):
            with self.subTest(cpus=cpus), patch.object(os, "cpu_count", return_value=cpus), \
                    patch.dict(os.environ, {"PW_TEST_HOST": "shared"}):
                self.assertEqual(execution.worker_budget({}), workers)
                self.assertEqual(resources.demand({"workers": "shared"}, 8), max(1, workers // 2))

    def test_dedicated_host_uses_every_cpu_for_shared_operations(self):
        for cpus, workers in ((1, 1), (3, 3), (4, 4), (16, 8)):
            with self.subTest(cpus=cpus), patch.object(os, "cpu_count", return_value=cpus), \
                    patch.dict(os.environ, {"PW_TEST_HOST": "dedicated"}):
                self.assertEqual(execution.worker_budget({}), workers)
                self.assertEqual(resources.demand({"workers": "shared"}, 8), workers)
                self.assertEqual(resources.demand({"workers": 1}, 8), 1)

    def test_worker_limits_bound_oversized_requests_across_runners(self):
        names = ("GO_TEST_P", "GO_TEST_PARALLEL", "GOMAXPROCS", "PW_VITEST_MAX_WORKERS",
                 "PW_FUZZ_WORKERS", "CARGO_BUILD_JOBS", "RUST_TEST_THREADS")
        for workers in range(1, 9):
            with self.subTest(workers=workers):
                env = execution.bounded_environment(dict.fromkeys(names, "99"), workers)
                self.assertLessEqual(int(env["GO_TEST_P"]) * int(env["GOMAXPROCS"]), workers)
                self.assertLessEqual(int(env["GO_TEST_P"]) * int(env["GO_TEST_PARALLEL"]), workers)
                for name in names:
                    self.assertLessEqual(int(env[name]), workers)
                reduced = execution.bounded_environment(dict.fromkeys(names, "1"), workers)
                for name in names:
                    self.assertEqual(reduced[name], "1")

    def test_worker_budget_rejects_invalid_values(self):
        with patch.object(os, "cpu_count", return_value=8), patch.dict(os.environ, {"PW_TEST_HOST": "shared"}):
            for value in ("0", "-1", "7", "8", "9", "many"):
                with self.subTest(value=value), self.assertRaises(ValueError):
                    execution.worker_budget({"PW_TEST_WORKERS": value})

    def test_go_arguments_and_replayed_environment_obey_current_budget(self):
        env, args = execution.bounded_go_arguments(
            ["-p", "99", "./internal/db", "-parallel=99", "-run", "TestFixture", "-args", "-parallel=77"],
            {"PW_TEST_WORKERS": "1", "GO_TEST_P": "99", "GOMAXPROCS": "99"})
        self.assertEqual(env["GOMAXPROCS"], "1")
        self.assertEqual(args, ["-p=1", "./internal/db", "-parallel=1", "-run", "TestFixture", "-args", "-parallel=77"])

    def test_go_arguments_preserve_lower_concurrency(self):
        _, args = execution.bounded_go_arguments(["-p=1", "-test.parallel", "8"], {"PW_TEST_WORKERS": "1"})
        self.assertEqual(args, ["-p=1", "-test.parallel=1"])

    def test_selection_patterns_are_not_worker_flags(self):
        args = ["-run", "-parallel", "-bench", "-p", "-skip", "-args"]
        _, bounded = execution.bounded_go_arguments(args, {"PW_TEST_WORKERS": "1"})
        self.assertEqual(bounded, args)


class ExecutionLifecycleTests(unittest.TestCase):
    @unittest.skipIf(os.name == "nt", "requires POSIX file descriptions")
    def test_exclusive_cleanup_preserves_an_inherited_lease(self):
        retained = None
        try:
            with execution.Lease(self.queue, "inherited fixture", 1) as lease:
                lease.entry["state"] = "running"
                lease.write()
                retained = os.dup(lease.file.fileno())
            self.assertTrue(lease.path.exists())
            self.assertEqual(len(self.queue.status()["runs"]), 1)
        finally:
            if retained is not None:
                os.close(retained)
        self.assertEqual(self.queue.status()["runs"], [])
        self.assertFalse(lease.path.exists())

    def test_wait_notices_do_not_repeat_and_health_sampling_continues(self):
        self.queue.pause("fixture")
        with execution.Lease(self.queue, "fixture", 1, {"kind": "invocation"}) as lease, \
                execution.Reservation(self.queue, execution.lock_file, lease.entry["ticket"], "fixture",
                                      execution.EXCLUSIVE, 1, execution.order_of(lease.entry)) as reservation:
            sleeps = 0

            def advance(_seconds):
                nonlocal sleeps
                sleeps += 1
                if sleeps == 3:
                    self.queue.resume()

            with patch.object(execution.time, "sleep", side_effect=advance), \
                    patch.object(execution.time, "monotonic", side_effect=[100, 100, 131, 131, 162, 162]), \
                    patch.object(execution, "report") as report, patch("builtins.print") as output:
                lease.acquire(reservation)
            self.assertEqual(lease.entry["state"], "running")
            self.assertEqual(reservation.entry["state"], "running")
            self.assertEqual(report.call_count, 3)
            self.assertEqual(output.call_count, 2)
        self.assertEqual(self.queue.status()["runs"], [])

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
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
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            if predicate():
                return
            time.sleep(0.01)
        self.fail("execution state did not settle")

    @unittest.skipIf(os.name == "nt", "harness reservations use POSIX descriptor inheritance")
    def test_harness_child_retains_reservation_after_supervisor_crash(self):
        import fcntl
        marker = self.root / "harness-child"
        runner = self.root / "runner.sh"
        runner.write_text('exec "$TEST_PYTHON" "$TEST_CHILD"\n')
        script = self.root / "child.py"
        script.write_text('''import os, time
from pathlib import Path
os.fstat(int(os.environ['PW_TEST_RESOURCE_FD']))
Path(os.environ['TEST_MARKER']).write_text(str(os.getpid()))
while True: time.sleep(.1)
''')
        lease = self.root / "reservation"
        child_pid = None
        with lease.open("w+") as held:
            execution.lock_file(held)
            descriptor = fcntl.fcntl(held.fileno(), fcntl.F_DUPFD, 201)
        env = {**self.env, "PW_TEST_RESOURCE_FD": str(descriptor), "TEST_PYTHON": sys.executable,
               "TEST_CHILD": str(script), "TEST_MARKER": str(marker),
               "LYCAON_E2E_STATE_DIR": str(self.root / "state"), "LYCAON_HARNESS_TIMEOUT_SECONDS": "15"}
        supervisor = subprocess.Popen([sys.executable, str(SCRIPT.parent / "harness/supervise.py"), str(runner)],
                                      env=env, pass_fds=(descriptor,), stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        os.close(descriptor)
        try:
            self.await_state(marker.exists)
            child_pid = int(marker.read_text())
            supervisor.kill()
            supervisor.wait(timeout=5)
            with lease.open("r+") as probe:
                with self.assertRaises(BlockingIOError):
                    execution.lock_file(probe, blocking=False)
        finally:
            if supervisor.poll() is None:
                supervisor.terminate()
            if child_pid is not None:
                try:
                    os.kill(child_pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
            supervisor.communicate(timeout=10)
        def released():
            with lease.open("r+") as probe:
                try:
                    execution.lock_file(probe, blocking=False)
                    return True
                except BlockingIOError:
                    return False
        self.await_state(released)

    def test_app_build_runs_while_queue_is_paused(self):
        self.queue.pause("fixture pause")
        task = self.root / "task.py"
        task.write_text("import sys\nprint(repr(sys.argv[1:]))\n")
        for arguments in (["den:app"], ["den:app", "--", "--debug", "--open"]):
            with self.subTest(arguments=arguments):
                result = subprocess.run(
                    [sys.executable, str(SCRIPT), "task", "--", sys.executable, str(task), *arguments],
                    env=self.env, capture_output=True, text=True, timeout=5)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout.strip(), repr(arguments))
        status = self.queue.status()
        self.assertEqual(status["paused"]["reason"], "fixture pause")
        self.assertEqual(status["runs"], [])

    def harness_fixture(self):
        scripts = self.root / "scripts"
        (scripts / "harness").mkdir(parents=True)
        (scripts / "e2e").mkdir()
        for name in ("den-harness.sh", "harness/lib.sh"):
            shutil.copy2(SCRIPT.parent / name, scripts / name)
        (scripts / "e2e/env.sh").write_text(":\n")
        with (scripts / "harness/lib.sh").open("a") as lib:
            lib.write('\n_harness_seed_project() { :; }\nharness_banner() { :; }\n')
        (scripts / "e2e-sidecar-build.sh").write_text(
            'set -eu\n"$TEST_PYTHON" "$TEST_EXECUTION" holding\n'
            'echo built > "$TEST_ROOT/built"\nexit "${TEST_BUILD_STATUS:-0}"\n')
        (scripts / "e2e-sidecar-bg.sh").write_text(
            'set -eu\n[[ "$1" == "--reuse-built" ]]\n[[ -f "$TEST_ROOT/built" ]]\n'
            'if "$TEST_PYTHON" "$TEST_EXECUTION" holding; then exit 92; fi\n')
        (scripts / "e2e-vite.sh").write_text(
            'echo ready > "$TEST_ROOT/ready"\n'
            'while [[ ! -f "$TEST_ROOT/finish" ]]; do sleep 0.05; done\n')
        task = self.root / "fixture-task"
        task.write_text('#!/usr/bin/env bash\nset -eu\nshift 2\n'
                        'if [[ "${1:-}" == "--dry" ]]; then echo "task: [den:harness] fixture $*" >&2; exit 0; fi\n'
                        'if [[ "${1:-}" == "--concurrency" ]]; then shift 2; fi\n'
                        '[[ "$1" == "den:harness" ]]\nshift\n'
                        'if [[ "${1:-}" == "--" ]]; then shift; else set -- --supervised; fi\n'
                        'exec bash "$TEST_ROOT/scripts/den-harness.sh" "$@"\n')
        task.chmod(0o700)
        wrapper = self.root / "task"
        wrapper.write_text('#!/usr/bin/env bash\nexec "$TEST_PYTHON" "$TEST_EXECUTION" task -- '
                           '"$TEST_TASK" "$TEST_ROOT" "$@"\n')
        wrapper.chmod(0o700)
        self.env.update(TEST_PYTHON=sys.executable, TEST_EXECUTION=str(SCRIPT),
                        TEST_TASK=str(task), TEST_ROOT=str(self.root),
                        LYCAON_E2E_STATE_DIR=str(self.root / "state"),
                        LYCAON_E2E_ADDR="127.0.0.1:1", LYCAON_E2E_VITE_PORT="2",
                        LYCAON_E2E_API_URL="http://127.0.0.1:1", LYCAON_E2E_TOKEN="fixture",
                        LYCAON_LLM_MOCK="1", LYCAON_LLM_MANUAL="0", LYCAON_HARNESS_REAL="0",
                        LYCAON_HARNESS_MODEL="", TMPDIR=str(self.root))
        finish = self.root / "finish"
        process = subprocess.Popen([str(wrapper), "den:harness"], env=self.env,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
                                   start_new_session=True)
        def cleanup():
            finish.touch()
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGTERM)
            process.communicate(timeout=10)
        self.addCleanup(cleanup)
        return process

    def test_harness_preparation_waits_then_releases_queue_before_interaction(self):
        self.queue.pause("fixture preparation pause")
        harness = self.harness_fixture()
        self.await_state(lambda: len(self.queue.status()["runs"]) == 1)
        self.assertFalse((self.root / "built").exists())
        self.assertFalse((self.root / "ready").exists())
        self.queue.resume()
        self.await_state(lambda: (self.root / "ready").exists() or harness.poll() is not None)
        if harness.poll() is not None:
            _, err = harness.communicate(timeout=5)
            self.fail(f"harness exited before interaction: {err}")
        self.assertEqual(self.queue.status()["runs"], [])
        following = self.start("following", "print('verified')")
        _, err = following.communicate(timeout=5)
        self.assertEqual(following.returncode, 0, err)
        self.queue.pause("fixture interaction pause")
        self.assertIsNone(harness.poll())
        (self.root / "finish").touch()
        _, err = harness.communicate(timeout=10)
        self.assertEqual(harness.returncode, 0, err)

    def test_failed_harness_preparation_does_not_start_interaction(self):
        self.env["TEST_BUILD_STATUS"] = "7"
        harness = self.harness_fixture()
        _, err = harness.communicate(timeout=10)
        self.assertEqual(harness.returncode, 7, err)
        self.assertFalse((self.root / "ready").exists())
        self.assertEqual(self.queue.status()["runs"], [])

    def test_pause_survives_queue_recreation_and_resume_releases_work(self):
        self.queue.pause("fixture pause")
        process = self.start("paused", "print('ran')")
        self.await_state(lambda: len(self.queue.status()["runs"]) == 1)
        self.assertIsNone(process.poll())
        other = execution.Queue(self.queue.root)
        self.assertEqual(other.status()["paused"]["reason"], "fixture pause")
        other.resume()
        out, err = process.communicate(timeout=5)
        self.assertEqual(process.returncode, 0, err)
        self.assertEqual(out.strip(), "ran")

    def test_waiters_run_in_fifo_order(self):
        self.queue.pause("queue fixture")
        result = self.root / "order"
        processes = []
        for index in range(3):
            processes.append(self.start(str(index),
                f"from pathlib import Path; p=Path({str(result)!r}); p.open('a').write('{index}\\n')"))
            self.await_state(lambda: len(self.queue.status()["runs"]) == index + 1)
        self.queue.resume()
        for process in processes:
            _, err = process.communicate(timeout=5)
            self.assertEqual(process.returncode, 0, err)
        self.assertEqual(result.read_text(), "0\n1\n2\n")

    def test_cancelled_waiter_does_not_block_following_work(self):
        self.queue.pause("cancel fixture")
        process = self.start("cancel", "raise AssertionError('must not run')")
        self.await_state(lambda: len(self.queue.status()["runs"]) == 1)
        process.terminate()
        process.communicate(timeout=5)
        self.assertEqual(self.queue.status()["runs"], [])
        self.queue.resume()
        following = self.start("following", "print('ok')")
        out, err = following.communicate(timeout=5)
        self.assertEqual(following.returncode, 0, err)
        self.assertEqual(out.strip(), "ok")

    def test_active_run_excludes_another_run(self):
        ready = self.root / "ready"
        release = self.root / "release"
        first = self.start("first", f"import time; from pathlib import Path; Path({str(ready)!r}).touch()\n"
                           f"while not Path({str(release)!r}).exists(): time.sleep(.01)")
        self.await_state(ready.exists)
        second = self.start("second", "print('second')")
        self.await_state(lambda: len(self.queue.status()["runs"]) == 2)
        self.assertEqual([entry["state"] for entry in self.queue.status()["runs"]], ["running", "queued"])
        release.touch()
        first.communicate(timeout=5)
        out, err = second.communicate(timeout=5)
        self.assertEqual(second.returncode, 0, err)
        self.assertEqual(out.strip(), "second")

    def test_nested_run_reuses_admission(self):
        command = [sys.executable, str(SCRIPT), "run", "--name", "nested", "--", sys.executable, "-c", "print('nested')"]
        process = self.start("outer", f"import subprocess; subprocess.run({command!r}, check=True)")
        out, err = process.communicate(timeout=5)
        self.assertEqual(process.returncode, 0, err)
        self.assertEqual(out.strip(), "nested")
        self.assertEqual(err.count("admitted"), 1)

    @unittest.skipIf(os.name == "nt", "requires inherited POSIX file locks")
    def test_child_keeps_admission_after_supervisor_is_killed(self):
        ready = self.root / "ready"
        release = self.root / "release"
        first = self.start("interrupted supervisor",
            f"import time; from pathlib import Path; Path({str(ready)!r}).touch()\n"
            f"while not Path({str(release)!r}).exists(): time.sleep(.01)")
        self.addCleanup(release.touch)
        self.await_state(ready.exists)
        first.kill()
        first.wait(timeout=5)
        from verification_batch import status_details
        status = status_details(self.queue)
        orphan = next(entry for entry in status["runs"] if entry["name"] == "interrupted supervisor")
        self.assertEqual(orphan["health"]["state"], "supervision_lost")
        self.assertEqual(orphan["health"]["action"], "none")
        second = self.start("following", "print('following')")
        self.await_state(lambda: len(self.queue.status()["runs"]) == 2)
        self.assertIsNone(second.poll())
        release.touch()
        first.communicate(timeout=5)
        out, err = second.communicate(timeout=5)
        self.assertEqual(second.returncode, 0, err)
        self.assertEqual(out.strip(), "following")

    def test_unlocked_abandoned_lease_is_collected(self):
        (self.queue.root / "abandoned.lease").write_text("interrupted write")
        self.assertEqual(self.queue.status()["runs"], [])

    def test_child_failure_releases_admission_and_preserves_exit_code(self):
        process = self.start("failure", "raise SystemExit(7)")
        process.communicate(timeout=5)
        self.assertEqual(process.returncode, 7)
        self.assertEqual(self.queue.status()["runs"], [])

    @unittest.skipIf(os.name == "nt", "requires POSIX signal handlers")
    def test_cancelling_active_run_terminates_its_child(self):
        ready = self.root / "ready"
        stopped = self.root / "stopped"
        process = self.start("cancel active",
            "import signal, time; from pathlib import Path\n"
            f"def stop(*_): Path({str(stopped)!r}).touch(); raise SystemExit(0)\n"
            "signal.signal(signal.SIGTERM, stop)\n"
            f"Path({str(ready)!r}).touch()\n"
            "while True: time.sleep(.01)")
        self.await_state(ready.exists)
        process.terminate()
        process.communicate(timeout=5)
        self.assertEqual(process.returncode, 130)
        self.assertTrue(stopped.exists())
        self.assertEqual(self.queue.status()["runs"], [])

    def test_environment_token_alone_cannot_reuse_admission(self):
        with execution.Lease(self.queue, "fixture", 1) as lease:
            lease.entry.update(state="running", pid=987654321)
            lease.write()
            with patch.dict(os.environ, {"PW_TEST_EXECUTION_TICKET": lease.entry["ticket"]}):
                self.assertFalse(self.queue.inherited())


if __name__ == "__main__":
    unittest.main()
