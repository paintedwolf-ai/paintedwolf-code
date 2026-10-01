from pathlib import Path
import tempfile
import unittest

import verification_plan as planning
from verification_execute import excluded_tier_tests, exclusion_notice, flag_tags, tier_tags


class TierExclusionTests(unittest.TestCase):
    def write(self, root, name, header):
        path = Path(root) / name
        path.write_text(f"{header}\n\npackage api\n")
        return name

    def test_a_stage_without_a_tier_tag_reports_the_tests_it_skips(self):
        with tempfile.TemporaryDirectory() as root:
            ignored = [
                self.write(root, "git_integration_test.go", "//go:build integration"),
                self.write(root, "store_stress_test.go", "//go:build stress && !race"),
                self.write(root, "path_windows_test.go", "//go:build windows"),
                self.write(root, "helper_windows.go", "//go:build windows"),
            ]
            records = [{"ImportPath": "example/api", "Dir": root, "IgnoredGoFiles": ignored},
                       {"ImportPath": "example/clean", "Dir": root, "IgnoredGoFiles": []}]
            tiers = {"integration": ["test:integration"], "stress": ["test:stress"]}

            excluded = excluded_tier_tests(records, tiers, set())

            # Platform constraints are not tiers; a non-test file never counts.
            self.assertEqual(excluded, {"example/api": {
                "tags": ["integration", "stress"],
                "files": ["git_integration_test.go", "store_stress_test.go"],
            }})
            self.assertEqual(excluded_tier_tests(records, tiers, {"integration", "stress"}), {})

    def test_the_notice_names_the_targets_that_run_the_skipped_tests(self):
        notice = exclusion_notice("test:digest", {
            "example/api": {"tags": ["integration"], "files": ["a_integration_test.go", "b_integration_test.go"]},
        }, {"integration": ["test:full", "test:integration"]})
        self.assertIn("test:digest: 2 test files in 1 packages need build tags integration", notice)
        self.assertIn("test:full, test:integration run them", notice)

    def test_the_catalog_declares_the_integration_tier(self):
        tiers = tier_tags(planning.catalog())
        self.assertIn("test:integration", tiers["integration"])
        digest = next(stage for stage in planning.expand(["test:digest"]) if stage["kind"] == "go")
        self.assertNotIn("integration", flag_tags(digest["options"], digest["flags"]))


if __name__ == "__main__":
    unittest.main()
