"""Launcher capability checks do not enter a provider or start the application."""
import os
import pathlib
import tempfile
import subprocess
import unittest
from unittest.mock import patch
import application_environment
import preparation
import run
from snapshot import ROOT


class LauncherPrerequisiteTests(unittest.TestCase):
    def test_missing_tools_fail_without_executing_node(self):
        for missing in ('jq', 'node'):
            with self.subTest(missing=missing), \
                    patch.object(application_environment.shutil, 'which', side_effect=lambda name: None if name == missing else '/bin/' + name), \
                    patch.object(application_environment.subprocess, 'run') as execute:
                with self.assertRaisesRegex(RuntimeError, 'Missing evaluation prerequisite: ' + missing):
                    application_environment.require_launcher_runtime()
                execute.assert_not_called()

    def test_present_node_must_support_the_builtins_used_by_launch(self):
        for failure in (subprocess.CalledProcessError(1, 'node'), OSError('unusable executable')):
            with self.subTest(failure=type(failure).__name__), \
                    patch.object(application_environment.shutil, 'which', return_value='/bin/tool'), \
                    patch.object(application_environment.subprocess, 'run', side_effect=failure):
                with self.assertRaisesRegex(RuntimeError, 'node:net and node:fs'):
                    application_environment.require_launcher_runtime()

    def test_supported_runtime_is_probed_without_binding_or_model_calls(self):
        with patch.object(application_environment.shutil, 'which', return_value='/bin/tool'), \
                patch.object(application_environment.subprocess, 'run') as execute:
            application_environment.require_launcher_runtime()
        execute.assert_called_once_with(['node', '-e', "require('node:net'); require('node:fs');"],
                                        check=True, capture_output=True, text=True)


class LauncherPrerequisiteBoundaryTests(unittest.TestCase):
    def test_preparation_refuses_before_application_smoke(self):
        with patch.object(preparation, 'require_launcher_runtime', side_effect=RuntimeError('unsupported runtime')), \
                patch.object(preparation.subprocess, 'run') as execute:
            with self.assertRaisesRegex(RuntimeError, 'unsupported runtime'):
                preparation.validate_runtime(pathlib.Path('unused'), pathlib.Path('unused'))
            execute.assert_not_called()

    def test_model_configuration_refuses_before_provider_queries(self):
        with patch.object(run, 'require_launcher_runtime', side_effect=RuntimeError('unsupported runtime')), \
                patch.object(run, 'configure') as configure, patch.object(run, 'describe') as describe:
            with self.assertRaisesRegex(RuntimeError, 'unsupported runtime'):
                run.model_configurations(pathlib.Path('unused'), pathlib.Path('unused'), {}, {})
            configure.assert_not_called()
            describe.assert_not_called()

    def test_direct_launch_refuses_present_but_incompatible_node_before_capture(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            (root / 'Taskfile.yml').touch()
            (root / 'lycaon').mkdir()
            (root / 'lycaon/go.mod').touch()
            (root / 'scripts').symlink_to(ROOT / 'scripts', target_is_directory=True)
            binary = root / 'bin'
            binary.mkdir()
            (binary / 'node').write_text('#!/bin/sh\nexit 42\n')
            (binary / 'node').chmod(0o755)
            capture = root / 'capture'
            command = ['bash', str(ROOT / 'scripts/eval-agent-live.sh'), '--allow-live',
                       '--source-config', str(root / 'missing-config'), '--provider', 'fixture',
                       '--model', 'fixture', '--label', 'fixture', '--out-dir', str(capture)]
            result = subprocess.run(command, cwd=root, capture_output=True, text=True,
                                    env={**os.environ, 'PATH': str(binary) + os.pathsep + os.environ['PATH']})
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('cannot load node:net and node:fs', result.stderr)
            self.assertFalse(capture.exists())
