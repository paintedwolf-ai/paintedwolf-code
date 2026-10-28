"""Cache rehearsal rejects altered closure bytes and untrusted substitution."""

from contextlib import contextmanager
from io import StringIO
import json
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import patch

from scripts.nix_cache import rehearsal, rehearsal_transport


OUTPUT = "/nix/store/" + "0" * 32 + "-application"
DEPENDENCY = "/nix/store/" + "1" * 32 + "-dependency"


class NixCacheRehearsalEdges(unittest.TestCase):
    def test_negative_cache_and_altered_closure_are_refused_before_success_report(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            cache = work / "cache"
            (cache / "nar").mkdir(parents=True)
            (cache / "nar/payload.nar.xz").write_bytes(b"signed NAR")
            (cache / ("0" * 32 + ".narinfo")).write_text("NarSize: 10\n")
            (cache / ("1" * 32 + ".narinfo")).write_text("NarSize: 9\n")
            keys = ["old-public", "new-public"]
            report = {"request": {"mode": "rehearsal", "system": "x86_64-linux"},
                      "closure": {OUTPUT: {"references": [DEPENDENCY]}, DEPENDENCY: {"references": []}},
                      "objects": [{"name": "nar/payload.nar.xz", "phase": "nar"}],
                      "nix_version": "nix 2.0"}
            selected = SimpleNamespace(work=work, system="x86_64-linux", source="local?root=source",
                                       output=OUTPUT, keys=keys, key_files=[work / "old", work / "new"],
                                       cache=cache, report=report)

            @contextmanager
            def fixture():
                yield selected

            def nix(*arguments, **_kwargs):
                if arguments[0] == "copy" and "--trusted-public-keys" in arguments:
                    if arguments[arguments.index("--trusted-public-keys") + 1] == keys[1]:
                        raise ValueError("wrong cold-substitution key")
                return ""

            def verify(candidate, _outputs, public):
                if candidate == cache and public == [keys[1]]:
                    raise ValueError("wrong cache signing key")
                if candidate == cache or candidate.name == "rotated":
                    return
                nar = candidate / "nar/payload.nar.xz"
                app = candidate / ("0" * 32 + ".narinfo")
                dependency = candidate / ("1" * 32 + ".narinfo")
                if (not nar.exists() or nar.read_bytes() != b"signed NAR" or
                        not app.exists() or app.read_text() != "NarSize: 10\n" or not dependency.exists()):
                    raise ValueError("changed cache closure")

            with patch.object(rehearsal, "cache_fixture", fixture), \
                 patch.object(rehearsal, "construct", side_effect=ValueError("wrong system")), \
                 patch.object(rehearsal, "verify", side_effect=verify), \
                 patch.object(rehearsal, "nix", side_effect=nix), \
                 patch.object(rehearsal, "path_info", return_value=report["closure"]), \
                 patch.object(rehearsal, "local_store", side_effect=lambda root: "local?root=" + str(root)), \
                 patch.object(rehearsal, "inventory", return_value=report["objects"]), \
                 patch.object(rehearsal, "validate_request", side_effect=ValueError("production refused")), \
                 patch.object(rehearsal_transport, "run", return_value=["ordinary-http"]), \
                 patch("sys.stdout", new_callable=StringIO) as output:
                rehearsal.main()
            result = json.loads(output.getvalue())
            self.assertEqual(result["status"], "passed")
            self.assertIn("altered-nar", result["checks"])
            self.assertIn("ordinary-http", result["checks"])

    def test_must_refuse_rejects_successful_invalid_operation(self):
        with self.assertRaisesRegex(ValueError, "accepted unexpected success"):
            rehearsal.must_refuse(lambda: None, "unexpected success")
        rehearsal.must_refuse(lambda: (_ for _ in ()).throw(ValueError("refused")), "invalid")


if __name__ == "__main__":
    unittest.main()
