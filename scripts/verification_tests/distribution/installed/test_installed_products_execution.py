"""Only actual selected product probes can produce a passed installed case."""
import copy
import hashlib
import json
from pathlib import Path
import platform
import sys
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release.installed_cases import products
from scripts.linux_release.installed_cases.process import run
from scripts.linux_release.installed_runtime import file_identity


class InstalledProductTests(unittest.TestCase):
    def binding(self):
        binary = "/nix/store/" + "0" * 32 + "-application/lib/painted-wolf-code/engine-root/bundled/opengrep"
        root = binary.partition("/engine-root")[0]
        return {"source_commit": "a" * 40, "install_root": root,
                "artifact": {"architecture": "amd64", "identity": {"store_path": root.partition("/lib/")[0]}},
                "images": {binary: {"sha256": "b" * 64, "bytes": 4096}}}

    def report(self, binding):
        return {"schema_version": 1, "status": "passed", "goos": "linux", "goarch": "amd64",
                "engine_root": binding["install_root"] + "/engine-root", "confined": True,
                "binary": next(iter(binding["images"])), "binary_sha256": "b" * 64, "binary_bytes": 4096,
                "cases": [{"file": file, "passed": True, "findings": 0} for file in sorted(products.SCANNER_FILES)]}

    def test_scanner_requires_actual_confined_native_identity_and_all_product_cases(self):
        binding = self.binding()
        report = self.report(binding)
        self.assertEqual(products.scanner_report(json.dumps(report).encode(), binding), report)
        for field, value in [("status", "unverified"), ("confined", False), ("goarch", "arm64"),
                             ("binary_sha256", "c" * 64), ("binary", "/unrelated/scanner"),
                             ("cases", report["cases"][:-1]), ("schema_version", True)]:
            changed = {**report, field: value}
            with self.subTest(field=field), self.assertRaises(ValueError):
                products.scanner_report(json.dumps(changed).encode(), binding)
        report["cases"][0]["passed"] = False
        with self.assertRaises(ValueError):
            products.scanner_report(json.dumps(report).encode(), binding)

    def test_missing_driver_never_invokes_a_command_or_passes(self):
        with patch.object(products, "run") as execute:
            result = products.execute({}, "vault", "service-and-password-onboarding", Path("unused"))
        self.assertEqual(result["status"], "unverified")
        execute.assert_not_called()

    def test_refused_guest_never_launches_product(self):
        with patch.object(products, "require_owned_guest", side_effect=ValueError("not owned")), patch.object(products, "run") as execute:
            with self.assertRaises(ValueError):
                products.execute(self.binding(), "documents", "helper-startup", Path("unused"))
        execute.assert_not_called()

    def test_product_execution_retains_exact_logs_and_cannot_substitute_a_proof_only_command(self):
        binding = self.binding()
        response = {"exit_code": 0, "timed_out": False, "output_limit": False,
                    "stdout": json.dumps(self.report(binding)).encode(), "stderr": b"diagnostic"}
        with tempfile.TemporaryDirectory() as directory, patch.object(products, "require_owned_guest"), patch.object(
                products, "run", return_value=copy.deepcopy(response)) as execute:
            root = Path(directory)
            result = products.execute(binding, "documents", "pinned-scanner-execution", root)
            self.assertEqual(result["status"], "passed")
            self.assertEqual(execute.call_args.args[2], ["scan", "engines", "verify-installed", "--root", binding["install_root"] + "/engine-root"])
            for item in result["evidence"]:
                self.assertEqual(hashlib.sha256((root / item["path"]).read_bytes()).hexdigest(), item["sha256"])
            record = json.loads((root / result["evidence"][-1]["path"]).read_text())
            self.assertEqual(record["artifact"], binding["artifact"])

    def test_failure_timeout_output_limit_and_incomplete_report_keep_evidence_and_fail(self):
        for outcome in [{"exit_code": 1}, {"timed_out": True}, {"output_limit": True}, {"stdout": b"{}"}]:
            response = {"exit_code": 0, "timed_out": False, "output_limit": False,
                        "stdout": json.dumps(self.report(self.binding())).encode(), "stderr": b"diagnostic", **outcome}
            with self.subTest(outcome=outcome), tempfile.TemporaryDirectory() as directory, patch.object(
                    products, "require_owned_guest"), patch.object(products, "run", return_value=response):
                result = products.execute(self.binding(), "documents", "pinned-scanner-execution", Path(directory))
                self.assertEqual(result["status"], "failed")
                self.assertEqual(len(result["evidence"]), 3)

    def test_descriptor_replacement_refuses_before_exec(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "pw"
            path.write_bytes(b"replaced native image")
            info = path.stat()
            binding = {"images": {str(path): {"sha256": "0" * 64, "bytes": info.st_size,
                       "mode": info.st_mode & 0o777, "device": info.st_dev, "inode": info.st_ino}}}
            with patch("scripts.linux_release.installed_cases.process.platform.system", return_value="Linux"), patch(
                    "scripts.linux_release.installed_cases.process.unchanged", return_value=path), patch(
                    "scripts.linux_release.installed_cases.process.subprocess.Popen") as execute:
                with self.assertRaisesRegex(ValueError, "descriptor"):
                    run(binding, "pw", ["diagnostics", "document-core"])
            execute.assert_not_called()

    def test_native_descriptor_process_completes_and_bounds_timeout_and_output(self):
        if platform.system() != "Linux":
            with self.assertRaisesRegex(ValueError, "require Linux"):
                run({}, "pw", [])
            return
        path = Path(sys.executable).resolve()
        identity = file_identity(path)
        identity.pop("header")
        binding = {"images": {str(path): identity}}
        with patch("scripts.linux_release.installed_cases.process.unchanged", return_value=path):
            result = run(binding, "pw", ["-I", "-c", "import sys; print('completed'); print('diagnostic',file=sys.stderr)"])
            self.assertEqual(result["exit_code"], 0)
            self.assertEqual(result["stdout"], b"completed\n")
            self.assertEqual(result["stderr"], b"diagnostic\n")
            self.assertFalse(result["timed_out"] or result["output_limit"])
            result = run(binding, "pw", ["-I", "-c", "import time; time.sleep(30)"], timeout=0.05)
            self.assertTrue(result["timed_out"])
            with patch("scripts.linux_release.installed_cases.process.LIMIT", 1024):
                result = run(binding, "pw", ["-I", "-c", "print('x'*2048)"])
            self.assertTrue(result["output_limit"])

    def test_nixos_root_controlled_symlink_marker_is_supported_but_root_execution_is_refused(self):
        binding = self.binding()
        with tempfile.TemporaryDirectory() as directory:
            physical = Path(directory) / "actual"
            physical.write_text(json.dumps({"format_version": 1, "desktop": "gnome", "package": binding["artifact"]["identity"]["store_path"]}))
            marker = Path(directory) / "declaration"
            marker.symlink_to(physical)
            info = SimpleNamespace(st_uid=0, st_mode=0o100444, st_size=physical.stat().st_size)
            with patch.object(Path, "lstat", return_value=info), patch.object(Path, "resolve", return_value=physical), patch.object(
                    Path, "stat", return_value=info), patch("os.getuid", return_value=1000):
                self.assertEqual(products.require_owned_guest(binding, marker)["desktop"], "gnome")
            with patch.object(Path, "lstat", return_value=info), patch.object(Path, "resolve", return_value=physical), patch.object(
                    Path, "stat", return_value=info), patch("os.getuid", return_value=0):
                with self.assertRaises(ValueError):
                    products.require_owned_guest(binding, marker)
