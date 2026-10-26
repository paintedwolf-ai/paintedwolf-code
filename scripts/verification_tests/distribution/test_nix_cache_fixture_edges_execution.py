"""Disposable cache fixtures pin their signing keys and source store."""

from pathlib import Path
import json
import stat
import tempfile
import unittest
from unittest.mock import patch

from scripts.nix_cache import rehearsal_fixture


OUTPUT = "/nix/store/" + "0" * 32 + "-application"


class NixCacheFixtureEdges(unittest.TestCase):
    def test_local_store_encodes_the_owned_root(self):
        with tempfile.TemporaryDirectory() as temporary:
            value = rehearsal_fixture.local_store(Path(temporary) / "space in name")
        self.assertTrue(value.startswith("local?root="))
        self.assertIn("space+in+name", value)

    def test_fixture_uses_two_private_keys_and_a_two_path_local_closure(self):
        commands = []
        construction = []

        def nix(*arguments, **kwargs):
            commands.append((arguments, kwargs))
            if arguments[:2] == ("key", "generate-secret"):
                return "private fixture key"
            if arguments[:2] == ("key", "convert-secret-to-public"):
                self.assertEqual(kwargs["stdin"], "private fixture key")
                return "public fixture key"
            if arguments[0] == "build":
                self.assertIn("--no-link", arguments)
                self.assertIn("builtin:buildenv", arguments[arguments.index("--expr") + 1])
                return json.dumps([{"outputs": {"out": OUTPUT}}])
            self.fail("unexpected fixture Nix operation")

        def construct(request, policy, key, cache, *, source_store):
            construction.append((request, policy, key, cache, source_store))
            return {"request": request, "closure": {OUTPUT: {"references": ["dependency"]}}}

        with patch.object(rehearsal_fixture.platform, "system", return_value="Linux"), \
             patch.object(rehearsal_fixture.platform, "machine", return_value="x86_64"), \
             patch.object(rehearsal_fixture, "nix", side_effect=nix), \
             patch.object(rehearsal_fixture, "construct", side_effect=construct):
            with rehearsal_fixture.cache_fixture() as fixture:
                self.assertEqual(fixture.system, "x86_64-linux")
                self.assertEqual(fixture.output, OUTPUT)
                self.assertEqual(len(fixture.key_files), 2)
                self.assertTrue(all(stat.S_IMODE(path.stat().st_mode) == 0o600 for path in fixture.key_files))
                self.assertEqual([path.read_text() for path in fixture.key_files],
                                 ["private fixture key\n", "private fixture key\n"])
                self.assertEqual(fixture.report["closure"][OUTPUT]["references"], ["dependency"])
        self.assertEqual(construction[0][0]["outputs"], [OUTPUT])
        self.assertTrue(construction[0][4].startswith("local?root="))
        self.assertEqual(len([args for args, _ in commands if args[:2] == ("key", "generate-secret")]), 2)

    def test_unsupported_architecture_refuses_before_key_generation(self):
        with patch.object(rehearsal_fixture.platform, "system", return_value="Linux"), \
             patch.object(rehearsal_fixture.platform, "machine", return_value="ppc64le"), \
             patch.object(rehearsal_fixture, "nix") as nix:
            with self.assertRaisesRegex(ValueError, "supported native Linux"):
                with rehearsal_fixture.cache_fixture():
                    pass
        nix.assert_not_called()


if __name__ == "__main__":
    unittest.main()
