import contextlib
import io
import json
import subprocess
import sys
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
                    return {"reused_prefix_tokens": 2}
                return {"progress": {"generated_tokens": 1, "token": None,
                                     "stop_reason": "eos"}}

        result = trial(Fixture(), {"prompt_tokens": [2, 3, 4], "generation_tokens": 10}, 1, 2)
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
