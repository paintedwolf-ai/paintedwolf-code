import copy
import importlib.util
import json
import os
from pathlib import Path
import signal
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

from ci_policy import hosted_plan as ci
from ci_policy import impact, evidence, resources


class AdmissionTests(unittest.TestCase):
    def test_a_pull_request_merge_is_measured_from_the_branch_it_merges_into(self):
        event = {"pull_request": {"head": {"sha": "head"}, "base": {"sha": "stale"}}}
        answers = {("rev-list", "--parents", "-n", "1", "HEAD"): "merge main head",
                   ("merge-base", "HEAD", "stale"): "old"}
        with patch.object(impact, "git", side_effect=lambda *args: answers[args]):
            self.assertEqual(impact.event_base(event, "pull_request"), "main")
        answers[("rev-list", "--parents", "-n", "1", "HEAD")] = "head parent"
        with patch.object(impact, "git", side_effect=lambda *args: answers[args]):
            self.assertEqual(impact.event_base(event, "pull_request"), "old")

    def test_shared_unknown_deleted_inputs_expand_not_skip(self):
        for paths in [['docs/openapi/api.yaml'], ['lycaon/go.mod'], ['scripts/x.py'], ['new-area/data.bin'],
                      ['lycaon/config/gitengine/pin.yaml']]:
            full, reasons = impact.broad_scope(paths)
            self.assertTrue(full)
            self.assertEqual(reasons, paths)
        self.assertFalse(impact.broad_scope(['lycaon/internal/a/a.go'])[0])

    def test_frontend_change_preserves_contracts_and_build(self):
        self.assertEqual(impact.change_areas(dict(full=False, paths=['lycaon-den/src/a.ts'])), {'always', 'den'})
        full = {row['lane'] for row in ci.matrix('integration', scope=dict(full=True))['include']}
        self.assertEqual(full, {name for name, lane in ci.lanes().items() if 'integration' in lane['profiles']})

    def test_broad_changes_run_every_queue_lane_their_paths_reach(self):
        queue = {name for name, lane in ci.lanes().items() if 'integration' in lane['profiles']}
        full = {row['lane'] for row in ci.matrix('integration', scope=dict(full=True))['include']}
        self.assertEqual(full, queue)
        # Lanes for the scripts, the dependencies and changed Go code wait for paths that reach them.
        broad = dict(full=True, paths=['new-area/data.bin'])
        gated = ci.lanes_of(ci.lanes(), {'scripts', 'dependencies', 'go-code'}) - ci.lanes_of(ci.lanes(), ci.AREAS - {'scripts', 'dependencies', 'go-code'})
        self.assertTrue(gated)
        self.assertEqual({row['lane'] for row in ci.matrix('integration', scope=broad)['include']}, queue - gated)
        dependencies = dict(full=True, paths=['lycaon/go.sum'])
        self.assertIn('dependencies', impact.change_areas(dependencies))
        # The check itself, its probe, and the harness fixtures widen the scope.
        for path in ['scripts/den-webkit-atspi.sh', 'scripts/harness/webkit_atspi_text_attributes.py',
                     'scripts/harness/webkit-atspi/2.54.Containerfile', 'lycaon/test/fixtures/e2e/minimal-go-project/go.mod',
                     'lycaon/test/fixtures/e2e/minimal-go-project/internal/app/app.go',
                     'lycaon-den/src/assets/icon.svg']:
            self.assertTrue(impact.broad_scope([path])[0], path)
        self.assertEqual(impact.change_areas(dict(full=False, paths=['lycaon/internal/a/a.go'])), {'always', 'go'})

    def test_command_language_changes_run_the_live_differential(self):
        for path in ['lycaon/config/packs/painted-wolf/platform/tools/command-language-v1/sed.json',
                     'lycaon/internal/commandsurface/program_parse.go', 'lycaon/internal/commandprogram/evaluate_simple.go',
                     'lycaon/internal/tools/native/search/grep.go', 'lycaon/test/commandconformance/corpus_live.go',
                     'lycaon/test/wiring/conformance/differential_test.go']:
            with self.subTest(path=path):
                self.assertIn('command-language', impact.change_areas(dict(full=False, paths=[path])))
        for path in ['lycaon/internal/commandsurfaces/a.go', 'lycaon/internal/tools/web/a.go', 'lycaon/internal/commandline/a.go']:
            with self.subTest(path=path):
                self.assertNotIn('command-language', impact.change_areas(dict(full=False, paths=[path])))

    def test_prose_runs_only_the_scope_every_change_runs(self):
        paths = ['docs/test-strategy.md', 'docs/operations/runbook.md', 'AGENTS.md', 'lycaon/AGENTS.md',
                 'lycaon-den/AGENTS.md', 'README.md']
        self.assertEqual(impact.broad_scope(paths), (False, []))
        scope = dict(full=False, paths=paths)
        self.assertEqual(impact.change_areas(scope), {'always'})
        with patch.object(impact.subprocess, 'run') as run:
            run.return_value = impact.subprocess.CompletedProcess([], 0, stdout=json.dumps({
                'ImportPath': 'github.com/lycaon/lycaon/test/smoke', 'Dir': str(impact.ROOT / 'lycaon/test/smoke')}))
            self.assertEqual(impact.go_scope(scope), ['./test/smoke'])
        # Prose beside code adds nothing to the code's own scope.
        self.assertEqual(impact.change_areas(dict(full=False, paths=['docs/git.md', 'lycaon/internal/a/a.go'])), {'always', 'go'})
        # Documentation that a build or contract parses still widens the gate.
        for path in ['CHANGELOG.md', 'docs/openapi/vocab/README.md', 'docs/openapi.yaml']:
            self.assertTrue(impact.broad_scope([path])[0], path)

    def test_confinement_changes_admit_the_other_architecture(self):
        for path in ['lycaon/internal/confine/boundary_linux.go', 'lycaon/internal/confine/linuxsandbox/seccomp_linux_arm64.go',
                     'lycaon/internal/confine/helper_main.go', 'lycaon/internal/exec/supervisor_tree_linux.go',
                     'lycaon/internal/exec/nice_linux.go', 'lycaon/internal/exec/supervisor_other.go',
                     'lycaon/internal/sandboxmark/mark_darwin.go']:
            self.assertIn('confinement', impact.change_areas(dict(full=False, paths=[path])), path)
        for path in ['lycaon/internal/exec/runner.go', 'lycaon/internal/exec/sub/supervisor.go',
                     'lycaon/internal/execution/supervisor_linux.go', 'lycaon-den/src/a.ts']:
            self.assertNotIn('confinement', impact.change_areas(dict(full=False, paths=[path])), path)
        fixture = dict(full=False, paths=['lycaon/internal/confine/testdata/fixture.json'])
        self.assertIn('confinement', impact.change_areas(fixture))
        # The lane and its target run every platform lane.
        for path in ['scripts/test-confine-linux.sh', 'scripts/verification-plan.json']:
            self.assertTrue(impact.broad_scope([path])[0], path)

    def test_reverse_graph_reaches_test_importers_and_cycles(self):
        graph = {'a': [], 'b': ['a', 'c'], 'c': ['b'], 'test': ['c'], 'unrelated': []}
        self.assertEqual(impact.reverse_closure(graph, {'a'}), {'a', 'b', 'c', 'test'})

    def test_machinery_tests_run_for_what_they_import_or_read_not_for_every_change(self):
        module = 'github.com/lycaon/lycaon/'
        records = [('internal/db', {}), ('internal/other', {}), ('test/contract/authority', {}),
                   ('test/contract/release', {'TestImports': ['internal/db']}),
                   ('test/contract/runner', {}), ('test/contract/runners', {}), ('test/smoke', {})]
        output = '\n'.join(json.dumps({
            'ImportPath': module + directory, 'Dir': str(impact.ROOT / 'lycaon' / directory),
            **{kind: [module + dependency for dependency in dependencies] for kind, dependencies in edges.items()},
        }) for directory, edges in records)
        result = impact.subprocess.CompletedProcess([], 0, stdout=output, stderr='')
        unconditional = ['./test/contract/authority', './test/contract/runners', './test/smoke']
        for paths, expected in (
                (['lycaon/internal/other/a.go'], ['./internal/other', *unconditional]),
                (['lycaon/internal/db/a.go'], ['./internal/db', './test/contract/release', *unconditional]),
                (['.github/workflows/release.yml'],
                 sorted([*unconditional, './test/contract/release', './test/contract/runner']))):
            with self.subTest(paths=paths), patch.object(impact.subprocess, 'run', return_value=result):
                self.assertEqual(impact.go_scope({'full': False, 'paths': paths}), sorted(expected))

    def test_owner_change_selects_production_and_relocated_test_callers(self):
        module = 'github.com/lycaon/lycaon/'
        owner = 'internal/tools/toolcontext'
        records = [
            ('internal/api/contractfixture', {'Imports': ['internal/api/sessionfixture']}),
            ('internal/api/sessionfixture', {'XTestImports': ['internal/app/sessionfixture']}),
            ('internal/app/sessionfixture', {'TestImports': ['internal/app/sessions']}),
            ('internal/app/sessions', {'Imports': [owner]}),
            (owner, {}),
            ('internal/unrelated', {}),
            ('test/contract/authority', {}),
            ('test/smoke', {}),
        ]
        output = '\n'.join(json.dumps({
            'ImportPath': module + directory,
            'Dir': str(impact.ROOT / 'lycaon' / directory),
            **{kind: [module + dependency for dependency in dependencies]
               for kind, dependencies in edges.items()},
        }) for directory, edges in records)
        result = impact.subprocess.CompletedProcess([], 0, stdout=output, stderr='')
        with patch.object(impact.subprocess, 'run', return_value=result):
            packages = impact.go_scope({
                'full': False, 'paths': [f'lycaon/{owner}/context.go'],
            })
        self.assertEqual(packages, sorted([
            './internal/api/contractfixture', './internal/api/sessionfixture',
            './internal/app/sessionfixture', './internal/app/sessions',
            './' + owner, './test/contract/authority', './test/smoke',
        ]))

    def test_no_graph_evidence_runs_full_recipe(self):
        with patch.object(impact.subprocess, 'run') as run:
            run.return_value.returncode = 1
            self.assertIsNone(impact.go_scope({'full': False, 'paths': ['lycaon/internal/a/a.go']}))

    def test_runner_oom_needs_a_measured_increment_and_no_source_failure(self):
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

    def test_package_memory_guard_attributes_own_process(self):
        monitor = resources.Monitor(123, 100)
        with patch.object(resources, 'proc_status_memory', return_value=(101, 105)):
            self.assertEqual(monitor.sample(), {'kind': 'package_rss', 'measured_bytes': 101, 'limit_bytes': 100})
        self.assertEqual(monitor.peak, 101)
        self.assertEqual(monitor.hwm, 105)
        self.assertEqual(monitor.finalize(), {
            'peak_bytes': 105, 'limit_bytes': 100, 'ratio': 1.05, 'source': 'proc_vmhwm', 'lower_bound': True,
        })
        rss_only = resources.Monitor(123, 200)
        with patch.object(resources, 'proc_status_memory', return_value=(80, None)):
            self.assertIsNone(rss_only.sample())
        self.assertEqual(rss_only.finalize(), {
            'peak_bytes': 80, 'limit_bytes': 200, 'ratio': 0.4, 'source': 'proc_vmrss', 'lower_bound': True,
        })
        missing = resources.Monitor(123, 200)
        with patch.object(resources, 'proc_status_memory', return_value=(None, None)):
            self.assertIsNone(missing.sample())
        self.assertEqual(missing.finalize(), {
            'peak_bytes': None, 'limit_bytes': 200, 'ratio': None, 'source': 'unavailable', 'lower_bound': False,
        })

    def test_proc_status_memory_reads_rss_and_hwm_without_substituting_zero(self):
        with tempfile.TemporaryDirectory() as directory:
            status = Path(directory) / 'status'
            status.write_text('Name:\tpkg.test\nVmHWM:\t   2048 kB\nVmRSS:\t   1024 kB\n')
            with patch.object(resources, 'Path', return_value=status):
                self.assertEqual(resources.proc_status_memory(123), (1024 * 1024, 2048 * 1024))
            status.write_text('Name:\tpkg.test\nState:\tZ (zombie)\n')
            with patch.object(resources, 'Path', return_value=status):
                self.assertEqual(resources.proc_status_memory(123), (None, None))
            status.unlink()
            with patch.object(resources, 'Path', return_value=status):
                self.assertEqual(resources.proc_status_memory(123), (None, None))

    @unittest.skipIf(os.name == 'nt', 'go-test-exec requires POSIX fork and waitpid')
    def test_go_test_exec_records_peak_rss_for_passing_breaching_and_reaped_fixtures(self):
        wrapper_path = Path(__file__).resolve().parents[2] / 'go-test-exec.py'
        spec = importlib.util.spec_from_file_location('go_test_exec_mu01', wrapper_path)
        wrapper = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(wrapper)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            passing = root / 'pass.sh'
            passing.write_text('#!/bin/sh\necho ok\n')
            passing.chmod(0o755)
            breaching = root / 'breach.sh'
            breaching.write_text('#!/bin/sh\nsleep 30\n')
            breaching.chmod(0o755)
            env = {'PATH': os.environ['PATH']}
            limit = 64 * 1024 * 1024
            with patch.object(wrapper.Path, 'is_dir', return_value=True):
                with tempfile.NamedTemporaryFile(dir=root) as owner, \
                        patch.object(resources, 'proc_status_memory', return_value=(12 * 1024 * 1024, 16 * 1024 * 1024)):
                    status, _, _, violation, leaked, memory = wrapper.execute(
                        str(passing), [], env, owner, False, lambda: {}, limit)
                self.assertEqual(os.waitstatus_to_exitcode(status), 0)
                self.assertIsNone(violation)
                self.assertEqual(leaked, {})
                self.assertEqual(memory, {
                    'peak_bytes': 16 * 1024 * 1024, 'limit_bytes': limit, 'ratio': 0.25,
                    'source': 'proc_vmhwm', 'lower_bound': True,
                })

                with tempfile.NamedTemporaryFile(dir=root) as owner, \
                        patch.object(resources, 'proc_status_memory', return_value=(limit + 4096, limit + 4096)):
                    status, _, _, violation, leaked, memory = wrapper.execute(
                        str(breaching), [], env, owner, False, lambda: {}, limit)
                self.assertEqual(os.waitstatus_to_exitcode(status), -signal.SIGKILL)
                self.assertEqual(violation, {'kind': 'package_rss', 'measured_bytes': limit + 4096, 'limit_bytes': limit})
                self.assertEqual(leaked, {})
                self.assertEqual(memory, {
                    'peak_bytes': limit + 4096, 'limit_bytes': limit, 'ratio': round((limit + 4096) / limit, 4),
                    'source': 'proc_vmhwm', 'lower_bound': True,
                })

                with tempfile.NamedTemporaryFile(dir=root) as owner, \
                        patch.object(resources, 'proc_status_memory', return_value=(None, None)):
                    status, _, _, violation, leaked, memory = wrapper.execute(
                        str(passing), [], env, owner, False, lambda: {}, limit)
                self.assertEqual(os.waitstatus_to_exitcode(status), 0)
                self.assertIsNone(violation)
                self.assertEqual(memory, {
                    'peak_bytes': None, 'limit_bytes': limit, 'ratio': None,
                    'source': 'unavailable', 'lower_bound': False,
                })

    @unittest.skipUnless(sys.platform == 'linux', 'per-PID /proc/<pid>/status scope requires Linux')
    def test_go_test_exec_excludes_reaped_descendant_rss_from_package_peak(self):
        wrapper_path = Path(__file__).resolve().parents[2] / 'go-test-exec.py'
        spec = importlib.util.spec_from_file_location('go_test_exec_descendant', wrapper_path)
        wrapper = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(wrapper)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fixture = root / 'with_helper.py'
            fixture.write_text(
                f'#!{sys.executable}\n'
                'import os, sys, time\n'
                'pid = os.fork()\n'
                'if pid == 0:\n'
                '    buf = bytearray(48 * 1024 * 1024)\n'
                '    for i in range(0, len(buf), 4096):\n'
                '        buf[i] = 1\n'
                '    os._exit(0)\n'
                'os.waitpid(pid, 0)\n'
                'print("ready", flush=True)\n'
                'time.sleep(0.1)\n'
            )
            fixture.chmod(0o755)
            limit = 256 * 1024 * 1024
            with tempfile.NamedTemporaryFile(dir=root) as owner:
                status, _, _, violation, leaked, memory = wrapper.execute(
                    str(fixture), [], {'PATH': os.environ['PATH']}, owner, False, lambda: {}, limit)
            self.assertEqual(os.waitstatus_to_exitcode(status), 0)
            self.assertIsNone(violation)
            self.assertEqual(leaked, {})
            self.assertEqual(memory['source'], 'proc_vmhwm')
            self.assertTrue(memory['lower_bound'])
            self.assertIsInstance(memory['peak_bytes'], int)
            self.assertGreater(memory['peak_bytes'], 0)
            # The reaped 48 MiB helper must not inflate the test process's own per-PID peak.
            self.assertLess(memory['peak_bytes'], 32 * 1024 * 1024)

    def test_stage_publish_carries_package_memory_for_passing_and_failing_packages(self):
        from contextlib import nullcontext
        import verification_execute as execution
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            events_path = root / 'stage-001-events.jsonl'
            raw_path = root / 'stage-001-go.json'
            result_path = root / 'stage-001.json'
            log = root / 'stage-001.log'
            log.write_text('')
            pass_mem = {'peak_bytes': 1024, 'limit_bytes': 4096, 'ratio': 0.25,
                        'source': 'proc_vmhwm', 'lower_bound': True}
            fail_mem = {'peak_bytes': 4097, 'limit_bytes': 4096, 'ratio': 1.0002,
                        'source': 'proc_vmhwm', 'lower_bound': True}
            events_path.write_text(
                json.dumps({'package': 'example/pass', 'exit_code': 0, 'memory': pass_mem}) + '\n'
                + json.dumps({'package': 'example/fail', 'exit_code': 1, 'memory': fail_mem,
                              'resource_limit': {'kind': 'package_rss', 'measured_bytes': 4097, 'limit_bytes': 4096}}) + '\n'
            )
            result_path.write_text(json.dumps({
                'completed': True, 'process_exit_code': 1, 'digest_exit_code': 1,
                'packages': {'example/pass': 'pass', 'example/fail': 'fail'},
            }))
            queue = types.SimpleNamespace(root=root, history=root, locked=nullcontext)
            executor = types.SimpleNamespace(queue=queue, directory=root, running={'req': {0}},
                                             cache={}, finished=set(), stages=[])
            job = {
                'first': {'kind': 'go', 'name': 'test:short', 'options': [], 'flags': [], 'exclude': ''},
                'key': 'go:test:short',
                'packages': ['example/pass', 'example/fail'],
                'result': {'stage_id': 'stage-001', 'names': ['test:short'], 'started_at': 100,
                           'consumers': ['req'], 'log': str(log)},
                'result_path': result_path,
                'events_path': events_path,
                'events': execution.StageEvents(events_path, raw_path),
                'failures': {},
                'future': types.SimpleNamespace(result=lambda: 1),
                'reservation': types.SimpleNamespace(entry={'started_at': 100}),
                'needs': [({'ticket': 'req'}, 0, {}, ['example/pass', 'example/fail'])],
            }
            execution.Executor.publish_stage(executor, job)
            self.assertEqual(executor.cache['go:test:short']['example/pass']['memory'], pass_mem)
            self.assertEqual(executor.cache['go:test:short']['example/fail']['memory'], fail_mem)

    def test_package_memory_ceiling_scales_for_race_builds_only(self):
        with tempfile.TemporaryDirectory() as directory:
            policy = Path(directory) / 'resources.json'
            policy.write_text(json.dumps({'default': {'rss_mib': 1000}, 'race_scale': 1.5,
                                          'packages': {'example.com/m/big': {'rss_mib': 2000}}}))
            with patch.object(resources, 'POLICY', policy):
                self.assertEqual(resources.budget('example.com/m/a'), 1000 * 1024 * 1024)
                self.assertEqual(resources.budget('example.com/m/a', race=True), 1500 * 1024 * 1024)
                self.assertEqual(resources.budget('example.com/m/big', race=True), 3000 * 1024 * 1024)
            policy.write_text(json.dumps({'default': {'rss_mib': 1000}, 'race_scale': 8, 'packages': {}}))
            with patch.object(resources, 'POLICY', policy):
                self.assertEqual(resources.budget('example.com/m/a'), 1000 * 1024 * 1024)
                with self.assertRaises(ValueError):
                    resources.budget('example.com/m/a', race=True)

    def test_package_memory_guard_sets_a_soft_runtime_limit_below_the_ceiling(self):
        self.assertEqual(resources.runtime_environment({'A': '1'}, 1000), {'A': '1', 'GOMEMLIMIT': '800'})
        self.assertEqual(resources.runtime_environment({'GOMEMLIMIT': '5MiB'}, 1000), {'GOMEMLIMIT': '5MiB'})

    def test_offline_namespace_drops_root_and_preserves_argument_boundaries(self):
        from ci_policy.offline import command
        argv = command(['python3', 'runner.py', 'lane;false'], 1001, 1001, 'linux')
        self.assertEqual(argv[-3:], ['python3', 'runner.py', 'lane;false'])
        shell = argv[argv.index('-c') + 1]
        self.assertIn('set -e; ip link set lo up', shell)
        self.assertIn('setpriv --reuid="$uid" --regid="$gid"', shell)
        self.assertNotIn('lane;false', shell)

    def test_offline_macos_lane_denies_only_network_beyond_the_machine(self):
        from ci_policy.offline import command
        argv = command(['python3', 'runner.py', 'lane;false'], 501, 20, 'darwin')
        self.assertEqual(argv[:2], ['sandbox-exec', '-p'])
        self.assertEqual(argv[3:], ['python3', 'runner.py', 'lane;false'])
        self.assertIn('(deny network-outbound)', argv[2])
        self.assertIn('(allow network-outbound (remote ip "localhost:*"))', argv[2])

    def test_health_counts_distinct_runs_not_repeated_artifacts(self):
        from ci_policy.health import summarize, queue_times
        runs = [{'id': n, 'html_url': str(n)} for n in range(3)]
        def get(_run):
            return ([{'lane': 'behavior', 'started_at': 0, 'finished_at': 10}],
                    [{'signature': 'a' * 20}, {'signature': 'a' * 20}])
        report = summarize(runs, get)
        self.assertEqual(report['recurring'], {'a' * 20: ['0', '1', '2']})
        self.assertEqual(report['lanes']['behavior']['p95_seconds'], 10)
        self.assertEqual(report['runs_without_receipts'], [])
        self.assertEqual(summarize(runs, lambda _: ([], []))['runs_without_receipts'], [0, 1, 2])
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
        reviewed = json.loads((Path(__file__).resolve().parents[2] / 'ci_policy' / 'queue-settings.json').read_text())
        self.assertEqual(result['rules'][1]['parameters'], reviewed)
        self.assertEqual(current['rules'][1]['parameters'], {'max_entries_to_build': 3})

    def test_dependency_snapshot_does_not_depend_on_the_current_module_graph(self):
        import importlib.util
        spec = importlib.util.spec_from_file_location('vulndb', Path(__file__).resolve().parents[2] / 'vendor-vulndb.py')
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
