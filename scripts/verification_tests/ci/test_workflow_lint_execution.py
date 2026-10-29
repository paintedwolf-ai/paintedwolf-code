"""Workflow validation includes dispatch-only files and enforces context scope."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import workflow_lint
from analysis_tools import ensure


class WorkflowLintTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        workflow_lint.prepare()
        cls.binary = ensure("actionlint")

    def test_every_repository_workflow_is_valid(self):
        with patch.object(workflow_lint, "ensure", return_value=self.binary) as ensure_tool:
            self.assertEqual(workflow_lint.validate(), 0)
        ensure_tool.assert_called_once_with("actionlint")

    def test_dispatch_context_scope_for_both_workflow_extensions(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            workflows = root / ".github/workflows"
            workflows.mkdir(parents=True)
            for suffix in (".yml", ".yaml"):
                with self.subTest(suffix=suffix):
                    workflow = workflows / ("dispatch" + suffix)
                    prefix = "on: workflow_dispatch\njobs:\n  observe:\n    runs-on: ubuntu-latest\n"
                    workflow.write_text(prefix + "    env:\n      SELECTION: ${{ runner.temp }}/selection.json\n"
                                        "    steps:\n      - run: echo ok\n")
                    with patch.object(workflow_lint.subprocess, "run", wraps=subprocess.run) as run:
                        self.assertNotEqual(workflow_lint.validate(root, self.binary), 0)
                    self.assertIn(str(workflow), run.call_args.args[0])
                    workflow.write_text(prefix + "    steps:\n      - run: echo ok\n"
                                        "        env:\n          SELECTION: ${{ runner.temp }}/selection.json\n")
                    self.assertEqual(workflow_lint.validate(root, self.binary), 0)
                    workflow.unlink()

    def test_missing_workflows_refuse_validation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / ".github/workflows").mkdir(parents=True)
            with self.assertRaisesRegex(ValueError, "no GitHub Actions workflows"):
                workflow_lint.validate(root, self.binary)

    def test_queue_schema_preserves_workflow_and_job_contexts(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            workflows = root / ".github/workflows"
            workflows.mkdir(parents=True)
            for suffix in (".yml", ".yaml"):
                for scope in ("workflow", "job"):
                    for queue, cancel, expected in [("max", "false", 0), ("single", "true", 0),
                                                     ("max", "${{ false }}", 0), ("max", None, 0),
                                                     ("bad", "false", 1), ("true", "false", 1),
                                                     ("max", "true", 1)]:
                        with self.subTest(suffix=suffix, scope=scope, queue=queue, cancel=cancel):
                            concurrency = "concurrency:\n  group: fixture\n  queue: " + queue + "\n"
                            if cancel is not None:
                                concurrency += "  cancel-in-progress: " + cancel + "\n"
                            source = "on: workflow_dispatch\n"
                            if scope == "workflow":
                                source += concurrency
                            source += "jobs:\n  observe:\n    runs-on: ubuntu-latest\n"
                            if scope == "job":
                                source += "".join("    " + line + "\n" for line in concurrency.splitlines())
                            source += "    steps:\n      - run: echo ok\n"
                            workflow = workflows / ("queue" + suffix)
                            workflow.write_text(source)
                            self.assertEqual(workflow_lint.validate(root, self.binary), expected)
                            workflow.unlink()

    def test_queue_filter_preserves_other_analyzer_findings(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            workflows = root / ".github/workflows"
            workflows.mkdir(parents=True)
            for addition in ("  unknown: max\n", ""):
                workflow = workflows / "queue.yml"
                source = ("on: workflow_dispatch\nconcurrency:\n  group: fixture\n  queue: max\n" + addition +
                          "jobs:\n  observe:\n    runs-on: ubuntu-latest\n")
                if not addition:
                    source += "    env:\n      SELECTION: ${{ runner.temp }}/selection.json\n"
                workflow.write_text(source + "    steps:\n      - run: echo ok\n")
                self.assertNotEqual(workflow_lint.validate(root, self.binary), 0)

    def test_preparation_prepares_both_pinned_tools(self):
        with patch.object(workflow_lint, "ensure") as analyzer, \
                patch.object(workflow_lint.python_tools, "provision") as python:
            workflow_lint.prepare()
        analyzer.assert_called_once_with("actionlint")
        python.assert_called_once_with(workflow_lint.REQUIREMENTS)

    def test_module_entry_points_prepare_and_validate_workflows(self):
        environment = {**os.environ, "PYTHONPATH": str(workflow_lint.ROOT / "scripts") +
                       os.pathsep + os.environ.get("PYTHONPATH", "")}
        for arguments in (["--provision"], []):
            with self.subTest(arguments=arguments):
                result = subprocess.run([sys.executable, "-m", "workflow_lint", *arguments],
                                        cwd=workflow_lint.ROOT, env=environment,
                                        capture_output=True, text=True, timeout=120)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_malformed_workflow_documents_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            workflows = root / ".github/workflows"
            workflows.mkdir(parents=True)
            for source in ("[]", "on: workflow_dispatch\njobs: []\n", "jobs: ["):
                with self.subTest(source=source):
                    (workflows / "invalid.yml").write_text(source)
                    self.assertNotEqual(workflow_lint.validate(root, self.binary), 0)
