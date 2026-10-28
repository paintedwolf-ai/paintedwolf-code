"""Source rehearsal uses a disposable committed flake and rejects changed identity."""

from io import StringIO
import json
import unittest
from unittest.mock import patch

from scripts.nix_cache import source_rehearsal


OUTPUT = "/nix/store/" + "0" * 32 + "-application"


class NixSourceRehearsalEdges(unittest.TestCase):
    def test_committed_source_is_stable_when_the_worktree_changes(self):
        first_request = []
        observations = []

        def describe(request, source, *, source_store, rehearsal=False):
            if source_store.startswith("ssh:"):
                raise ValueError("remote store refused")
            if first_request and request != first_request[0]:
                raise ValueError("source authority changed")
            if not first_request:
                first_request.append(dict(request))
            observations.append((source, source_store, rehearsal, (source / "VERSION").read_text()))
            return {"closure": {OUTPUT: {}}, "source_commit": request["application_commit"]}

        def nix(*arguments, **_kwargs):
            self.assertEqual(arguments[0], "build")
            self.assertIn("--no-write-lock-file", arguments)
            return json.dumps([{"outputs": {"out": OUTPUT}}])

        with patch.object(source_rehearsal.platform, "system", return_value="Linux"), \
             patch.object(source_rehearsal.platform, "machine", return_value="x86_64"), \
             patch.object(source_rehearsal, "nix", side_effect=nix), \
             patch.object(source_rehearsal, "describe_verified_source", side_effect=describe), \
             patch.object(source_rehearsal, "local_store", side_effect=lambda root: "local?root=" + str(root)), \
             patch("sys.stdout", new_callable=StringIO) as output:
            source_rehearsal.main()
        self.assertEqual(len(observations), 2)
        self.assertEqual([row[3] for row in observations], ["0.0.0-rehearsal\n", "9.9.9\n"])
        self.assertTrue(all(row[2] for row in observations))
        self.assertEqual(first_request[0]["outputs"], [OUTPUT])
        self.assertEqual(json.loads(output.getvalue())["status"], "passed")

    def test_unsupported_host_refuses_before_creating_a_fixture(self):
        with patch.object(source_rehearsal.platform, "system", return_value="Darwin"), \
             patch.object(source_rehearsal, "nix") as nix:
            with self.assertRaisesRegex(ValueError, "supported native Linux"):
                source_rehearsal.main()
        nix.assert_not_called()


if __name__ == "__main__":
    unittest.main()
