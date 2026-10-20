"""Latency observations must include actual engine requests, never replay-cache hits."""
from contextlib import redirect_stdout
import io
import json
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from scripts.bialy import bench_latency


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
