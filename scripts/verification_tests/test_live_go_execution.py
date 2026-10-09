import json
import os
from pathlib import Path
import shutil
import subprocess
import time
import unittest

import verification_batch as batch
import verification_resources as resources
import verification_reuse as reuse
from verification_tests import support

SCRIPT = support.SCRIPT


class LiveGoBatchTests(support.BatchFixture):
    # Real source capture, compiler startup, and publication share this deadline.
    state_timeout = 60

    def setUp(self):
        super().setUp()
        for name in ["task.sh", "go-test-digest.sh", "go-test-exec.py", "test-digest.py", "test-run-manifest.py",
                     "test-host-capacity.sh", "process-group-watchdog.py", "verification_timings.py"]:
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

    def reused(self, receipt, package):
        return receipt["evidence"][0]["results"][package].get("reused")

    @unittest.skipUnless(reuse.fork_observable(), "reuse requires process fork observation")
    def test_identical_binary_and_inputs_reuse_a_recorded_pass(self):
        package = self.repo / "lycaon" / "good"
        (package / "testdata").mkdir()
        (package / "testdata" / "value.txt").write_text("one")
        (package / "fixture_test.go").write_text('''package good
import ("os"; "testing")
func TestFixture(t *testing.T) {
 if data, err := os.ReadFile("testdata/value.txt"); err != nil || len(data) != 3 { t.Fatalf("read: %q %v", data, err) }
}
''')
        (self.repo / "lycaon" / "shared" / "fixture_test.go").write_text('''package shared
import ("os/exec"; "testing")
func TestFixture(t *testing.T) {
 if err := exec.Command("true").Run(); err != nil { t.Fatal(err) }
}
''')
        self.queue.resume()

        def verify():
            self.collect(self.start("test:digest", "--", "./good", "./shared"))
            self.await_condition(lambda: not self.queue.status()["runs"])
            return max(self.receipts(), key=lambda r: r["finished_at"])
        first = verify()
        self.assertIsNone(self.reused(first, "fixture/good"))
        events = reuse.read_events(Path(first["evidence"][0]["results"]["fixture/good"]["log"]).with_name(
            first["evidence"][0]["results"]["fixture/good"]["stage_id"] + "-events.jsonl"))
        self.assertTrue(events["fixture/good"]["recorded"])
        self.assertTrue(events["fixture/shared"]["forked"])
        self.assertFalse(events["fixture/shared"]["recorded"])
        second = verify()
        self.assertEqual(self.reused(second, "fixture/good")["batch_id"], first["batch_id"])
        self.assertIsNone(self.reused(second, "fixture/shared"))
        (package / "testdata" / "value.txt").write_text("two")
        third = verify()
        self.assertIsNone(self.reused(third, "fixture/good"))
        self.assertEqual(self.reused(verify(), "fixture/good")["batch_id"], third["batch_id"])

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
