"""Exercise real artifact oracles against distinct correct and broken deliveries."""
import copy
import io
import json
import pathlib
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import focused_outcomes as oracle
from portable_command.contracts import DIRECTORY, bytes_value, load_suite
from portable_command.artifacts import grade_artifacts, delivered_suite
from portable_command.grading import grade_case, grade_resources
from test_focused_outcomes import local_execute

CASES = {c['id']: c for c in load_suite()['cases']}
CONTROLS = json.loads((DIRECTORY / 'controls.json').read_text())


def project_files(root, case, changes=None):
    files = dict(case['expected']['files'])
    for generator in case['setup'].get('generators', []):
        for index in range(generator['count']):
            files[f"{generator['directory']}/{index:05}.txt"] = generator['body']
    files.update(CONTROLS.get(case['id'], {}).get('positive', {}))
    files.update(changes or {})
    for name, value in files.items():
        path = root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(bytes_value(value))
    for name, spec in case['expected'].get('artifact_formats', {}).items():
        with tarfile.open(root / name, 'w:bz2') as archive:
            for member, value in spec['tar_entries'].items():
                body = bytes_value(value)
                info = tarfile.TarInfo(member)
                info.size = len(body)
                archive.addfile(info, io.BytesIO(body))


def observation(case):
    expected = case['expected']
    receipts = [dict(row, execution_id=f'child-{i}') for i, row in enumerate(expected['receipts'])]
    return {'version': 1, 'case_id': case['id'], 'capture_kind': 'synthetic-control',
            'identity': {'source': 'synthetic-control'},
            'program': {k: expected[k] for k in ('stdout', 'stderr', 'status', 'host_stop', 'complete')},
            'receipts': receipts, 'effects': copy.deepcopy(expected['effects']),
            'facts': {k: True for k in case['required_evidence']},
            'deliveries': copy.deepcopy(expected['deliveries']), 'obligations': expected['obligations'],
            'closeout': expected['closeout'], 'forbidden_replay': False,
            'recovery': expected.get('recovery'),
            'invocations': [{'id': f'program-{i}', 'program': t['program'],
                             'outcome': t['expected_outcome'], 'rejection_code': t.get('expected_rejection')}
                            for i, t in enumerate(case['turns'])]}


