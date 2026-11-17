"""Tests that skip without a sandbox, scanner or native host are run by a scope that provides it.

Off such a host a gated test skips, and a skip is indistinguishable from a pass, so each gate's
inventory comes from the Go sources and each scope from what the task, plan, workflow or script runs.
"""
import unittest

from verification_tests.ci import gotests
from verification_tests.ci.workflows import plan, taskfile, workflow

CONFINE_PACKAGE = "./internal/confine"
CONFINE_ENGAGEMENT = ("Available", "EnableAutoConfine", "TestingSetAutoConfine")
SCANNER_RESOURCE = "testutil.MissingScannerResource"
SUPERVISOR = "./internal/exec"
SANDBOX_PACKAGE = "./internal/confine/linuxsandbox"
LAUNCH_PACKAGE = "./internal/launchguard"
PRIVILEGE_TEST = "TestLinuxNativePrivilegeQualification"
# The leg that needs a staged desktop artifact; its refusal without one has its own Go test.
EXPLICIT_LEGS = {"launch-desktop"}


def workflow_scopes(name, job):
    return [scope for step in workflow(name).jobs[job].steps
            for scope in gotests.script_scopes(f"{name} {job}", step.run)]


def seatbelt_scopes():
    return [*gotests.catalog_scopes("test:seatbelt"), *workflow_scopes("platform-verification", "confinement"),
            *gotests.confine_scopes()]


def scanner_scopes():
    scopes = gotests.task_scopes("test:scanners")
    if not scopes:
        raise AssertionError("test:scanners runs no Go tests")
    return scopes


def uncovered(inventory, scopes, skip=()):
    return [f"{package} {test}" for package, tests in sorted(inventory.items()) if package not in skip
            for test in tests if not any(scope.covers(package, test) for scope in scopes)]


def dead_names(scopes):
    live = gotests.live_tests()
    return sorted({f"{scope.source}: {name}" for scope in scopes for name in scope.named() - live})


class SeatbeltScopeLaws(unittest.TestCase):
    def test_every_test_that_engages_confinement_runs_in_a_sandboxed_scope(self):
        roots = [*CONFINE_ENGAGEMENT, *(f"confine.{name}" for name in CONFINE_ENGAGEMENT)]
        inventory = gotests.reaching(roots)
        self.assertGreater(len(inventory), 1)
        self.assertEqual(uncovered(inventory, seatbelt_scopes(), skip={CONFINE_PACKAGE}), [])

    def test_the_engagement_api_the_inventory_derives_from_is_exported(self):
        self.assertLessEqual(set(CONFINE_ENGAGEMENT), gotests.exported_functions(CONFINE_PACKAGE))

    def test_the_confinement_package_runs_unfiltered_and_every_catalog_leg_runs_in_full(self):
        self.assertTrue(any(scope.covers_package(CONFINE_PACKAGE) and not scope.run for scope in seatbelt_scopes()))
        commands = [command for command in taskfile()["test:seatbelt"]["cmds"]]
        self.assertTrue(any("test:seatbelt" in str(command) for command in commands))
        for scope in gotests.catalog_scopes("test:seatbelt"):
            self.assertIs(scope.option("--full"), True, scope.source)

    def test_seatbelt_filters_name_only_tests_that_exist(self):
        self.assertEqual(dead_names(seatbelt_scopes()), [])


