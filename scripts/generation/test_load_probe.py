"""The load probe binds its receipt to the bundle, the export and the device, with a scripted runtime."""
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import load_probe
from load_probe import PROMPT, main, served_cases


class Runtime:
    """Stands in for the native runner's command protocol."""
    script = {}

    def __init__(self, process):
        self.steps = iter(self.script.get("tokens", [7, 8, 9]))

    def call(self, method, **fields):
        if method in self.script.get("rejects", ()):
            raise RuntimeError("native runner rejected " + method + ": Unsupported E4B architecture")
        if method == "hello":
            return {"backend": self.script.get("backend", "mlx"), "physical_device": {"kind": "apple_silicon"}, "artifact_id": "a" * 64}
        if method == "inspect":
            return {"plan": {"device_reservation_bytes": 1, "host_reservation_bytes": 1}}
        if method == "advance":
            token = next(self.steps, None)
            return {"progress": {"token": token, "stop_reason": None if token is not None else "max_tokens"}}
        return {}


class Process:
    stdin = stdout = type("Pipe", (), {"close": lambda self: None})()

    def poll(self):
        return None

    def kill(self):
        pass

    def wait(self):
        return 0


class LoadProbe(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = root = Path(directory.name)
        (root / "bundle").mkdir()
        (root / "bundle/manifest.json").write_text('{"signed": true}')
        for name in ("runner", "renderer", "verifier", "authority"):
            (root / name).write_text(name)
        self.prompt = [2, 105, 9]
        reference = {"prompts": [{"name": PROMPT, "prompt_tokens": self.prompt, "tokens": [7, 8, 9], "margins": [6.0, 5.0, 4.0]}]}
        (root / "behavior.json").write_text(json.dumps(reference))
        self.behavior = hashlib.sha256((root / "behavior.json").read_bytes()).hexdigest()
        self.export = {"student": "e4b", "behavior_sha256": self.behavior, "outputs": {"mlx": {"sha256": "a" * 64}, "vulkan": {"sha256": "b" * 64}}}
        (root / "export_receipt.json").write_text(json.dumps(self.export))
        self.rendered, self.dirty, self.signed = self.prompt, "", "a" * 64
        Runtime.script = {}
        patcher = patch.object(load_probe, "Client", Runtime)
        patcher.start()
        self.addCleanup(patcher.stop)

    def run_tool(self, command, **kwargs):
        done = lambda out="", code=0, err="": subprocess.CompletedProcess(command, code, out, err)  # noqa: E731
        if command[0] == "git":
            return done("c" * 40 + "\n" if "rev-parse" in command else self.dirty)
        if command[0] == str(self.root / "verifier"):
            if self.signed is None:
                return done(code=1, err="signature mismatch")
            return done(json.dumps({"manifest": {"revision": "r1", "artifact": {"digest": self.signed}}, "signature": "s"}))
        self.request = json.loads(Path(command[2]).read_text())
        return done(json.dumps({"prompt_tokens": self.rendered})) if self.rendered else done(code=1, err="unsupported conversation format")

    def probe(self, device="mlx", student="e4b"):
        argv = ["--student", student, "--device", device, "--export-receipt", str(self.root / "export_receipt.json"),
                "--reference", str(self.root / "behavior.json"), "--out", str(self.root / "receipt.json"), "--metallib", "/lib",
                "--device-pci", "0000:01:00.0"]
        for name in ("bundle", "runner", "renderer", "verifier", "authority"):
            argv += ["--" + name, str(self.root / name)]
        status = main(argv, self.run_tool, lambda *args, **kwargs: Process())
        return status, json.loads((self.root / "receipt.json").read_text())

    def test_a_loaded_bundle_that_reproduces_the_export_passes_with_its_bindings(self):
        status, receipt = self.probe()
        self.assertEqual(status, 0)
        self.assertEqual(receipt["schema"], "pw-load-probe/1")
        self.assertTrue(receipt["passed"])
        self.assertIsNone(receipt["failure"])
        self.assertEqual((receipt["student"], receipt["bundle"], receipt["model"], receipt["revision"]),
                         ("e4b", "mlx", "gemma4-e4b-paintedwolf", "r1"))
        self.assertEqual(receipt["manifest_sha256"], hashlib.sha256(b'{"signed": true}').hexdigest())
        self.assertEqual(receipt["artifact_sha256"], "a" * 64)
        self.assertEqual(receipt["export_receipt_sha256"], hashlib.sha256((self.root / "export_receipt.json").read_bytes()).hexdigest())
        self.assertEqual(receipt["behavior_sha256"], self.behavior)
        self.assertEqual(receipt["host_commit"], "c" * 40)
        self.assertEqual(receipt["device"]["physical_device"], {"kind": "apple_silicon"})
        self.assertEqual(receipt["generated_tokens"], [7, 8, 9])
        self.assertEqual(receipt["behavior"]["agreed_tokens"], [3])
        case = next(case for case in served_cases() if case["name"] == PROMPT)
        self.assertEqual(self.request, {"messages": case["messages"], "tools": case["tools"]})

    def test_each_runtime_failure_is_a_failed_receipt_naming_its_step(self):
        for change, code in (({"rejects": ("load",)}, "load_failed"), ({"rejects": ("begin",)}, "generation_failed"),
                             ({"backend": "vulkan"}, "generation_failed"), ({"tokens": []}, "generation_failed"),
                             ({"tokens": [7, 1, 9]}, "behavior_mismatch")):
            with self.subTest(change=change):
                Runtime.script = change
                status, receipt = self.probe()
                self.assertEqual((status, receipt["passed"], receipt["failure"]["code"]), (2, False, code))
                self.assertEqual(receipt["artifact_sha256"], "a" * 64)
        Runtime.script = {}
        for rendered, code in (([2, 105], "rendering_mismatch"), (None, "render_failed")):
            self.rendered = rendered
            self.assertEqual(self.probe()[1]["failure"]["code"], code)

    def test_a_tier_the_runtime_does_not_load_fails_before_the_runtime_starts(self):
        self.export["student"] = "12b"
        (self.root / "export_receipt.json").write_text(json.dumps(self.export))
        Runtime.script = {"rejects": ("hello",)}
        status, receipt = self.probe(student="12b")
        self.assertEqual((status, receipt["failure"]["code"]), (2, "native_runtime_unsupported"))
        self.assertIsNone(receipt["device"])
        declared = json.loads(load_probe.TIERS.read_text())
        self.assertEqual(declared["schema"], "pw-native-tiers/1")
        self.assertEqual(set(declared) - {"schema"}, set(load_probe.PRECISION))
        self.assertTrue(all(set(declared[device]) <= set(load_probe.MODELS) for device in load_probe.PRECISION))

    def test_a_bundle_that_is_not_the_export_yields_no_receipt(self):
        for change, reason in (({"signed": None}, "does not verify"), ({"signed": "f" * 64}, "does not package this export"),
                               ({"dirty": " M file\n"}, "clean checkout")):
            with self.subTest(change=change):
                saved = {name: getattr(self, name) for name in change}
                for name, value in change.items():
                    setattr(self, name, value)
                with self.assertRaisesRegex(ValueError, reason):
                    self.probe()
                for name, value in saved.items():
                    setattr(self, name, value)
        with self.assertRaisesRegex(ValueError, "does not package this export"):
            self.probe(student="12b")
        (self.root / "behavior.json").write_text("{}")
        with self.assertRaisesRegex(ValueError, "not the export's"):
            self.probe()
        self.assertFalse((self.root / "receipt.json").exists())


if __name__ == "__main__":
    unittest.main()
