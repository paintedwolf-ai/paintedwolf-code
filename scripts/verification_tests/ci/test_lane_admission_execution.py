"""Each lane's commands meet the admission and package selection they meet when the lane runs."""
import unittest
from unittest.mock import patch

import verification_plan
from ci_policy import hosted_plan, lane_admission

SCAN_STRESS = "lycaon/test/scanstress/baseline_test.go"


def judged(lanes, scopes):
    with patch.object(hosted_plan, "lanes", return_value=lanes):
        return lane_admission.violations(scopes)


def lane(*targets, **fields):
    return {"targets": list(targets), "profiles": ["integration"], **fields}


class LaneAdmissionTests(unittest.TestCase):
    def test_a_selection_admission_refuses_is_reported_with_its_lane_and_profile(self):
        found = judged({"runner": lane("test:runner", arguments=["-v"])}, [None])
        self.assertEqual(len(found), 1)
        self.assertIn("runner (integration, no scope): ./task test:runner -- -v", found[0])
        self.assertEqual(judged({"runner": lane("test:runner", arguments=[verification_plan.EVERY_SUITE])}, [None]), [])

    def test_a_package_with_no_go_files_on_the_runner_is_reported(self):
        self.assertEqual(lane_admission.command_violations(["./task", "test:race", "--", "./test/scanstress"], "linux"),
                         ["test:race selects packages with no Go files on linux: ./test/scanstress"])
        self.assertEqual(lane_admission.command_violations(["./task", "test:race", "--", "./internal/confine"], "linux"),
                         [])

    def test_a_changed_package_lane_that_selects_nothing_runnable_is_reported(self):
        scope = {"base": None, "head": None, "full": False, "reasons": [], "paths": [SCAN_STRESS]}
        found = judged({"race": lane("test:race", phases="changed-packages")}, [None, scope])
        self.assertEqual(len(found), 1)
        self.assertIn("race (integration, every path changed)", found[0])
        self.assertIn("no changed Go package that builds on this runner", found[0])

    def test_each_runner_family_names_the_system_it_builds_for(self):
        self.assertEqual(lane_admission.runner_system({"runner": "macos-15"}), "darwin")
        self.assertEqual(lane_admission.runner_system({"runner": "ubuntu-24.04-arm"}), "linux")
        self.assertEqual(lane_admission.runner_system({}), "linux")


if __name__ == "__main__":
    unittest.main()
