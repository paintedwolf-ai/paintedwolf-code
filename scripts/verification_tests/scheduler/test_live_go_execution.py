import json
import os
from pathlib import Path
import shutil
import subprocess
import time

import verification_batch as batch
import verification_resources as resources
from verification_tests import support

SCRIPT = support.SCRIPT


class LiveGoBatchTests(support.BatchFixture):
    # Real source capture, compiler startup, and publication share this deadline.
    state_timeout = 60

    def setUp(self):
        super().setUp()
        for name in ["task.sh", "analysis_tools.py", "go-test-digest.sh", "go-test-exec.py", "test-digest.py",
                     "test-run-manifest.py", "process-group-watchdog.py",
                     "verification_timings.py"]:
            shutil.copy2(SCRIPT.with_name(name), self.scripts / name)
        shutil.copytree(SCRIPT.parent / "ci_policy", self.scripts / "ci_policy",
                        ignore=shutil.ignore_patterns("__pycache__"))
        # The fixture has no document core to build; digests only need the export.
        (self.scripts / "document-core-env.sh").write_text(
            "#!/usr/bin/env bash\nexport LYCAON_DOCUMENT_CORE_BINARY=/nonexistent/document-core\n")
        shutil.copy2(SCRIPT.parent.parent / "task", self.repo / "task")
        from artifact_paths import bin_dir
        task_src = bin_dir(SCRIPT.parent.parent) / "task"
        bin_dir_target = self.root / "bin"
        bin_dir_target.mkdir(parents=True, exist_ok=True)
        shutil.copy2(task_src, bin_dir_target / "task")
        self.env["PW_BIN_DIR"] = str(bin_dir_target)
        (self.tools / "go").unlink()
        self.env["PATH"] = os.environ["PATH"]
        # A high fixture budget protects the enclosing runner's shared cache.
        self.env["GOCACHE"] = os.environ.get("GOCACHE") or subprocess.check_output(
            ["go", "env", "GOCACHE"], text=True).strip()
        self.env.update(GOCACHE_MAX_GIB="1048576", GOCACHE_TARGET_GIB="1048576")
        version = next(line for line in (SCRIPT.parent.parent / "lycaon" / "go.mod").read_text().splitlines()
                       if line.startswith("go "))
        (self.repo / "lycaon" / "go.mod").write_text("module fixture\n\n" + version + "\n")
        (self.repo / "Taskfile.yml").write_text('''version: '3'
tasks:
  test:digest:
    cmds:
      - python3 scripts/test-execution.py plan -- test:digest -- {{.CLI_ARGS}}
''')
        for name in ("good", "bad", "shared"):
            package = self.repo / "lycaon" / name
            package.mkdir(exist_ok=True)
            body = 't.Fatal("intentional batch fixture failure")' if name == "bad" else ""
            (package / "fixture_test.go").write_text('package ' + name + '\nimport "testing"\n' +
                                                     'func TestFixture(t *testing.T) { ' + body + ' }\n')

    def start(self, *names, env=None):
        process = subprocess.Popen([str(self.repo / "task"), *names], cwd=self.repo, env=env or self.env,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        self.processes.append(process)
        return process

    def test_every_independently_failing_package_reaches_the_receipt_and_annotations(self):
        self.catalog["go"]["test:digest"]["report_all_failures"] = True
        self.save_catalog()
        # Started later than the first failure's report, so termination would lose this outcome.
        slow = self.repo / "lycaon" / "slowbad"
        slow.mkdir(exist_ok=True)
        (slow / "fixture_test.go").write_text(
            'package slowbad\nimport ("testing"; "time")\n'
            'func TestSlow(t *testing.T) { time.Sleep(3 * time.Second); t.Fatal("second independent failure") }\n')
        self.git("add", ".")
        self.git("-c", "user.name=Verification fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "Second failure")
        run = self.start("test:digest", "--", "./bad", "./slowbad", "./good")
        self.queued(1)
        self.queue.resume()
        self.collect(run, 1)
        receipt = self.receipts()[0]
        results = receipt["evidence"][0]["results"]
        self.assertEqual(results["fixture/bad"]["status"], "failed")
        self.assertEqual(results["fixture/slowbad"]["status"], "failed")
        self.assertEqual(results["fixture/slowbad"]["tests"], ["TestSlow"])
        self.assertEqual(results["fixture/good"]["status"], "passed")
        directory = self.artifacts / "verification" / receipt["batch_id"]
        events = [json.loads(line) for line in (directory / "stage-001-events.jsonl").read_text().splitlines()]
        self.assertFalse([event for event in events if event["exit_code"] < 0])
        from ci_policy.verification_report import annotation, failures
        annotations = [annotation(failure) for failure in failures(self.artifacts)]
        self.assertIn("::error title=test:digest failed::fixture/bad: TestFixture", annotations)
        self.assertIn("::error title=test:digest failed::fixture/slowbad: TestSlow", annotations)

    def test_real_task_snapshot_go_digest_and_per_request_outcomes(self):
        first = self.start("test:digest", "--", "./good", "./shared")
        self.queued(1)
        second = self.start("test:digest", "--", "./bad", "./shared")
        self.queued(2)
        started = time.monotonic()
        self.queue.resume()
        self.collect(first)
        self.collect(second, 1)
        receipts = self.receipts()
        self.assertEqual(sorted(r["status"] for r in receipts), ["failed", "passed"])
        self.assertEqual(len({r["source_commit"] for r in receipts}), 1)
        directory = self.artifacts / "verification" / receipts[0]["batch_id"]
        stages = json.loads((directory / "stages.json").read_text())
        self.assertEqual(len(stages), 1)
        self.assertEqual(stages[0]["packages"], ["fixture/bad", "fixture/good", "fixture/shared"])
        report = json.loads((directory / "stage-001.json").read_text())
        self.assertTrue(report["completed"])
        self.assertEqual(report["packages"]["fixture/shared"], "pass")
        events = [json.loads(line) for line in (directory / "stage-001-go.json").read_text().splitlines() if line.startswith("{")]
        self.assertEqual(sum(e.get("Package") == "fixture/shared" and e.get("Action") == "start" for e in events), 1)
        self.assertEqual(report["invocation"]["environment"]["GO_TEST_P"], "1")
        self.assertTrue((self.artifacts / "last-run" / "latest-go-failure").exists())
        print(f"Live batch smoke: 2 requests, 1 Go invocation, 3 packages, independent pass/fail, "
              f"1 worker, {time.monotonic() - started:.1f}s")

    def test_raw_go_progress_is_visible_before_the_digest_finishes(self):
        (self.repo / "lycaon" / "good" / "fixture_test.go").write_text('''package good
import ("os"; "testing"; "time")
func TestFixture(t *testing.T) {
 deadline := time.Now().Add(60*time.Second)
 for {
  if _, err := os.Stat(os.Getenv("FIXTURE_RELEASE")); err == nil { return }
  if time.Now().After(deadline) { t.Fatal("await release: fixture deadline exceeded") }
  time.Sleep(10*time.Millisecond)
 }
}
''')
        first = self.start("test:digest", "--", "./good")
        self.queued(1)
        self.queue.resume()
        self.await_condition(lambda: any(e.get("kind") == "batch" for e in self.queue.status()["runs"]))
        active = next(e for e in self.queue.status()["runs"] if e.get("kind") == "batch")
        directory = Path(active["directory"])
        raw = directory / "stage-001-go.json"
        self.await_condition(lambda: raw.exists() and '"Action":"run"' in raw.read_text())
        self.assertIsNone(first.poll())
        self.assertFalse((directory / "stage-001.json").exists())
        status = batch.status_details(self.queue)
        operation = next(e for e in status["operations"] if str(raw) in e.get("activity_paths", []))
        self.assertEqual(operation["health"]["child"], "present")
        self.release.touch()
        self.collect(first)
        self.assertIn('"Action":"pass"', raw.read_text())

    def test_a_later_run_fails_when_what_a_child_process_read_changed(self):
        """Go's result cache cannot see an input only a child process reads, in a shared stage or in
        an exclusive one (`-failfast` runs outside the batch, without the exec wrapper)."""
        package = self.repo / "lycaon" / "good"
        (package / "testdata").mkdir()
        value = package / "testdata" / "value.txt"
        (package / "fixture_test.go").write_text('''package good
import ("os/exec"; "testing")
func TestFixture(t *testing.T) {
 out, err := exec.Command("cat", "testdata/value.txt").Output()
 if err != nil || string(out) != "one" { t.Fatalf("read: %q %v", out, err) }
}
''')
        self.queue.resume()

        def verify(*flags):
            process = self.start("test:digest", "--", *flags, "./good")
            out, err = process.communicate(timeout=self.state_timeout + 5)
            self.await_condition(lambda: not self.queue.status()["runs"])
            return process.returncode, out + err
        for flags in ((), ("-failfast",)):
            with self.subTest(flags=flags):
                value.write_text("one")
                code, output = verify(*flags)
                self.assertEqual(code, 0, output)
                value.write_text("two")
                code, output = verify(*flags)
                self.assertNotEqual(code, 0, output)
                self.assertIn('read: "two"', output)

    def test_an_exclusive_target_runs_again_whatever_its_sources_checksum_says(self):
        """Task skips a target whose `sources:` are unchanged, even when its arguments now select a failure."""
        with (self.repo / "Taskfile.yml").open("a") as taskfile:
            taskfile.write('''  fixture:sourced:
    sources: [source.txt]
    cmds:
      - test "{{.CLI_ARGS}}" != fail
''')
        self.catalog["tasks"]["fixture:sourced"] = ["fixture:sourced"]
        self.catalog["selection"]["fixture:sourced"] = "passthrough"
        self.save_catalog()
        self.queue.resume()
        self.collect(self.start("fixture:sourced", "--", "pass"))
        failed = self.start("fixture:sourced", "--", "fail")
        out, err = failed.communicate(timeout=self.state_timeout + 5)
        self.assertNotEqual(failed.returncode, 0, out + err)

    def test_concurrent_go_publication_cannot_replace_another_runs_evidence(self):
        if resources.capacity() < 3:
            self.skipTest("concurrent publication fixture requires three worker slots")
        self.catalog["resources"]["go"] = {"locks": ["go"], "shared_locks": ["go"], "workers": 1}
        self.save_catalog()
        first_published = self.root / "first-published"
        second_published = self.root / "second-published"
        self.env.update(FIXTURE_FIRST_PUBLISHED=str(first_published), FIXTURE_SECOND_PUBLISHED=str(second_published))
        lock = self.scripts / "digest-run-lock.sh"
        body = lock.read_text().replace("digest_release_lock()", "fixture_release_base()")
        lock.write_text(body + '''
digest_release_lock() {
  local publishing=0 attempt
  if [[ -n "${DIGEST_LOCK_DIR:-}" && -n "${RAW:-}" && "${TMP_RAW-unset}" == "" ]]; then
    publishing=1
  fi
  fixture_release_base
  if [[ "$publishing" == 1 ]]; then
    case "${FIXTURE_PUBLICATION_ROLE:-}" in
      first)
        touch "$FIXTURE_FIRST_PUBLISHED"
        for ((attempt=0; attempt<12000; attempt++)); do
          [[ -e "$FIXTURE_SECOND_PUBLISHED" ]] && return 0
          sleep .01
        done
        return 1
        ;;
      second) touch "$FIXTURE_SECOND_PUBLISHED" ;;
    esac
  fi
}
''')
        first = self.start("test:digest", "--", "./good", env={**self.env, "FIXTURE_PUBLICATION_ROLE": "first"})
        self.queued(1)
        self.queue.resume()
        self.await_condition(first_published.exists)
        second = self.start("test:digest", "--", "./shared", env={**self.env, "FIXTURE_PUBLICATION_ROLE": "second"})
        self.collect(second)
        self.collect(first)
        receipts = {r["evidence"][0]["packages"][0]: r for r in self.receipts()}
        for package in ("fixture/good", "fixture/shared"):
            result = receipts[package]["evidence"][0]["results"][package]
            report = json.loads(Path(result["report"]).read_text())
            self.assertEqual(report["packages"], {package: "pass"})
        self.assertNotEqual(receipts["fixture/good"]["batch_id"], receipts["fixture/shared"]["batch_id"])
