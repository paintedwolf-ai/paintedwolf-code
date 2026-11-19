"""The hosted plan's rules, each proved on a small catalog; the live catalog is checked when the plan job runs."""
import json
from pathlib import Path
import unittest

from ci_policy import hosted_plan as ci
from verification_tests.ci.fixture_plan import lane, lanes, plan


def narrow(data):
    """The gate's Go recipe selects one package, so a package provision reaches only the lanes that run it."""
    data["go"]["test:short"]["packages"] = ["./internal/egressclass"]


def rows(profile, **arguments):
    return {row["lane"]: row for row in ci.matrix(profile, **arguments)["include"]}


class LaneRuleTests(unittest.TestCase):
    def refused(self, **arguments):
        with plan(**arguments), self.assertRaises(ValueError):
            ci.lanes()

    def test_the_smallest_valid_catalog_plans_every_profile(self):
        with plan():
            for profile in ("fast", "check", "integration", "nightly"):
                self.assertTrue(rows(profile), profile)

    def test_each_lane_field_is_declared_and_in_range(self):
        for change in ({"minutes": 0}, {"minutes": 301}, {"minutes": 1.5}, {"profiles": []}, {"profiles": ["daily"]},
                       {"suite": "typo"}, {"setup": "typo"}, {"workers": 9}, {"shards": 1}, {"shards": 9},
                       {"priority": "urgent"}, {"runner": "windows-2025"}, {"paths": ["docs/"]},
                       {"targets": ["no:such:task"]}, {"targets": []}):
            with self.subTest(change=change):
                self.refused(ci=lanes(tests={**lane("test:short"), **change}))
        self.refused(ci={"Tests": lane("test:short"), "stress": lanes()["stress"]})

    def test_arguments_and_admission_checks_belong_to_their_targets(self):
        gate = dict(check=("test:short", "lint:fast"), fast=("test:short", "lint:fast"))
        for change in ({"arguments": []}, {"arguments": [""]},
                       {"admission_checks": [["test:confine:linux", "--", "supervisor"]]}):
            with self.subTest(change=change):
                self.refused(ci=lanes(tests={**lane("test:short", "lint:fast"), **change}), **gate)

    def test_setups_refuse_targets_their_toolchains_cannot_run(self):
        for setup in ("frontend", "analysis"):
            with self.subTest(setup=setup):
                self.refused(ci=lanes(tests=lane("test:short", setup=setup)))
        gate = dict(check=("test:short", "lint:fast"), fast=("test:short", "lint:fast"))
        with plan(ci=lanes(lint=lane("lint:fast", setup="analysis")), **gate):
            self.assertEqual(rows("check")["lint"]["setup"], "analysis")

    def test_each_tier_runs_its_gate_stages_exactly_once(self):
        gate = dict(check=("test:short", "lint:fast"), fast=("test:short", "lint:fast"))
        self.refused(**gate)
        self.refused(ci=lanes(again=lane("test:short")))
        self.refused(ci=lanes(tests=lane("test:short", profiles=("check", "integration", "nightly"))))
        self.refused(ci=lanes(tests=lane("test:short", profiles=("fast", "check", "nightly"))))
        with plan(ci=lanes(lint=lane("lint:fast")), **gate):
            self.assertEqual(set(rows("check")), {"tests", "lint"})

    def test_a_change_gate_runs_only_where_a_change_base_exists(self):
        gate = dict(check=("test:short", "den:knip:changes", "scripts:coverage:changes"),
                    fast=("test:short", "den:knip:changes"))
        scripts = lane("scripts:coverage:changes", profiles=("check", "integration", "nightly"))
        with plan(ci=lanes(knip=lane("den:knip:changes"), scripts=scripts), **gate):
            for profile in ("fast", "check"):
                self.assertIn("knip", rows(profile), profile)
            for profile in ("integration", "nightly"):
                self.assertNotIn("knip", rows(profile), profile)
            self.assertEqual(ci.profile_invocations(ci.lanes()["scripts"], "check"), [["scripts:coverage:changes"]])
            self.assertEqual(ci.profile_invocations(ci.lanes()["scripts"], "integration"),
                             [["test:runner", "--", "--every-suite"]])
        # A lane's own arguments would reach the replacement, which takes its own.
        self.refused(ci=lanes(knip=lane("den:knip:changes", arguments=["x"]), scripts=scripts), **gate)

    def test_nightly_includes_every_qualification_lane_and_the_whole_stress_partition(self):
        self.refused(ci=lanes(tests=lane("test:short", profiles=("fast", "check", "integration"))))
        partial = lane("test:stress", profiles=("nightly",), arguments=["./internal/db"])
        self.refused(ci=lanes(stress=partial), stress=("./internal/db", "./internal/git"))
        rest = lane("test:stress", profiles=("nightly",), arguments=["./internal/git"])
        with plan(ci=lanes(stress=partial, rest=rest), stress=("./internal/db", "./internal/git")):
            self.assertLessEqual({"stress", "rest"}, set(rows("nightly")))

    def test_platform_lanes_run_beside_the_gate_on_their_platform_runners(self):
        platform = dict(platform="linux", runner="ubuntu-24.04-arm", profiles=("integration", "nightly", "release"))
        for change in ({"runner": "macos-15"}, {"profiles": ("check", "nightly")}, {"platform": "plan9"}):
            with self.subTest(change=change):
                self.refused(ci=lanes(arm=lane("lint:fast", **{**platform, **change})))
        self.refused(ci=lanes(arm=lane("test:short", **platform)))
        with plan(ci=lanes(arm=lane("lint:fast", **platform))):
            self.assertEqual(set(rows("integration", platform=True)), {"arm"})
            self.assertEqual(rows("nightly")["arm"]["runner"], "ubuntu-24.04-arm")
            with self.assertRaises(ValueError):
                ci.matrix("check", platform=True)


