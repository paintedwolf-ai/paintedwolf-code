import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import ci_verification as ci
from ci_policy import impact, evidence, resources, recovery


class AdmissionTests(unittest.TestCase):
    def test_shared_unknown_deleted_inputs_expand_not_skip(self):
        for paths in [['docs/openapi/api.yaml'], ['lycaon/go.mod'], ['scripts/x.py'], ['new-area/data.bin'],
                      ['lycaon/internal/a/testdata/fixture.json'], ['lycaon/config/prompt.md']]:
            full, reasons = impact.broad_scope(paths)
            self.assertTrue(full)
            self.assertEqual(reasons, paths)
        self.assertFalse(impact.broad_scope(['lycaon/internal/a/a.go'])[0])

    def test_frontend_change_preserves_contracts_and_build(self):
        scope = dict(full=False, paths=['lycaon-den/src/a.ts'])
        lanes = {row['lane'] for row in ci.matrix('integration', scope=scope)['include']}
        self.assertEqual(lanes, {'limits', 'contracts', 'integration-build', 'frontend', 'behavior'})
        full = {row['lane'] for row in ci.matrix('integration', scope=dict(full=True))['include']}
        self.assertIn('behavior', full)
        self.assertIn('runner', full)
        self.assertIn('native', full)

    def test_reverse_graph_reaches_test_importers_and_cycles(self):
        graph = {'a': [], 'b': ['a', 'c'], 'c': ['b'], 'test': ['c'], 'unrelated': []}
        self.assertEqual(impact.reverse_closure(graph, {'a'}), {'a', 'b', 'c', 'test'})

    def test_no_graph_evidence_runs_full_recipe(self):
        with patch.object(impact.subprocess, 'run') as run:
            run.return_value.returncode = 1
            self.assertIsNone(impact.go_scope({'full': False, 'paths': ['lycaon/internal/a/a.go']}))

    def test_only_proven_infrastructure_failure_retries_once(self):
        for classification in ['test_failure', 'unknown', 'check_failure', 'passed']:
            self.assertFalse(evidence.retryable([{'classification': classification}], 1))
        self.assertTrue(evidence.retryable([{'classification': 'runner_oom'}], 1))
        self.assertFalse(evidence.retryable([{'classification': 'runner_oom'}], 2))
        self.assertFalse(evidence.retryable([{'classification': 'runner_oom'}, {'classification': 'test_failure'}], 1))
        record = {'oom_before': 0, 'oom_after': 1}
        self.assertEqual(evidence.classify(record, []), 'runner_oom')
        self.assertEqual(evidence.classify(record, [{'tests': ['TestA']}]), 'test_failure')
        self.assertEqual(evidence.classify(record, [{'resource_limit': {'kind': 'package_rss'}}]), 'test_failure')
        self.assertEqual(evidence.classify(record, [{'exit_code': 1}]), 'test_failure')
        self.assertEqual(evidence.classify(record, [{'exit_code': -9}]), 'runner_oom')
        self.assertEqual(evidence.classify({'exit_code': 137}, []), 'unknown')
        self.assertEqual(evidence.classify({**record, 'status': 'passed'}, []), 'passed')

    def test_signature_ignores_output_and_test_order(self):
        first = {'stage': 'go', 'subject': 'pkg', 'tests': ['TestB', 'TestA'], 'log': '/one'}
        second = {**first, 'tests': ['TestA', 'TestB'], 'log': '/two'}
        self.assertEqual(evidence.failure_signature(first), evidence.failure_signature(second))
        self.assertNotEqual(evidence.failure_signature(first), evidence.failure_signature({**first, 'subject': 'other'}))

    def test_revert_requires_exact_unique_introduction_and_qualified_parent(self):
        run = dict(path='.github/workflows/qualification.yml', event='push', head_branch='main', head_sha='a', conclusion='failure')
        commit = {'parents': [{'sha': 'b'}]}
        pull = {'merged_at': 'date', 'base': {'ref': 'main'}, 'merge_commit_sha': 'a', 'number': 1}
        self.assertEqual(recovery.revert_candidate(run, commit, [pull], True, 'a'), pull)
        for args in [(run, commit, [pull], False, 'a'), (run, commit, [pull], True, 'new'),
                     (run, commit, [pull, pull], True, 'a'), (run, {'parents': []}, [pull], True, 'a'),
                     ({**run, 'event': 'pull_request'}, commit, [pull], True, 'a')]:
            self.assertIsNone(recovery.revert_candidate(*args))

    def test_package_memory_guard_attributes_own_process(self):
        monitor = resources.Monitor(123, 100)
        with patch.object(resources, 'resident_bytes', return_value=101):
            self.assertEqual(monitor.sample(), {'kind': 'package_rss', 'measured_bytes': 101, 'limit_bytes': 100})
        self.assertEqual(monitor.peak, 101)

    def test_package_memory_guard_sets_a_soft_runtime_limit_below_the_ceiling(self):
        self.assertEqual(resources.runtime_environment({'A': '1'}, 1000), {'A': '1', 'GOMEMLIMIT': '800'})
        self.assertEqual(resources.runtime_environment({'GOMEMLIMIT': '5MiB'}, 1000), {'GOMEMLIMIT': '5MiB'})

    def test_offline_namespace_drops_root_and_preserves_argument_boundaries(self):
        from ci_policy.offline import command
        argv = command(['python3', 'runner.py', 'lane;false'], 1001, 1001)
        self.assertEqual(argv[-3:], ['python3', 'runner.py', 'lane;false'])
        shell = argv[argv.index('-c') + 1]
        self.assertIn('set -e; ip link set lo up', shell)
        self.assertIn('setpriv --reuid="$uid" --regid="$gid"', shell)
        self.assertNotIn('lane;false', shell)

    def test_quarantine_expires_and_only_skips_exact_package_test(self):
        from ci_policy import quarantine
        from datetime import date
        row = {'package': 'github.com/lycaon/lycaon/internal/a', 'test': 'TestFlake',
               'issue': 'https://github.com/paintedwolf-ai/paintedwolf-code/issues/1',
               'owner': 'maintainer', 'expires': '2099-01-01'}
        with tempfile.TemporaryDirectory() as directory:
            policy = Path(directory) / 'quarantine.json'
            policy.write_text(json.dumps([row]))
            with patch.object(quarantine, 'POLICY', policy):
                self.assertEqual(quarantine.arguments(row['package'], []), ['-test.skip=^(TestFlake)$'])
                self.assertEqual(quarantine.arguments('unrelated', []), [])
                self.assertEqual(quarantine.arguments(row['package'], [], observe=True), [])
                with self.assertRaises(ValueError):
                    quarantine.entries(date(2099, 1, 2))
                policy.write_text(json.dumps([{**row, 'test': 'Test.*'}]))
                with self.assertRaises(ValueError):
                    quarantine.entries()

    def test_health_counts_distinct_runs_not_repeated_artifacts(self):
        from ci_policy.health import summarize, queue_times
        runs = [{'id': n, 'html_url': str(n)} for n in range(3)]
        def get(_run):
            return ([{'lane': 'behavior', 'started_at': 0, 'finished_at': 10}],
                    [{'signature': 'a' * 20}, {'signature': 'a' * 20}], [])
        report = summarize(runs, get)
        self.assertEqual(report['recurring'], {'a' * 20: ['0', '1', '2']})
        self.assertEqual(report['lanes']['behavior']['p95_seconds'], 10)
        self.assertEqual(report['runs_without_receipts'], [])
        self.assertEqual(summarize(runs, lambda _: ([], [], []))['runs_without_receipts'], [0, 1, 2])
        self.assertEqual(summarize(runs[:2], get)['recurring'], {})
        times = queue_times([{'number': 1, 'mergedAt': '2026-10-08T12:20:00Z', 'timelineItems': {'nodes': [
            {'__typename': 'AddedToMergeQueueEvent', 'createdAt': '2026-10-08T10:00:00Z'},
            {'__typename': 'AddedToMergeQueueEvent', 'createdAt': '2026-10-08T12:00:00Z'}]}}])
        self.assertEqual(times, [{'pr': 1, 'seconds': 1200}])

    def test_queue_settings_preserve_every_unrelated_protection(self):
        from ci_policy.queue_settings import proposed
        current = {'name': 'main', 'target': 'branch', 'enforcement': 'active', 'conditions': {'ref_name': {}},
                   'bypass_actors': [], 'rules': [{'type': 'required_status_checks', 'parameters': {'required': ['check']}},
                                                {'type': 'merge_queue', 'parameters': {'max_entries_to_build': 3}}]}
        result = proposed(current)
        self.assertEqual(result['rules'][0], current['rules'][0])
        self.assertEqual(result['rules'][1]['parameters']['max_entries_to_build'], 1)
        self.assertEqual(current['rules'][1]['parameters']['max_entries_to_build'], 3)

    def test_dependency_snapshot_does_not_depend_on_the_current_module_graph(self):
        import importlib.util
        spec = importlib.util.spec_from_file_location('vulndb', Path(__file__).resolve().parents[1] / 'vendor-vulndb.py')
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            db = root / 'db'
            (db / 'index').mkdir(parents=True)
            (db / 'index/modules.json').write_text(json.dumps([{'path': 'stdlib'}, {'path': 'toolchain'}]))
            pin = root / 'pin.json'
            pin.write_text(json.dumps({'coverage': 'complete', 'tree_sha256': module.tree_digest(db)}))
            with patch.object(module, 'VENDOR', db), patch.object(module, 'PROVENANCE', pin):
                self.assertEqual(module.check(), 0)
                (db / 'index/modules.json').write_text('[]')
                self.assertEqual(module.check(), 1)

    def test_pr_recovery_never_downloads_untrusted_artifacts(self):
        run = {'path': '.github/workflows/ci.yml', 'event': 'pull_request'}
        with patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), patch.object(recovery, 'evidence') as read:
            recovery.recover(run)
        read.assert_not_called()
