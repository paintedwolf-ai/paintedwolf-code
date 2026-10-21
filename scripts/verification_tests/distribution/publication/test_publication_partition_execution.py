"""Public native channels cannot split one family into architecture generations."""

import json
import tempfile
import unittest

from linux_release.publication.trust.repositories import digest, verify_repositories
from verification_tests.distribution.test_linux_release_admission_execution import LinuxReleaseAdmissionTests


class PublicationPartitionTests(unittest.TestCase):
    def test_native_architecture_discriminator_is_refused_even_when_operation_matches(self):
        for family in ("deb", "rpm"):
            for architecture in ("amd64", "arm64", None):
                with self.subTest(family=family, architecture=architecture), tempfile.TemporaryDirectory() as directory:
                    root, _, _ = LinuxReleaseAdmissionTests().fixture(directory)
                    plan = json.loads((root / "repositories.json").read_text())
                    artifacts = json.loads((root / "artifacts.json").read_text())
                    manifest = plan["generations"][0]["manifest"]
                    manifest.update(family=family, architecture=architecture)
                    operation = plan["generations"][0]["operation"]
                    operation.update(family=family, architecture=architecture, generation=digest(manifest))
                    with self.assertRaisesRegex(ValueError, "one combined architecture channel"):
                        verify_repositories(plan, artifacts, "a" * 40, version="1.0.0", channel="stable",
                            signing_generation=1, key_fingerprint="A" * 40, release_build=14, verified_files=[])

    def test_operation_cannot_invent_a_native_architecture_channel(self):
        with tempfile.TemporaryDirectory() as directory:
            root, _, _ = LinuxReleaseAdmissionTests().fixture(directory)
            plan = json.loads((root / "repositories.json").read_text())
            artifacts = json.loads((root / "artifacts.json").read_text())
            plan["generations"][0]["operation"]["architecture"] = "amd64"
            with self.assertRaisesRegex(ValueError, "one combined architecture channel"):
                verify_repositories(plan, artifacts, "a" * 40, version="1.0.0", channel="stable",
                    signing_generation=1, key_fingerprint="A" * 40, release_build=14, verified_files=[])
