"""BB03 defect controls and benign controls for Portable Command grading."""
import copy
import json
import pathlib
import tempfile
import unittest
from unittest.mock import patch
import focused_outcomes as oracle
from portable_command.contracts import DIRECTORY, bytes_value, digest, load_suite
from portable_command.grading import grade_case
from portable_command.observe import _verified_source_binding, scan_sidecar_launches
from test_focused_outcomes import local_execute

CASES = {c['id']: c for c in load_suite()['cases']}
CONTROLS = json.loads((DIRECTORY / 'controls.json').read_text())
IDENTITY = {'source': 'bb03-control', 'git_head': '0' * 40}


def write_project(root, case, changes=None):
    files = dict(case['expected']['files'])
    files.update(CONTROLS.get(case['id'], {}).get('positive', {}))
    files.update(changes or {})
    for name, value in files.items():
        path = root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(bytes_value(value))


def make_observation(case, capture_kind='native-runtime'):
    expected = case['expected']
    recovery = expected.get('recovery')
    owner_code = recovery.get('owner_code') if isinstance(recovery, dict) else None
    single_rejected = sum(1 for t in case['turns'] if t['expected_outcome'] == 'rejected') == 1
    invocations = []
    for i, turn in enumerate(case['turns']):
        code = turn.get('expected_rejection')
        if code is None and turn['expected_outcome'] == 'rejected' and single_rejected and owner_code:
            code = owner_code
        invocations.append({
            'id': f'program-{i}',
            'program': turn['program'],
            'outcome': turn['expected_outcome'],
            'rejection_code': code,
        })
    receipts = [dict(row, execution_id=f'child-{i}') for i, row in enumerate(expected['receipts'])]
    return {
        'version': 1,
        'case_id': case['id'],
        'capture_kind': capture_kind,
        'identity': copy.deepcopy(IDENTITY),
        'program': {k: expected[k] for k in ('stdout', 'stderr', 'status', 'host_stop', 'complete')},
        'receipts': receipts,
        'effects': copy.deepcopy(expected['effects']),
        'facts': {k: True for k in case['required_evidence']},
        'deliveries': copy.deepcopy(expected['deliveries']),
        'obligations': copy.deepcopy(expected['obligations']),
        'closeout': expected['closeout'],
        'forbidden_replay': False,
        'recovery': copy.deepcopy(recovery),
        'invocations': invocations,
    }


