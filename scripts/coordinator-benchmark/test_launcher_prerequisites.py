"""Launcher capability checks do not enter a provider or start the application."""
import subprocess
import unittest
from unittest.mock import patch
import launcher_prerequisites


class LauncherPrerequisiteTests(unittest.TestCase):
    def test_missing_tools_fail_without_executing_node(self):
        for missing in ('jq', 'node'):
            with self.subTest(missing=missing), \
                    patch.object(launcher_prerequisites.shutil, 'which', side_effect=lambda name: None if name == missing else '/bin/' + name), \
                    patch.object(launcher_prerequisites.subprocess, 'run') as execute:
                with self.assertRaisesRegex(RuntimeError, 'Missing evaluation prerequisite: ' + missing):
                    launcher_prerequisites.require_launcher_runtime()
                execute.assert_not_called()

    def test_present_node_must_support_the_builtins_used_by_launch(self):
        for failure in (subprocess.CalledProcessError(1, 'node'), OSError('unusable executable')):
            with self.subTest(failure=type(failure).__name__), \
                    patch.object(launcher_prerequisites.shutil, 'which', return_value='/bin/tool'), \
                    patch.object(launcher_prerequisites.subprocess, 'run', side_effect=failure):
                with self.assertRaisesRegex(RuntimeError, 'node:net and node:fs'):
                    launcher_prerequisites.require_launcher_runtime()

    def test_supported_runtime_is_probed_without_binding_or_model_calls(self):
        with patch.object(launcher_prerequisites.shutil, 'which', return_value='/bin/tool'), \
                patch.object(launcher_prerequisites.subprocess, 'run') as execute:
            launcher_prerequisites.require_launcher_runtime()
        execute.assert_called_once_with(['node', '-e', "require('node:net'); require('node:fs');"],
                                        check=True, capture_output=True, text=True)
