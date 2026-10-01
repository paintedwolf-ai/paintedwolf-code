"""Recorded Go package results: keys, observed inputs, replay, and early outcomes."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

import verification_reuse as reuse
from verification_execute import StageEvents

WRAPPER = Path(__file__).resolve().parents[1] / "go-test-exec.py"


class KeyTests(unittest.TestCase):
    def test_selection_arguments_keep_what_chooses_tests_and_refuse_side_effects(self):
        self.assertEqual(reuse.selection_arguments(
            ["-test.paniconexit0", "-test.timeout=20m0s", "-test.v=test2json", "-test.count=1",
             "-test.parallel=3", "-test.run=^TestA$", "-test.short=true"]),
            ["-test.paniconexit0", "-test.v=test2json", "-test.count=1", "-test.run=^TestA$", "-test.short=true"])
        for arguments in (["-test.coverprofile=c.out"], ["-test.bench=."], ["-test.cpuprofile=p"],
                          ["-test.fuzz=FuzzA"], ["-test.shuffle=on"], ["positional"], ["-test.unknown=1"]):
            with self.subTest(arguments=arguments):
                self.assertIsNone(reuse.selection_arguments(arguments))

    def test_build_arguments_keep_patterns_and_compile_flags(self):
        self.assertEqual(reuse.build_arguments(
            ["-json", "-p=3", "-parallel", "2", "-timeout", "20m", "-count=1", "-tags", "integration", "-race",
             "./internal/...", "github.com/example/pkg", "-run=^TestA$", "-skip", "TestB"]),
            (["./internal/...", "github.com/example/pkg"], ["-tags=integration", "-race"]))

    def test_build_identity_follows_compiled_content_and_ignores_the_checkout_path(self):
        with tempfile.TemporaryDirectory() as directory:
            def checkout(name):
                root = Path(directory) / name
                (root / "pkg").mkdir(parents=True)
                (root / "dep").mkdir()
                (root / "pkg" / "a.go").write_text("package pkg\n")
                (root / "pkg" / "a_test.go").write_text("package pkg\n")
                (root / "dep" / "d.go").write_text("package dep\n")
                (root / "dep" / "data.txt").write_text("embedded\n")
                return root

            def records(root):
                main = {"Path": "example", "Main": True, "GoVersion": "1.26"}
                return [
                    {"ImportPath": "example/pkg.test", "Name": "main", "Dir": str(root / "pkg"), "Module": main,
                     "Deps": ["example/dep", "example/pkg [example/pkg.test]", "fmt", "remote/lib", "local/lib"]},
                    {"ImportPath": "example/pkg [example/pkg.test]", "Dir": str(root / "pkg"), "Module": main,
                     "GoFiles": ["a.go"], "TestGoFiles": ["a_test.go"]},
                    {"ImportPath": "example/dep", "Dir": str(root / "dep"), "Module": main, "GoFiles": ["d.go"],
                     "EmbedFiles": ["data.txt"]},
                    {"ImportPath": "fmt", "Standard": True},
                    {"ImportPath": "remote/lib", "Module": {"Path": "remote", "Version": "v1.2.3"}},
                    {"ImportPath": "local/lib", "Dir": str(root / "dep"), "GoFiles": ["d.go"],
                     "Module": {"Path": "local", "Version": "v0.1.0", "Replace": {"Path": "../local"}}}]

            toolchain = {"environment": {"GOVERSION": "go1.26"}, "flags": []}
            first, second = checkout("one"), checkout("two")
            identity = reuse.package_identities(records(first), toolchain)["example/pkg"]
            self.assertEqual(identity, reuse.package_identities(records(second), toolchain)["example/pkg"])
            self.assertNotEqual(identity, reuse.package_identities(
                records(first), {**toolchain, "flags": ["-race"]})["example/pkg"])
            for change in (lambda: (second / "dep" / "data.txt").write_text("changed\n"),
                           lambda: (second / "pkg" / "a_test.go").write_text("package pkg // changed\n")):
                change()
                self.assertNotEqual(identity, reuse.package_identities(records(second), toolchain)["example/pkg"])
            broken = records(first)
            broken[2]["DepsErrors"] = [{"Err": "missing"}]
            self.assertEqual(reuse.package_identities(broken, toolchain), {})
            (first / "dep" / "d.go").unlink()
            self.assertEqual(reuse.package_identities(records(first), toolchain), {})

    def test_admission_shaped_settings_do_not_change_identity(self):
        base = {"PATH": "/bin", "LYCAON_LLM_MOCK": "1"}
        self.assertEqual(reuse.environment_identity(base),
                         reuse.environment_identity({**base, "PW_TEST_WORKERS": "6", "GOMAXPROCS": "2"}))
        self.assertNotEqual(reuse.environment_identity(base), reuse.environment_identity({**base, "LYCAON_X": "1"}))


class InputTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.source = self.root / "source"
        self.scratch = self.root / "scratch"
        (self.source / "lycaon" / "pkg" / "testdata").mkdir(parents=True)
        (self.source / "lycaon" / "go.mod").write_text("module fixture\n")
        (self.scratch / "tmp").mkdir(parents=True)
        self.context = reuse.Context(str(self.source), str(self.scratch), str(self.source / "lycaon"))
        self.package = self.source / "lycaon" / "pkg"
        (self.package / "testdata" / "a.txt").write_text("one")

    def test_log_paths_resolve_like_go_and_scratch_output_is_not_an_input(self):
        log = "\n".join(["# test log", "getenv HOME", "open testdata/a.txt", "stat /usr/bin/true", "open .",
                         f"open {self.scratch}/tmp/TestA/001/x", "chdir testdata", "open a.txt",
                         f"stat {self.source}/lycaon/go.mod"])
        inputs = reuse.observed_inputs(log, str(self.package), self.context)
        self.assertEqual(inputs, sorted({("getenv", "HOME"), ("open", "<source>/lycaon/pkg/testdata/a.txt"),
                                         ("stat", "/usr/bin/true"), ("open", "<source>/lycaon/pkg"),
                                         ("stat", "<source>/lycaon/go.mod")}))
        self.assertIsNone(reuse.observed_inputs("not a log", str(self.package), self.context))
        self.assertIsNone(reuse.observed_inputs("# test log\nexec x", str(self.package), self.context))

    def test_package_import_path_comes_from_the_module(self):
        self.assertEqual(self.context.package(str(self.package)), "fixture/pkg")
        self.assertEqual(self.context.package(str(self.source / "lycaon")), "fixture")

    def test_fingerprint_follows_content_listings_and_values(self):
        inputs = [("open", "<source>/lycaon/pkg/testdata/a.txt"), ("open", "<source>/lycaon/pkg/testdata"),
                  ("getenv", "LYCAON_FLAG"), ("getenv", "HOME"), ("getenv", "PW_TEST_TIMEOUT_SCALE"),
                  ("stat", "<source>/lycaon/pkg/missing")]
        environment = {"LYCAON_FLAG": "1", "HOME": str(self.scratch / "home"), "PW_TEST_TIMEOUT_SCALE": "1"}
        first = reuse.fingerprint(inputs, self.context, environment)
        moved = reuse.Context(str(self.source), str(self.root / "other-scratch"), str(self.source / "lycaon"))
        self.assertEqual(first, reuse.fingerprint(inputs, self.context, {**environment, "PW_TEST_TIMEOUT_SCALE": "3"}))
        self.assertEqual(first, reuse.fingerprint(inputs, moved, {**environment,
                                                                  "HOME": str(self.root / "other-scratch" / "home")}))
        os.utime(self.package / "testdata" / "a.txt", (1, 1))
        self.assertEqual(first, reuse.fingerprint(inputs, self.context, environment))
        ancestor = [("stat", str(self.root))]
        before = reuse.fingerprint(ancestor, self.context, environment)
        (self.root / "unrelated").write_text("")
        self.assertEqual(before, reuse.fingerprint(ancestor, self.context, environment))
        for change in (lambda: (self.package / "testdata" / "a.txt").write_text("two"),
                       lambda: (self.package / "testdata" / "b.txt").write_text(""),
                       lambda: (self.package / "missing").write_text("")):
            with self.subTest(change=change):
                change()
                self.assertNotEqual(first, reuse.fingerprint(inputs, self.context, environment))
        self.assertNotEqual(first, reuse.fingerprint(inputs[:3], self.context, {**environment, "LYCAON_FLAG": "2"}))

    def test_store_replays_only_a_matching_variant_and_prunes_to_budget(self):
        store = self.root / "store"
        inputs = [("open", "<source>/lycaon/pkg/testdata/a.txt")]
        self.assertTrue(reuse.record(store, "k" * 64, "fixture/pkg", inputs, self.context, {}, b"PASS\n", 1.0,
                                     {"batch_id": "one"}))
        self.assertEqual(reuse.lookup(store, "k" * 64, self.context, {}), (b"PASS\n", {"batch_id": "one"}))
        (self.package / "testdata" / "a.txt").write_text("two")
        self.assertIsNone(reuse.lookup(store, "k" * 64, self.context, {}))
        reuse.record(store, "k" * 64, "fixture/pkg", inputs, self.context, {}, b"PASS two\n", 1.0, {"batch_id": "two"})
        self.assertEqual(reuse.lookup(store, "k" * 64, self.context, {})[1], {"batch_id": "two"})
        (self.package / "testdata" / "a.txt").write_text("one")
        self.assertEqual(reuse.lookup(store, "k" * 64, self.context, {})[1], {"batch_id": "one"})
        self.assertFalse(reuse.record(store, "j" * 64, "fixture/pkg", inputs, self.context, {},
                                      b"x" * (reuse.MAX_OUTPUT_BYTES + 1), 1.0, {}))
        now = time.time() + 2 * reuse.ORPHAN_SECONDS
        with patch.object(reuse, "STORE_BYTES", 1):
            reuse.prune(store, now)
        self.assertEqual(list((store / "entries").glob("*/*.json")), [])
        self.assertEqual(list((store / "outputs").glob("*/*.gz")), [])


class EventTests(unittest.TestCase):
    def test_stage_events_report_failures_once_and_ignore_interruptions(self):
        with tempfile.TemporaryDirectory() as directory:
            events, raw = Path(directory) / "events.jsonl", Path(directory) / "raw.json"
            watcher = StageEvents(events, raw)
            self.assertEqual(watcher.failures(), {})
            events.write_text(json.dumps({"package": "a", "exit_code": 1, "tests": ["TestA"]}) + "\n"
                              + json.dumps({"package": "b", "exit_code": -9}) + "\n"
                              + json.dumps({"package": "c", "exit_code": 0}) + "\n" + '{"package": "d", "exit')
            raw.write_text(json.dumps({"Package": "e", "Action": "fail"}) + "\n"
                           + json.dumps({"Package": "f", "Test": "TestF", "Action": "fail"}) + "\n"
                           + json.dumps({"ImportPath": "g [g.test]", "Action": "build-fail"}) + "\n")
            self.assertEqual(watcher.failures(), {"a": {"tests": ["TestA"]}, "e": {}, "g": {"build_failed": True}})
            with events.open("a") as stream:
                stream.write('_code": 2}\n')
            self.assertEqual(watcher.failures(), {"d": {}})
            self.assertEqual(watcher.failures(), {})

    def test_package_durations_keep_recent_measurements_per_stage(self):
        with tempfile.TemporaryDirectory() as directory:
            queue = type("Queue", (), {"root": Path(directory), "locked": lambda self: open(os.devnull)})()
            for seconds in (10, 30, 20):
                reuse.record_package_durations(queue, "stage", {"a": {"elapsed": seconds}, "b": {"elapsed": 1},
                                                                "c": {"elapsed": 99, "reused": {"batch_id": "x"}}})
            self.assertEqual(reuse.package_durations(queue, "stage"), {"a": 20, "b": 1})
            self.assertEqual(reuse.package_durations(queue, "other"), {})


@unittest.skipIf(os.name == "nt", "the wrapper runs POSIX test binaries")
class WrapperTests(unittest.TestCase):
    """A shell script stands in for a Go test binary: it honors the test log flag and uses only shell
    builtins unless it is asked to start a process. The stage supplies its build identity."""

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.module = self.root / "source" / "lycaon"
        self.package = self.module / "pkg"
        self.package.mkdir(parents=True)
        (self.module / "go.mod").write_text("module fixture\n")
        (self.package / "input.txt").write_text("one\n")
        self.scratch = self.root / "scratch"
        self.scratch.mkdir()
        self.events = self.root / "stage-001-events.jsonl"
        self.binary = self.root / "pkg.test"
        self.identities = self.root / "identities.json"
        self.identities.write_text(json.dumps({"fixture/pkg": "build-one"}))
        self.binary.write_bytes(b'''#!/bin/sh
for argument in "$@"; do
  case "$argument" in -test.testlogfile=*) log="${argument#-test.testlogfile=}" ;; esac
done
read -r value < input.txt
if [ -n "$log" ]; then
  printf '# test log\\ngetenv FIXTURE_MODE\\nopen input.txt\\n' > "$log"
fi
printf '=== RUN   TestFixture\\n'
if [ "$FIXTURE_MODE" = fork ]; then /usr/bin/true; fi
if [ "$FIXTURE_MODE" = fail ]; then printf -- '--- FAIL: TestFixture (0.00s)\\nFAIL\\n'; exit 1; fi
printf -- '--- PASS: TestFixture (0.00s)\\nPASS %s\\n' "$value"
''')
        self.binary.chmod(0o755)

    def run_wrapper(self, mode="pass"):
        env = {"PATH": os.environ["PATH"], "FIXTURE_MODE": mode, "PW_TEST_STAGE_EVENTS": str(self.events),
               "PW_TEST_REUSE_STORE": str(self.root / "store"), "PW_TEST_SCRATCH_ROOT": str(self.scratch),
               "PW_TEST_SOURCE_ROOT": str(self.root / "source"), "PW_TEST_MODULE_ROOT": str(self.module),
               "PW_TEST_BATCH_DIRECTORY": str(self.root / f"batch-{mode}"), "PW_TEST_STAGE_ID": "stage-001",
               "PW_TEST_PACKAGE_IDENTITIES": str(self.identities)}
        result = subprocess.run([sys.executable, str(WRAPPER), str(self.binary), "-test.paniconexit0",
                                 "-test.timeout=10m0s", "-test.v=test2json"], cwd=self.package, env=env,
                                capture_output=True, timeout=30)
        return result, reuse.read_events(self.events)["fixture/pkg"]

    def test_outside_a_stage_the_binary_runs_unchanged(self):
        result = subprocess.run([sys.executable, str(WRAPPER), str(self.binary)], cwd=self.package,
                                env={"PATH": os.environ["PATH"]}, capture_output=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(b"PASS one", result.stdout)
        self.assertFalse(self.events.exists())

    def test_failures_are_reported_as_they_happen_with_their_output(self):
        result, event = self.run_wrapper("fail")
        self.assertEqual(result.returncode, 1)
        self.assertEqual(event["exit_code"], 1)
        self.assertEqual(event["tests"], ["TestFixture"])
        self.assertIn("--- FAIL: TestFixture", Path(event["output"]).read_text())
        self.assertFalse(event["recorded"])

    @unittest.skipUnless(reuse.fork_observable(), "recording requires process fork observation")
    def test_a_recorded_pass_replays_until_an_observed_input_changes(self):
        first, event = self.run_wrapper()
        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertTrue(event["recorded"])
        self.assertIs(event["forked"], False)
        second, event = self.run_wrapper()
        self.assertEqual(second.stdout, first.stdout)
        self.assertEqual(event["reused"]["batch_id"], "batch-pass")
        self.identities.write_text(json.dumps({"fixture/pkg": "build-two"}))
        self.assertFalse(self.run_wrapper()[1]["reused"])
        self.identities.write_text(json.dumps({"fixture/pkg": "build-one"}))
        (self.package / "input.txt").write_text("two\n")
        third, event = self.run_wrapper()
        self.assertFalse(event["reused"])
        self.assertIn(b"PASS two", third.stdout)
        self.assertEqual(self.run_wrapper()[1]["reused"]["batch_id"], "batch-pass")

    @unittest.skipUnless(reuse.fork_observable(), "recording requires process fork observation")
    def test_a_test_that_starts_a_process_is_never_recorded(self):
        result, event = self.run_wrapper("fork")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIs(event["forked"], True)
        self.assertFalse(event["recorded"])
        self.assertFalse(self.run_wrapper("fork")[1]["reused"])


if __name__ == "__main__":
    unittest.main()