class PortableCommandControls(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        patcher = patch.object(oracle, 'execute', side_effect=local_execute)
        patcher.start()
        self.addCleanup(patcher.stop)

    def grade(self, case, obs):
        return grade_case(case, obs, self.root, IDENTITY)

    def test_normal_successful_attempt_grades_correctly(self):
        case = CASES['first-mutation']
        write_project(self.root, case)
        result = self.grade(case, make_observation(case))
        self.assertEqual(result['qualification'], 'native-runtime')
        self.assertTrue(result['passed'])
        self.assertTrue(all(result['dimensions'].values()))
        self.assertEqual(len(result['dimensions']), 9)
        self.assertTrue(result['first_action_success'])
        self.assertFalse(result['forbidden_replay'])

    def test_wrong_bytes_fails_artifacts(self):
        case = CASES['first-mutation']
        write_project(self.root, case, {'result.txt': 'corrupted\n'})
        result = self.grade(case, make_observation(case))
        self.assertFalse(result['passed'])
        self.assertFalse(result['dimensions']['artifacts'])
        self.assertTrue(result['dimensions']['execution'])
        self.assertTrue(result['dimensions']['receipts'])

    def test_unchanged_bytes_with_falsely_reported_write_fails_artifacts(self):
        case = CASES['same-file-transform']
        write_project(self.root, case, {'settings.json': case['setup']['files']['settings.json']})
        result = self.grade(case, make_observation(case))
        self.assertFalse(result['passed'])
        self.assertFalse(result['dimensions']['artifacts'])
        self.assertTrue(result['dimensions']['execution'])
        self.assertTrue(result['dimensions']['receipts'])
        self.assertTrue(result['dimensions']['authority'])

    def test_correct_bytes_with_wrong_operation_or_wrong_revision_fails(self):
        case = CASES['draft-read-transform']
        write_project(self.root, case)
        baseline = make_observation(case)
        self.assertTrue(self.grade(case, baseline)['passed'])

        wrong_op = copy.deepcopy(baseline)
        wrong_op['receipts'][2]['operation'] = 'external_exec'
        wrong_op['effects'][0]['operation'] = 'external_exec'
        op_grade = self.grade(case, wrong_op)
        self.assertTrue(op_grade['dimensions']['artifacts'])
        self.assertFalse(op_grade['dimensions']['receipts'])
        self.assertFalse(op_grade['dimensions']['authority'])
        self.assertFalse(op_grade['passed'])

        wrong_rev = copy.deepcopy(baseline)
        wrong_rev['receipts'][0]['input_revision'] = 'baseline:settings.json'
        rev_grade = self.grade(case, wrong_rev)
        self.assertTrue(rev_grade['dimensions']['artifacts'])
        self.assertFalse(rev_grade['dimensions']['receipts'])
        self.assertFalse(rev_grade['passed'])

        wrong_rev_fact = copy.deepcopy(baseline)
        wrong_rev_fact['facts']['execution_revision'] = False
        fact_grade = self.grade(case, wrong_rev_fact)
        self.assertTrue(fact_grade['dimensions']['artifacts'])
        self.assertFalse(fact_grade['dimensions']['evidence'])
        self.assertFalse(fact_grade['passed'])

    def test_missing_child_and_duplicate_child_fail_receipts(self):
        case = CASES['first-mutation']
        write_project(self.root, case)
        baseline = make_observation(case)

        missing_child = copy.deepcopy(baseline)
        missing_child['receipts'].pop(1)
        self.assertFalse(self.grade(case, missing_child)['dimensions']['receipts'])

        duplicate_id = copy.deepcopy(baseline)
        duplicate_id['receipts'][1]['execution_id'] = duplicate_id['receipts'][0]['execution_id']
        self.assertFalse(self.grade(case, duplicate_id)['dimensions']['receipts'])

        duplicate_row = copy.deepcopy(baseline)
        duplicate_row['receipts'].append(dict(duplicate_row['receipts'][1], execution_id='child-extra'))
        self.assertFalse(self.grade(case, duplicate_row)['dimensions']['receipts'])

    def test_extra_effect_fails_authority(self):
        case = CASES['first-mutation']
        write_project(self.root, case)
        extra = make_observation(case)
        extra['effects'].append({
            'operation': 'write', 'outcome': 'completed', 'path': 'unexpected.txt',
            'accepted': True, 'published': True,
        })
        result = self.grade(case, extra)
        self.assertFalse(result['dimensions']['authority'])
        self.assertFalse(result['passed'])

    def test_wrong_handle_fails_receipts_and_authority(self):
        case = CASES['first-mutation']
        write_project(self.root, case)
        wrong_path = make_observation(case)
        wrong_path['receipts'][2]['path'] = 'other.txt'
        wrong_path['effects'][1]['path'] = 'other.txt'
        result = self.grade(case, wrong_path)
        self.assertFalse(result['dimensions']['receipts'])
        self.assertFalse(result['dimensions']['authority'])
        self.assertFalse(result['passed'])

        temp_case = CASES['temp-native-process-handoff']
        with tempfile.TemporaryDirectory() as tmp:
            temp_root = pathlib.Path(tmp)
            write_project(temp_root, temp_case)
            wrong_ns = make_observation(temp_case)
            wrong_ns['receipts'][2]['namespace'] = 'workspace'
            wrong_ns['effects'][1]['namespace'] = 'workspace'
            ns_grade = grade_case(temp_case, wrong_ns, temp_root, IDENTITY)
            self.assertFalse(ns_grade['dimensions']['receipts'])
            self.assertFalse(ns_grade['dimensions']['authority'])
            self.assertFalse(ns_grade['passed'])

    def test_missing_terminal_event_or_non_terminal_state_fails(self):
        case = CASES['first-mutation']
        write_project(self.root, case)
        baseline = make_observation(case)

        non_terminal_program = copy.deepcopy(baseline)
        non_terminal_program['program']['complete'] = False
        non_terminal_program['program']['host_stop'] = 'incomplete'
        self.assertFalse(self.grade(case, non_terminal_program)['dimensions']['execution'])

        missing_closeout = copy.deepcopy(baseline)
        missing_closeout['closeout'] = None
        self.assertFalse(self.grade(case, missing_closeout)['dimensions']['closeout'])

        non_terminal_invocation = copy.deepcopy(baseline)
        non_terminal_invocation['invocations'][0]['outcome'] = 'running'
        self.assertFalse(self.grade(case, non_terminal_invocation)['dimensions']['recovery'])

    def test_stale_verification_fails_delivered_tests(self):
        case = CASES['fractional-logstat']
        write_project(self.root, case)
        baseline = make_observation(case)
        baseline['verification_facts'] = {
            'session_id': 'sess-1',
            'sessions': [{'id': 'sess-1'}],
            'delivered_paths': ['logstat.py', 'test_logstat.py'],
            'snapshots': {'rev-2': {'matches_delivered': True, 'roots_key': 'digest-2', 'roots': ['.']}},
            'verification': [{
                'type': 'verify', 'slot': 'tests', 'run_id': 'sess-1', 'verdict': 'passed',
                'artifacts': {
                    'session_id': 'sess-1', 'is_check': True, 'exit_code': 0,
                    'producer': 'command', 'check_id': 'chk-1',
                    'source_revision': 'rev-2', 'source_root_digest': 'digest-2',
                    'cwd': '.', 'command': 'python3 -B -m unittest discover',
                },
            }],
        }
        self.assertTrue(self.grade(case, baseline)['passed'])

        for field, stale in [('source_revision', 'rev-1'), ('source_root_digest', 'digest-1')]:
            broken = copy.deepcopy(baseline)
            broken['verification_facts']['verification'][0]['artifacts'][field] = stale
            with self.subTest(field=field):
                result = self.grade(case, broken)
                self.assertFalse(result['dimensions']['delivered_tests'])
                self.assertFalse(result['passed'])

    def test_truncated_export_fails_or_is_unqualified(self):
        case = CASES['first-mutation']
        write_project(self.root, case)
        baseline = make_observation(case)

        for key in ('version', 'case_id', 'capture_kind', 'identity'):
            truncated = copy.deepcopy(baseline)
            truncated.pop(key)
            with self.subTest(dropped_header=key):
                result = self.grade(case, truncated)
                self.assertEqual(result['qualification'], 'unqualified')
                self.assertFalse(result['passed'])

        partial_identity = copy.deepcopy(baseline)
        partial_identity['identity'].pop('git_head')
        self.assertEqual(self.grade(case, partial_identity)['qualification'], 'unqualified')

        for key, expected_dim in [
            ('program', 'execution'),
            ('receipts', 'receipts'),
            ('effects', 'authority'),
            ('deliveries', 'delivery'),
            ('closeout', 'closeout'),
            ('invocations', 'recovery'),
        ]:
            truncated = copy.deepcopy(baseline)
            truncated.pop(key)
            with self.subTest(dropped_field=key):
                result = self.grade(case, truncated)
                self.assertFalse(result['dimensions'][expected_dim])
                self.assertFalse(result['passed'])

    def test_wrong_missing_and_misplaced_rejection_codes_fail_recovery(self):
        # Single rejected turn with omitted turn.expected_rejection bound via recovery.owner_code.
        refusal_case = CASES['native-option-no-fallback']
        write_project(self.root, refusal_case)
        for label, bad_code in [('missing', None), ('wrong', 'COMMAND_SYNTAX_INVALID')]:
            obs = make_observation(refusal_case)
            obs['invocations'][0]['rejection_code'] = bad_code
            with self.subTest(case=refusal_case['id'], defect=label):
                result = self.grade(refusal_case, obs)
                self.assertFalse(result['dimensions']['recovery'])
                self.assertFalse(result['passed'])

        # Multi-turn case with explicit turn.expected_rejection and an unrelated completed turn.
        oar_case = CASES['oar-option-correction']
        with tempfile.TemporaryDirectory() as tmp:
            oar_root = pathlib.Path(tmp)
            write_project(oar_root, oar_case)
            baseline = make_observation(oar_case)
            self.assertTrue(grade_case(oar_case, baseline, oar_root, IDENTITY)['passed'])

            for label, turn_index, bad_code in [
                ('missing_explicit', 0, None),
                ('wrong_explicit', 0, 'COMMAND_SYNTAX_INVALID'),
                ('misplaced_on_completed_turn', 1, 'COMMAND_OPTION_UNSUPPORTED'),
            ]:
                broken = copy.deepcopy(baseline)
                broken['invocations'][turn_index]['rejection_code'] = bad_code
                with self.subTest(case=oar_case['id'], defect=label):
                    result = grade_case(oar_case, broken, oar_root, IDENTITY)
                    self.assertFalse(result['dimensions']['recovery'])
                    self.assertFalse(result['passed'])

        # Misplaced rejection code on a normal single-turn completed attempt.
        ok_case = CASES['first-mutation']
        with tempfile.TemporaryDirectory() as tmp:
            ok_root = pathlib.Path(tmp)
            write_project(ok_root, ok_case)
            misplaced = make_observation(ok_case)
            misplaced['invocations'][0]['rejection_code'] = 'COMMAND_OPTION_UNSUPPORTED'
            result = grade_case(ok_case, misplaced, ok_root, IDENTITY)
            self.assertFalse(result['dimensions']['recovery'])
            self.assertFalse(result['passed'])

    def test_benign_expected_refusal_controls_pass_all_dimensions(self):
        for case_id in ('native-option-no-fallback', 'syntax-with-progress'):
            case = CASES[case_id]
            with self.subTest(case=case_id), tempfile.TemporaryDirectory() as tmp:
                root = pathlib.Path(tmp)
                write_project(root, case)
                result = grade_case(case, make_observation(case), root, IDENTITY)
                self.assertEqual(result['qualification'], 'native-runtime')
                self.assertTrue(result['passed'])
                self.assertTrue(all(result['dimensions'].values()))
                self.assertEqual(len(result['dimensions']), 9)

    def test_benign_truncation_control_passes_while_incomplete_fails(self):
        case = CASES['unseen-output-no-edit-base']
        write_project(self.root, case)
        obs = make_observation(case)
        obs['program']['preview_truncated'] = True
        obs['metrics'] = {'preview_truncated': True, 'retained_bytes': 11}
        result = self.grade(case, obs)
        self.assertTrue(result['passed'])
        self.assertTrue(all(result['dimensions'].values()))

        non_benign = copy.deepcopy(obs)
        non_benign['program']['complete'] = False
        failed = self.grade(case, non_benign)
        self.assertFalse(failed['dimensions']['execution'])
        self.assertFalse(failed['passed'])

    def test_sidecar_log_truncated_after_startup_or_session_fails_completeness(self):
        sid = 'sess1234-0000-0000-0000-000000000001'
        case = CASES['native-option-no-fallback']
        write_project(self.root, case)
        for label, content in [
            ('truncated_after_startup', 'level=INFO msg="server listening" addr=127.0.0.1:9999\n'),
            ('truncated_after_session', 'level=INFO msg="server listening"\nlevel=INFO msg="session created" session_id=sess1234\n'),
        ]:
            log_path = self.root / f'{label}.log'
            log_path.write_text(content)
            scan = scan_sidecar_launches(log_path, sid)
            with self.subTest(defect=label):
                self.assertFalse(scan['complete'])
                self.assertIsNone(scan['command_launches'])
                obs = make_observation(case)
                obs['facts']['no_external_launch'] = bool(scan['available'] and scan['complete'] and scan['command_launches'] == 0)
                result = self.grade(case, obs)
                self.assertFalse(result['dimensions']['evidence'])
                self.assertFalse(result['passed'])
        full_log = self.root / 'complete.log'
        full_log.write_text('level=INFO msg="server listening"\nlevel=INFO msg="session created" session_id=sess1234\nlevel=INFO msg="shutdown signal received" signal=terminated\n')
        ok = scan_sidecar_launches(full_log, sid, window={'post_tool_lines': 2})
        self.assertTrue(ok['complete'] and ok['command_launches'] == 0)
        self.assertFalse(scan_sidecar_launches(full_log, sid, window={'post_tool_lines': 3})['complete'])

    def test_wrong_source_pin_fails_exact_source_binding(self):
        case = CASES['search-empty-publication']
        good_sha = digest(bytes_value(case['setup']['files']['input.txt']))
        execs = [{'id': 'ex-1'}]
        good_pin = {'execution_id': 'ex-1', 'role': 'source', 'mode': 'published_disk', 'path': 'input.txt', 'content_sha256': good_sha}
        self.assertTrue(_verified_source_binding(case, [good_pin], execs, {'input.txt': good_sha})[0])
        for bad in (
            dict(good_pin, path='wrong.txt'), dict(good_pin, content_sha256='0' * 64), dict(good_pin, execution_id='unknown'),
        ):
            self.assertFalse(_verified_source_binding(case, [bad], execs, {'input.txt': good_sha})[0])
