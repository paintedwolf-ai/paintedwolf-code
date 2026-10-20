"""Latency observations must include actual engine requests, never replay-cache hits."""
from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from scripts.bialy import bench_latency
from linux_release.installed_cases import bialy_data, bialy_performance
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
