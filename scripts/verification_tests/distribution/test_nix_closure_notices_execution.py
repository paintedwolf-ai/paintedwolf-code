"""A redistributed Nix closure attributes every path to a licence and offers copyleft source."""

import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from scripts.linux_release import nix_closure_notices as notices

SCRIPT = Path(notices.__file__)


def store(name, digit="0"):
    return f"/nix/store/{digit * 32}-{name}"


GLIBC = {"name": "glibc", "version": "2.42", "outputs": [store("glibc-2.42", "1"), store("glibc-2.42-bin", "2")],
         "licenses": [{"spdx": "LGPL-2.1-or-later", "name": "GNU Lesser General Public License v2.1 or later",
                       "free": True, "redistributable": True}],
         "homepage": "https://www.gnu.org/software/libc/",
         "source": {"store_path": store("glibc-2.42.tar.xz", "3"), "urls": ["mirror://gnu/glibc/glibc-2.42.tar.xz"],
                    "output_hash": "sha256-AAAA"},
         "nixpkgs_position": "pkgs/development/libraries/glibc/common.nix:1"}
APPLICATION = {"name": "painted-wolf-code", "version": "1.0.2", "outputs": [store("painted-wolf-code-1.0.2", "4")],
               "licenses": [{"spdx": "Apache-2.0", "name": "Apache License 2.0", "free": True, "redistributable": True}],
               "homepage": None, "source": None, "nixpkgs_position": None}


class ClosureNoticeTests(unittest.TestCase):
    def catalog(self, *packages):
        return {"format_version": 1, "nixpkgs_revision": "f" * 40, "application": APPLICATION["outputs"][0],
                "source_commit": "a" * 40, "packages": list(packages)}

    def test_each_path_has_its_licence_and_copyleft_paths_their_fixed_source(self):
        result = notices.attribute(self.catalog(GLIBC, APPLICATION),
                                   [APPLICATION["outputs"][0], GLIBC["outputs"][1]])
        by_path = {entry["store_path"]: entry for entry in result["paths"]}
        self.assertEqual(by_path[GLIBC["outputs"][1]]["corresponding_source"], GLIBC["source"])
        self.assertIsNone(by_path[APPLICATION["outputs"][0]]["corresponding_source"])
        self.assertEqual(result["nixpkgs_revision"], "f" * 40)

    def test_unknown_unlicensed_unfree_or_sourceless_copyleft_paths_refuse(self):
        unlicensed = {**GLIBC, "licenses": []}
        unfree = {**APPLICATION, "licenses": [{"spdx": None, "name": "Unfree", "free": False, "redistributable": False}]}
        sourceless = {**GLIBC, "source": None}
        unhashed = {**GLIBC, "source": {**GLIBC["source"], "output_hash": None}}
        for packages, paths, message in (
                ((APPLICATION,), [GLIBC["outputs"][0]], "no described package"),
                ((unlicensed,), [GLIBC["outputs"][0]], "no licence"),
                ((unfree,), APPLICATION["outputs"], "not redistributable"),
                ((sourceless,), [GLIBC["outputs"][0]], "corresponding source"),
                ((unhashed,), [GLIBC["outputs"][0]], "corresponding source"),
                ((GLIBC,), ["/usr/lib/libc.so.6"], "invalid store path")):
            with self.subTest(message=message), self.assertRaisesRegex(ValueError, message):
                notices.attribute(self.catalog(*packages), paths)

    def test_unfree_but_redistributable_packages_are_attributed(self):
        firmware = {**APPLICATION, "name": "firmware", "outputs": [store("firmware", "5")],
                    "licenses": [{"spdx": None, "name": "Redistributable firmware", "free": False,
                                  "redistributable": True}]}
        self.assertEqual(len(notices.attribute(self.catalog(firmware), firmware["outputs"])["paths"]), 1)

    def test_command_writes_json_markdown_and_the_source_list(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "catalog.json").write_text(json.dumps(self.catalog(GLIBC, APPLICATION)))
            (root / "store-paths").write_text("\n".join([APPLICATION["outputs"][0], *GLIBC["outputs"]]) + "\n")
            subprocess.run([sys.executable, "-I", str(SCRIPT), "--catalog", str(root / "catalog.json"),
                            "--store-paths", str(root / "store-paths"), "--out", str(root / "out")], check=True)
            self.assertEqual((root / "out/corresponding-sources").read_text(), GLIBC["source"]["store_path"] + "\n")
            self.assertIn("LGPL-2.1-or-later", (root / "out/CLOSURE-NOTICES.md").read_text())
            self.assertEqual(len(json.loads((root / "out/closure-notices.json").read_text())["paths"]), 3)
            (root / "store-paths").write_text(store("unknown") + "\n")
            failed = subprocess.run([sys.executable, "-I", str(SCRIPT), "--catalog", str(root / "catalog.json"),
                                     "--store-paths", str(root / "store-paths"), "--out", str(root / "again")],
                                    capture_output=True, text=True)
            self.assertEqual(failed.returncode, 1)
            self.assertFalse((root / "again").exists())


if __name__ == "__main__":
    unittest.main()
