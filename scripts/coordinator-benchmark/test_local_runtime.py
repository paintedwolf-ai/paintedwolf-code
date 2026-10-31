import contextlib
import io
import json
import pathlib
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import Mock, call, patch
import yaml
import capture
import local_runtime
from local_runtime import live_members, signal_group, stop_process


class LocalRuntimeTests(unittest.TestCase):
    def response(self, value):
        return contextlib.closing(io.BytesIO(json.dumps(value).encode()))

    def test_model_identity_records_defaults_without_disclosing_private_text(self):
        tags = {'models': [{'name': 'qwen:latest', 'digest': 'a'*64}]}
        show = {'details': {'format': 'gguf', 'family': 'qwen35', 'parameter_size': '27B', 'quantization_level': 'Q4_K_M'},
                'parameters': 'temperature 1\ntop_k 20\nstop "private stop string"', 'system': 'private system', 'template': 'template'}
        with patch.object(local_runtime.urllib.request, 'urlopen', side_effect=[self.response(value) for value in
                          [tags, show, {'version': '0.33.3'}, tags]]) as request:
            runtime = local_runtime.ollama_model('http://private-server:11434/v1', 'qwen:latest')
        self.assertEqual(runtime['model_digest'], 'a'*64)
        self.assertEqual(runtime['sampler_defaults'], {'temperature': 1, 'top_k': 20})
        self.assertNotIn('private', json.dumps(runtime))
        self.assertEqual(request.call_args_list[1].args[0].full_url, 'http://private-server:11434/api/show')
        changed = {'models': [{'name': 'qwen:latest', 'digest': 'b'*64}]}
        with patch.object(local_runtime.urllib.request, 'urlopen', side_effect=[self.response(value) for value in
                          [tags, show, {'version': '0.33.3'}, changed]]):
            with self.assertRaisesRegex(ValueError, 'changed while capturing'):
                local_runtime.ollama_model('http://private-server:11434', 'qwen:latest')

    def test_capture_hides_endpoint_and_verification_rejects_model_changes(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            for name in ('pw-logs', 'pw-document-core', 'bialy'):
                (root / name).write_bytes(name.encode())
                (root / name).chmod(0o755)
            providers = {'providers': [{'id': 'local', 'kind': 'ollama', 'base_url': 'http://private-server:11434',
                                        'models': [{'id': 'qwen:latest'}]},
                                       {'id': 'cloud', 'kind': 'fixture', 'base_url': 'https://example.com/v1'}]}
            hosted = {'provider_id': 'cloud', 'model': 'worker'}
            policy = {'coordinator': {'provider_id': 'local', 'model': 'qwen:latest'}, 'lite': hosted,
                      'agent_pool': {'models': [hosted], 'selection': 'first'}}
            (root / 'providers.local.yaml').write_text(yaml.safe_dump(providers))
            (root / 'model-policy.yaml').write_text(yaml.safe_dump(policy))
            (root / 'engine').write_bytes(b'engine')
            (root / 'catalog').mkdir()
            runtime = {'model_digest': 'a'*64}
            with patch.object(capture, 'ollama_model', return_value=runtime):
                config = capture.describe(root, root / 'engine', root / 'catalog')
            self.assertIsNone(config['coordinator']['endpoint'])
            self.assertNotIn('private-server', json.dumps(config))
            with patch.object(local_runtime, 'ollama_model', return_value=runtime):
                self.assertEqual(local_runtime.verify_runtime(config, root), local_runtime.runtime_identity(config))
            with patch.object(local_runtime, 'ollama_model', return_value={'model_digest': 'b'*64}):
                with self.assertRaisesRegex(ValueError, 'differs from the frozen plan'):
                    local_runtime.verify_runtime(config, root)


class ProcessGroupTests(unittest.TestCase):
    def test_permission_denial_requires_observed_absence_of_live_members(self):
        for rows, live in [('41 Z\n42 S\n', False), ('42 S\n', False),
                           ('41 Z+\n41 S\n', True), ('41 T\n', True)]:
            with self.subTest(rows=rows), \
                 patch('local_runtime.os.killpg', side_effect=PermissionError(1, 'denied')), \
                 patch('local_runtime.subprocess.check_output', return_value=rows):
                if live:
                    with self.assertRaises(PermissionError):
                        signal_group(41, signal.SIGTERM)
                else:
                    self.assertFalse(signal_group(41, signal.SIGTERM))

    def test_failed_or_malformed_process_observation_is_not_clean_shutdown(self):
        failures = [subprocess.CalledProcessError(1, 'ps'), ValueError('invalid table')]
        for failure in failures:
            with self.subTest(failure=failure), \
                 patch('local_runtime.os.killpg', side_effect=PermissionError(1, 'denied')), \
                 patch('local_runtime.subprocess.check_output', side_effect=failure):
                with self.assertRaises(type(failure)):
                    signal_group(41, signal.SIGTERM)
        for rows in ['not-a-group Z\n', '41\n', '41 S unexpected\n']:
            with self.subTest(rows=rows), patch('local_runtime.subprocess.check_output', return_value=rows):
                with self.assertRaises(ValueError):
                    live_members(41)

    def test_refuses_broadcast_group_ids(self):
        for pgid in [-41, -1, 0, 1]:
            with self.subTest(pgid=pgid), patch('local_runtime.os.killpg') as send:
                with self.assertRaises(ValueError):
                    signal_group(pgid, signal.SIGKILL)
                send.assert_not_called()

    def test_missing_group_needs_no_process_table(self):
        with patch('local_runtime.os.killpg', side_effect=ProcessLookupError), \
             patch('local_runtime.live_members') as observe:
            self.assertFalse(signal_group(41, signal.SIGTERM))
            observe.assert_not_called()

    def test_forced_cleanup_waits_for_live_children_to_exit(self):
        process = Mock(pid=41)
        process.wait.return_value = 37
        with patch('local_runtime.signal_group', return_value=True) as send, \
             patch('local_runtime.live_members', side_effect=[True, True, False]) as observe, \
             patch('local_runtime.time.sleep'):
            self.assertEqual(stop_process(process, grace_seconds=0), 37)
        self.assertEqual(send.call_args_list, [call(41, signal.SIGTERM), call(41, signal.SIGKILL)])
        self.assertEqual(observe.call_count, 3)
        process.wait.assert_called_once()

    def test_surviving_children_after_kill_are_not_reported_as_cleaned(self):
        process = Mock(pid=41)
        with patch('local_runtime.signal_group', return_value=True), \
             patch('local_runtime.live_members', return_value=True):
            with self.assertRaises(TimeoutError):
                stop_process(process, grace_seconds=0, kill_wait_seconds=0)
        process.wait.assert_not_called()

    def test_unreaped_exited_launcher_keeps_its_actual_exit_status(self):
        process = subprocess.Popen([sys.executable, '-c', 'import os; os._exit(37)'],
                                   start_new_session=True)
        try:
            deadline = time.monotonic() + 15
            while True:
                rows = subprocess.check_output(['ps', '-axo', 'pid=,stat='], text=True)
                if any(int(pid) == process.pid and state[0] == 'Z'
                       for pid, state in (row.split() for row in rows.splitlines())):
                    break
                self.assertLess(time.monotonic(), deadline, 'child did not reach unreaped exit state')
                time.sleep(.02)
            self.assertFalse(live_members(process.pid))
            self.assertEqual(stop_process(process, grace_seconds=.1), 37)
            self.assertFalse(live_members(process.pid))
        finally:
            if process.poll() is None:
                process.kill()
            process.wait(timeout=5)


if __name__ == '__main__':
    unittest.main()
