"""Exercise launcher isolation and prebuilt execution without any model calls."""
from concurrent.futures import ThreadPoolExecutor
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import unittest
from snapshot import ROOT
from application_environment import isolated_command


ENGINE = '''
import http.server, json, os, pathlib
config = pathlib.Path(os.environ['LYCAON_CONFIG_DIR'])
(config.parent / 'engine-rate-state.txt').write_text(os.environ.get('LYCAON_LLM_RATE_STATE_DIR', ''))
(config.parent / 'engine-controls.json').write_text(json.dumps({k:v for k,v in os.environ.items() if k.startswith('LYCAON_')}))
class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def do_GET(self):
        body = (config / 'model-policy.yaml').read_bytes() if self.path.startswith('/v1/') else b'{}'
        self.send_response(200); self.end_headers(); self.wfile.write(body)
    def do_PATCH(self):
        (config / 'model-policy.yaml').write_bytes(self.rfile.read(int(self.headers['Content-Length'])))
        self.send_response(200); self.end_headers(); self.wfile.write(b'{}')
host, port = os.environ['LYCAON_ADDR'].split(':')
http.server.HTTPServer((host, int(port)), Handler).serve_forever()
'''

DRIVER = '''
import json, os, pathlib, sys
args = sys.argv[1:]
if '--refresh' in args:
    capture = pathlib.Path(args[args.index('--refresh') + 1]).parent
else:
    capture = pathlib.Path(args[args.index('--capture') + 1])
    pathlib.Path(args[args.index('--out') + 1]).write_text(json.dumps({'cases': []}))
with (capture / 'driver-calls.jsonl').open('a') as stream:
    stream.write(json.dumps({'args': args, 'cwd': os.getcwd(), 'address': os.environ['LYCAON_ADDR']}) + '\\n')
'''


class LauncherTests(unittest.TestCase):
    def test_parallel_captures_use_prebuilt_driver_and_scrub_private_configuration(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            (root / 'Taskfile.yml').touch()
            (root / 'lycaon/config').mkdir(parents=True)
            (root / 'lycaon/go.mod').touch()
            (root / 'lycaon/config/catalog.yaml').write_text('fixture: true\n')
            (root / 'scripts').symlink_to(ROOT / 'scripts', target_is_directory=True)
            (root / 'source').mkdir()
            provider = {'id': 'fixture', 'kind': 'fixture', 'base_url': 'https://unused.invalid/v1'}
            ref = {'provider_id': 'fixture', 'model': 'fixture'}
            (root / 'source/providers.local.yaml').write_text(json.dumps({'providers': [provider]}))
            (root / 'source/model-policy.yaml').write_text(json.dumps({'coordinator': ref, 'lite': ref,
                'agent_pool': {'selection': 'first', 'models': [ref]}}))
            (root / 'source/credential-vault.age').write_text('fixture credential')
            (root / 'suite.json').write_text('{}')
            for name, body in [('engine', ENGINE), ('driver', DRIVER)]:
                (root / name).write_text('#!' + sys.executable + '\n' + body)
                (root / name).chmod(0o755)
            (root / 'task').write_text('#!/bin/sh\necho "unexpected build during episode" >&2\nexit 91\n')
            (root / 'task').chmod(0o755)

            def launch(index, expected_plan=None, refused=False):
                capture = root / ('capture-' + str(index))
                command = ['bash', str(ROOT / 'scripts/eval-agent-live.sh'), '--allow-live',
                           '--source-config', str(root / 'source'), '--provider', 'fixture', '--model', 'fixture',
                           '--label', 'fixture', '--engine', str(root / 'engine'), '--driver', str(root / 'driver'),
                           '--suite', str(root / 'suite.json'), '--out-dir', str(capture),
                           '--rate-state-dir', str(root / 'provider-rate-state')]
                if expected_plan is not None:
                    command.extend(['--expected-plan', str(expected_plan)])
                result = subprocess.run(isolated_command(command, ROOT), cwd=root, capture_output=True, text=True, timeout=30,
                                        env={**os.environ, 'LYCAON_LLM_MOCK': '1', 'LYCAON_LLM_MANUAL': '1',
                                             'LYCAON_OPENGREP_CANDIDATE': '/unsealed/scanner',
                                             'LYCAON_BYPASS_APPROVALS': '1', 'LYCAON_CONFIG_STAGED': '/foreign/config'})
                if refused:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertFalse((capture / 'driver-calls.jsonl').exists())
                    failure = json.loads((capture / 'setup-failure.json').read_text())
                    self.assertEqual(failure['failure'], {'kind': 'harness', 'code': 'configuration_mismatch', 'retryable': False})
                    self.assertIn('catalog_sha256', failure['diagnostic'])
                    self.assertFalse((capture / 'config/credential-vault.age').exists())
                    return
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertTrue((capture / 'driver-calls.jsonl').exists(), result.stdout + result.stderr)
                calls = [json.loads(line) for line in (capture / 'driver-calls.jsonl').read_text().splitlines()]
                self.assertEqual(len(calls), 2)
                self.assertEqual(calls[0]['args'][:2], ['eval', 'tool-usage'])
                self.assertEqual(pathlib.Path(calls[0]['cwd']).resolve(), (root / 'lycaon').resolve())
                self.assertIn('--refresh', calls[1]['args'])
                self.assertTrue((capture / 'configuration.json').exists())
                self.assertEqual((capture / 'engine-rate-state.txt').read_text(), str(root / 'provider-rate-state'))
                controls = json.loads((capture / 'engine-controls.json').read_text())
                self.assertEqual(controls['LYCAON_LLM_MOCK'], '0')
                self.assertEqual(controls['LYCAON_LLM_MANUAL'], '0')
                self.assertEqual(controls['LYCAON_COMMAND_PATH'], os.environ['PATH'])
                self.assertEqual(controls['LYCAON_ENGINE_ROOT'], str(root.resolve() / 'lycaon-den/src-tauri/engine-root'))
                for name in ('LYCAON_OPENGREP_CANDIDATE', 'LYCAON_BYPASS_APPROVALS', 'LYCAON_CONFIG_STAGED'):
                    self.assertNotIn(name, controls)
                self.assertFalse((capture / 'config/providers.local.yaml').exists())
                self.assertFalse((capture / 'config/credential-vault.age').exists())
                self.assertFalse((capture / 'config/api.token').exists())
                self.assertFalse((capture / 'engine').samefile(root / 'engine'))
                return calls[0]['address']

            with ThreadPoolExecutor(max_workers=2) as pool:
                addresses = list(pool.map(launch, range(2)))
            self.assertEqual(len(set(addresses)), 2)
            self.assertEqual((root / 'source/credential-vault.age').read_text(), 'fixture credential')
            expected_plan = root / 'plan.json'
            expected_plan.write_text(json.dumps({'models': [{'provider': 'fixture', 'model': 'fixture',
                'configuration': json.loads((root / 'capture-0/configuration.json').read_text())}]}))
            (root / 'lycaon/config/.DS_Store').write_bytes(b'Finder metadata')
            launch(2, expected_plan)
            (root / 'lycaon/config/catalog.yaml').write_text('fixture: changed\n')
            launch(3, expected_plan, refused=True)


if __name__ == '__main__':
    unittest.main()
