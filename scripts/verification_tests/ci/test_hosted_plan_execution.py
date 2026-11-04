import copy, json, unittest
from pathlib import Path
from unittest.mock import patch

from ci_policy import hosted_plan as ci
import verification_plan as planning


class HostedPlanTests(unittest.TestCase):
    def catalog_patch(self, mutate=None):
        data = copy.deepcopy(planning.catalog())
        if mutate:
            mutate(data)
        return patch.object(ci, 'catalog', return_value=data)

    def test_native_admission_cannot_omit_targets_or_accept_unsafe_roots(self):
        for declaration in (None, [], {'unknown:test': ['lycaon-den/src-tauri/']},
                {'den:test:rust': [], 'decide:test': ['lycaon/internal/decide/native/']},
                {'den:test:rust': ['../outside/'], 'decide:test': ['lycaon/internal/decide/native/']}):
            with self.catalog_patch(lambda d: d['ci']['ready'].update(compile_targets=declaration)), self.assertRaises(ValueError):
                ci.matrix('fast')

    def test_tier_partitions_cover_every_local_gate_stage_once(self):
        lanes = ci.lanes()
        expected = sorted(stage["name"] for stage in planning.expand(["check"]))
        actual = sorted(stage["name"] for lane in lanes.values() if "check" in lane["profiles"]
                        for stage in planning.expand(lane["targets"]))
        self.assertEqual(actual, expected)
        check = {row["lane"] for row in ci.matrix("check")["include"]}
        release = {row["lane"] for row in ci.matrix("release")["include"]}
        platform = {name for name, lane in lanes.items() if ci.supplemental(lane)}
        # Releases gate on whether the product works; style, tooling, and the deep tiers run elsewhere.
        self.assertLessEqual(release, check | platform)
        self.assertEqual(release, {"build", "behavior", "frontend", "native", "vulnerabilities",
                                   "webkit-gtk", "confinement-arm64", "reference-userlands", "command-scale"})
        # Qualification runs the check gate, then release's platform lanes beside the platform gates.
        self.assertEqual({row["lane"] for row in ci.matrix("release", platform=True)["include"]}, release & platform)

    def test_fast_tier_runs_each_handoff_stage_once(self):
        rows = ci.matrix("fast")["include"]
        self.assertEqual({row["lane"] for row in rows}, {"ready", "ready-tests"})
        stages = [stage["name"] for name in {row["lane"] for row in rows}
                  for stage in planning.expand(ci.lanes()[name]["targets"])]
        self.assertEqual(sorted(stages), sorted(set(stages)))
        handoff = {stage["name"] for stage in planning.expand(["check-fast"])}
        self.assertEqual(set(stages), handoff - {"licenses:notices"})

    def test_script_coverage_keeps_repository_tools_without_host_runtime_engines(self):
        for profile in ('check', 'integration', 'nightly'):
            rows = ci.matrix(profile)['include']
            runner = next(row for row in rows if row['lane'] == 'runner')
            self.assertFalse(runner['runtime_engines'])
            self.assertTrue(runner['repository_tools'])
            behavior = next(row for row in rows if row['lane'] == 'behavior')
            self.assertTrue(behavior['runtime_engines'])
        for targets in (['test:full'], ['scripts:coverage:changes', 'test:full']):
            with self.subTest(targets=targets), self.catalog_patch(lambda d: d['ci']['runner'].update(targets=targets)):
                with self.assertRaisesRegex(ValueError, 'requires runtime engines'):
                    ci.lanes()

    def test_scale_cases_run_at_landing_and_release_and_partition_nightly_once(self):
        lanes = ci.lanes()
        expected = set(planning.catalog()['go']['test:stress']['packages'])
        selected = [package for lane in lanes.values() if lane['targets'] == ['test:stress']
                    and 'nightly' in lane['profiles'] for package in lane['arguments']]
        self.assertEqual(set(selected), expected)
        self.assertEqual(len(selected), len(expected))
        for scope in (None, {'full': True}, {'full': False, 'paths': ['lycaon/internal/session/a.go']}):
            self.assertIn('command-scale', {row['lane'] for row in ci.matrix('integration', scope=scope)['include']})
        self.assertIn('command-scale', {row['lane'] for row in ci.matrix('release', platform=True)['include']})
        self.assertNotIn('command-scale', {row['lane'] for row in ci.matrix('fast')['include']})
        self.assertNotIn('command-scale', {row['lane'] for row in ci.matrix('integration',
                         scope={'full': False, 'paths': ['lycaon-den/src/ui.ts']})['include']})
        with self.catalog_patch(lambda d: d['ci']['stress']['arguments'].append(d['ci']['command-scale']['arguments'][0])), \
                self.assertRaisesRegex(ValueError, 'stress partition'):
            ci.lanes()

    def test_partition_drift_refuses_to_plan_before_any_tests_run(self):
        mutations = {
            "missing": lambda d: d["ci"].pop("frontend"),
            "fast-outside": lambda d: d["ci"]["ready"]["targets"].append("test:full"),
            "fast-twice": lambda d: d["ci"]["ready"]["targets"].append("budgets"),
            "fast-missing": lambda d: d["ci"].pop("ready-lint"),
            "duplicate": lambda d: d["ci"].update(duplicate=d["ci"]["frontend"]),
            "unknown": lambda d: d["ci"]["frontend"].update(targets=["not:a:task"]),
            "unbounded": lambda d: d["ci"]["frontend"].update(minutes=360),
        }
        for mutation, mutate in mutations.items():
            with self.subTest(mutation=mutation), self.catalog_patch(mutate), self.assertRaises(ValueError):
                ci.matrix("check")

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
                                   "comments:check", "lint:vuln", "den:typecheck", "den:lint",
                                   "den:test:rust", "decide:test"})
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
        common_checks = {"limits", "build", "lint", "vulnerabilities", "runner", "frontend", "native"}
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
            with self.subTest(lane=name), self.catalog_patch(lambda d: d["ci"][name]["profiles"].remove("nightly")), self.assertRaises(ValueError):
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
        mutations = {
            "unknown": lambda lane: lane.update(setup="everything"),
            "missing": lambda lane: lane.pop("setup"),
            "boolean": lambda lane: (lane.pop("setup"), lane.update(native=True)),
        }
        for mutation, mutate in mutations.items():
            with self.subTest(mutation=mutation), self.catalog_patch(lambda d: mutate(d["ci"]["webkit"])), self.assertRaises(ValueError):
                ci.matrix("check")

    def test_frontend_setup_refuses_go_or_shell_targets(self):
        for target in ("build", "decide:test", "den:test:rust", "lint:full"):
            with self.subTest(target=target), self.catalog_patch(lambda d: d["ci"]["frontend"]["targets"].append(target)), self.assertRaises(ValueError):
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
        mutations = {
            "unknown": lambda p: p.update(platform="windows"),
            "macos": lambda p: p.update(runner="macos-15"),
            "windows": lambda p: p.update(runner="windows-latest"),
            "check": lambda p: p["profiles"].append("check"),
            "fast": lambda p: p["profiles"].append("fast"),
            "repeats": lambda p: p["targets"].append("den:coverage:changes"),
        }
        for mutation, mutate in mutations.items():
            with self.subTest(mutation=mutation), self.catalog_patch(lambda d: mutate(d["ci"]["webkit-gtk"])), self.assertRaises(ValueError):
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
        self.assertFalse(any(row["repository_tools"] for row in ci.matrix("fast")["include"]))
        self.assertIn("runner", [row["lane"] for row in ci.matrix("check")["include"] if row["repository_tools"]])

        scope = {"base": "base", "head": "head", "paths": ["scripts/ci_policy/hosted_plan.py"], "full": False}
        self.assertEqual([row["lane"] for row in ci.matrix("fast", scope=scope)["include"]
                          if row["repository_tools"]], [])
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
        mutations = {
            "arguments-many-targets": lambda d: d["ci"]["frontend"].update(arguments=["browser"]),
            "arguments-empty": lambda d: d["ci"]["confinement-arm64"].update(arguments=[]),
            "paths": lambda d: d["ci"]["nix"].update(paths=["packaging/nix/"]),
        }
        for mutation, mutate in mutations.items():
            with self.subTest(mutation=mutation), self.catalog_patch(mutate), self.assertRaises(ValueError):
                ci.matrix("nightly")
        with self.catalog_patch(lambda d: d["ci"]["confinement-arm64"].update(arguments=["nix"])):
            row = next(row for row in ci.matrix("nightly")["include"] if row["lane"] == "confinement-arm64")
        self.assertEqual((row["nix"], row["browser"]), (True, False))

    def test_platform_selection_keeps_only_platform_lanes(self):
        for profile in ("integration", "nightly", "release"):
            with self.subTest(profile=profile):
                rows = ci.matrix(profile, platform=True)["include"]
                self.assertTrue(rows)
                self.assertTrue(all(ci.supplemental(ci.lanes()[row["lane"]]) for row in rows))
        scope = dict(full=False, paths=["lycaon/internal/confine/boundary_linux.go"])
        self.assertEqual({row["lane"] for row in ci.matrix("integration", scope=scope, platform=True)["include"]},
                         {"confinement-arm64", "command-scale"})
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
            with self.subTest(mutation=mutation), self.catalog_patch(lambda d: d["capacity"]["max_parallel"].pop("nightly")
                    if mutation == "missing" else d["capacity"]["max_parallel"].update(nightly=0 if mutation == "zero" else 1.5)), self.assertRaises(ValueError):
                ci.max_parallel("check")

    def test_capped_matrix_starts_cheap_frequent_failures_then_its_longest_lanes(self):
        lanes = ci.lanes()
        rows = ci.matrix("integration")["include"]
        self.assertEqual({row["lane"] for row in rows[:2]}, {"limits", "lint"})
        self.assertEqual(rows[2]["lane"], "runner")
        self.assertEqual([row["lane"] for row in rows[3:5]], ["behavior", "behavior"])
        for profile in ci.PROFILES:
            rows = ci.matrix(profile)['include']
            ordinary = [row for row in rows if row['priority'] != 'compile']
            order = [(ci.PRIORITIES[row['priority']], -row['minutes']) for row in ordinary]
            self.assertEqual(order, sorted(order))
        with self.catalog_patch(lambda d: d["ci"]["limits"].update(priority="invalid")), self.assertRaises(ValueError):
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