class PortableCommandGrades(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        patcher = patch.object(oracle, 'execute', side_effect=local_execute)
        patcher.start()
        self.addCleanup(patcher.stop)

    def grade(self, case, actual):
        return grade_case(case, actual, self.root, {'source': 'synthetic-control'})

    def test_all_frozen_artifact_oracles_accept_independent_positive_deliveries(self):
        for case in CASES.values():
            with self.subTest(case=case['id']), tempfile.TemporaryDirectory() as temporary:
                root = pathlib.Path(temporary)
                project_files(root, case)
                self.assertTrue(grade_artifacts(root, case))

    def test_status_stdout_effect_receipt_and_actual_bytes_are_independent(self):
        case = CASES['first-mutation']
        project_files(self.root, case)
        actual = observation(case)
        self.assertTrue(self.grade(case, actual)['passed'])
        for dimension, mutate in [
            ('execution', lambda a: a['program'].update(status=1)),
            ('execution', lambda a: a['program'].update(stdout='claimed success')),
            ('authority', lambda a: a['effects'].append({'operation': 'forbidden-write', 'outcome': 'completed'})),
            ('receipts', lambda a: a['receipts'].clear()),
            ('delivery', lambda a: a['deliveries'].append({'source': 'unseen'})),
            ('evidence', lambda a: a['facts'].clear()),
            ('recovery', lambda a: a['invocations'].append(a['invocations'][0])),
        ]:
            broken = copy.deepcopy(actual)
            mutate(broken)
            with self.subTest(dimension=dimension):
                self.assertFalse(self.grade(case, broken)['dimensions'][dimension])
        target = self.root / next(iter(case['expected']['files']))
        target.write_bytes(b'wrong bytes despite successful status')
        self.assertFalse(self.grade(case, actual)['dimensions']['artifacts'])
        self.assertTrue(self.grade(case, actual)['first_invocation_completed'])
        self.assertFalse(self.grade(case, actual)['first_action_success'])

    def test_missing_browser_obligation_cannot_be_closed_by_build_success(self):
        case = CASES['browser-obligation']
        project_files(self.root, case)
        actual = observation(case)
        self.assertTrue(self.grade(case, actual)['passed'])
        actual['closeout'] = 'complete'
        actual['obligations'] = []
        self.assertFalse(self.grade(case, actual)['dimensions']['closeout'])
        self.assertFalse(self.grade(case, actual)['first_action_success'])
        actual = observation(case)
        actual['facts'].pop(case['required_evidence'][0])
        self.assertFalse(self.grade(case, actual)['dimensions']['evidence'])

    def test_recovery_is_invocation_and_receipt_backed_not_final_prose(self):
        case = CASES['oar-option-correction']
        project_files(self.root, case)
        actual = observation(case)
        result = self.grade(case, actual)
        self.assertTrue(result['passed'])
        self.assertFalse(result['first_action_success'])
        self.assertTrue(result['recovery_success'])
        for dimension in ('receipts', 'effects'):
            broken = observation(case)
            if dimension == 'receipts': broken['receipts'].clear()
            else: broken['effects'].append({'operation':'forbidden-write','outcome':'completed'})
            self.assertFalse(self.grade(case, broken)['recovery_success'])
        broken = observation(case)
        broken['obligations'] = [{'kind':'browser', 'tool':'capture_page', 'state':'pending'}]
        self.assertFalse(self.grade(case, broken)['recovery_success'])
        target = self.root / next(iter(case['expected']['files']))
        original = target.read_bytes()
        target.write_bytes(b'wrong corrected bytes')
        self.assertFalse(self.grade(case, observation(case))['recovery_success'])
        target.write_bytes(original)
        actual['invocations'][0]['outcome'] = 'completed'
        actual['final_prose'] = 'Recovered successfully'
        self.assertFalse(self.grade(case, actual)['dimensions']['recovery'])
        actual = observation(case)
        actual['forbidden_replay'] = True
        self.assertFalse(self.grade(case, actual)['passed'])
        actual = observation(case)
        actual['invocations'].append(dict(actual['invocations'][0], id='replayed-program'))
        self.assertTrue(self.grade(case, actual)['forbidden_replay'])

    def test_fractional_and_wordle_controls_grade_behavior_and_own_tests_separately(self):
        for name, control in CONTROLS.items():
            case = CASES[name]
            for mutation, changes in {'positive': {}, **control['negative']}.items():
                with self.subTest(case=name, mutation=mutation), tempfile.TemporaryDirectory() as temporary:
                    root = pathlib.Path(temporary)
                    project_files(root, case, changes)
                    artifacts = grade_artifacts(root, case)
                    tests = delivered_suite(root)
                    if mutation == 'positive':
                        self.assertTrue(artifacts)
                        self.assertTrue(tests)
                    elif mutation == 'broken_delivered_test':
                        self.assertTrue(artifacts)
                        self.assertFalse(tests)
                    else:
                        self.assertFalse(artifacts)

    def test_delivered_test_evidence_requires_the_actual_delivered_revision(self):
        case = CASES['fractional-logstat']
        project_files(self.root, case)
        actual = observation(case)
        actual['verification_facts'] = {
            'session_id':'synthetic-session', 'sessions':[{'id':'synthetic-session'}],
            'delivered_paths':['logstat.py','test_logstat.py'],
            'snapshots':{'source-v2':{'matches_delivered':True, 'roots_key':'digest-v2', 'roots':['.']}},
            'verification':[{'type':'verify','slot':'tests','run_id':'synthetic-session','verdict':'passed',
                'artifacts':{'session_id':'synthetic-session','is_check':True,'exit_code':0,
                    'producer':'command','check_id':'check-1','source_revision':'source-v2',
                    'source_root_digest':'digest-v2','cwd':'.','command':'python3 -B -m unittest discover'}}]}
        self.assertTrue(self.grade(case, actual)['passed'])
        for field, value in [('source_revision','source-v1'),('source_root_digest','digest-v1'),('exit_code',1)]:
            broken = copy.deepcopy(actual)
            broken['verification_facts']['verification'][0]['artifacts'][field] = value
            self.assertFalse(self.grade(case, broken)['dimensions']['delivered_tests'])
        actual['verification_facts']['verification'] = []
        self.assertFalse(self.grade(case, actual)['dimensions']['delivered_tests'])

    def test_archive_tree_and_symlink_cannot_hide_forbidden_artifacts(self):
        case = CASES['archive-create-extract']
        project_files(self.root, case)
        archive = self.root / next(iter(case['expected']['artifact_formats']))
        archive.write_bytes(b'not an archive')
        self.assertFalse(grade_artifacts(self.root, case))
        (self.root / 'outside-link').symlink_to('/synthetic/no-such-file')
        self.assertFalse(grade_artifacts(self.root, case))

    def test_identity_resources_and_repeated_grading_are_exact(self):
        case = CASES['first-mutation']
        project_files(self.root, case)
        actual = observation(case)
        self.assertEqual(self.grade(case, actual), self.grade(case, copy.deepcopy(actual)))
        actual['identity'] = {}
        self.assertEqual(self.grade(case, actual)['qualification'], 'unqualified')
        profile = load_suite()['resource_profiles'][0]
        binding = dict(hardware_sha256='a'*64, dataset_sha256='b'*64, source_sha256='c'*64, cache_state='cold')
        measured = {**binding, **{k: profile[k] for k in ('wall_ms', 'peak_rss_bytes', 'spool_bytes', 'cancel_ms')}}
        self.assertTrue(grade_resources(profile, measured, binding)['passed'])
        measured['wall_ms'] += 1
        self.assertFalse(grade_resources(profile, measured, binding)['passed'])
        measured['hardware_sha256'] = 'different'
        self.assertEqual(grade_resources(profile, measured, binding)['qualification'], 'unqualified')
