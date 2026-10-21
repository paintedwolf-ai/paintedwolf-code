"""Helper-family boundaries are decided from typed kernel facts and suite records."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from linux_release.installed_cases import helper_families, helper_probe, helper_suite, process_tree
from linux_release.installed_cases.records import CaseFailed, CaseUnavailable

ROOT = {'pid': 10, 'parent_pid': 5, 'start_time_ticks': 30}
OUTER = {'user': [4, 100], 'mnt': [4, 101], 'pid': [4, 102], 'net': [4, 103]}
INNER = {'user': [4, 200], 'mnt': [4, 201], 'pid': [4, 202], 'net': [4, 203]}
IMAGE = [64768, 77, 4096]


def process(identity, namespaces, image, **changes):
    row = {'identity': identity, 'exe': '/opt/painted-wolf-code/bin/fixture', 'image': image,
           'namespaces': namespaces, 'user_namespace_owner': 1000, 'uid_map': [[0, 1000, 1]], 'gid_map': [[0, 1000, 1]],
           'uids': [1000] * 4, 'no_new_privs': 1, 'seccomp': 2,
           'capabilities': {name: 0 for name in process_tree.CAPABILITIES}}
    row.update(changes)
    return row


def tree(*helpers):
    return {'root': ROOT, 'initial': {'namespaces': OUTER, 'user_namespace_owner': 0},
            'processes': [process(ROOT, OUTER, [64768, 1, 1], no_new_privs=0, seccomp=0), *helpers]}


HELPER = {'pid': 11, 'parent_pid': 10, 'start_time_ticks': 31}


def observation(**changes):
    value = {'namespaces': {'user': 200, 'mnt': 201, 'pid': 202, 'net': 203}, 'seccomp': 2, 'no_new_privs': 1,
             'capabilities': {name: 0 for name in process_tree.CAPABILITIES}, 'outside_write': 'EACCES',
             'control_read': 'ENOENT', 'future_protected_create': 'EACCES', 'sibling_write': None}
    value.update(changes)
    return value


class ProcessTreeTests(unittest.TestCase):
    def test_validate_accepts_only_the_exact_typed_tree_of_its_root(self):
        document = tree(process(HELPER, INNER, IMAGE))
        self.assertIs(process_tree.validate(document, ROOT), document)
        mutations = [lambda d: d.update(root={**ROOT, 'start_time_ticks': 99}),
                     lambda d: d['processes'].pop(0),
                     lambda d: d['processes'].append(d['processes'][1]),
                     lambda d: d['processes'][1].update(uid_map=[[0, 1000]]),
                     lambda d: d['processes'][1].update(image=[1, 2]),
                     lambda d: d['processes'][1]['namespaces'].pop('net'),
                     lambda d: d['processes'][1]['capabilities'].update(CapEff=True),
                     lambda d: d['processes'][1].update(mounts=[['/', 7]]),
                     lambda d: d['processes'][1].update(unexpected=True),
                     lambda d: d['initial'].update(user_namespace_owner='0')]
        for index, mutate in enumerate(mutations):
            document = json.loads(json.dumps(tree(process(HELPER, INNER, IMAGE))))
            mutate(document)
            with self.subTest(index=index), self.assertRaises(ValueError):
                process_tree.validate(document, ROOT)

    def test_boundary_failures_name_each_missing_kernel_fact(self):
        row = process(HELPER, INNER, IMAGE)
        self.assertEqual(process_tree.boundary_failures(row, OUTER, 1000), [])
        shared = process(HELPER, dict(INNER, net=OUTER['net']), IMAGE, seccomp=0, no_new_privs=0,
                         capabilities={**row['capabilities'], 'CapEff': 1}, uids=[0, 1000, 1000, 1000])
        self.assertEqual(process_tree.boundary_failures(shared, OUTER, 1000),
                         ['namespace-shared-net', 'seccomp-filter-absent', 'no-new-privileges-absent',
                          'capabilities-held', 'identity-changed'])

    def test_memory_open_reports_errno_names_without_reading_memory(self):
        with patch.object(process_tree.os, 'open', side_effect=PermissionError(13, 'fixture')):
            self.assertEqual(process_tree.memory_open_errno(5), 'EACCES')
        with patch.object(process_tree.os, 'open', return_value=7), patch.object(process_tree.os, 'close') as closed:
            self.assertEqual(process_tree.memory_open_errno(5), 'opened')
            closed.assert_called_once_with(7)


class HelperFamilyTests(unittest.TestCase):
    OUTER_INODES = {'user': 100, 'mnt': 101, 'pid': 102, 'net': 103}

    def test_in_domain_report_requires_every_denial_and_the_permitted_sibling(self):
        self.assertEqual(helper_probe.domain_failures(observation(), self.OUTER_INODES, project=True), [])
        reported = observation(namespaces={'user': 100, 'mnt': 201, 'pid': 202, 'net': 203}, seccomp=0,
                               outside_write=None, control_read=None, future_protected_create=None, sibling_write='EACCES')
        self.assertEqual(helper_probe.domain_failures(reported, self.OUTER_INODES, project=True),
                         ['namespace-shared-user', 'seccomp-filter-absent', 'outside-write-admitted',
                          'control-plane-read-admitted', 'future-protected-name-created', 'permitted-sibling-write-refused'])
        mcp = {key: value for key, value in observation().items() if key not in {'future_protected_create', 'sibling_write'}}
        self.assertEqual(helper_probe.domain_failures(mcp, self.OUTER_INODES, project=False), [])
        for malformed in (None, mcp, observation(namespaces={'user': '200'}), observation(capabilities={'CapEff': 0})):
            with self.subTest(malformed=malformed):
                self.assertEqual(helper_probe.domain_failures(malformed, self.OUTER_INODES, project=True), ['observation-malformed'])

    def test_tree_outcome_never_passes_an_unobserved_helper(self):
        self.assertEqual(helper_families.tree_outcome(None, [IMAGE], 10, 1000),
                         {'status': 'unverified', 'reason': 'helper-not-observed'})
        self.assertEqual(helper_families.tree_outcome(tree(), [IMAGE], 10, 1000)['status'], 'unverified')
        passed = helper_families.tree_outcome(tree(process(HELPER, INNER, IMAGE)), [IMAGE], 10, 1000)
        self.assertEqual(passed['status'], 'passed')
        self.assertEqual(passed['failures'], {'11': []})
        failed = helper_families.tree_outcome(tree(process(HELPER, OUTER, IMAGE, seccomp=0)), [IMAGE], 10, 1000)
        self.assertEqual(failed['status'], 'failed')
        self.assertIn('namespace-shared-user', failed['reason'])
        self.assertIn('seccomp-filter-absent', failed['reason'])
        with self.assertRaises(ValueError):
            helper_families.tree_outcome(tree(process(HELPER, INNER, IMAGE)), [IMAGE], 99, 1000)

    def test_aggregate_fails_before_unverified_and_requires_every_family(self):
        passed = {name: {'status': 'passed'} for name in helper_families.FAMILIES}
        self.assertEqual(helper_families.aggregate(passed), 'passed')
        self.assertEqual(helper_families.aggregate({**passed, 'local-mcp': {'status': 'unverified'}}), 'unverified')
        self.assertEqual(helper_families.aggregate({**passed, 'local-mcp': {'status': 'unverified'},
                                                    'terminal': {'status': 'failed'}}), 'failed')
        for broken in ({name: row for name, row in passed.items() if name != 'ocr-engine'},
                       {**passed, 'terminal': {'status': 'skipped'}}):
            with self.subTest(broken=sorted(broken)), self.assertRaises(ValueError):
                helper_families.aggregate(broken)

    def test_family_exceptions_classify_like_the_security_dispatcher(self):
        def raises(error):
            def function():
                raise error
            return function
        self.assertEqual(helper_families.guarded(raises(CaseUnavailable('fixture')))['status'], 'unverified')
        self.assertEqual(helper_families.guarded(raises(OSError('fixture')))['status'], 'unverified')
        self.assertEqual(helper_families.guarded(raises(CaseFailed('fixture')))['status'], 'failed')
        refused = helper_families.guarded(raises(ValueError('fixture contract')))
        self.assertEqual((refused['status'], refused['reason']), ('failed', 'installed-contract-refused'))
        with self.assertRaises(ValueError):
            helper_families.outcome('failed')
        with self.assertRaises(ValueError):
            helper_families.outcome('passed', 'unexpected-reason')

    def test_images_come_only_from_the_sealed_manifest(self):
        binding = {'images': {'/opt/painted-wolf-code/bin/bialy': {'device': 1, 'inode': 2, 'bytes': 3}}}
        self.assertEqual(helper_families.images(binding, [Path('/opt/painted-wolf-code/bin/bialy')]), [[1, 2, 3]])
        for paths in ([], [Path('/opt/painted-wolf-code/bin/other')]):
            with self.subTest(paths=paths), self.assertRaises(CaseUnavailable):
                helper_families.images(binding, paths)

    @unittest.skipUnless(Path('/proc/self/status').exists(), 'the in-domain probe reads Linux /proc')
    def test_probe_reports_from_inside_without_retaining_fixture_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            outside, control = root / 'outside', root / 'control'
            outside.write_bytes(b'public')
            control.write_bytes(b'{"pid": 1}')
            project = root / 'project'
            project.mkdir()
            subprocess.run([sys.executable, '-I', '-c', helper_probe.PROBE, 'file', str(outside), str(control)],
                           cwd=project, check=True, timeout=30)
            reported = json.loads((project / helper_probe.WITNESS).read_text())
        self.assertEqual(set(reported), {'namespaces', 'seccomp', 'no_new_privs', 'capabilities', 'outside_write',
                                         'control_read', 'future_protected_create', 'sibling_write'})
        # Unconfined, every write is admitted: the report says so rather than inventing a denial.
        self.assertIsNone(reported['outside_write'])
        self.assertIsNone(reported['future_protected_create'])
        self.assertIn('outside-write-admitted', helper_probe.domain_failures(
            reported, {name: -1 for name in process_tree.NAMESPACES}, project=True))


class HelperSuiteTests(unittest.TestCase):
    DIGEST = 'a' * 64

    def run_output(self, record, *, passed=True):
        lines = [f'=== RUN   {helper_suite.TEST}']
        if record is not None:
            lines.append('    boundary_linux_test.go:152: installed_boundary=' + json.dumps(record))
        lines.append(f'--- {"PASS" if passed else "FAIL"}: {helper_suite.TEST} (0.42s)')
        return ('\n'.join(lines) + '\n').encode()

    def test_suite_record_binds_outcome_digest_and_refusal_code(self):
        applied = {'schema_version': 1, 'outcome': 'applied', 'binary_sha256': self.DIGEST, 'refusal_code': ''}
        self.assertEqual(helper_suite.parse(self.run_output(applied), 0, 'applied', self.DIGEST, '')['status'], 'passed')
        refused = dict(applied, outcome='refused', refusal_code='user_namespaces_denied')
        self.assertEqual(helper_suite.parse(self.run_output(refused), 0, 'refused', self.DIGEST, 'user_namespaces_denied')['status'], 'passed')
        cases = [(self.run_output(refused), 0, 'refused', 'user_namespaces_denied_apparmor', 'installed-helper-record-mismatch'),
                 (self.run_output(dict(applied, binary_sha256='b' * 64)), 0, 'applied', '', 'installed-helper-record-mismatch'),
                 (self.run_output(None), 0, 'applied', '', 'installed-helper-record-missing'),
                 (self.run_output(applied, passed=False), 1, 'applied', '', 'installed-helper-suite-failed'),
                 (self.run_output(applied), 1, 'applied', '', 'installed-helper-suite-failed'),
                 (b'Failed to start transient service unit\n', 1, 'refused', 'user_namespaces_denied', 'installed-helper-suite-not-started')]
        for output, code, outcome, refusal, reason in cases:
            with self.subTest(reason=reason, code=code):
                self.assertEqual(helper_suite.parse(output, code, outcome, self.DIGEST, refusal)['reason'], reason)
        self.assertEqual(helper_suite.parse(self.run_output(None), 1, 'applied', '', '')['status'], 'failed')
        self.assertEqual(helper_suite.parse(b'', 1, 'refused', '', 'x')['status'], 'unverified')

    def test_expected_refusal_follows_the_kernel_apparmor_restriction(self):
        with tempfile.TemporaryDirectory() as directory:
            sysctl = Path(directory) / 'apparmor_restrict_unprivileged_userns'
            with patch.object(helper_suite, 'APPARMOR_SYSCTL', sysctl):
                self.assertEqual(helper_suite.expected_refusal(), 'user_namespaces_denied')
                sysctl.write_text('1\n')
                self.assertEqual(helper_suite.expected_refusal(), 'user_namespaces_denied_apparmor')
                sysctl.write_text('0\n')
                self.assertEqual(helper_suite.expected_refusal(), 'user_namespaces_denied')

    def test_missing_suite_or_toolchain_is_a_typed_unverified_result(self):
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(helper_suite, 'SUITE', Path(directory) / 'absent'), patch.object(helper_suite, 'ROOT', Path(directory)):
                status, facts, streams = helper_suite.helper_suite({})
            self.assertEqual((status, facts['reason'], streams), ('unverified', 'installed-helper-suite-absent', {}))
            suite = Path(directory) / 'suite'
            suite.mkdir()
            (suite / 'selected_linux_test.go').write_text('package confinement\n')
            with patch.object(helper_suite, 'SUITE', suite), patch.object(helper_suite.shutil, 'which', return_value=None):
                status, facts, _ = helper_suite.helper_suite({})
            self.assertEqual((status, facts['reason']), ('unverified', 'go-toolchain-absent'))

    def test_both_runs_classify_the_case(self):
        applied = {'schema_version': 1, 'outcome': 'applied', 'binary_sha256': self.DIGEST, 'refusal_code': ''}
        refused = dict(applied, outcome='refused', refusal_code='user_namespaces_denied')
        binding = {'images': {'/opt/painted-wolf-code/bin/pw': {'sha256': self.DIGEST}}}

        class Built:
            returncode, stdout, stderr = 0, b'', b''

        def build(command, **kwargs):
            Path(command[command.index('-o') + 1]).write_bytes(b'test binary')
            return Built()
        for outputs, expected in (([(self.run_output(applied), 0), (self.run_output(refused), 0)], 'passed'),
                                  ([(self.run_output(applied), 0), (b'', 1)], 'unverified'),
                                  ([(self.run_output(applied, passed=False), 1), (self.run_output(refused), 0)], 'failed')):
            with self.subTest(expected=expected), patch.object(helper_suite.shutil, 'which', return_value='/usr/bin/go'), \
                    patch.object(helper_suite, 'unchanged', return_value=Path('/opt/painted-wolf-code/bin/pw')), \
                    patch.object(helper_suite.subprocess, 'run', side_effect=build), \
                    patch.object(helper_suite, 'expected_refusal', return_value='user_namespaces_denied'), \
                    patch.object(helper_suite, 'execute', side_effect=outputs) as executed, \
                    patch.object(Path, 'is_file', return_value=True):
                status, facts, streams = helper_suite.helper_suite(binding)
                self.assertEqual(status, expected)
                self.assertEqual(set(streams), {'applied-output', 'missing-capability-output'})
                unit = executed.call_args_list[1].args[0]
                self.assertEqual(unit[:8], ['systemd-run', '--user', '--wait', '--pipe', '--quiet', '--collect', '-p', 'RestrictNamespaces=yes'])
                self.assertIn('--setenv=PW_INSTALLED_CONFINEMENT_REFUSAL=user_namespaces_denied', unit)
                self.assertEqual(facts['missing_capability']['expected_refusal'], 'user_namespaces_denied')


if __name__ == '__main__':
    unittest.main()
