import copy
import json
from pathlib import Path
import unittest
from unittest.mock import patch

from ci_policy import hosted_plan as ci
import verification_plan as planning


class HostedPlanTests(unittest.TestCase):
    def test_native_admission_cannot_omit_targets_or_accept_unsafe_roots(self):
        for declaration in (None, {}, [], {'den:test:rust': ['lycaon-den/src-tauri/']},
                {'den:test:rust': [], 'decide:test': ['lycaon/internal/decide/native/']},
                {'den:test:rust': ['../outside/'], 'decide:test': ['lycaon/internal/decide/native/']}):
            data = copy.deepcopy(planning.catalog())
            data['ci']['ready']['compile_targets'] = declaration
            with patch.object(ci, 'catalog', return_value=data), self.assertRaises(ValueError):
                ci.matrix('fast')

    def test_tier_partitions_cover_every_local_gate_stage_once(self):
        lanes = ci.lanes()
        expected = sorted(stage["name"] for stage in planning.expand(["check"]))
        actual = sorted(stage["name"] for lane in lanes.values() if "check" in lane["profiles"]
                        for stage in planning.expand(lane["targets"]))
        self.assertEqual(actual, expected)
        check = {row["lane"] for row in ci.matrix("check")["include"]}
        release = {row["lane"] for row in ci.matrix("release")["include"]}
        platform = {name for name, lane in lanes.items() if "platform" in lane}
        # Releases gate on whether the product works; style, tooling, and the deep tiers run elsewhere.
        self.assertLessEqual(release, check | platform)
        self.assertEqual(release, {"build", "contracts", "behavior", "frontend", "native", "vulnerabilities",
                                   "webkit-gtk", "confinement-arm64", "reference-userlands"})
        # Qualification runs the check gate, then release's platform lanes beside the platform gates.
        self.assertEqual({row["lane"] for row in ci.matrix("release", platform=True)["include"]}, release & platform)

    def test_ready_tier_is_one_runner_with_contracts_and_early_diagnostics(self):
        rows = ci.matrix("fast")["include"]
        self.assertEqual(len(rows), 1)
        self.assertEqual(ci.max_parallel("fast"), 1)
        stages = [stage["name"] for stage in planning.expand(ci.lanes()[rows[0]["lane"]]["targets"])]
        self.assertEqual(sorted(stages), sorted(set(stages)))
        self.assertEqual(set(stages), {"build", "lint:full", "budgets", "den:typecheck", "den:lint", "den:test:fast",
                                       "coverage:changes", "den:coverage:changes", "scripts:coverage:changes",
                                       "comments:check", "test:fail-messages:check", "docs:links"})
        self.assertEqual(ci.lanes()["ready"]["preflight"], ["test:contract", "--", "./test/contract/..."])
        handoff = {stage["name"] for stage in planning.expand(["check-fast"])}
        self.assertLessEqual(set(stages) | {"test:contract"}, handoff)
        # Changed coverage of every language admits here: a shortfall found only in the
        # merge group ejects the group and rebuilds the one behind it.
        self.assertLessEqual({"coverage:changes", "den:coverage:changes", "scripts:coverage:changes"}, set(stages))
        # Product suites remain in the integration gate.
        self.assertFalse(set(stages) & {"test:short", "test:contract"})
        self.assertGreaterEqual(rows[0]["minutes"], ci.lanes()["lint"]["minutes"])

    def test_partition_drift_refuses_to_plan_before_any_tests_run(self):
        for mutation in ("missing", "fast-outside", "fast-twice", "duplicate", "unknown", "unbounded"):
            with self.subTest(mutation=mutation):
                data = copy.deepcopy(planning.catalog())
                if mutation == "missing":
                    del data["ci"]["frontend"]
                elif mutation == "fast-outside":
                    data["ci"]["ready"]["targets"].append("test:full")
                elif mutation == "fast-twice":
                    data["ci"]["ready"]["targets"].append("budgets")
                elif mutation == "duplicate":
                    data["ci"]["duplicate"] = data["ci"]["frontend"]
                elif mutation == "unknown":
                    data["ci"]["frontend"]["targets"] = ["not:a:task"]
                else:
                    data["ci"]["frontend"]["minutes"] = 360
                with patch.object(ci, "catalog", return_value=data), self.assertRaises(ValueError):
                    ci.matrix("check")

    def test_admission_refuses_invalid_package_selection(self):
        for mutation in ("missing", "target", "scope", "empty", "sharded", "selector", "traversal", "duplicate", "mixed", "absent"):
            with self.subTest(mutation=mutation):
                data = copy.deepcopy(planning.catalog())
                lane = data["ci"]["ready"]
                if mutation == "missing":
                    del lane["preflight"]
                elif mutation == "target":
                    lane["preflight"][0] = "test:full"
                elif mutation == "scope":
                    lane["preflight"][2] = "./..."
                elif mutation == "empty":
                    lane["preflight"] = ["test:contract", "--"]
                elif mutation == "sharded":
                    lane["shards"] = 2
                elif mutation == "selector":
                    lane["preflight"] += ["-run", "^TestMissing"]
                elif mutation == "traversal":
                    lane["preflight"][2] = "./test/contract/../internal"
                elif mutation == "duplicate":
                    lane["preflight"].append(lane["preflight"][2])
                elif mutation == "mixed":
                    lane["preflight"].append("./test/contract/host")
                else:
                    lane["preflight"][2] = "./test/contract/absent"
                with patch.object(ci, "catalog", return_value=data), self.assertRaises(ValueError):
                    ci.matrix("fast")

    def test_admission_accepts_existing_contract_packages_without_name_filters(self):
        data = copy.deepcopy(planning.catalog())
        data["ci"]["ready"]["preflight"] = ["test:contract", "--", "./test/contract/evidence"]
        with patch.object(ci, "catalog", return_value=data):
            self.assertEqual(len(ci.matrix("fast")["include"]), 1)

    def test_nightly_selection_includes_only_requested_and_unconditional_jobs(self):
        lanes = ci.lanes()
        nightly = {target for row in ci.matrix("nightly")["include"] for target in lanes[row["lane"]]["targets"]}
        self.assertEqual(nightly, {"test:full", "test:race", "test:fuzz", "test:stress", "check:coverage",
                                   "den:coverage-check", "den:test:transcript-scale", "lint:vuln:fresh",
                                   "perf:bench", "perf:sidecar", "perf:soak", "den:webkit:scroll",
                                   "den:webkit:atspi", "test:confine:linux", "nix:check", "test:userlands", "test:conformance:live",
                                   "budgets", "coverage:changes", "den:coverage:changes", "scripts:coverage:changes",
                                   "build", "build:cross", "licenses:notices", "check:drift", "openapi:lint",
                                   "validate:workflows", "oar:verify", "lint:full", "test:fail-messages:check",
                                   "comments:check", "lint:vuln", "test:runner", "den:typecheck", "den:lint",
                                   "den:test", "den:test:rust", "decide:test"})
        for suite in ci.SUITES - {"all"}:
            with self.subTest(suite=suite):
                names = {row["lane"] for row in ci.matrix("nightly", suite)["include"]}
                expected = {name for name, lane in lanes.items() if "nightly" in lane["profiles"]
                            and (lane["suite"] == suite or lane["suite"] == "all" and "check" not in lane["profiles"])}
                self.assertEqual(names, expected)
                self.assertIn("vulnerability-freshness", names)
        for profile, suite in [("unknown", "all"), ("release", "race"), ("nightly", "typo")]:
            with self.assertRaises(ValueError):
                ci.matrix(profile, suite)

    def test_full_nightly_covers_checks_without_widening_partial_suites(self):
        common_checks = {"limits", "build", "contracts", "lint", "vulnerabilities", "runner", "frontend", "native"}
        full = {row["lane"] for row in ci.matrix("nightly", "all")["include"]}
        self.assertLessEqual(common_checks | {"behavior"}, full)
        for suite in ("performance", "e2e"):
            with self.subTest(suite=suite):
                partial = {row["lane"] for row in ci.matrix("nightly", suite)["include"]}
                self.assertFalse(common_checks & partial)
                self.assertIn("vulnerability-freshness", partial)
        behavior = {row["lane"] for row in ci.matrix("nightly", "behavior")["include"]}
        self.assertIn("behavior", behavior)

    def test_daily_plan_rejects_missing_qualification_coverage(self):
        for name, lane in ci.lanes().items():
            if "check" not in lane["profiles"] and not ("release" in lane["profiles"] and "platform" in lane):
                continue
            data = copy.deepcopy(planning.catalog())
            data["ci"][name]["profiles"].remove("nightly")
            with self.subTest(lane=name), patch.object(ci, "catalog", return_value=data), self.assertRaises(ValueError):
                ci.lanes()

    def test_host_platform_and_time_budgets_are_explicit(self):
        for profile in ci.PROFILES:
            for row in ci.matrix(profile)["include"]:
                with self.subTest(profile=profile, lane=row["lane"]):
                    self.assertEqual(row["job_minutes"] - row["minutes"], 30)
                    self.assertLess(row["job_minutes"], 360)
                    # macOS hosts only the lanes that test macOS-specific behavior, and arm64 only
                    # architecture-specific confinement.
                    runner = {"webkit": "macos-15", "confinement-arm64": "ubuntu-24.04-arm"}.get(row["lane"], "ubuntu-24.04")
                    self.assertEqual(row["runner"], runner)

    def test_linux_verification_uses_the_declared_release_build_baseline(self):
        root = Path(__file__).resolve().parents[3]
        platforms = json.loads((root / "packaging/release-platforms.json").read_text())["platforms"]
        linux_runners = {platform["runner"] for platform in platforms if platform["goos"] == "linux"}
        for profile in ci.PROFILES:
            for platform in (False, True):
                if platform and not any(profile in lane["profiles"] and "platform" in lane for lane in ci.lanes().values()):
                    continue
                for row in ci.matrix(profile, platform=platform)["include"]:
                    if row["runner"].startswith("ubuntu"):
                        with self.subTest(profile=profile, platform=platform, lane=row["lane"]):
                            self.assertIn(row["runner"], linux_runners)

    def test_each_lane_installs_only_its_declared_setup(self):
        setups = {row["lane"]: row["setup"] for row in ci.matrix("check")["include"]}
        self.assertEqual(setups["native"], "shell")
        self.assertEqual(setups["frontend"], "frontend")
        # The WebKit harness builds without the shell, so the lane skips its packaging inputs.
        nightly = {row["lane"]: row["setup"] for row in ci.matrix("nightly", "e2e")["include"]}
        self.assertEqual(nightly["webkit"], "harness")
        self.assertEqual({setup for lane, setup in setups.items() if lane not in {"native", "webkit", "frontend", "lint", "vulnerabilities"}},
                         {"verification"})
        for mutation in ("unknown", "missing", "boolean"):
            with self.subTest(mutation=mutation):
                data = copy.deepcopy(planning.catalog())
                lane = data["ci"]["webkit"]
                if mutation == "unknown":
                    lane["setup"] = "everything"
                elif mutation == "missing":
                    del lane["setup"]
                else:
                    del lane["setup"]
                    lane["native"] = True
                with patch.object(ci, "catalog", return_value=data), self.assertRaises(ValueError):
                    ci.matrix("check")

    def test_frontend_setup_refuses_go_or_shell_targets(self):
        for target in ("build", "decide:test", "den:test:rust", "lint:full"):
            with self.subTest(target=target):
                data = copy.deepcopy(planning.catalog())
                data["ci"]["frontend"]["targets"].append(target)
                with patch.object(ci, "catalog", return_value=data), self.assertRaises(ValueError):
                    ci.matrix("check")

    def test_e2e_leaves_the_check_gate_and_has_an_explicit_nightly_selection(self):
        gate = [stage["name"] for stage in planning.expand(["check"])]
        self.assertFalse({"den:webkit:scroll", "den:webkit:atspi"} & set(gate))
        self.assertFalse({"webkit", "webkit-gtk"} & {row["lane"] for row in ci.matrix("check")["include"]})
        self.assertEqual({row["lane"] for row in ci.matrix("nightly", "e2e")["include"]},
                         {"webkit", "webkit-gtk", "vulnerability-freshness"})
        # Only a platform lane takes an end-to-end check into merge admission.
        admitted = {row["lane"] for row in ci.matrix("integration")["include"]}
        self.assertEqual({name for name in admitted if ci.lanes()[name]["suite"] == "e2e"}, {"webkit-gtk"})

    def test_platform_lanes_run_beside_the_check_gate_and_never_inside_it(self):
        self.assertEqual({ci.lanes()[name]["platform"] for name in ("webkit-gtk", "confinement-arm64")}, {"linux"})
        for mutation in ("unknown", "macos", "windows", "check", "fast", "repeats"):
            with self.subTest(mutation=mutation):
                data = copy.deepcopy(planning.catalog())
                platform = data["ci"]["webkit-gtk"]
                if mutation == "unknown":
                    platform["platform"] = "windows"
                elif mutation == "macos":
                    platform["runner"] = "macos-15"
                elif mutation == "windows":
                    platform["runner"] = "windows-latest"
                elif mutation in {"check", "fast"}:
                    platform["profiles"].append(mutation)
                else:
                    platform["targets"].append("den:test")
                with patch.object(ci, "catalog", return_value=data), self.assertRaises(ValueError):
                    ci.lanes()

    def test_offline_lanes_learn_which_container_images_they_need(self):
        for profile in ci.PROFILES:
            for row in ci.matrix(profile)["include"]:
                with self.subTest(profile=profile, lane=row["lane"]):
                    self.assertEqual(row["containers"], "den:webkit:atspi" in ci.lanes()[row["lane"]]["targets"])
        self.assertTrue([row for row in ci.matrix("integration")["include"] if row["containers"]])

    def test_offline_lanes_learn_whether_they_need_the_pinned_browser(self):
        for profile in ci.PROFILES:
            for row in ci.matrix(profile)["include"]:
                with self.subTest(profile=profile, lane=row["lane"]):
                    self.assertEqual(row["browser"], "test:confine:linux" in ci.lanes()[row["lane"]]["targets"])
                    targets = ci.lanes()[row["lane"]]["targets"]
                    self.assertEqual(row["linux_launch"], any(ci.runs_protected_launch(target) for target in targets))
        self.assertEqual([row["lane"] for row in ci.matrix("integration")["include"] if row["browser"]], ["confinement-arm64"])

    def test_lanes_learn_whether_they_need_the_reference_userlands(self):
        for profile in ci.PROFILES:
            for row in ci.matrix(profile)["include"]:
                with self.subTest(profile=profile, lane=row["lane"]):
                    self.assertEqual(row["userlands"], "test:userlands" in ci.lanes()[row["lane"]]["targets"])
        for profile in ("integration", "nightly", "release"):
            with self.subTest(profile=profile):
                self.assertEqual([row["lane"] for row in ci.matrix(profile)["include"] if row["userlands"]],
                                 ["reference-userlands"])

    def test_lanes_that_reach_the_protected_launch_leg_provision_its_display(self):
        # Catalog recipes over the whole tree reach internal/launchguard; scripted runs declare it.
        for target in ("test:full", "test:race", "check:coverage", "coverage:changes", "test:confine:linux"):
            with self.subTest(target=target):
                self.assertTrue(ci.runs_protected_launch(target))
        for target in ("test:stress", "test:fuzz", "perf:bench", "build", "lint:full"):
            with self.subTest(target=target):
                self.assertFalse(ci.runs_protected_launch(target))
        nightly = {row["lane"] for row in ci.matrix("nightly")["include"] if row["linux_launch"]}
        self.assertTrue({"go-coverage", "race"} <= nightly, nightly)
        data = copy.deepcopy(planning.catalog())
        data["go"]["test:race"]["packages"] = ["./internal/db/...", "./test/wiring"]
        with patch.object(ci, "catalog", return_value=data):
            self.assertFalse(ci.runs_protected_launch("test:race"))
            data["go"]["test:race"]["packages"].append("./internal/launchguard")
            self.assertTrue(ci.runs_protected_launch("test:race"))

    def test_lanes_learn_whether_they_drive_the_native_repository_tools(self):
        # Without a scope (a dispatched run) the coverage target resolves its own change base and may run every
        # scripts suite, so every lane carrying it provisions the tools alongside the runner suite.
        for profile in ci.PROFILES:
            for row in ci.matrix(profile)["include"]:
                with self.subTest(profile=profile, lane=row["lane"]):
                    self.assertEqual(row["repository_tools"],
                                     bool({"test:runner", "scripts:coverage:changes"} & set(ci.lanes()[row["lane"]]["targets"])))
        self.assertTrue(any(row["lane"] == "ready" and row["repository_tools"] for row in ci.matrix("fast")["include"]))
        self.assertIn("runner", [row["lane"] for row in ci.matrix("check")["include"] if row["repository_tools"]])

        scope = {"base": "base", "head": "head", "paths": ["scripts/ci_policy/hosted_plan.py"], "full": False}
        self.assertEqual([row["lane"] for row in ci.matrix("fast", scope=scope)["include"]
                          if row["repository_tools"]], ["ready"])
        scope["paths"] = ["lycaon/internal/settings/settings.go"]
        self.assertFalse(any(row["repository_tools"] for row in ci.matrix("fast", scope=scope)["include"]))
        # A broad scope runs every scripts suite, and may carry no paths at all.
        for broad in ({"full": True}, {"base": "", "head": "head", "paths": [], "full": True}):
            for profile in ("fast", "integration"):
                with self.subTest(profile=profile, scope=broad):
                    self.assertEqual(
                        sorted(row["lane"] for row in ci.matrix(profile, scope=broad)["include"] if row["repository_tools"]),
                        sorted(row["lane"] for row in ci.matrix(profile, scope=broad)["include"]
                               if {"test:runner", "scripts:coverage:changes"} & set(ci.lanes()[row["lane"]]["targets"])))

    def test_nix_lane_is_nightly_only_and_provisions_nix_before_running_offline(self):
        # Nix verification informs; it never gates a merge or a release.
        self.assertEqual(ci.lanes()["nix"]["profiles"], ["nightly"])
        for profile in ("check", "integration", "release"):
            with self.subTest(profile=profile):
                self.assertNotIn("nix", {row["lane"] for row in ci.matrix(profile)["include"]})
        rows = [row for row in ci.matrix("nightly")["include"] if row["lane"] == "nix"]
        self.assertEqual([(row["nix"], row["browser"]) for row in rows], [(True, False)])

    def test_lane_arguments_are_explicit(self):
        for mutation in ("arguments-many-targets", "arguments-empty", "paths"):
            with self.subTest(mutation=mutation):
                data = copy.deepcopy(planning.catalog())
                if mutation == "arguments-many-targets":
                    data["ci"]["frontend"]["arguments"] = ["browser"]
                elif mutation == "arguments-empty":
                    data["ci"]["confinement-arm64"]["arguments"] = []
                else:
                    data["ci"]["nix"]["paths"] = ["packaging/nix/"]
                with patch.object(ci, "catalog", return_value=data), self.assertRaises(ValueError):
                    ci.matrix("nightly")
        data = copy.deepcopy(planning.catalog())
        data["ci"]["confinement-arm64"]["arguments"] = ["nix"]
        with patch.object(ci, "catalog", return_value=data):
            row = next(row for row in ci.matrix("nightly")["include"] if row["lane"] == "confinement-arm64")
        self.assertEqual((row["nix"], row["browser"]), (True, False))

    def test_platform_selection_keeps_only_platform_lanes(self):
        for profile in ("integration", "nightly", "release"):
            with self.subTest(profile=profile):
                rows = ci.matrix(profile, platform=True)["include"]
                self.assertTrue(rows)
                self.assertTrue(all("platform" in ci.lanes()[row["lane"]] for row in rows))
        scope = dict(full=False, paths=["lycaon/internal/confine/boundary_linux.go"])
        self.assertEqual({row["lane"] for row in ci.matrix("integration", scope=scope, platform=True)["include"]},
                         {"confinement-arm64"})
        with self.assertRaises(ValueError):
            ci.matrix("check", platform=True)

    def test_each_profile_caps_the_runners_its_matrix_holds(self):
        caps = planning.catalog()["capacity"]["max_parallel"]
        for profile in ci.PROFILES:
            with self.subTest(profile=profile):
                self.assertEqual(ci.max_parallel(profile), caps[profile])
        with self.assertRaises(ValueError):
            ci.max_parallel("unknown")
        for mutation in ("missing", "zero", "fraction"):
            with self.subTest(mutation=mutation):
                data = copy.deepcopy(planning.catalog())
                if mutation == "missing":
                    del data["capacity"]["max_parallel"]["nightly"]
                else:
                    data["capacity"]["max_parallel"]["nightly"] = 0 if mutation == "zero" else 1.5
                with patch.object(ci, "catalog", return_value=data), self.assertRaises(ValueError):
                    ci.max_parallel("check")

    def test_capped_matrix_starts_cheap_frequent_failures_then_its_longest_lanes(self):
        lanes = ci.lanes()
        rows = ci.matrix("integration")["include"]
        self.assertEqual({row["lane"] for row in rows[:2]}, {"limits", "lint"})
        self.assertEqual([row["lane"] for row in rows[2:4]], ["behavior", "behavior"])
        for profile in ci.PROFILES:
            rows = ci.matrix(profile)['include']
            ordinary = [row for row in rows if row['priority'] != 'compile']
            order = [(ci.PRIORITIES[row['priority']], -row['minutes']) for row in ordinary]
            self.assertEqual(order, sorted(order))
        data = copy.deepcopy(planning.catalog())
        data["ci"]["limits"]["priority"] = "invalid"
        with patch.object(ci, "catalog", return_value=data), self.assertRaises(ValueError):
            ci.matrix("integration")

    def test_combined_analysis_targets_restore_both_tool_sets(self):
        self.assertEqual(ci.analysis_set(["lint:full", "lint:vuln"]), "all")

    def test_lane_setup_restores_only_the_required_tool_sets(self):
        for profile in ci.PROFILES:
            for row in ci.matrix(profile)["include"]:
                targets = ci.lanes()[row["lane"]]["targets"]
                self.assertEqual(row["notices"], "licenses:notices" in targets)
                if row["analysis"] == "lint":
                    self.assertTrue(set(targets) & {"lint:fast", "lint:full"})
                elif row["analysis"] == "vulnerabilities":
                    self.assertTrue(set(targets) & {"lint:vuln", "lint:vuln:fresh"})
                else:
                    self.assertEqual(row["analysis"], "none")
        self.assertEqual(len([row for row in ci.matrix("check")["include"] if row["notices"]]), 1)
        # Notices run in the merge queue, not the ready pull request tier.
        self.assertFalse([row for row in ci.matrix("fast")["include"] if row["notices"]])
