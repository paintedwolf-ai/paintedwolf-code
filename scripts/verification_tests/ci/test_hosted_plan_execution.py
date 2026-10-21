import copy
import unittest
from unittest.mock import patch

from ci_policy import hosted_plan as ci
import verification_plan as planning


class HostedPlanTests(unittest.TestCase):
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
                                   "webkit-gtk", "confinement-arm64", "nix"})
        # Qualification runs the check gate, then release's platform lanes beside the platform gates.
        self.assertEqual({row["lane"] for row in ci.matrix("release", platform=True)["include"]}, release & platform)

    def test_ready_tier_is_one_short_static_job_from_the_handoff_gate(self):
        handoff = {stage["name"] for stage in planning.expand(["check-fast"])}
        rows = ci.matrix("fast")["include"]
        self.assertEqual(len(rows), 1)
        self.assertEqual(ci.max_parallel("fast"), 1)
        stages = [stage["name"] for stage in planning.expand(ci.lanes()[rows[0]["lane"]]["targets"])]
        self.assertEqual(sorted(stages), sorted(set(stages)))
        self.assertLessEqual(set(stages), handoff)
        self.assertLessEqual({"lint:fast", "budgets", "den:typecheck", "den:lint"}, set(stages))
        # Anything that runs tests waits for the merge queue.
        self.assertFalse(set(stages) & {"test:short", "test:contract", "den:test:fast", "coverage:changes",
                                         "den:coverage:changes"})
        self.assertLessEqual(rows[0]["minutes"], 15)

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

    def test_nightly_selection_includes_only_requested_and_unconditional_jobs(self):
        lanes = ci.lanes()
        nightly = {target for row in ci.matrix("nightly")["include"] for target in lanes[row["lane"]]["targets"]}
        self.assertEqual(nightly, {"test:full", "test:race", "test:fuzz", "test:stress", "check:coverage",
                                   "den:coverage-check", "den:test:transcript-scale", "lint:vuln:fresh",
                                   "perf:bench", "perf:sidecar", "perf:soak", "den:webkit:scroll",
                                   "den:webkit:atspi", "test:confine:linux", "nix:check"})
        for suite in ci.SUITES - {"all"}:
            with self.subTest(suite=suite):
                names = {row["lane"] for row in ci.matrix("nightly", suite)["include"]}
                expected = {name for name, lane in lanes.items() if "nightly" in lane["profiles"]
                            and lane["suite"] in {"all", suite}}
                self.assertEqual(names, expected)
                self.assertIn("vulnerability-freshness", names)
        for profile, suite in [("unknown", "all"), ("release", "race"), ("nightly", "typo")]:
            with self.assertRaises(ValueError):
                ci.matrix(profile, suite)

    def test_host_platform_and_time_budgets_are_explicit(self):
        for profile in ci.PROFILES:
            for row in ci.matrix(profile)["include"]:
                with self.subTest(profile=profile, lane=row["lane"]):
                    self.assertEqual(row["job_minutes"] - row["minutes"], 30)
                    self.assertLess(row["job_minutes"], 360)
                    # macOS hosts only the lanes that test macOS-specific behavior, and arm64 only
                    # architecture-specific confinement.
                    runner = {"webkit": "macos-15", "confinement-arm64": "ubuntu-24.04-arm"}.get(row["lane"], "ubuntu-latest")
                    self.assertEqual(row["runner"], runner)

    def test_each_lane_installs_only_its_declared_setup(self):
        setups = {row["lane"]: row["setup"] for row in ci.matrix("check")["include"]}
        self.assertEqual(setups["native"], "shell")
        # The WebKit harness builds without the shell, so the lane skips its packaging inputs.
        nightly = {row["lane"]: row["setup"] for row in ci.matrix("nightly", "e2e")["include"]}
        self.assertEqual(nightly["webkit"], "harness")
        self.assertEqual({setup for lane, setup in setups.items() if lane not in {"native", "webkit"}},
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
        self.assertEqual([row["lane"] for row in ci.matrix("integration")["include"] if row["browser"]], ["confinement-arm64"])

    def test_nix_lane_follows_nix_sources_and_provisions_nix_before_running_offline(self):
        def lanes(paths, full):
            return {row["lane"] for row in ci.matrix("integration", scope=dict(full=full, paths=paths),
                                                       platform=True)["include"]}
        for path in ("packaging/nix/shell.nix", "flake.lock", "default.nix", "scripts/linux_release/nix.py"):
            with self.subTest(path=path):
                self.assertIn("nix", lanes([path], True))
        self.assertNotIn("nix", lanes(["scripts/ci_policy/impact.py"], True))
        self.assertNotIn("nix", lanes(["lycaon/internal/confine/boundary_linux.go"], False))
        for profile in ("nightly", "release"):
            rows = [row for row in ci.matrix(profile)["include"] if row["lane"] == "nix"]
            self.assertEqual([(row["nix"], row["browser"]) for row in rows], [(True, False)])
        self.assertEqual({row["lane"] for row in ci.matrix("integration")["include"] if row["nix"]}, {"nix"})

    def test_path_selection_and_lane_arguments_are_explicit(self):
        for mutation in ("unplatformed", "empty", "absolute", "arguments-many-targets", "arguments-empty"):
            with self.subTest(mutation=mutation):
                data = copy.deepcopy(planning.catalog())
                if mutation == "unplatformed":
                    data["ci"]["frontend"]["paths"] = ["lycaon-den/"]
                elif mutation in ("empty", "absolute"):
                    data["ci"]["nix"]["paths"] = [] if mutation == "empty" else ["/nix/store"]
                elif mutation == "arguments-many-targets":
                    data["ci"]["frontend"]["arguments"] = ["browser"]
                else:
                    data["ci"]["confinement-arm64"]["arguments"] = []
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
            with self.subTest(profile=profile):
                order = [(not lanes[row["lane"]].get("first"), -row["minutes"]) for row in ci.matrix(profile)["include"]]
                self.assertEqual(order, sorted(order))
        data = copy.deepcopy(planning.catalog())
        data["ci"]["limits"]["first"] = False
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
