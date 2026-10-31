import json
import os
import pathlib
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import Mock, patch

import application_environment as env_module
import episode
import preparation
import provider_preflight
import run
import smoke
from application_environment import application_environment, isolated_command, release_external_resource
from snapshot import ROOT


INHERITED = {'LYCAON_OPENGREP_CANDIDATE': '/unsealed/scanner', 'LYCAON_LLM_MANUAL': '1',
             'LYCAON_BYPASS_APPROVALS': '1', 'LYCAON_BYPASS_PERMISSIONS': '1',
             'LYCAON_CONFIG_ROOT': '/foreign/catalog', 'LYCAON_DB_FRESH': '1',
             'LYCAON_SANDBOX_WRITE_ROOTS': '/', 'LYCAON_FUTURE_OVERRIDE': 'unowned'}


class ResourceReleaseTests(unittest.TestCase):
    def test_release_uses_frozen_owner_and_propagates_failure(self):
        capture = pathlib.Path('/capture')
        source = pathlib.Path('/source')
        with patch('application_environment.subprocess.run') as execute:
            release_external_resource(capture, source)
            self.assertEqual(execute.call_args.args[0], ['/source/.bin/lycaon-debug', 'eval', 'tool-usage', '--release-resources', '/capture'])
            self.assertTrue(execute.call_args.kwargs['check'])
            execute.side_effect = subprocess.CalledProcessError(1, 'release')
            with self.assertRaises(subprocess.CalledProcessError):
                release_external_resource(capture, source)


class LauncherPrerequisiteTests(unittest.TestCase):
    def test_missing_tools_fail_without_executing_node(self):
        for missing in ('jq', 'node'):
            with self.subTest(missing=missing), \
                    patch.object(env_module.shutil, 'which', side_effect=lambda name: None if name == missing else '/bin/' + name), \
                    patch.object(env_module.subprocess, 'run') as execute:
                with self.assertRaisesRegex(RuntimeError, 'Missing evaluation prerequisite: ' + missing):
                    env_module.require_launcher_runtime()
                execute.assert_not_called()

    def test_present_node_must_support_the_builtins_used_by_launch(self):
        for failure in (subprocess.CalledProcessError(1, 'node'), OSError('unusable executable')):
            with self.subTest(failure=type(failure).__name__), \
                    patch.object(env_module.shutil, 'which', return_value='/bin/tool'), \
                    patch.object(env_module.subprocess, 'run', side_effect=failure):
                with self.assertRaisesRegex(RuntimeError, 'node:net and node:fs'):
                    env_module.require_launcher_runtime()

    def test_supported_runtime_is_probed_without_binding_or_model_calls(self):
        with patch.object(env_module.shutil, 'which', return_value='/bin/tool'), \
                patch.object(env_module.subprocess, 'run') as execute:
            env_module.require_launcher_runtime()
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


class ApplicationEnvironmentTests(unittest.TestCase):
    def test_only_explicit_application_controls_survive(self):
        with patch.dict(os.environ, {**INHERITED, 'PATH': '/host/tools', 'PROVIDER_API_KEY': 'fixture'}, clear=True):
            actual = application_environment({'LYCAON_CONFIG_DIR': '/owned/config'})
            self.assertEqual(actual, {'PATH': '/host/tools', 'PROVIDER_API_KEY': 'fixture',
                'LYCAON_DEV': '1', 'LYCAON_HARNESS': '1', 'LYCAON_LLM_MOCK': '0',
                'LYCAON_LLM_MANUAL': '0', 'LYCAON_COMMAND_PATH': '/host/tools', 'LYCAON_CONFIG_DIR': '/owned/config'})
            self.assertEqual(os.environ['LYCAON_OPENGREP_CANDIDATE'], '/unsealed/scanner')

    def test_exec_boundary_normalizes_the_environment_at_launch(self):
        command = isolated_command([sys.executable, '-c',
            'import json,os;print(json.dumps({k:v for k,v in os.environ.items() if k.startswith("LYCAON_")}))'], ROOT)
        output = subprocess.check_output(command, env={**os.environ, **INHERITED}, text=True)
        self.assertEqual(json.loads(output), {'LYCAON_DEV': '1', 'LYCAON_HARNESS': '1',
                                              'LYCAON_LLM_MOCK': '0', 'LYCAON_LLM_MANUAL': '0',
                                              'LYCAON_COMMAND_PATH': os.environ.get('PATH', '')})

    def test_smoke_and_paid_probe_own_their_modes_and_roots(self):
        for manual in (True, False):
            with self.subTest(manual=manual), tempfile.TemporaryDirectory() as temporary:
                root = pathlib.Path(temporary)
                config = root / 'source'
                config.mkdir()
                process = Mock(pid=123, poll=Mock(return_value=0))
                with patch.dict(os.environ, INHERITED), patch.object(smoke.subprocess, 'Popen', return_value=process) as spawn:
                    app = smoke.Application(root / 'owned', None if manual else config, root / 'rates')
                    app.close()
                actual = spawn.call_args.kwargs['env']
                self.assertEqual(actual['LYCAON_LLM_MANUAL'], '1' if manual else '0')
                self.assertEqual(actual['LYCAON_LLM_MOCK'], '1' if manual else '0')
                self.assertEqual(actual['LYCAON_CONFIG_ROOT'], str(ROOT / 'lycaon'))
                self.assertEqual(actual['LYCAON_ENGINE_ROOT'], str(ROOT / 'lycaon-den/src-tauri/engine-root'))
                self.assertEqual(actual['LYCAON_CONFIG_DIR'], str(app.config))
                self.assertEqual(actual['LYCAON_COMMAND_PATH'], actual['PATH'])
                self.assertEqual(actual['LYCAON_LLM_RATE_STATE_DIR'], str(root / 'rates'))
                for name in INHERITED.keys() - {'LYCAON_CONFIG_ROOT', 'LYCAON_LLM_MANUAL'}:
                    self.assertNotIn(name, actual)

    def test_candidate_and_retained_commands_use_the_frozen_boundary(self):
        root = pathlib.Path('/retained/run')
        model = {'id': 'candidate', 'provider': 'cloud', 'model': 'model'}
        plan = {'models': [model], 'roster': {'worker': model, 'episode_timeout': '0s'}}
        item = {'model': 'candidate', 'case': 'operation', 'repeat': 0}
        prefix = isolated_command([], root / 'source')
        command = episode.command_for(root, plan, root / 'credentials', item, root / 'capture')
        self.assertEqual(command[:len(prefix)], prefix)
        with patch.object(episode, 'run_retained_step') as launch:
            episode.finalize(root, root / 'capture', model, root / 'credentials', threading.Event())
        self.assertEqual(launch.call_args.args[1][:len(prefix)], prefix)

    def test_provider_preflight_uses_the_frozen_boundary(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            model = {'id': 'candidate', 'provider': 'cloud', 'model': 'model', 'configuration': {}}
            with patch.object(provider_preflight, 'execute', return_value={'kind': 'exited', 'returncode': 1}) as launch:
                provider_preflight.probe(root, root / 'credentials', model, threading.Event())
            prefix = isolated_command([], root / 'source')
            self.assertEqual(launch.call_args.args[1][:len(prefix)], prefix)


if __name__ == '__main__':
    unittest.main()
