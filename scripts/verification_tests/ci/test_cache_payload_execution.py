"""Native cache hits skip warming while compiler payloads retain real boundaries."""
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from ci_policy import cache_payload


class CachePayloadTests(unittest.TestCase):
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

    def test_all_heavy_workflow_steps_share_the_prepare_guard(self):
        import yaml
        root = Path(__file__).resolve().parents[3]
        workflow = yaml.safe_load((root / ".github/workflows/build-caches.yml").read_text())
        heavy = []
        for step in workflow["jobs"]["verification"]["steps"]:
            probe = step.get("with", {}).get("lookup-only") == "true"
            checkout = step.get("uses", "").startswith("actions/checkout@")
            if not probe and not checkout and step.get("id") != "state":
                heavy.append(step)
                self.assertIn("steps.state.outputs.prepare == 'true'", step.get("if", ""), step.get("name", step))
        self.assertGreater(len(heavy), 10)

    def test_lookup_mode_bypasses_provisioning_and_uses_real_tool_keys(self):
        import yaml
        root = Path(__file__).resolve().parents[3]
        setup = yaml.safe_load((root / ".github/actions/setup-verification/action.yml").read_text())
        metadata = {"paths", "go", "analysis", "document", "rust", "decide", "opengrep", "notices", "bun"}
        for step in setup["runs"]["steps"]:
            if step.get("id") not in metadata:
                self.assertIn("inputs.lookup-only != 'true'", step.get("if", ""), step)
            elif step.get("id") != "paths":
                self.assertEqual(step["with"]["lookup-only"], "${{ inputs.lookup-only }}")
        source = (root / ".github/actions/cache-analysis-tools/action.yml").read_text()
        self.assertIn('python3 scripts/analysis_tools.py key base', source)
        self.assertIn('key: analyzers-${{ runner.os }}-${{ runner.arch }}-${{ steps.paths.outputs.base }}', source)
        source = (root / ".github/actions/cache-go/action.yml").read_text()
        self.assertIn('from analysis_tools import toolchain; print(toolchain())', source)
        self.assertNotIn('github.sha', source)
        self.assertIn("inputs.setup == 'harness'", next(step for step in setup["runs"]["steps"]
                      if step.get("name") == "Provision native dependencies")["if"])

    def test_native_lookup_and_save_have_identical_payload_versions(self):
        import yaml
        root = Path(__file__).resolve().parents[3]
        for name in ("cache-go", "cache-rust", "cache-decide", "cache-document-core", "cache-release-build",
                     "cache-notices-tools", "cache-analysis-tools", "cache-lint-diagnostics", "setup-opengrep",
                     "setup-linux-native", "setup-webkit-atspi-images", "setup-verification"):
            action = yaml.safe_load((root / f".github/actions/{name}/action.yml").read_text())
            restores = {(step["with"]["key"], step["with"]["path"])
                        for step in action["runs"]["steps"]
                        if step.get("uses", "").startswith("actions/cache/restore@")}
            for step in action["runs"]["steps"]:
                if step.get("uses", "").startswith("actions/cache/save@"):
                    self.assertIn((step["with"]["key"], step["with"]["path"]), restores, name)
                    self.assertIn("cache-hit != 'true'", step["if"], name)
            if name == "cache-rust":
                self.assertNotIn("~/.cargo/registry", str(restores))
                self.assertNotIn("~/.cargo/git", str(restores))
            if name == "cache-decide":
                self.assertTrue(all("steps.paths.outputs.metal" in paths for _, paths in restores))

    def test_compiler_keys_include_their_payload_policy(self):
        root = Path(__file__).resolve().parents[3]
        for family, warm_script in (("go", None), ("decide", "warm-workspace-build.sh"),
                                    ("rust", "warm-workspace-build.sh"),
                                    ("release-build", "warm-release-build.sh")):
            source = (root / f".github/actions/cache-{family}/action.yml").read_text()
            keys = [line for line in source.splitlines() if line.strip().startswith("key:")]
            self.assertEqual(len(keys), 2)
            for key in keys:
                self.assertIn("'scripts/ci_policy/cache_payload.py'", key)
                if warm_script:
                    self.assertIn(f"'scripts/{warm_script}'", key)
                self.assertNotIn("github.sha", key)

    def test_empty_lookup_results_and_targeted_workspace_cli(self):
        for records in ({}, [], {"go": dict(active=False)}):
            with self.assertRaises(ValueError):
                cache_payload.status(records, "push")
        for args, function, expected in [(["workspace", "manifest", "--target", "aarch64-apple-darwin"],
                                         "prune_workspace", (Path("manifest"), "aarch64-apple-darwin")),
                                        (["go", "compiler-cache"], "trim_go", (Path("compiler-cache"),)),
                                        (["status"], "report_status", ())]:
            with patch('sys.argv', ["cache_payload", *args]), patch.object(cache_payload, function) as call:
                cache_payload.main()
                call.assert_called_once_with(*expected)

    def test_go_trimming_keeps_non_cache_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / ("a" * 64 + "-d")).write_bytes(b"12345")
            (root / "README").write_text("keep")
            (root / "link").symlink_to(root / ("a" * 64 + "-d"))
            self.assertEqual(cache_payload.trim_go(root, 4), 4)
            self.assertEqual(sorted(p.name for p in root.iterdir()), ["README", "link"])
            self.assertEqual(cache_payload.trim_go(root, 0), 4)

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
        root = Path(__file__).resolve().parents[3]
        workspace = (root / "scripts/warm-workspace-build.sh").read_text()
        self.assertIn('cache_payload workspace "${ROOT}/lycaon/internal/decide/native/Cargo.toml"', workspace)
        self.assertNotIn("rm -rf", workspace)
        for members in ([], ["missing"]):
            with patch.object(cache_payload.subprocess, "run") as run, self.assertRaises(ValueError):
                run.return_value.stdout = json.dumps({**data, "workspace_members": members})
                cache_payload.prune_workspace(Path("workspace/Cargo.toml"))
            self.assertEqual(run.call_count, 1)
