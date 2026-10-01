import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import verification_batch as batch
import verification_plan as planning
import verification_resources as resources
from verification_results import package_results
from verification_execute import shared_peers
from verification_state import read_json, write_json
from verification_tests import support

execution = support.execution
SCRIPT = support.SCRIPT


class PlanningTests(unittest.TestCase):
    def test_subscriber_reports_state_changes_without_repeated_wait_notices(self):
        with tempfile.TemporaryDirectory() as root:
            queue = execution.Queue(Path(root) / "queue")
            queue.pause("fixture")
            sleeps = 0

            def advance(_seconds):
                nonlocal sleeps
                sleeps += 1
                if sleeps == 2:
                    queue.resume()
                if sleeps == 3:
                    raise KeyboardInterrupt()

            with patch.object(batch.time, "sleep", side_effect=advance), \
                    patch.object(batch.time, "monotonic", side_effect=[100, 100, 131, 131, 162, 162]), \
                    patch.object(batch, "batch_prefix", return_value=[]), \
                    patch.object(batch, "report") as report, patch("builtins.print") as output:
                with self.assertRaises(KeyboardInterrupt):
                    batch.subscribe(queue, execution.Lease, execution.lock_file, root, sys.executable,
                                    ["build"], planning.request(["build"], {}), {"PW_TEST_WORKERS": "1"})
            self.assertEqual(report.call_count, 3)
            self.assertEqual(output.call_count, 3)
            self.assertEqual(queue.status()["runs"], [])

    def test_oar_gate_proves_host_catalog_and_standard_conformance(self):
        stages = planning.expand(["oar:verify"])
        tests = next(stage for stage in stages if stage["kind"] == "go")
        for package in ("./internal/catalogview", "./internal/toolvocab", "./internal/tools", "./internal/oar", "./internal/oarcore"):
            self.assertIn(package, tests["packages"])
        self.assertTrue(any(stage["name"] == "oar:conformance" for stage in stages))
        full = {planning.stage_key(stage) for stage in planning.expand(["check"])}
        for stage in stages:
            self.assertIn(planning.stage_key(stage), full)
        self.assertIsNotNone(planning.request(["oar:verify"], {}))

    def test_stress_recipe_preserves_its_selection_and_serial_execution(self):
        self.assertIsNotNone(planning.request(["test:stress"], {}))
        stage = planning.expand(["test:stress"])[0]
        self.assertEqual(stage["flags"], ["-p=1", "-parallel=1", "-run=^TestStress"])
        self.assertEqual(stage["options"], ["--full", "--tags", "stress", "--timeout", "90m"])
        command = planning.digest_command(stage, stage["packages"])
        self.assertIn("./internal/db", command)
        self.assertIn("-run=^TestStress", command)
        different = {**stage, "flags": ["-run=TestOther"]}
        self.assertNotEqual(planning.stage_key(stage), planning.stage_key(different))

    def test_harness_filters_use_bounded_resources_and_reject_unsupported_flags(self):
        args = ["den:harness:test", "--", "recording-encoding-measurement.spec.ts", "--grep", "capture",
                "--retries=0"]
        plan = planning.request(args, {})
        self.assertEqual(plan["stages"][0]["arguments"], args)
        spec = resources.profile(plan["stages"][0], planning.catalog())
        self.assertEqual(set(spec["locks"]), {"harness", "frontend"})
        self.assertEqual(spec["workers"], "shared")
        for flags in (["--update-snapshots"], ["--workers=8"], ["--project=desktop"],
                      ["--config=other.ts"], ["--ui"], ["--grep"]):
            with self.subTest(flags=flags):
                with self.assertRaisesRegex(ValueError, "den:harness:test:"):
                    planning.request(["den:harness:test", "--", *flags], {})

    def test_catalog_declares_resources_for_every_stage(self):
        catalog = planning.catalog()
        self.assertLessEqual(set(catalog["tasks"]) | {"go", "go:bundled"}, set(catalog["resources"]))
        taskfile = (SCRIPT.parent.parent / "Taskfile.yml").read_text()
        for name in set(catalog["resources"]) - set(catalog["tasks"]) - {"go", "go:bundled"}:
            with self.subTest(invocation=name):
                self.assertRegex(taskfile, rf"(?m)^  {re.escape(name)}:$")
                self.assertNotIn(name, planning.declared_names(catalog))

    def test_invocations_hold_what_their_stages_declare(self):
        catalog = planning.catalog()
        with patch.object(resources, "capacity", return_value=6):
            dev = planning.invocation_profile(["build:lycaon-dev"])
            self.assertEqual(set(dev["locks"]), set(catalog["resources"]["build:lycaon-dev"]["locks"]))
            self.assertNotIn("*", dev["locks"])
            digest = planning.invocation_profile(["test:digest"], ["./internal/api", "-count=2"])
            self.assertEqual(digest, {"locks": ["go"], "shared_locks": ["go"], "workers": 3})
            gate = planning.invocation_profile(["check-fast"])
            self.assertIn("frontend", gate["locks"])
            self.assertIn("lint", gate["locks"])
            self.assertNotIn("frontend", gate["shared_locks"])
            self.assertEqual(gate["workers"], 3)
            for names in (["test:unknown"], ["build:lycaon-dev", "perf:bench"], ["den:harness"]):
                with self.subTest(names=names):
                    self.assertEqual(planning.invocation_profile(names), resources.EXCLUSIVE)

    def test_unclassified_stage_still_requires_exclusive_admission(self):
        spec = resources.profile({"kind": "task", "name": "unknown"}, planning.catalog())
        with patch.object(resources, "capacity", return_value=6):
            candidate = {**spec, "workers": resources.demand(spec, 6), "ticket": "2", "state": "queued"}
            running = {"locks": [], "workers": 1, "ticket": "1", "state": "running", "name": "check"}
            self.assertEqual(candidate["workers"], 6)
            self.assertIn("resource held", resources.blocked_reason(candidate, [running, candidate]))

    def test_read_only_checks_can_overlap_harness_and_go_work(self):
        catalog = planning.catalog()
        lightweight = ["comments:check", "docs:links", "oar:vendor:check", "openapi:bundle:check",
                       "openapi:lint", "scan:rules:vendor:check", "secret-mint:vendor:check",
                       "test:fail-messages:check"]
        generators = ["codegen:approval-explanations:check", "codegen:client-notices:check",
                      "codegen:contribution-facts:check", "codegen:contribution-inventory:check",
                      "codegen:den-fonts:check", "codegen:den-types:check", "codegen:detection-packs:check",
                      "codegen:guidance-registry:check", "codegen:host-markers:check",
                      "codegen:native-tool-contracts:check", "codegen:theme-tokens:check",
                      "codegen:tool-command-equivalence:check", "codegen:tool-presentation:check",
                      "codegen:web-research-catalog:check", "codegen:wire-enums:check",
                      "codegen:worker-branch-tools:check", "db:sqlc:check", "db:sqlc:vet",
                      "oar:conformance", "validate:workflows"]

        def operation(name, ticket, state):
            stage = planning.expand([name])[0]
            spec = resources.profile(stage, catalog)
            return {**spec, "workers": resources.demand(spec, 6), "name": name,
                    "ticket": ticket, "state": state}

        with patch.object(resources, "capacity", return_value=6):
            harness = operation("den:harness:test", "1", "running")
            go = operation("test:digest", "3", "queued")
            for name in lightweight + generators:
                with self.subTest(stage=name):
                    check = operation(name, "2", "queued")
                    self.assertIsNone(resources.blocked_reason(check, [harness, check]))
                    self.assertIsNone(resources.blocked_reason(go, [harness, check, go]))
                    check["state"] = "running"
                    self.assertIsNone(resources.blocked_reason(go, [check, go]))
                    self.assertEqual(resources.blocked_reason(go, [harness, check, go]), "worker capacity")
                    exclusive = {**check, "locks": ["*"], "ticket": "0", "name": "exclusive"}
                    self.assertIsNotNone(resources.blocked_reason(check, [exclusive, check]))
                    busy = {**harness, "workers": 5}
                    check["state"] = "queued"
                    reason = resources.blocked_reason(check, [busy, check])
                    self.assertEqual(reason, None if name in lightweight else "worker capacity")

    def test_status_names_exclusive_waiter_and_held_resource(self):
        with tempfile.TemporaryDirectory() as root:
            active = {"ticket": "1-batch", "kind": "batch", "name": "verification batch", "state": "running",
                      "members": [], "directory": root, "source": root}
            invocation = {"ticket": "2", "name": "exclusive fixture", "kind": "invocation", "state": "queued",
                          "queued_at": 1}
            waiting = {"ticket": "r-2", "batch": "2", "name": "exclusive fixture", "state": "queued", "locks": ["*"],
                       "workers": 1, "order": 10 ** 9, "blocked_on": "resource held by Go digest (operation-1)"}
            request = {"ticket": "3", "name": "Checks", "kind": "request", "state": "queued", "queued_at": 2,
                       "compatibility": "same", "admission": {"locks": [], "workers": 1}}
            operation = {"ticket": "operation-1", "name": "Go digest", "state": "running", "locks": ["go"],
                         "workers": 1, "order": 0}
            queue = execution.Queue(Path(root) / "queue")
            snapshot = {"paused": None, "runs": [active, invocation, request], "operations": [operation, waiting]}
            with patch.object(queue, "status", return_value=snapshot):
                result = batch.status_details(queue)
            self.assertIn("Go digest (operation-1)", result["runs"][1]["blocked_on"])
            self.assertIn("exclusive fixture (r-2)", result["runs"][2]["blocked_on"])
            request = {**request, "name": "Go tests", "admission": {"locks": ["go"], "workers": 1}}
            snapshot.update(runs=[active, request], operations=[operation])
            with patch.object(queue, "status", return_value=snapshot):
                result = batch.status_details(queue)
            self.assertIn("Go digest (operation-1)", result["runs"][1]["blocked_on"])

    def test_capacity_and_resource_conflicts_control_admission(self):
        def operation(ticket, locks, workers, state="queued"):
            return {"ticket": ticket, "locks": locks, "workers": workers, "state": state, "name": ticket}
        with patch.object(resources, "capacity", return_value=6):
            rust = operation("1", ["rust"], 3, "running")
            go = operation("2", ["go"], 3)
            self.assertIsNone(resources.blocked_reason(go, [rust, go]))
            self.assertIsNotNone(resources.blocked_reason(operation("3", ["rust"], 1), [rust]))
            self.assertIsNotNone(resources.blocked_reason(operation("3", ["*"], 1), [rust]))
            self.assertIsNotNone(resources.blocked_reason(operation("3", ["frontend"], 4), [rust]))
            self.assertIsNone(resources.blocked_reason(operation("3", [], 3), [rust]))
            self.assertIsNone(resources.blocked_reason(operation("3", [], 3), [operation("1", [], 3, "running")]))
            self.assertIsNotNone(resources.blocked_reason(operation("3", [], 1), [operation("1", ["*"], 1, "running")]))
            self.assertEqual(resources.demand({"workers": "shared"}, 6), 3)
            self.assertEqual(resources.demand({"workers": "shared"}, 1), 1)

    def test_resource_wait_allows_unrelated_work_but_prevents_starvation(self):
        running = {"ticket": "1", "locks": ["rust"], "workers": 3, "state": "running", "name": "rust"}
        waiting = {"ticket": "2", "locks": ["rust"], "workers": 3, "state": "queued", "name": "next rust"}
        later = {"ticket": "3", "locks": ["frontend"], "workers": 1, "state": "queued", "name": "frontend"}
        with patch.object(resources, "capacity", return_value=6):
            self.assertIsNone(resources.blocked_reason(later, [running, waiting, later]))
            waiting["workers"] = 6
            self.assertIsNotNone(resources.blocked_reason(later, [running, waiting, later]))
            waiting.update(workers=1, locks=["*"])
            self.assertIsNotNone(resources.blocked_reason(later, [running, waiting, later]))

    def test_shared_resource_leases_overlap_but_respect_exclusive_holders_and_waiters(self):
        first = {"ticket": "1", "locks": ["go"], "shared_locks": ["go"], "workers": 2,
                 "state": "running", "name": "first"}
        second = {**first, "ticket": "3", "state": "queued", "name": "second"}
        exclusive = {**first, "ticket": "2", "shared_locks": [], "state": "queued", "name": "exclusive"}
        with patch.object(resources, "capacity", return_value=6):
            self.assertIsNone(resources.blocked_reason(second, [first, second]))
            self.assertIsNotNone(resources.blocked_reason(second, [first, exclusive, second]))
            self.assertIsNotNone(resources.blocked_reason(second, [{**first, "shared_locks": []}, second]))

    def test_groups_share_actual_stages_without_assuming_full_means_short(self):
        fast = planning.expand(["check-fast"])
        full = planning.expand(["check"])
        self.assertEqual(planning.stage_key(fast[0]), planning.stage_key(full[0]))
        self.assertNotEqual(planning.stage_key(next(s for s in fast if s["name"] == "test:short")),
                            planning.stage_key(next(s for s in full if s["name"] == "test:full")))
        self.assertIn("test:runner", [s["name"] for s in full])
        self.assertNotIn("check:tests", [s["name"] for s in full])

    def test_short_scope_excludes_external_tiers_and_keeps_unit_helpers(self):
        stage = planning.expand(["test:short"])[0]
        excluded = re.compile(stage["exclude"])
        for package in ("test/contract", "test/integration", "test/security", "test/smoke", "test/wiring",
                        "test/integration/nested", "test/security/nested"):
            with self.subTest(package=package):
                self.assertIsNotNone(excluded.search("example.org/project/" + package))
        for package in ("internal/api", "pkg/api", "test/openapi", "test/property", "internal/test/wiringhelper"):
            with self.subTest(package=package):
                self.assertIsNone(excluded.search("example.org/project/" + package))

    def test_release_race_runs_integration_scenarios_without_short(self):
        stage = planning.expand(["test:race"])[0]
        command = planning.digest_command(stage, ["./internal/api"])
        self.assertIn("--full", command)
        self.assertIn("--race", command)
        self.assertEqual(command[command.index("--tags") + 1], "integration")
        self.assertNotIn("-short", command)

    def test_integration_recipe_reaches_sql_lifecycle_properties(self):
        stage = planning.expand(["test:integration"])[0]
        self.assertIn("./test/property/...", stage["packages"])
        self.assertIn("--full", stage["options"])

    def test_package_union_requires_identical_execution_options(self):
        a = planning.expand(["test:digest"], ["./internal/a", "-run", "TestA"])[0]
        b = planning.expand(["test:digest"], ["./internal/b", "-run=TestA"])[0]
        self.assertEqual(planning.stage_key(a), planning.stage_key(b))
        b["flags"] = ["-run=TestB"]
        self.assertNotEqual(planning.stage_key(a), planning.stage_key(b))

    def test_nonshareable_requests_stay_exclusive(self):
        for args in (["test:digest", "--", "./internal/api", "-count=2"],
                     ["test:digest", "--", "-coverprofile=coverage.out"],
                     ["test:digest", "--", "-bench=BenchmarkA"],
                     ["test:digest", "--", "-shuffle=on"], ["test:digest", "--", "-failfast"], ["test:failed"],
                     ["test:unknown"], ["den:lint:fix"], ["--dir", "/tmp", "build"],
                     ["check", "CONFIG=other"]):
            with self.subTest(args=args):
                self.assertIsNone(planning.request(args, {}))
        self.assertIsNone(planning.request(["check"], {"UPDATE_SCHEMA_LOCK": "1"}))
        self.assertIsNone(planning.request(["check"], {"PW_TEST_REPLAY_MANIFEST": "replay.json"}))
        self.assertIsNone(planning.request(["check"], {"GOFLAGS": "-count=2"}))
        self.assertIsNone(planning.request(["check"], {"GOFLAGS": "-failfast"}))
        self.assertIsNone(planning.request(["den:test"], {"LYCAON_VITEST_PERF": "1"}))
        self.assertIsNotNone(planning.request([], {}))

    def test_maintainability_budget_refresh_uses_exclusive_admission(self):
        args = ["test:contract", "--", "./test/contract/maintainability", "-run", "^TestMaintainabilityWithinBudget$"]
        self.assertIsNotNone(planning.request(args, {}))
        self.assertIsNone(planning.request(args, {"UPDATE_MAINTAINABILITY_BUDGETS": "1"}))
        self.assertIsNotNone(planning.request(args, {"UPDATE_MAINTAINABILITY_BUDGETS": "0"}))
        with self.assertRaisesRegex(ValueError, "unsupported flag"):
            planning.request([*args, "-bogus"], {"UPDATE_MAINTAINABILITY_BUDGETS": "1"})

    def test_unknown_flags_cannot_hide_behind_exclusive_inputs(self):
        for args in (["test:digest", "--", "./internal/a", "-bogus"],
                     ["test:digest", "--", "-count=2", "-bogus"],
                     ["test:digest", "--", "file.go", "-bogus"],
                     ["test:digest", "--", "-args", "-custom"],
                     ["den:test:digest", "--", "outside-src.test.ts", "--bogus"],
                     ["den:harness:test", "--", "e2e/fixture.spec.ts", "--workers=1"]):
            for env in ({}, {"UPDATE_SCHEMA_LOCK": "1"}, {"PW_TEST_REPLAY_MANIFEST": "replay.json"}):
                with self.subTest(args=args, env=env), self.assertRaisesRegex(ValueError, "unsupported flag"):
                    planning.request(args, env)

    def test_selection_flags_require_values_before_admission(self):
        for target, flag in (("test:digest", "-run"), ("test:digest", "-count"),
                             ("den:test:digest", "-t"), ("den:harness:test", "--grep")):
            for flags in ([flag], [flag + "="], [flag, "--bogus"]):
                with self.subTest(target=target, flags=flags), self.assertRaisesRegex(ValueError, "requires a value"):
                    planning.request([target, "--", *flags], {})

    def test_go_flag_values_are_checked_before_admission(self):
        for flags in (["-p", "0"], ["-parallel=-1"], ["-count=x"], ["-cpu", "1,,2"], ["-timeout", "5"],
                      ["-timeout=20 m"], ["-shuffle=sometimes"]):
            with self.subTest(flags=flags), self.assertRaisesRegex(ValueError, "No verification was queued"):
                planning.request(["test:digest", "--", "./internal/api", *flags], {})
        for flags in (["-p=2"], ["-parallel", "4"], ["-count=0"], ["-cpu=1,2,4"], ["-timeout=1h30m"],
                      ["-timeout", "0"], ["-timeout=1.5s"], ["-shuffle=on"], ["-shuffle=-7"]):
            with self.subTest(flags=flags):
                planning.validate_selection(["test:digest"], ["./internal/api", *flags])

    def test_selections_that_match_nothing_are_refused_before_admission(self):
        for names, args, message in (
                (["test:digest"], ["./internal/no-such-package/..."], "no package directory"),
                (["test:integration"], ["./internal/apii"], "no package directory"),
                (["test:digest"], ["./internal/api/no_such_test.go"], "no package directory"),
                (["den:test:digest"], ["src/no-such-module.test.ts"], "no test file"),
                (["den:harness:test"], ["e2e/no-such.spec.ts", "--retries=0"], "no web-project spec"),
                (["den:harness:test"], ["no-such-spec"], "no web-project spec")):
            with self.subTest(args=args), self.assertRaisesRegex(ValueError, message):
                planning.validate_selection(names, args)
        spec = next((planning.DEN / "e2e").glob("*.spec.ts")).name
        test = next((planning.DEN / "src").rglob("*.test.ts")).relative_to(planning.DEN).as_posix()
        for names, args in ((["test:digest"], ["./internal/api/...", "./..."]),
                            (["den:test:digest"], [test + ":12"]),
                            (["den:test:digest"], [test.upper()]),
                            (["den:test:digest"], ["src/"]),
                            (["den:harness:test"], [f"e2e/{spec}:40"]),
                            (["den:harness:test"], [spec.removesuffix(".spec.ts").upper()]),
                            (["den:harness:test"], ["/" + spec.replace(".", "\\.") + "/"]),
                            (["den:harness:test"], ["(unreadable"])):
            with self.subTest(args=args):
                planning.validate_selection(names, args)

    def test_declared_environment_and_checkout_identity_decide_compatibility(self):
        root = SCRIPT.parent.parent

        def key(environment, checkout=root):
            return planning.compatibility(checkout, SCRIPT, planning.execution_environment(environment))
        base = {"PATH": "one", "PW_TEST_TIMEOUT_SCALE": "2"}
        self.assertEqual(key(base), key({**base, "SHLVL": "2", "CLAUDE_CODE_SESSION_ID": "a", "TERM": "x"}))
        # The engine's own namespace is this run's state, so it never splits requests.
        self.assertEqual(key(base), key({**base, "LYCAON_DEBUG_SESSION_DIR": "/c/debug/boot-a"}))
        for env in ({**base, "PATH": "two"}, {**base, "PW_NEW_INPUT": "value"}, {**base, "GOFLAGS": "-tags=x"}):
            with self.subTest(env=env):
                self.assertNotEqual(key(base), key(env))
        self.assertNotEqual(key(base), key(base, root.parent))

    def test_only_declared_variables_reach_shared_execution(self):
        agent = {"CODEX_THREAD_ID": "one", "CLAUDE_CODE_SESSION_ID": "two", "CLAUDE_CODE_MESSAGING_TOKEN": "secret",
                 "SENTRY-TRACE": "trace", "SSH_AUTH_SOCK": "/socket", "TERM": "xterm"}
        declared = {"PATH": "/bin", "HOME": "/home/user", "GOFLAGS": "-mod=mod", "LC_ALL": "C",
                    "PW_TEST_WORKERS": "2", "UPDATE_SCHEMA_LOCK": "1"}
        scheduler = {"PW_TEST_RESOURCE_FD": "201", "PW_TEST_EXECUTION_TICKET": "1", "PW_TEST_STAGE_EVENTS": "x"}
        # This run's state and a live network route are not inputs to a shared stage.
        host = {"LYCAON_LOG_FILE": "/c/debug/a/sidecar.log", "HTTPS_PROXY": "http://127.0.0.1:51845"}
        self.assertEqual(planning.execution_environment({**agent, **declared, **scheduler, **host}), declared)

    def test_fifo_membership_and_batch_size_bound(self):
        def entry(key="a", kind="request", state="queued"):
            return {"compatibility": key, "kind": kind, "state": state, "ticket": key, "name": key,
                    "admission": {"locks": [], "workers": 1}}
        self.assertEqual(len(batch.batch_prefix([entry(), entry(), entry("b"), entry()], [])), 2)
        # Invocations are ordered through their own reservations, not by splitting a batch.
        self.assertEqual(len(batch.batch_prefix([entry(), entry(kind="invocation"), entry()], [])), 2)
        exclusive = {"ticket": "x", "name": "x", "state": "running", "locks": ["*"], "workers": 1}
        self.assertEqual(batch.batch_prefix([entry()], [exclusive]), [])
        self.assertEqual(len(batch.batch_prefix([entry()] * 100, [])), batch.MAX_REQUESTS)

    def test_exclusive_waiters_block_only_later_arrivals(self):
        active = {"ticket": "1-batch", "kind": "batch", "state": "running", "members": ["1"]}
        subscriber = {"ticket": "1", "kind": "request", "state": "queued", "compatibility": "a",
                      "name": "request", "admission": {"locks": [], "workers": 1}, "queued_at": 10}
        waiting = {**subscriber, "ticket": "2", "compatibility": "b", "queued_at": 30}
        self.assertEqual(batch.batch_prefix([subscriber, active, waiting], []), [waiting])
        exclusive = {"ticket": "x", "name": "exclusive", "state": "queued", "locks": ["*"], "workers": 1,
                     "order": 20 * 10 ** 9}
        self.assertEqual(batch.batch_prefix([subscriber, active, waiting], [exclusive]), [])
        self.assertEqual(batch.batch_prefix([subscriber, active, {**waiting, "queued_at": 15}], [exclusive]),
                         [{**waiting, "queued_at": 15}])
        self.assertEqual(batch.batch_prefix([active] * batch.MAX_ACTIVE_BATCHES + [waiting], []), [])

    def test_admission_passes_independent_work_but_not_an_earlier_conflicting_waiter(self):
        def request(ticket, locks, arrival):
            return {"ticket": ticket, "kind": "request", "state": "queued", "name": ticket, "queued_at": arrival,
                    "compatibility": ticket, "admission": {"locks": locks, "workers": 1}}
        busy = {"ticket": "active", "locks": ["frontend"], "workers": 1, "state": "running", "name": "typecheck"}
        exclusive = {"ticket": "exclusive", "name": "exclusive", "state": "queued", "locks": ["*"], "workers": 1,
                     "order": 15 * 10 ** 9}
        frontend, go = request("1", ["frontend"], 10), request("2", ["go"], 20)
        with patch.object(resources, "capacity", return_value=6):
            self.assertEqual(batch.batch_prefix([frontend, go], [busy]), [go])
            self.assertEqual(batch.batch_prefix([frontend, go], [busy, exclusive]), [])
            self.assertEqual(batch.batch_prefix([request("1", ["*"], 10), go], [busy]), [])
            self.assertEqual(batch.batch_prefix([frontend, request("2", ["frontend"], 20)], [busy]), [])

    def test_a_request_is_admissible_when_any_of_its_stages_could_start(self):
        busy = {"ticket": "active", "locks": ["frontend"], "workers": 1, "state": "running", "name": "typecheck"}
        gate = {"ticket": "1", "kind": "request", "state": "queued", "name": "gate", "compatibility": "a",
                "queued_at": 1, "admissions": [{"locks": ["frontend"], "workers": 1}, {"locks": [], "workers": 1}],
                "admission": {"locks": ["frontend"], "workers": 1}}
        with patch.object(resources, "capacity", return_value=6):
            self.assertEqual(batch.batch_prefix([gate], [busy]), [gate])
            self.assertEqual(batch.batch_prefix([{**gate, "admissions": gate["admissions"][:1]}], [busy]), [])

    def test_backfill_passes_a_waiter_only_within_its_expected_start(self):
        now = 1000.0
        running = {"ticket": "r", "name": "lint", "state": "running", "locks": ["lint"], "workers": 3,
                   "started_at": now - 60, "estimate": 600, "order": 1}
        waiter = {"ticket": "w", "name": "exclusive", "state": "queued", "locks": ["*"], "workers": 6, "order": 2}
        with patch.object(resources, "capacity", return_value=6):
            short = {"ticket": "c", "name": "short", "state": "queued", "locks": [], "workers": 1, "order": 3}
            self.assertIsNone(resources.blocked_reason({**short, "estimate": 120}, [running, waiter], now))
            self.assertIn("earlier operation", resources.blocked_reason({**short, "estimate": 900}, [running, waiter], now))
            self.assertIn("earlier operation", resources.blocked_reason(short, [running, waiter], now))
            overrun = {**running, "started_at": now - 700}
            self.assertIn("earlier operation", resources.blocked_reason({**short, "estimate": 1}, [overrun, waiter], now))
            unmeasured = {**running, "estimate": None}
            self.assertIn("earlier operation", resources.blocked_reason({**short, "estimate": 1}, [unmeasured, waiter], now))
            earlier = {**short, "order": 1, "ticket": "a", "estimate": 900}
            self.assertIsNone(resources.blocked_reason(earlier, [running, waiter], now))

    def test_capacity_shadow_uses_the_order_running_work_is_expected_to_finish(self):
        now = 1000.0
        first = {"ticket": "a", "name": "a", "state": "running", "locks": [], "workers": 2, "started_at": now,
                 "estimate": 100, "order": 1}
        second = {**first, "ticket": "b", "name": "b", "estimate": 500}
        waiter = {"ticket": "w", "name": "wide", "state": "queued", "locks": [], "workers": 4, "order": 2}
        with patch.object(resources, "capacity", return_value=6):
            self.assertEqual(resources.shadow_start(waiter, [first, second], now), now + 100)
            candidate = {"ticket": "c", "name": "c", "state": "queued", "locks": [], "workers": 1, "order": 3}
            self.assertIsNone(resources.blocked_reason({**candidate, "estimate": 50}, [first, second, waiter], now))
            self.assertIsNotNone(resources.blocked_reason({**candidate, "estimate": 150}, [first, second, waiter], now))

    def test_package_evidence_preserves_independent_failures_and_incomplete_results(self):
        raw = '\n'.join(json.dumps(event) for event in [
            {"Package": "a", "Action": "pass"}, {"Package": "b", "Action": "fail"},
            {"Package": "c", "Test": "TestC", "Action": "pass"},
            {"Package": "d", "Action": "skip"}])
        result = package_results(raw, 1, 1)
        self.assertTrue(result["completed"])
        self.assertEqual(result["packages"], {"a": "pass", "b": "fail", "d": "skip"})
        self.assertFalse(package_results(raw, 124, 1)["completed"])
        self.assertFalse(package_results('{"Package":"a","Action":"pass"}', 1, 1)["completed"])
        for raw in ("", "[]", '{"Package":[],"Action":"pass"}', '{"Package":"a","Action":[]}'):
            with self.subTest(raw=raw):
                self.assertFalse(package_results(raw, 0, 0)["completed"])

    def test_a_large_follower_does_not_expand_a_small_oldest_request_without_bound(self):
        stage = planning.expand(["test:digest"])[0]
        oldest = ({}, stage, ["a"])
        large = ({}, stage, [str(i) for i in range(100)])
        small = ({}, stage, ["b"])
        self.assertEqual(shared_peers([oldest, large, small]), [oldest, small])

    @unittest.skipIf(os.name == "nt", "requires POSIX file descriptions")
    def test_opening_the_active_lease_does_not_inherit_its_admission(self):
        with tempfile.TemporaryDirectory() as directory:
            queue = execution.Queue(directory)
            with execution.Lease(queue, "ownership fixture", 1) as lease:
                lease.entry.update(state="running", pid=987654321, lease_fd=200)
                lease.write()
                body = ("import os, sys; "
                        f"fd=os.open({str(lease.path)!r},os.O_RDWR); os.dup2(fd,200,inheritable=True); "
                        f"os.execv(sys.executable,[sys.executable,{str(SCRIPT)!r},'holding'])")
                result = subprocess.run([sys.executable, "-c", body], capture_output=True, timeout=5,
                                        env={**os.environ, "PW_TEST_EXECUTION_ROOT": directory,
                                             "PW_TEST_EXECUTION_TICKET": lease.entry["ticket"]})
                self.assertEqual(result.returncode, 1, result.stderr.decode())

    def test_abandoned_batch_recovers_captured_plan_and_completed_evidence(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary) / "batch"
            directory.mkdir()
            queue = execution.Queue(Path(temporary) / "queue")
            request = {"ticket": "request", "source": temporary,
                       "plan": {"names": ["gate"], "stages": [{"name": "obsolete"}]}}
            entry = {"ticket": "batch", "kind": "batch", "directory": str(directory)}
            write_json(directory / "plan.json", {"requests": [request]})
            request["plan"]["stages"] = [{"name": "build"}, {"name": "lint"}]
            write_json(directory / "resolved-plan.json", [request])
            write_json(directory / "source.json", {"source_commit": "captured"})
            write_json(directory / "request.progress.json", {"completed": [0], "evidence": [{"stage": "build"}]})
            write_json(queue.root / "batch.lease", entry)
            self.assertEqual(queue.status()["runs"], [])
            receipt = read_json(directory / "request.json")
            self.assertEqual(receipt["status"], "unverified")
            self.assertEqual(receipt["source_commit"], "captured")
            self.assertEqual(receipt["unverified_stages"], ["lint"])
            self.assertEqual(receipt["evidence"], [{"stage": "build"}])
            self.assertTrue((directory / "finished.json").is_file())
