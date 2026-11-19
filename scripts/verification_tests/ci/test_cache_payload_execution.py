"""Native cache hits skip warming while compiler payloads retain real boundaries."""
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from ci_policy import cache_payload


class CachePayloadTests(unittest.TestCase):
    def test_native_cache_inputs_cover_workspace_manifests_and_the_runner_os_release(self):
        import yaml
        root = Path(__file__).resolve().parents[3]
        for family, workspace in (("rust", "lycaon-den/src-tauri"),
                                  ("decide", "lycaon/internal/decide/native"),
                                  ("generation", "lycaon/internal/generation/native"),
                                  ("document-core", "lycaon/internal/documentcore/native")):
            action = yaml.safe_load((root / f".github/actions/cache-{family}/action.yml").read_text())
            steps = action["runs"]["steps"]
            restore = next(step for step in steps if step.get("id") == "restore")
            patterns = re.findall(r"'([^']+)'", restore["with"]["key"])
            matched = {path for pattern in patterns for path in root.glob(pattern) if path.is_file()}
            required = {path for path in (root / workspace).rglob("Cargo.toml")}
            required |= set((root / workspace).rglob("build.rs"))
            self.assertTrue(required)
            self.assertLessEqual(required, matched, family)
            paths = next(step for step in steps if step.get("id") == "paths")
            prefixes = {}
            with tempfile.TemporaryDirectory() as directory:
                # Hosted runners of one OS release serve several image builds at once, so a build shares the cache
                # of its release; another release starts its own.
                for release, image in (("ubuntu24", "20261101.1"), ("ubuntu24", "20261102.1"), ("ubuntu26", "20261101.1")):
                    output = Path(directory) / f"{release}-{image}"
                    result = subprocess.run(["bash", "-e", "-c", paths["run"]], cwd=root, text=True,
                                            capture_output=True, check=False,
                                            env={**os.environ, "RUNNER_OS": "Linux", "RUNNER_ARCH": "X64",
                                                 "ImageOS": release, "ImageVersion": image,
                                                 "GITHUB_WORKSPACE": str(root), "GITHUB_OUTPUT": str(output)})
                    self.assertEqual(result.returncode, 0, result.stderr)
                    prefixes[release, image] = next(line for line in output.read_text().splitlines()
                                                    if line.startswith("prefix="))
            self.assertEqual(prefixes["ubuntu24", "20261101.1"], prefixes["ubuntu24", "20261102.1"], family)
            self.assertNotEqual(prefixes["ubuntu24", "20261101.1"], prefixes["ubuntu26", "20261101.1"], family)

    def test_all_exact_hits_skip_preparation_on_push(self):
        records = {name: dict(active=True, hit="true") for name in ("go", "rust", "analysis")}
        records["release"] = dict(active=False, hit="")
        self.assertEqual(cache_payload.status(records, "push"),
                         dict(warm=False, maintenance=False, prepare=False))
        for hit in ("false", "", None):
            with self.subTest(hit=hit):
                changed = {**records, "analysis": dict(active=True, hit=hit)}
                self.assertTrue(cache_payload.status(changed, "push")["prepare"])

    def test_source_only_changes_keep_keys_and_tool_pin_changes_warm(self):
        import analysis_tools as tools
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "lycaon").mkdir()
            (root / "scripts").mkdir()
            (root / "lycaon/go.mod").write_text("module fixture\ngo 1.26.10\n")
            pin = root / "scripts/task.sh"
            pin.write_text('TASK_VERSION="v3.51.1"\n')
            def key():
                return tools.toolchain(root) + "-task-" + tools.pin("task", root)[2]
            stored = key()
            (root / "lycaon/source.go").write_text("package fixture\n")
            self.assertEqual(key(), stored)
            self.assertFalse(cache_payload.status({"base": dict(active=True, hit="true")}, "push")["warm"])
            pin.write_text('TASK_VERSION="v3.51.2"\n')
            self.assertNotEqual(key(), stored)
            self.assertTrue(cache_payload.status({"base": dict(active=True, hit=str(key() == stored).lower())}, "push")["warm"])

    def test_maintenance_and_summary_keep_misses_visible(self):
        records = {"go": dict(active=True, hit="true"), "rust": dict(active=True, hit="false"),
                   "native": dict(active=False, deferred=True)}
        for event in ("schedule", "workflow_dispatch"):
            state = cache_payload.status({"go": records["go"]}, event)
            self.assertEqual(state, dict(warm=False, maintenance=True, prepare=True))
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = dict(CACHE_HITS=json.dumps(records), GITHUB_EVENT_NAME="push",
                       GITHUB_OUTPUT=str(root / "output"), GITHUB_STEP_SUMMARY=str(root / "summary"))
            with patch.dict(os.environ, env):
                cache_payload.report_status()
            self.assertIn("prepare=true", (root / "output").read_text())
            self.assertIn("| go | exact hit |", (root / "summary").read_text())
            self.assertIn("| rust | miss |", (root / "summary").read_text())
            self.assertIn("deferred to preparation; not probed", (root / "summary").read_text())
            records["rust"]["hit"] = "true"
            (root / "summary").write_text("")
            with patch.dict(os.environ, {**env, "CACHE_HITS": json.dumps(records)}):
                cache_payload.report_status()
            self.assertIn("not needed; not probed", (root / "summary").read_text())

    def test_empty_lookup_results_and_targeted_workspace_cli(self):
        for records in ({}, [], {"go": dict(active=False)}):
            with self.assertRaises(ValueError):
                cache_payload.status(records, "push")
        for args, function, expected in [(["workspace", "manifest", "--target", "aarch64-apple-darwin"],
                                         "prune_workspace", (Path("manifest"), "aarch64-apple-darwin")),
                                        (["status"], "report_status", ())]:
            with patch('sys.argv', ["cache_payload", *args]), patch.object(cache_payload, function) as call:
                cache_payload.main()
                call.assert_called_once_with(*expected)

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
        with patch.object(cache_payload.subprocess, "run") as run:
            run.return_value.stdout = json.dumps(data)
            cache_payload.prune_workspace(Path("workspace/Cargo.toml"), "aarch64-apple-darwin")
            self.assertEqual(run.call_args.args[0][6:8], ["--target", "aarch64-apple-darwin"])
        for members in ([], ["missing"]):
            with patch.object(cache_payload.subprocess, "run") as run, self.assertRaises(ValueError):
                run.return_value.stdout = json.dumps({**data, "workspace_members": members})
                cache_payload.prune_workspace(Path("workspace/Cargo.toml"))
            self.assertEqual(run.call_count, 1)
