import json
import os
import pathlib
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import Mock, patch

import episode
import provider_preflight
import smoke
from application_environment import application_environment, isolated_command
from snapshot import ROOT


INHERITED = {'LYCAON_OPENGREP_CANDIDATE': '/unsealed/scanner', 'LYCAON_LLM_MANUAL': '1',
             'LYCAON_BYPASS_APPROVALS': '1', 'LYCAON_BYPASS_PERMISSIONS': '1',
             'LYCAON_CONFIG_ROOT': '/foreign/catalog', 'LYCAON_DB_FRESH': '1',
             'LYCAON_SANDBOX_WRITE_ROOTS': '/', 'LYCAON_FUTURE_OVERRIDE': 'unowned'}


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
