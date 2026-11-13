"""Contracts for uploading signed student bundles."""

import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import publish_bundle


def signed(model, device, revision="r1"):
    payload = "model.safetensors" if device == "mlx" else "model-Q4_K_M.gguf"
    files = [{"name": "config.json"}, {"name": "chat_template.jinja"}] if device == "mlx" else []
    return {"manifest": {"model": model, "revision": revision, "device": device, "artifact": {"name": payload}, "files": files},
            "signature": "s", "key_id": "k"}


class Recorder:
    """Stands in for model-bundle and the immutable put."""

    def __init__(self, answer, refuse=None):
        self.answer, self.refuse, self.calls = answer, refuse, []

    def __call__(self, args, check=False, **kw):
        self.calls.append(args)
        if args[0] == "bash" and self.refuse and args[args.index("--key") + 1].endswith(self.refuse):
            raise subprocess.CalledProcessError(1, args)
        return subprocess.CompletedProcess(args, 0, json.dumps(self.answer) if args[0] != "bash" else "", "")


class PublishTests(unittest.TestCase):
    def test_every_student_and_device_uploads_payloads_then_the_manifest(self):
        for model in publish_bundle.MODELS:
            for device in publish_bundle.DEVICES:
                with self.subTest(model=model, device=device):
                    run = Recorder(signed(model, device))
                    keys = publish_bundle.publish("/bin/model-bundle", "/bundle", "/keys.json", model, device, run=run)
                    prefix = "models/%s/%s/r1/" % (model, device)
                    self.assertTrue(all(key.startswith(prefix) for key in keys))
                    self.assertEqual(keys[-1], prefix + "manifest.json")
                    self.assertEqual(run.calls[0][:3], ["/bin/model-bundle", "-bundle", "/bundle"])
                    puts = [call for call in run.calls if call[0] == "bash"]
                    self.assertEqual([call[call.index("--key") + 1] for call in puts], keys)
                    types = {call[call.index("--key") + 1]: call[call.index("--content-type") + 1] for call in puts}
                    self.assertEqual(types[prefix + "manifest.json"], "application/json")

    def test_a_bundle_for_another_slot_or_an_unknown_student_is_refused(self):
        run = Recorder(signed("gemma4-12b-paintedwolf", "mlx"))
        with self.assertRaisesRegex(ValueError, "another model or device"):
            publish_bundle.publish("/bin/model-bundle", "/bundle", "/keys.json", "gemma4-e4b-paintedwolf", "mlx", run=run)
        self.assertFalse([call for call in run.calls if call[0] == "bash"])
        with self.assertRaisesRegex(ValueError, "a student bundle is one of"):
            publish_bundle.publish("/bin/model-bundle", "/bundle", "/keys.json", "gemma4-e4b-paintedwolf", "cuda", run=run)

    def test_an_unverified_bundle_uploads_nothing(self):
        def refuse(args, **kw):
            raise subprocess.CalledProcessError(1, args)
        with self.assertRaises(subprocess.CalledProcessError):
            publish_bundle.publish("/bin/model-bundle", "/bundle", "/keys.json", "gemma4-e4b-paintedwolf", "vulkan", run=refuse)

    def test_a_refused_payload_leaves_the_manifest_unpublished(self):
        run = Recorder(signed("gemma4-26b-paintedwolf", "mlx"), refuse="config.json")
        with self.assertRaises(subprocess.CalledProcessError):
            publish_bundle.publish("/bin/model-bundle", "/bundle", "/keys.json", "gemma4-26b-paintedwolf", "mlx", run=run)
        keys = [call[call.index("--key") + 1] for call in run.calls if call[0] == "bash"]
        self.assertFalse(any(key.endswith("manifest.json") for key in keys))

    def test_a_real_verifier_and_put_script_run_end_to_end(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            fake = root / "model-bundle"
            fake.write_text("#!/bin/sh\necho '%s'\n" % json.dumps(signed("gemma4-e4b-paintedwolf", "vulkan")))
            fake.chmod(0o755)
            log = root / "puts.log"
            put = root / "put.sh"
            put.write_text('#!/bin/sh\necho "$2" >> %s\n' % log)
            keys = publish_bundle.publish(fake, root, "/keys.json", "gemma4-e4b-paintedwolf", "vulkan", put=put)
            self.assertEqual(log.read_text().split(), keys)
            self.assertEqual(keys, ["models/gemma4-e4b-paintedwolf/vulkan/r1/model-Q4_K_M.gguf",
                                    "models/gemma4-e4b-paintedwolf/vulkan/r1/manifest.json"])

    def test_the_command_line_prints_each_key(self):
        calls = []
        argv = ["publish_bundle.py", "--model-bundle", "/bin/mb", "--bundle", "/b", "--authority", "/k.json",
                "--model", "gemma4-12b-paintedwolf", "--device", "mlx"]
        with patch.object(sys, "argv", argv), patch.object(publish_bundle, "publish", lambda *a: calls.append(a) or ["k1", "k2"]), \
                patch("sys.stdout", new_callable=io.StringIO) as out:
            publish_bundle.main()
        self.assertEqual(out.getvalue().split(), ["k1", "k2"])
        self.assertEqual(calls[0][3:], ("gemma4-12b-paintedwolf", "mlx"))

if __name__ == "__main__":
    unittest.main()
