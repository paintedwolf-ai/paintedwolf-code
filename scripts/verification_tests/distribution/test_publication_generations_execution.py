"""Protected generation admission selects format owners and fixed public trust."""

import json
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest.mock import Mock, patch

from linux_release.publication_generations import CATALOG, verify
from linux_release.publication_repositories import digest


class PublicationGenerationsTests(unittest.TestCase):
    def fixture(self, root, family):
        manifest = {"family": family, "source_commit": "a" * 40, "rehearsal": False}
        directory = root / "generations" / digest(manifest)
        directory.mkdir(parents=True)
        (directory / "generation.json").write_text(json.dumps(manifest))
        return manifest, directory

    def test_native_and_pacman_admission_receive_controller_catalog_and_production_mode(self):
        for family, owner in [("deb", "repository_validation"), ("rpm", "repository_validation"),
                              ("pacman", "pacman_validation")]:
            with self.subTest(family=family), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                manifest, directory = self.fixture(root, family)
                module = types.ModuleType(owner)
                module.admit = Mock()
                with patch.dict(sys.modules, {"scripts.linux_release." + owner: module}):
                    verify(root, {"generations": [{"manifest": manifest}]})
                module.admit.assert_called_once_with(manifest, directory, CATALOG, rehearsal=False)

    def test_changed_retained_generation_refuses_before_format_admission(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            manifest, directory = self.fixture(root, "deb")
            (directory / "generation.json").write_text(json.dumps({**manifest, "source_commit": "b" * 40}))
            with self.assertRaisesRegex(ValueError, "selected generation"):
                verify(root, {"generations": [{"manifest": manifest}]})
