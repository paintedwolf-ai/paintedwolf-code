"""A passing result on one release never covers a different declared release."""

import copy
import hashlib
import json
import unittest

from linux_release.installed_evidence import coverage
from linux_release.support_matrix import CATALOG, variants


class LinuxInstalledCoverageTests(unittest.TestCase):
    def setUp(self):
        catalog = json.loads(CATALOG.read_text())
        self.capabilities = {item["id"] for item in catalog["capabilities"]}
        self.fedora = next(item for item in catalog["environments"] if item["id"] == "fedora-maintained-gnome")
        self.observed = []
        for variant in variants(self.fedora, "documents", self.capabilities):
            release = variant["release"]
            self.observed.append({"id": variant["id"], "status": "passed", "observed": {
                "os_id": release["os_ids"][0], "version_id": release["version_id"],
                "snapshot": {"kind": release["snapshot_kind"], "channel": release["channel"],
                             "value": hashlib.sha256(release["id"].encode()).hexdigest()},
                "display_protocol": variant["display"]["protocol"], "scales_percent": variant["scales_percent"]}})

    def check(self, observed):
        return coverage(observed, self.fedora, "documents", self.capabilities)

    def test_both_fedora_releases_observed_exactly_cover_the_row(self):
        self.assertEqual({item["observed"]["version_id"] for item in self.observed}, {"43", "44"})
        self.assertEqual(self.check(self.observed), ([], False))

    def test_one_release_run_cannot_stand_in_for_the_other(self):
        relabeled = copy.deepcopy(self.observed)
        relabeled[1]["observed"] = copy.deepcopy(relabeled[0]["observed"])
        errors, failed = self.check(relabeled)
        self.assertFalse(failed)
        self.assertTrue(any("release version is not the declared release" in error for error in errors))
        self.assertTrue(any("cannot cover two declared releases" in error for error in errors))
        errors, _ = self.check(self.observed[:1])
        self.assertEqual(errors, [f"variant {self.observed[1]['id']}: missing"])

    def test_observed_environment_must_match_each_declared_axis(self):
        for field, value, message in (("os_id", "ubuntu", "distribution"),
                                      ("display_protocol", "x11", "display protocol"),
                                      ("scales_percent", [150], "monitor scales"),
                                      ("snapshot", {"kind": "nixos-revision", "value": "a" * 40, "channel": None},
                                       "release authority")):
            observed = copy.deepcopy(self.observed)
            observed[0]["observed"][field] = value
            with self.subTest(field=field):
                errors, _ = self.check(observed)
                self.assertTrue(any(message in error for error in errors), errors)

    def test_variant_outcomes_and_shape(self):
        observed = copy.deepcopy(self.observed)
        observed[0]["status"] = "failed"
        errors, failed = self.check(observed)
        self.assertTrue(failed)
        self.assertIn(f"variant {observed[0]['id']}: failed", errors)
        observed[0]["status"] = "skipped"
        self.assertEqual(self.check(observed)[1], False)
        for mutate in (lambda value: value.append(copy.deepcopy(value[0])),
                       lambda value: value[0].update(id="fedora-99-scale-100"),
                       lambda value: value[0].update(status="maybe"),
                       lambda value: value[0]["observed"].pop("snapshot"),
                       lambda value: value[0].update(extra=True)):
            observed = copy.deepcopy(self.observed)
            mutate(observed)
            with self.assertRaises(ValueError):
                self.check(observed)
        with self.assertRaises(ValueError):
            self.check({})


if __name__ == "__main__":
    unittest.main()
