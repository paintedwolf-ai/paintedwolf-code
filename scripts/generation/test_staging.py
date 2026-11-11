"""Application staging preserves authenticated bytes and excludes extra files."""

from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import staging


class StagingTests(unittest.TestCase):
    def test_seed_copies_only_verified_named_payloads_and_preserves_input(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "source"
            source.mkdir()
            for name in ("manifest.json", "model.safetensors", "unused-secret"):
                (source / name).write_text(name)
            authority = root / "keys"
            authority.write_text("trusted keys")
            signed = {"manifest": {"artifact": {"name": "model.safetensors"}, "files": []}}
            resources = root / "generation"
            def verify(verifier, bundle, keys, backend):
                self.assertEqual((bundle / "model.safetensors").read_text(), "model.safetensors")
                self.assertEqual(keys.read_text(), "trusted keys")
                self.assertEqual(backend, "mlx")
                return signed
            with patch.object(staging, "verify_seed", side_effect=verify) as verify_mock:
                staging.stage_seed(Path("verifier"), source, authority, resources, "mlx")
            self.assertEqual(verify_mock.call_count, 2)
            self.assertEqual({p.name for p in (resources / "models/mlx").iterdir()}, {"manifest.json", "model.safetensors"})
            self.assertTrue((source / "unused-secret").is_file())
            self.assertEqual((resources / "release-keys.json").read_text(), "trusted keys")

    def test_invalid_or_changed_seed_does_not_replace_existing_resources(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "source"
            source.mkdir()
            for name in ("manifest.json", "model", "keys"):
                (source / name).write_text(name)
            resources = root / "generation"
            current = resources / "models/mlx"
            current.mkdir(parents=True)
            (current / "preserve").write_text("existing")
            valid = {"manifest": {"artifact": {"name": "model"}}}
            for invalid in ("", "../escape", "manifest.json"):
                with patch.object(staging, "verify_seed", return_value={"manifest": {"artifact": {"name": invalid}}}):
                    with self.assertRaises(ValueError):
                        staging.stage_seed(Path("verifier"), source, source / "keys", resources, "mlx")
            with patch.object(staging, "verify_seed", side_effect=[valid, ValueError("damaged copy")]):
                with self.assertRaisesRegex(ValueError, "damaged"):
                    staging.stage_seed(Path("verifier"), source, source / "keys", resources, "mlx")
            self.assertEqual((current / "preserve").read_text(), "existing")
            self.assertEqual(sorted(p.name for p in resources.iterdir()), ["models"])

    def test_runtime_requires_build_receipt_and_copies_executable_and_shader(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            binary = root / "nativegen"
            binary.write_bytes(b"binary")
            (root / "metal").mkdir()
            shader = root / "metal/mlx.metallib"
            shader.write_bytes(b"shader")
            receipt = {"binary_sha256": staging.file_digest(binary), "metallib_sha256": staging.file_digest(shader)}
            destination = root / "output/nativegen"
            resources = root / "output/generation"
            with patch.object(staging, "verified_build", return_value=receipt):
                self.assertEqual(staging.stage_runtime(root, binary, destination, resources, "mlx"), receipt)
            self.assertEqual(destination.read_bytes(), b"binary")
            self.assertEqual(destination.stat().st_mode & 0o777, 0o755)
            self.assertEqual((resources / "mlx.metallib").read_bytes(), b"shader")
            with patch.object(staging, "verified_build", side_effect=ValueError("rebuild")):
                with self.assertRaisesRegex(ValueError, "rebuild"):
                    staging.stage_runtime(root, binary, destination, resources, "mlx")
            link = root / "link"
            link.symlink_to(binary)
            with self.assertRaises(ValueError):
                staging.publish_file(link, destination)

    def test_seed_is_optional_but_requires_complete_trust_inputs(self):
        import importlib.util
        spec = importlib.util.spec_from_file_location("stage_generation", Path(__file__).with_name("stage.py"))
        command = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(command)
        with patch.object(command.runtime, "build") as build, \
                patch.object(command.runtime, "paths", return_value=(Path("source"), Path("build"))), \
                patch.object(command.runtime, "native_environment", return_value=({}, "vulkan")), \
                patch.object(command, "stage_runtime") as stage_runtime:
            with patch.dict(command.os.environ, {}, clear=True):
                command.stage(Path("nativegen"), Path("generation"))
            build.assert_called_once()
            stage_runtime.assert_called_once_with(
                Path("build"), Path("build/release/nativegen"), Path("nativegen"),
                Path("generation"), "vulkan")
            build.reset_mock()
            for environment in ({"BIALY_GENERATION_BUNDLE": "/seed"},
                                {"BIALY_GENERATION_AUTHORITY": "/keys"}):
                with patch.dict(command.os.environ, environment, clear=True):
                    with self.assertRaisesRegex(ValueError, "requires"):
                        command.stage(Path("nativegen"), Path("generation"))
            build.assert_not_called()
        with tempfile.TemporaryDirectory() as temporary:
            with patch.object(command.runtime, "build"), \
                    patch.object(command.runtime, "paths", return_value=(Path("source"), Path(temporary))), \
                    patch.object(command.runtime, "native_environment", return_value=({}, "vulkan")), \
                    patch.object(command, "stage_runtime"), patch.object(command.runtime, "run") as run, \
                    patch.object(command, "stage_seed") as stage_seed, \
                    patch.dict(command.os.environ, {
                        "BIALY_GENERATION_BUNDLE": "/seed",
                        "BIALY_GENERATION_AUTHORITY": "/keys",
                    }, clear=True):
                command.stage(Path("nativegen"), Path("generation"))
        run.assert_called_once()
        self.assertEqual(run.call_args.args[0][:2], ["go", "build"])
        self.assertEqual(stage_seed.call_args.args[1:], (
            Path("/seed"), Path("/keys"), Path("generation"), "vulkan"))

    def test_vulkan_library_is_staged_for_build_and_installed_layouts(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            binary = root / "nativegen"
            binary.write_bytes(b"binary")
            adapter = root / "libnativegen_vulkan.so"
            adapter.write_bytes(b"adapter")
            receipt = {"binary_sha256": staging.file_digest(binary), "adapter_sha256": staging.file_digest(adapter)}
            output = root / "output/bin/nativegen"
            resources = root / "output/engine-root/generation"
            with patch.object(staging, "verified_build", return_value=receipt):
                staging.stage_runtime(root, binary, output, resources, "vulkan")
            self.assertEqual((output.parent / adapter.name).read_bytes(), b"adapter")
            self.assertEqual((resources / adapter.name).read_bytes(), b"adapter")
