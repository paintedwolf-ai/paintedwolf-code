import argparse
import contextlib
import io
import json
import subprocess
import sys
from pathlib import Path
import unittest
from unittest import mock

import runtime

from native_bench import Client, trial


class NativeBenchmarkTests(unittest.TestCase):
    def exchange(self, response):
        program = """
import json, struct, sys
n, = struct.unpack('<I', sys.stdin.buffer.read(4))
request = json.loads(sys.stdin.buffer.read(n))
assert request['version'] == 2
response = json.loads(sys.argv[1])
if response == 'echo':
    response = {'id': request['id'], 'status': 'ok', 'result': request['command']}
payload = json.dumps(response).encode()
sys.stdout.buffer.write(struct.pack('<I', len(payload)) + payload)
sys.stdout.buffer.flush()
"""
        process = subprocess.Popen([sys.executable, "-c", program, json.dumps(response)],
                                   stdin=subprocess.PIPE, stdout=subprocess.PIPE, bufsize=0)
        try:
            return Client(process).call("hello")
        finally:
            process.stdin.close()
            process.stdout.close()
            process.wait(timeout=5)

    def test_e2b_is_limited_to_the_explicit_reference_backend(self):
        flags = ["runtime", "bench", "--reference-model-size", "e2b"]
        with mock.patch.object(sys, "argv", flags), mock.patch.object(runtime, "bench") as bench, contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit):
                runtime.main()
            bench.assert_not_called()
        with mock.patch.object(sys, "argv", flags + ["--backend", "reference-llama"]), mock.patch.object(runtime, "bench") as bench:
            runtime.main()
            self.assertEqual(bench.call_args.args[0].reference_model_size, "e2b")

    def test_portable_cpu_is_an_explicit_reference_build(self):
        for action, backend, accepted in (("build", "reference-llama", True),
                                          ("bench", "reference-llama", False),
                                          ("build", "native", False)):
            with mock.patch.object(sys, "argv", ["runtime", action, "--backend", backend, "--reference-cpu", "portable"]), mock.patch.object(runtime, "build") as build, contextlib.redirect_stderr(io.StringIO()):
                if accepted:
                    runtime.main()
                    self.assertEqual(build.call_args.args[0].reference_cpu, "portable")
                else:
                    with self.assertRaises(SystemExit):
                        runtime.main()
                    build.assert_not_called()

    def test_framed_exchange_and_rejected_responses(self):
        self.assertEqual(self.exchange("echo"), {"method": "hello"})
        for response in ({"id": 2, "status": "ok", "result": {}},
                         {"id": 1, "status": "error", "code": "device"}):
            with self.assertRaises(RuntimeError):
                self.exchange(response)

    def test_eos_counts_generated_token_without_inventing_decode_rate(self):
        class Fixture:
            def call(self, method, **fields):
                if method == "begin":
                    assert fields["request"]["output_contract"] == {"allowed_tools": ["command"]}
                    return {"reused_prefix_tokens": 2}
                return {"progress": {"generated_tokens": 1, "token": None,
                                     "stop_reason": "eos"}}

        result = trial(Fixture(), {"prompt_tokens": [2, 3, 4], "generation_tokens": 10, "allowed_tools": ["command"]}, 1, 2)
        self.assertEqual(result["generated_tokens"], 1)
        self.assertEqual(result["tokens"], [])
        self.assertEqual(result["reused_prefix_tokens"], 2)
        self.assertIsNone(result["decode_tokens_per_second"])
        self.assertEqual(result["stop_reason"], "eos")

    def test_decode_rate_excludes_first_sample(self):
        class Fixture:
            generated = 0

            def call(self, method, **fields):
                if method == "begin":
                    return {"reused_prefix_tokens": 0}
                self.generated += 1
                return {"progress": {"generated_tokens": self.generated, "token": 100,
                                     "stop_reason": "max_tokens" if self.generated == 3 else None}}

        result = trial(Fixture(), {"prompt_tokens": [2, 3], "generation_tokens": 3}, 1, 0)
        self.assertEqual(result["tokens"], [100, 100, 100])
        self.assertEqual(len(result["steps"]), 3)
        self.assertAlmostEqual(result["decode_tokens_per_second"] * result["decode_seconds"], 2)

    def test_cli_accepts_full_context_prefixes_and_rejects_invalid_bounds(self):
        for checkpoint in (0, 8193, 65535):
            with mock.patch.object(sys, "argv", ["runtime", "bench", "--checkpoint-tokens", str(checkpoint)]), mock.patch.object(runtime, "bench") as bench:
                runtime.main()
                self.assertEqual(bench.call_args.args[0].checkpoint_tokens, checkpoint)
        for flags in (["--checkpoint-tokens", "-1"],
                      ["--checkpoint-tokens", "65536"],
                      ["--checkpoint-tokens", "8193", "--backend", "reference-llama"]):
            with mock.patch.object(sys, "argv", ["runtime", "bench", *flags]), mock.patch.object(runtime, "bench") as bench, contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit) as error:
                    runtime.main()
                self.assertEqual(error.exception.code, 2)
                bench.assert_not_called()


class NativeBuildTests(unittest.TestCase):
    def test_cmake_cache_is_bound_to_its_configured_source_location(self):
        with mock.patch.object(runtime, "compiler_cache_dir", return_value=Path("/cache")), \
                mock.patch.object(runtime, "bin_dir", return_value=Path("/tools")):
            with mock.patch.object(runtime, "ROOT", Path("/checkout")):
                original = runtime.paths("reference-llama")
                self.assertEqual(original, runtime.paths("reference-llama"))
            with mock.patch.object(runtime, "ROOT", Path("/snapshot")):
                captured = runtime.paths("reference-llama")
            self.assertEqual(original[0], captured[0])
            self.assertNotEqual(original[1], captured[1])

    def test_linux_native_tests_build_the_matching_adapter_before_linking(self):
        calls = []
        with mock.patch.object(runtime.sys, "platform", "linux"), \
                mock.patch.object(runtime, "paths", return_value=(Path("/source"), Path("/output"))), \
                mock.patch.object(runtime, "native_environment", return_value=({}, "vulkan")), \
                mock.patch.object(runtime, "build", side_effect=lambda args: calls.append(args.backend)), \
                mock.patch.object(runtime, "run", side_effect=lambda command, **kwargs: calls.append(command[0])):
            runtime.test(argparse.Namespace(backend="native"))
        self.assertEqual(calls, ["reference-llama", sys.executable, "cargo"])
