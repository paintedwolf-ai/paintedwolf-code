"""Compiler storage accounting uses compressed bytes and explicit workspace ownership."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from ci_policy import cache_budget, cache_payload


class CachePayloadTests(unittest.TestCase):
    def test_compression_measurement_counts_the_stream_without_retaining_an_archive(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            payload = root / "payload"
            payload.write_bytes(b"a" * 2**20)
            compressor = root / "zstd"
            compressor.write_text("#!/usr/bin/env python3\nimport sys\n"
                                  "data = sys.stdin.buffer.read()\nassert b'a' * 1024 in data\n"
                                  "assert '--long=30' in sys.argv\nsys.stdout.buffer.write(b'compressed')\n")
            compressor.chmod(0o755)
            with patch.dict(os.environ, PATH=str(root) + os.pathsep + os.environ["PATH"], RUNNER_TEMP=str(root)):
                measured = cache_payload.compressed_size({payload})
            self.assertEqual(measured, 10)
            self.assertTrue(cache_budget.plan([], "go-Linux-X64-new", measured, limit=2**20 + 100)["allowed"])
            self.assertEqual(sorted(path.name for path in root.iterdir()), ["payload", "zstd"])

    def test_measurement_failure_does_not_become_an_empty_payload(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            payload = root / "payload"
            payload.write_bytes(b"data")
            compressor = root / "zstd"
            compressor.write_text("#!/bin/sh\nexit 17\n")
            compressor.chmod(0o755)
            with patch.dict(os.environ, PATH=str(root) + os.pathsep + os.environ["PATH"]), \
                    self.assertRaises(subprocess.CalledProcessError):
                cache_payload.compressed_size({payload})
        self.assertEqual(cache_payload.compressed_size(set()), 0)

    def test_workspace_pruning_keeps_registry_dependencies(self):
        data = dict(workspace_members=["local-b", "local-a"], packages=[
            dict(id="registry-c", name="dependency"), dict(id="local-a", name="desktop"),
            dict(id="local-b", name="document-core")])
        with patch.object(cache_payload.subprocess, "run") as run:
            run.return_value.stdout = json.dumps(data)
            cache_payload.prune_workspace(Path("workspace/Cargo.toml"))
            metadata, clean = run.call_args_list
            self.assertIn("--offline", metadata.args[0])
            self.assertEqual(clean.args[0], ["cargo", "clean", "--manifest-path", "workspace/Cargo.toml", "--locked", "--offline",
                                            "--package", "desktop", "--package", "document-core"])
        for members in ([], ["missing"]):
            with patch.object(cache_payload.subprocess, "run") as run, self.assertRaises(ValueError):
                run.return_value.stdout = json.dumps({**data, "workspace_members": members})
                cache_payload.prune_workspace(Path("workspace/Cargo.toml"))
            self.assertEqual(run.call_count, 1)
