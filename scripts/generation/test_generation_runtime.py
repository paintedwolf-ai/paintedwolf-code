"""Deterministic orchestration contracts for the generation command."""

import argparse
import contextlib
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest import mock

import runtime


class RuntimeCoverageTests(unittest.TestCase):
    def test_paths_environments_and_subprocess_arguments(self):
        with tempfile.TemporaryDirectory() as temporary, \
                mock.patch.object(runtime, "ROOT", Path(temporary)), \
                mock.patch.object(runtime, "compiler_cache_dir", return_value=Path("/cache")), \
                mock.patch.object(runtime, "bin_dir", return_value=Path("/tools")):
            swift = runtime.paths("reference-swift")
            rust = runtime.paths("reference-rust")
            llama = runtime.paths("reference-llama")
            self.assertTrue(str(swift[1]).endswith("generation-swift"))
            self.assertTrue(str(rust[1]).endswith("generation-rust"))
            with mock.patch.object(runtime, "ROOT", Path(temporary) / "snapshot"):
                self.assertNotEqual(llama[1], runtime.paths("reference-llama")[1])
            with mock.patch.object(runtime.sys, "platform", "darwin"), \
                    mock.patch.object(runtime.platform, "machine", return_value="arm64"):
                environment, backend = runtime.native_environment(Path("/build"))
                self.assertEqual(backend, "mlx")
                self.assertEqual(environment["CARGO_TARGET_DIR"], "/build")
                self.assertEqual(runtime.swift_environment(Path("/build"))["PW_REFERENCE_METALLIB"],
                                 "/build/metal/mlx.metallib")
            with mock.patch.object(runtime.sys, "platform", "linux"), \
                    mock.patch.object(runtime, "paths", return_value=(Path("/src"), Path("/vulkan"))):
                environment, backend = runtime.native_environment(Path("/build"))
                self.assertEqual(backend, "vulkan")
                self.assertEqual(environment["LD_LIBRARY_PATH"], "/vulkan")
            with mock.patch.object(runtime.sys, "platform", "freebsd"):
                with self.assertRaisesRegex(ValueError, "requires Apple silicon or Linux Vulkan"):
                    runtime.native_environment(Path("/build"))
            with mock.patch.object(runtime.subprocess, "run") as run:
                runtime.run([Path("/tool"), "arg"], cwd=Path("/work"), env={"KEY": "value"})
            run.assert_called_once_with(["/tool", "arg"], cwd=Path("/work"), env={"KEY": "value"}, check=True)
            self.assertEqual(runtime.rust_environment(Path("/build"))["CARGO_TARGET_DIR"], "/build")

    def test_prepare_selects_platform_tools_and_rejects_unsupported_targets(self):
        args = argparse.Namespace(backend="reference-swift", install_build_tools=True,
                                  refresh_lock=False, model=None)
        with mock.patch.object(runtime.sys, "platform", "darwin"), \
                mock.patch.object(runtime, "paths", return_value=(Path("/swift"), Path("/build"))), \
                mock.patch.object(runtime.swift_toolchain, "prepare", return_value=Path("/swiftc")), \
                mock.patch.object(runtime, "run") as run:
            runtime.prepare(args)
        self.assertEqual(run.call_args_list[0].args[0][:2], ["xcodebuild", "-downloadComponent"])
        self.assertEqual(run.call_args_list[1].args[0][1:3], ["package", "--package-path"])

        args = argparse.Namespace(backend="reference-llama", install_build_tools=False,
                                  refresh_lock=False, model=None)
        with mock.patch.object(runtime.sys, "platform", "linux"), \
                mock.patch.object(runtime.vulkan_tools, "prepare") as prepare, \
                mock.patch.object(runtime, "ensure_source", return_value=Path("/source")) as ensure, \
                contextlib.redirect_stdout(io.StringIO()):
            runtime.prepare(args)
        prepare.assert_called_once()
        ensure.assert_called_once()

        args = argparse.Namespace(backend="reference-mlx", install_build_tools=False,
                                  refresh_lock=False, model=Path("/model"), output=Path("/out"),
                                  artifact_identity="digest", precision=None)
        with mock.patch.object(runtime.sys, "platform", "darwin"), \
                mock.patch.object(runtime.platform, "machine", return_value="arm64"), \
                mock.patch.object(runtime, "run") as run:
            runtime.prepare(args)
        self.assertIn("convert_mlx.py", str(run.call_args.args[0]))
        for invalid in (
                argparse.Namespace(backend="reference-mlx", install_build_tools=True),
                argparse.Namespace(backend="reference-mlx", install_build_tools=False, refresh_lock=False,
                                   model=Path("/model"), output=None, artifact_identity=None),
        ):
            with mock.patch.object(runtime.sys, "platform", "linux"), \
                    self.assertRaises(ValueError):
                runtime.prepare(invalid)

        args = argparse.Namespace(backend="reference-rust", install_build_tools=False,
                                  refresh_lock=True)
        with mock.patch.object(runtime, "paths", return_value=(Path("/native"), Path("/build"))), \
                mock.patch.object(runtime, "run") as run:
            runtime.prepare(args)
        self.assertEqual(run.call_args.args[0][:2], ["cargo", "fetch"])

    def test_install_build_tools_installs_arch_appropriate_vulkan_packages(self):
        args = argparse.Namespace(backend="native", install_build_tools=True,
                                  refresh_lock=False, model=None)
        for machine, expected_extra in (("aarch64", ["glslc"]), ("x86_64", [])):
            with self.subTest(machine=machine), \
                    mock.patch.object(runtime.sys, "platform", "linux"), \
                    mock.patch.object(runtime.platform, "machine", return_value=machine), \
                    mock.patch.object(runtime.vulkan_tools, "prepare"), \
                    mock.patch.object(runtime, "ensure_source"), \
                    mock.patch.object(runtime, "run") as run:
                runtime.prepare(args)
            self.assertEqual(run.call_args_list[0].args[0], ["sudo", "-n", "apt-get", "update"])
            self.assertEqual(run.call_args_list[1].args[0],
                             ["sudo", "-n", "apt-get", "install", "-y", "--no-install-recommends",
                              "cmake", "ninja-build", "libvulkan-dev", *expected_extra])

    def test_build_uses_recorded_native_and_reference_artifacts(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source, output, tools, library = (root / name for name in ("source", "build", "tools", "vulkan"))
            source.mkdir()
            output.mkdir()
            (output / "release").mkdir()
            tools.mkdir()
            sdk = tools / runtime.vulkan_tools.CACHE_NAME
            (sdk / "bin").mkdir(parents=True)
            (sdk / "bin/glslc").write_text("compiler")
            library.mkdir()
            (library / "libnativegen_vulkan.so").write_bytes(b"adapter")
            (source / ".source-sha256").write_text("source")
            args = argparse.Namespace(backend="reference-llama", reference_cpu="portable")
            with mock.patch.object(runtime, "paths", return_value=(source, output)), \
                    mock.patch.object(runtime, "bin_dir", return_value=tools), \
                    mock.patch.object(runtime.sys, "platform", "linux"), \
                    mock.patch.object(runtime.platform, "machine", return_value="x86_64"), \
                    mock.patch.object(runtime, "run") as run, \
                    mock.patch.object(runtime, "record_build") as record:
                runtime.build(args)
            self.assertEqual(run.call_count, 2)
            self.assertIn("-DGGML_AVX2=OFF", run.call_args_list[0].args[0])
            record.assert_called_once_with("reference-llama", output, output / "generation-reference")

            def paths(backend):
                return (source, output) if backend == "native" else (source, output)

            env = {"PW_GENERATION_VULKAN_LIB_DIR": str(library)}
            real_build = runtime.build
            with mock.patch.object(runtime, "paths", side_effect=paths), \
                    mock.patch.object(runtime, "bin_dir", return_value=tools), \
                    mock.patch.object(runtime.sys, "platform", "linux"), \
                    mock.patch.object(runtime.platform, "machine", return_value="x86_64"), \
                    mock.patch.object(runtime, "build", wraps=real_build) as build, \
                    mock.patch.object(runtime, "native_environment", return_value=(env, "vulkan")), \
                    mock.patch.object(runtime, "run") as run, \
                    mock.patch.object(runtime, "record_build") as record:
                runtime.build(argparse.Namespace(backend="native"))
            self.assertEqual(build.call_count, 2)
            self.assertEqual(build.call_args_list[0].args[0].backend, "native")
            self.assertEqual(build.call_args_list[1].args[0].backend, "reference-llama")
            self.assertTrue((output / "release/libnativegen_vulkan.so").is_file())
            self.assertEqual(run.call_args.args[0][0], "cargo")
            self.assertEqual(record.call_args_list[-1].args, (
                "native", output, output / "release/nativegen"))

            with mock.patch.object(runtime.sys, "platform", "linux"), \
                    mock.patch.object(runtime, "paths", return_value=(root / "missing", output)):
                with self.assertRaisesRegex(ValueError, "generation:prepare"):
                    runtime.build(args)

    def test_test_dispatches_only_the_selected_backend(self):
        with mock.patch.object(runtime, "run") as run:
            runtime.test(argparse.Namespace(backend="reference-llama"))
        self.assertEqual(run.call_count, 1)
        self.assertIn("test_vulkan_tools", run.call_args.args[0])
        with mock.patch.object(runtime, "swift_shaders", return_value=Path("/shader")), \
                mock.patch.object(runtime, "paths", return_value=(Path("/source"), Path("/build"))), \
                mock.patch.object(runtime.swift_toolchain, "executable", return_value=Path("/swift")), \
                mock.patch.object(runtime, "run") as run:
            runtime.test(argparse.Namespace(backend="reference-swift"))
        self.assertEqual(str(run.call_args.args[0][0]), "/swift")
        with mock.patch.object(runtime, "run") as run, \
                mock.patch.object(runtime, "python_environment", return_value={"KEY": "value"}):
            runtime.test(argparse.Namespace(backend="reference-mlx"))
        self.assertEqual(run.call_args.args[0][0], "uv")
        with mock.patch.object(runtime, "paths", return_value=(Path("/source"), Path("/build"))), \
                mock.patch.object(runtime, "rust_environment", return_value={"KEY": "value"}), \
                mock.patch.object(runtime, "run") as run:
            runtime.test(argparse.Namespace(backend="reference-rust"))
        self.assertEqual(run.call_args.args[0][0], "cargo")

    def test_bench_bounds_fixture_prompt_and_recorded_invocation(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            model = root / "model"
            model.mkdir()
            fixture = root / "prompt.json"
            fixture.write_text(json.dumps({"prompt_tokens": [1, 2], "allowed_tools": ["command"]}))
            args = argparse.Namespace(
                model=model, artifact_identity="identity", prompt=3, messages=None, prompt_file=fixture,
                tokens=2, backend="reference-llama", reference_model_size="e4b", trials=1,
                ple="mapped", prefill_chunk=2, kv_bits=16, verify_last_logits=False,
                checkpoint_tokens=0, device_pci=None, reference_cpu="avx2",
            )
            build = root / "build"
            binary = build / "generation-reference"
            with mock.patch.object(runtime, "artifact_root", return_value=root / "artifacts"), \
                    mock.patch.object(runtime, "paths", return_value=(root / "source", build)), \
                    mock.patch.object(runtime, "verified_build", return_value={"binary_sha256": "abc"}), \
                    mock.patch.object(runtime, "file_digest", return_value="digest"), \
                    mock.patch.object(runtime.os, "getloadavg", return_value=(1, 2, 3)), \
                        mock.patch.object(runtime.platform, "platform", return_value="fixture-platform"), \
                        mock.patch.object(runtime.subprocess, "run", return_value=types.SimpleNamespace(returncode=0)) as run, \
                        contextlib.redirect_stdout(io.StringIO()) as output:
                    runtime.bench(args)
            self.assertIn(str(binary), str(run.call_args.args[0]))
            record = next((root / "artifacts/generation-bench").glob("*/run.json"))
            metadata = json.loads(record.read_text())
            self.assertEqual(metadata["artifact_identity"], "identity")
            self.assertEqual(json.loads(Path(metadata["input"]).read_text())["prompt_tokens"], [1, 2])

            args.device_pci = None
            args.backend = "native"
            with mock.patch.object(runtime.sys, "platform", "linux"), \
                    mock.patch.object(runtime, "artifact_root", return_value=root / "native-artifacts"), \
                    mock.patch.object(runtime.os, "getloadavg", return_value=(1, 2, 3)), \
                    mock.patch.object(runtime, "paths", return_value=(root / "source", build)), \
                    mock.patch.object(runtime, "verified_build", return_value={"binary_sha256": "abc"}):
                with self.assertRaisesRegex(ValueError, "requires --device-pci"):
                    runtime.bench(args)

    def test_cli_validates_selection_constraints_before_dispatch(self):
        with mock.patch.object(runtime.sys, "argv", ["runtime", "test", "--backend", "reference-llama"]), \
                mock.patch.object(runtime, "test") as test:
            runtime.main()
        test.assert_called_once()
        invalid = (
            ["build", "--reference-cpu", "portable"],
            ["bench", "--backend", "native", "--reference-model-size", "e2b"],
            ["bench", "--kv-bits", "8"],
            ["bench", "--checkpoint-tokens", "-1"],
            ["bench", "--backend", "native", "--ple", "resident"],
            ["bench", "--verify-last-logits"],
            ["bench", "--prompt", "65536"],
        )
        for arguments in invalid:
            with self.subTest(arguments=arguments), \
                    mock.patch.object(runtime.sys, "argv", ["runtime", *arguments]), \
                    mock.patch.object(runtime, "bench") as bench, \
                    contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit):
                    runtime.main()
                bench.assert_not_called()


if __name__ == "__main__":
    unittest.main()