class SelectionFieldTests(unittest.TestCase):
    def test_a_selected_lane_declares_known_areas_and_each_phase_belongs_to_one_lane(self):
        for tests in ({**lane("test:short"), "changes": []}, {**lane("test:short"), "changes": ["prose"]},
                      {**lane("test:short"), "changes": ["go", "go"]}, lane("test:short", phases="everything")):
            with self.subTest(tests=tests), plan(ci=lanes(tests=tests)), self.assertRaises(ValueError):
                ci.lanes()
        twice = lanes(stress=lane("test:stress", profiles=("nightly",), phases="affected-packages"),
                      tests=lane("test:short", phases="affected-packages"))
        with plan(ci=twice), self.assertRaises(ValueError):
            ci.lanes()

    def test_lanes_are_found_by_the_areas_they_declare(self):
        values = lanes(go=lane("lint:fast", changes=["go"], profiles=("nightly",)),
                       den=lane("lint:full", changes=["den", "shell-native"], profiles=("nightly",)),
                       tests=lane("test:short", phases="admission"))
        self.assertEqual(ci.lanes_of(values, {"go"}), {"go"})
        self.assertEqual(ci.lanes_of(values, {"shell-native", "always"}), {"den", "tests"})


class ProvisionTests(unittest.TestCase):
    def test_a_lane_gets_each_input_its_targets_and_packages_need_and_no_other(self):
        provisions = {"containers": {"targets": {"lint:fast": []}},
                      "linux_launch": {"targets": {}, "packages": ["./internal/db"]},
                      "notices": {"targets": {"lint:full": []}}}
        with plan(ci=lanes(lint=lane("lint:fast", profiles=("nightly",)),
                           notices=lane("lint:full", profiles=("nightly",))), provisions=provisions, mutate=narrow):
            planned = rows("nightly")
        self.assertEqual({name for name, row in planned.items() if row["containers"]}, {"lint"})
        self.assertEqual({name for name, row in planned.items() if row["linux_launch"]}, {"stress"})
        self.assertEqual({name for name, row in planned.items() if row["notices"]}, {"notices"})

    def test_a_provision_for_some_arguments_applies_only_when_a_lane_runs_them(self):
        provisions = {"browser": {"targets": {"test:confine:linux": ["browser"]}},
                      "nix": {"targets": {"test:confine:linux": []}}}
        for arguments, browser in ((None, True), (["browser"], True), (["sandbox"], False)):
            confine = lane("test:confine:linux", profiles=("nightly",), **({"arguments": arguments} if arguments else {}))
            with self.subTest(arguments=arguments), plan(ci=lanes(confine=confine), provisions=provisions):
                self.assertEqual((rows("nightly")["confine"]["browser"], rows("nightly")["confine"]["nix"]), (browser, True))

    def test_removing_a_package_dependency_from_the_lane_that_needs_it_fails_the_plan(self):
        provisions = {"linux_launch": {"targets": {}, "packages": ["./internal/git"]}}
        with plan(provisions=provisions, stress=("./internal/git",), mutate=narrow):
            self.assertTrue(rows("nightly")["stress"]["linux_launch"])
            self.assertFalse(rows("nightly")["tests"]["linux_launch"])
        with plan(provisions=provisions, mutate=narrow), self.assertRaises(ValueError):
            ci.lanes()

    def test_a_provision_must_name_real_targets_and_packages_and_serve_a_lane(self):
        for provisions in ({"containers": {"targets": {"no:such:task": []}}},
                           {"linux_launch": {"targets": {}, "packages": ["./internal/missing-package"]}},
                           {"containers": {"targets": {"lint:fast": []}}},
                           {"display": {"targets": {"test:short": []}}}):
            with self.subTest(provisions=provisions), plan(provisions=provisions), self.assertRaises(ValueError):
                ci.lanes()

    def test_runtime_engines_stay_unless_every_target_runs_without_them(self):
        with plan(ci=lanes(native=lane("lint:fast", "lint:full", profiles=("nightly",)),
                           mixed=lane("lint:fast", "lint:vuln", profiles=("nightly",))),
                  provisions={"engine_free": {"targets": {"lint:fast": [], "lint:full": []}}}):
            planned = rows("nightly")
        self.assertEqual({name for name, row in planned.items() if not row["runtime_engines"]}, {"native"})

    def test_analysis_restores_exactly_the_tool_sets_its_targets_use(self):
        provisions = {"analysis_lint": {"targets": {"lint:fast": []}},
                      "analysis_vulnerabilities": {"targets": {"lint:vuln": []}}}
        cases = {"both": lane("lint:fast", "lint:vuln", profiles=("nightly",), setup="analysis"),
                 "lint": lane("lint:fast", profiles=("nightly",), setup="analysis"),
                 "vulnerabilities": lane("lint:vuln", profiles=("nightly",), setup="analysis")}
        with plan(ci=lanes(**cases), provisions=provisions):
            planned = rows("nightly")
        self.assertEqual({name: planned[name]["analysis"] for name in cases},
                         {"both": "all", "lint": "lint", "vulnerabilities": "vulnerabilities"})
        self.assertEqual(planned["tests"]["analysis"], "none")