class LinuxConfinementScopeLaws(unittest.TestCase):
    def test_every_default_leg_runs_in_full_and_names_only_live_tests(self):
        scopes = gotests.confine_scopes()
        self.assertTrue(scopes)
        for scope in scopes:
            self.assertIs(scope.option("--full"), True, scope.source)
        self.assertEqual(dead_names(scopes), [])
        for package in (SANDBOX_PACKAGE, LAUNCH_PACKAGE):
            self.assertTrue(any(scope.covers_package(package) and not scope.run for scope in scopes), package)

    def test_only_the_artifact_leg_waits_for_an_explicit_request(self):
        default = {scope.option("--name") for scope in gotests.confine_scopes()}
        legs = gotests.confine_legs()
        self.assertTrue(legs)
        explicit = set()
        for leg in legs:
            requested = {scope.option("--name") for scope in gotests.confine_scopes(leg)}
            self.assertEqual(len(requested), 1, leg)
            if not requested & default:
                explicit.add(leg)
        self.assertEqual(explicit, EXPLICIT_LEGS)

    def test_every_command_supervisor_test_runs_on_each_architecture(self):
        inventory = {SUPERVISOR: gotests.file_tests(SUPERVISOR, "supervisor")}
        self.assertTrue(inventory[SUPERVISOR])
        self.assertEqual(uncovered(inventory, gotests.confine_scopes()), [])

    def test_applied_helper_browser_and_process_stat_fixtures_run(self):
        fixtures = {
            "./internal/osprocess": ["TestParseLinuxStatPreservesKernelFacts",
                                     "TestParseLinuxStatRejectsMalformedRecords",
                                     "TestParseLinuxStatBoundsTheEntireRecord"],
            "./internal/mcp": ["TestLinuxStdioMCPAppliedBoundary"],
            "./internal/scan/integration": ["TestLinuxScannerBoundaryKeepsSourceReadOnlyAndOutputWritable"],
            "./internal/documentcore": ["TestLinuxDocumentCoreAppliedBoundary"],
            "./internal/browser": ["TestBrowserUnavailableLinuxBoundaryDoesNotLaunch",
                                   "TestBrowserBoundaryRefusesWhenConfinementCannotBeBuilt",
                                   "TestLaunchHeadlessRefusesUnsafeProjectRootBeforeProvisioning"],
        }
        live = gotests.test_functions()
        for package, tests in fixtures.items():
            for test in tests:
                self.assertIn(test, live[package], f"{package} {test}")
        self.assertEqual(uncovered(fixtures, gotests.confine_scopes()), [])

    def test_privilege_qualification_runs_with_its_native_build_and_setup(self):
        (scope,) = [scope for scope in gotests.confine_scopes() if scope.covers(SUPERVISOR, PRIVILEGE_TEST)]
        self.assertIn("nativeprivileges", str(scope.option("--tags")).split(","))
        self.assertEqual(scope.environment.get("PW_NATIVE_PRIVILEGE_QUALIFICATION"), "ci")

    def test_both_architectures_run_every_default_leg(self):
        default = {scope.option("--name").rsplit("-", 1)[-1] for scope in gotests.confine_scopes()}
        amd64 = []
        for job in workflow("platform-verification").jobs.values():
            for step in job.steps:
                for argv in step.commands():
                    if argv[:2] == ["./task", "test:confine:linux"]:
                        runner = job.raw["runs-on"]
                        self.assertTrue(runner.startswith("ubuntu-") and "arm" not in runner, runner)
                        legs = argv[3:] if argv[2:3] == ["--"] else []
                        self.assertNotIn("--prepare", legs)
                        amd64.extend(legs or sorted(default))
        self.assertEqual(sorted(amd64), sorted(default))
        lanes = [lane for lane in plan()["ci"].values() if "test:confine:linux" in lane["targets"]
                 and lane.get("runner", "").endswith("-arm")]
        self.assertTrue(any({"release", "nightly"} <= set(lane["profiles"]) for lane in lanes))


class ScannerScopeLaws(unittest.TestCase):
    def test_the_scanner_suite_runs_exactly_the_tests_that_need_a_bundled_scanner(self):
        inventory = gotests.reaching([SCANNER_RESOURCE])
        self.assertTrue(inventory)
        scopes = scanner_scopes()
        self.assertEqual(uncovered(inventory, scopes), [])
        allowed = {test for tests in inventory.values() for test in tests}
        self.assertEqual(sorted({name for scope in scopes for name in scope.named()} - allowed), [])
        for scope in scopes:
            self.assertIs(scope.option("--full"), True)
            self.assertTrue(scope.run)

    def test_hosted_profiles_run_the_full_suite_with_its_bundled_scanners_once(self):
        self.assertTrue(plan()["go"]["test:full"]["bundled"])
        for profile in ("check", "nightly", "release"):
            self.assertTrue(any(profile in lane["profiles"] and "test:full" in lane["targets"]
                                for lane in plan()["ci"].values()), profile)
        self.assertNotIn("test:scanners", plan()["groups"]["check:tests"])


if __name__ == "__main__":
    unittest.main()
