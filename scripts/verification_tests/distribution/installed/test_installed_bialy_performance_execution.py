"""Latency observations must include actual engine requests, never replay-cache hits."""
from contextlib import contextmanager, redirect_stdout
import io
import json
from pathlib import Path
import tempfile
import subprocess
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from scripts.bialy import bench_latency
from linux_release.installed_cases import bialy_data, bialy_performance, bialy_stdio, process
from linux_release.installed_cases.records import CaseUnavailable


class BialyLatencyTests(unittest.TestCase):
    def test_repeated_states_always_invoke_uncached_engine(self):
        clock = [0.0]
        engines = []
        class Engine:
            def __init__(self, path):
                self.hello = {'engine': 'fixture'}
                self.requests = []
                self.closed = False
                engines.append(self)
            def call(self, request):
                raise AssertionError('latency cannot use the replay answer cache')
            def ask(self, request):
                self.requests.append(request)
                clock[0] += 0.025
                return {'answers': {}}
            def close(self):
                self.closed = True
        corpus = SimpleNamespace(surface_questions=lambda _: ['kind', 'candidate'],
                                 spec={'deadline_ms': 10000})
        output = io.StringIO()
        with patch.object(bench_latency, 'Engine', Engine), \
                patch.object(bench_latency.Corpus, 'load', return_value=corpus), \
                patch.object(bench_latency.time, 'perf_counter', side_effect=lambda: clock[0]), \
                patch('sys.argv', ['bench', '--corpus', 'fixture', '--engine', 'fixture', '--rounds', '20']), \
                redirect_stdout(output):
            bench_latency.main()
        self.assertEqual(len(engines[0].requests), 21)
        self.assertEqual(engines[0].requests[1], engines[0].requests[7])
        self.assertTrue(engines[0].closed)
        report = json.loads(output.getvalue())
        self.assertEqual((report['p50_ms'], report['p95_ms']), (25, 25))
        self.assertEqual(report['deadline_ms'], 10000)

    def test_verified_data_receipt_refuses_mutation_missing_or_incomplete_checkpoint(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            self.assertIsNone(bialy_data.capture(root))
            model = root / 'engine-root/decide/models/fixed-revision'
            for name in ['rl_agent_config.json', 'model.safetensors', 'encoder/config.json',
                         'tokenizer/tokenizer.json', 'tokenizer/tokenizer_config.json']:
                path = model / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(b'fixed data')
                path.chmod(0o644)
            with patch.object(bialy_data, 'physical'):
                receipt = bialy_data.capture(root)
                binding = {'install_root': str(root), 'bialy_data': receipt}
                self.assertEqual(bialy_data.unchanged(binding), receipt)
                (model / 'model.safetensors').write_bytes(b'changed')
                with self.assertRaisesRegex(ValueError, 'changed'):
                    bialy_data.unchanged(binding)
                (model / 'model.safetensors').unlink()
                with self.assertRaisesRegex(ValueError, 'incomplete'):
                    bialy_data.capture(root)
            with self.assertRaises(CaseUnavailable):
                bialy_data.unchanged({'install_root': str(root)})

    def test_data_receipt_refuses_symlinks_privileged_or_oversized_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            model = root / 'engine-root/decide/models/model'
            model.mkdir(parents=True)
            file = model / 'data'
            file.write_bytes(b'data')
            file.chmod(0o755)
            with patch.object(bialy_data, 'physical'):
                with self.assertRaisesRegex(ValueError, 'bounds'):
                    bialy_data.capture(root)
                file.chmod(0o644)
                with patch.object(bialy_data, 'MAX_BYTES', 1):
                    with self.assertRaisesRegex(ValueError, 'bounds'):
                        bialy_data.capture(root)
            with self.assertRaises(ValueError):
                bialy_data.physical(file, directory=False)
            file.unlink()
            file.symlink_to('/etc/passwd')
            with self.assertRaises(ValueError):
                bialy_data.physical(file, directory=False)

    def test_reference_uses_fixed_shipped_probe_and_never_qualifies_quality_or_selected_device(self):
        data = {'model': '/sealed/model', 'files': {}}
        result = {'stdout': b'{"probe_ms": 1200}', 'stderr': b'', 'launch_identity': {'pid': 42},
                  'timed_out': False, 'output_limit': False, 'exit_code': 0}
        with patch.object(bialy_performance, 'unchanged', return_value=data) as identity, \
                patch.object(bialy_performance, 'limits', return_value={'reference_ms': 1000, 'turn_ms': 10000}), \
                patch.object(bialy_performance, 'run', return_value=result) as run:
            report, streams = bialy_performance.reference({})
            self.assertFalse(report['reference_within_existing_budget'])
            self.assertFalse(report['quality_baseline_qualified'])
            self.assertFalse(report['selected_application_device_qualified'])
            self.assertEqual(identity.call_count, 2)
            run.assert_called_once_with({}, 'bialy', ['probe', '--model', '/sealed/model', '--device', 'cpu'], timeout=120)
            self.assertEqual(streams['bialy-stdout'], result['stdout'])
            for raw in [b'{"probe_ms": true}', b'{"probe_ms": -1}', b'{"probe_ms": NaN}',
                        b'{"probe_ms": 1, "unexpected": 2}', b'[]']:
                result['stdout'] = raw
                with self.subTest(raw=raw), self.assertRaises(ValueError):
                    bialy_performance.reference({})
            result['stdout'] = b'{"probe_ms": 1}'
            result['timed_out'] = True
            with self.assertRaisesRegex(ValueError, 'complete'):
                bialy_performance.reference({})

    def test_turn_latency_excludes_hello_and_warmup_without_a_quality_claim(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'heads').mkdir()
            (root / 'heads/release.json').write_text(json.dumps({'preload': {'encoding': 'independent', 'options': {str(i): str(i) for i in range(16)}}}))
            data = {'root': str(root), 'model': '/sealed/model',
                    'files': {'heads/release.json': {}, 'heads/turn-load.safetensors': {}}}
            result = {'responses': [{'engine': {'device': 'cpu'}}] + [{'answers': {'tools': {}}}] * 21,
                      'roundtrip_ms': [500, 2000] + [25] * 20, 'stdout': b'', 'stderr': b''}
            policy = {'engine': {'reference_ms': 1000}, 'turn': {'deadline_ms': 10000, 'tools': {'question': 'fixed'}}}
            with patch.object(bialy_performance, 'unchanged', return_value=data), \
                    patch.object(bialy_performance, 'policy', return_value=policy), \
                    patch.object(bialy_performance, 'exchange', return_value=result) as calls:
                report, _ = bialy_performance.turn_latency({})
                self.assertEqual(report['measurements_ms']['p95'], 25)
                self.assertEqual(report['measurements_ms']['samples'], 20)
                self.assertFalse(report['quality_baseline_qualified'])
                self.assertFalse(report['selected_application_device_qualified'])
                requests = calls.call_args.args[2]
                self.assertEqual(len(requests), 22)
                self.assertTrue(requests[1]['questions']['tools']['independent'])
                self.assertEqual(requests[1], requests[7])
                result['responses'][-1] = {'answers': {}}
                with self.assertRaisesRegex(ValueError, 'fixed reference'):
                    bialy_performance.turn_latency({})

    def test_interactive_input_cannot_expand_to_other_native_roles(self):
        with self.assertRaisesRegex(ValueError, 'restricted'):
            with process.launch({}, 'pw', [], bialy_input=True):
                self.fail('foreign role cannot launch')

    @unittest.skipUnless(sys.platform == 'linux', 'actual bounded native pipe fixture requires Linux')
    def test_stdio_has_one_inflight_request_and_rejects_protocol_mismatch(self):
        processes = []
        @contextmanager
        def fixture_launch(binding, role, arguments, *, bialy_input):
            self.assertEqual(role, 'bialy')
            self.assertTrue(bialy_input)
            child = subprocess.Popen([sys.executable, '-u', '-c', arguments[0]],
                stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
            processes.append(child)
            try:
                yield child, {'pid': child.pid}
            finally:
                process.stop_stack(child)
        echo = "import sys,json\nfor n,line in enumerate(sys.stdin,1):\n r=json.loads(line);print(json.dumps({'id':r['id'],'count':n}),flush=True)"
        with patch.object(bialy_stdio, 'launch', fixture_launch):
            result = bialy_stdio.exchange({}, [echo], [{'method': 'hello'}] + [{'method': 'decide'}] * 21)
            self.assertEqual([r['count'] for r in result['responses']], list(range(1, 23)))
            self.assertEqual(len(result['roundtrip_ms']), 22)
            self.assertTrue(all(value >= 0 for value in result['roundtrip_ms']))
            for invalid in ["print('{\"id\":2}',flush=True)",
                            "print('{\"id\":1}\\n{\"id\":2}',flush=True)",
                            "print('{\"id\":true}',flush=True)"]:
                with self.subTest(program=invalid), self.assertRaises(ValueError):
                    bialy_stdio.exchange({}, [invalid], [{'method': 'hello'}])
            with patch.object(bialy_stdio, 'LIMIT', 16):
                with self.assertRaisesRegex(ValueError, 'bound'):
                    bialy_stdio.exchange({}, ["import sys;sys.stderr.write('x'*100);sys.stderr.flush()"], [{'method': 'hello'}])
        self.assertTrue(all(child.poll() is not None for child in processes))