class MatrixTests(unittest.TestCase):
    def test_a_sharded_lane_expands_into_one_row_per_shard(self):
        with plan(ci=lanes(tests=lane("test:short", shards=3))):
            shards = [row["shard"] for row in ci.matrix("check")["include"] if row["lane"] == "tests"]
        self.assertEqual(sorted(shards), ["1/3", "2/3", "3/3"])

    def test_every_row_has_explicit_step_and_job_budgets(self):
        with plan():
            for row in ci.matrix("nightly")["include"]:
                self.assertEqual((row["step_minutes"], row["job_minutes"]), (row["minutes"] + 1, row["minutes"] + 30))

    def test_a_partial_nightly_suite_keeps_its_own_lanes_and_leaves_gate_lanes_out(self):
        race = lane("test:race", profiles=("nightly",), suite="race")
        sweep = lane("lint:vuln", profiles=("nightly",))
        with plan(ci=lanes(race=race, sweep=sweep)):
            self.assertEqual(set(rows("nightly", suite="race")), {"race", "sweep", "stress"})
            self.assertEqual(set(rows("nightly")), {"tests", "race", "sweep", "stress"})
            for profile, suite in (("unknown", "all"), ("release", "race"), ("nightly", "typo")):
                with self.subTest(profile=profile, suite=suite), self.assertRaises(ValueError):
                    ci.matrix(profile, suite)

    def test_each_profile_caps_its_parallel_runners(self):
        with plan() as data:
            for profile in ci.PROFILES:
                self.assertEqual(ci.max_parallel(profile), data["capacity"]["max_parallel"][profile])
            with self.assertRaises(ValueError):
                ci.max_parallel("unknown")
        for change in ({"nightly": 0}, {"nightly": 1.5}):
            mutate = lambda data, change=change: data["capacity"]["max_parallel"].update(change)
            with self.subTest(change=change), plan(mutate=mutate), self.assertRaises(ValueError):
                ci.max_parallel("check")


class LiveCatalogTests(unittest.TestCase):
    def test_every_linux_runner_is_one_the_release_platforms_build_on(self):
        root = Path(__file__).resolve().parents[3]
        platforms = json.loads((root / "packaging/release-platforms.json").read_text())["platforms"]
        linux = {platform["runner"] for platform in platforms if platform["goos"] == "linux"}
        runners = {lane.get("runner", "ubuntu-24.04") for lane in ci.lanes().values()}
        self.assertTrue(runners)
        self.assertLessEqual({runner for runner in runners if runner.startswith("ubuntu")}, linux)


if __name__ == "__main__":
    unittest.main()
