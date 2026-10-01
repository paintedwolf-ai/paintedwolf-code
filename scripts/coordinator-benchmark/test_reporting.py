import json
import pathlib
import shutil
import tempfile
import unittest
from unittest.mock import patch
import report
import ledger
from grade import grade


class ReportingTests(unittest.TestCase):
    def setUp(self):
        mock=patch('report.episode_evidence.export',return_value={})
        mock.start();self.addCleanup(mock.stop)
        scripted = patch.object(ledger, 'scripted_execution_errors', return_value=[])
        scripted.start(); self.addCleanup(scripted.stop)
        self.verification = patch.object(report, 'verification_checks', return_value=[
            {'id': 'coordinator-verification-current', 'passed': True}])
        self.verification.start()
        self.addCleanup(self.verification.stop)
        for name in ['preparation_checks','returned_worker_checks','sandbox_checks','planned_dispatch_checks',
                     'decision_checks','question_checks','capability_checks','method_checks',
                     'cited_finding_checks','outside_checks','workflow_checks']:
            mocked = patch.object(report,name,return_value=[])
            mocked.start();self.addCleanup(mocked.stop)
        observed = patch.object(report.structure,'observe',return_value={'tool_calls':1})
        observed.start();self.addCleanup(observed.stop)

    def test_score_requires_full_sampling_and_complete_operation_coverage(self):
        for repetitions in [1, 3, 4, 5, 6]:
            cases = [{'id': case, 'passed': repetitions, 'measured': repetitions, 'attempts': repetitions}
                     for case in report.CASES]
            with self.subTest(repetitions=repetitions):
                expected = 100 if repetitions >= 5 else None
                self.assertAlmostEqual(report.overall_score(cases, repetitions), expected) if expected is not None else self.assertIsNone(report.overall_score(cases, repetitions))
        self.assertIsNone(report.overall_score(cases[1:], 30))
        cases[0]['measured'] -= 1
        self.assertIsNone(report.overall_score(cases, 30))

    def test_every_scored_case_requires_its_outcome_and_a_completed_handoff(self):
        suite = json.loads((report.ROOT / report.MANIFEST['suite']).read_text())
        config = {'coordinator': {'provider': 'p', 'model': 'candidate'}, 'workers': []}
        rows = [{'call': 'stream', 'session_id': 'root', 'provider_id': 'p', 'model': 'candidate',
                 'request_controls': [{'reasoning_effort': 'medium'}]}]
        for spec in suite['cases']:
            with self.subTest(case=spec['id']), tempfile.TemporaryDirectory() as tmp:
                capture = pathlib.Path(tmp)
                project = capture / 'project'
                shutil.copytree(report.ROOT / 'lycaon/test/fixtures/eval' / spec['project'], project)
                case = {'id': spec['id'], 'session_id': 'root', 'status': 'review_required',
                        'project_dir': str(project), 'run': 1, 'final': 'Completed'}
                if 'sandbox' in spec:
                    case['sandbox'] = {'kind': spec['sandbox'], 'receipt': 'nonce', 'port': 1234, 'observed': True}
                    kind = 'fixture_read_denied' if spec['sandbox'] == 'deny_read' else 'fixture_approved_once'
                    case['automatic_responses'] = [{'kind': kind}]
                    (project / 'receipt.json').write_text('{"receipt":"nonce"}')
                    (project / 'fallback.json').write_text('{"receipt":"nonce"}')
                    (project / 'endpoint.json').write_text('{"url":"http://127.0.0.1:1234/receipt"}')
                with patch.object(report, 'grade', return_value={'checks': [{'id': 'independent-oracle', 'passed': True}]}), \
                     patch.object(report, 'worker_checks', return_value=[{'id': 'worker-review', 'passed': True}]), \
                     patch.object(report, 'returned_worker_checks', return_value=[]), \
                     patch.object(report, 'closeout_checks', return_value=[]), \
                     patch.object(report, 'progress_checks', return_value=[]):
                    self.assertEqual(report.evaluate_case(case, config, rows, capture, spec, report.ROOT)['outcome'], 'passed')
                    case['final'] = ' '
                    expected = 'passed' if spec.get('workflow_id') else 'failed'
                    self.assertEqual(report.evaluate_case(case, config, rows, capture, spec, report.ROOT)['outcome'], expected)
                    case['final'] = 'Completed'
                    case['status'] = 'failed'
                    self.assertEqual(report.evaluate_case(case, config, rows, capture, spec, report.ROOT)['outcome'], 'failed')
                    case['status'] = 'review_required'
                    (project / 'receipt.json').write_text('{"receipt":"wrong"}')
                    with patch.object(report, 'grade', return_value={'checks': [{'id': 'independent-oracle', 'passed': False}]}):
                        self.assertEqual(report.evaluate_case(case, config, rows, capture, spec, report.ROOT)['outcome'], 'failed')

    def test_duplicate_check_identity_is_a_grading_error(self):
        with tempfile.TemporaryDirectory() as temporary:
            capture = pathlib.Path(temporary)
            case = {'id': 'current-verification', 'session_id': 'root',
                    'project_dir': str(capture), 'status': 'review_required', 'final': 'Completed'}
            with patch.object(report, 'grade', return_value={'checks': [{'id': 'final-handoff', 'passed': True}]}):
                with self.assertRaises(report.CheckIdentityError) as error:
                    report.outcome_checks(capture, case, {'project': 'unused'}, report.ROOT)
            self.assertEqual(error.exception.duplicates, ['final-handoff'])

    def test_score_averages_all_attempts_instead_of_selecting_the_best(self):
        cases = [{'id': case, 'passed': 24, 'measured': 25, 'attempts': 25} for case in report.CASES]
        self.assertAlmostEqual(report.overall_score(cases, 25), 96)

    def test_tiers_are_scored_separately_with_reliability_and_structure(self):
        cases = []
        for case in report.CASES:
            op = report.OPERATIONS[case]
            trials = [{'id': case, 'outcome': 'passed', 'structure': {'tool_calls': 4, 'assistant_turns': 2}}] * 5
            cases.append({'id': case, 'family': op['family'], 'passed': 5, 'measured': 5, 'attempts': 5, 'trials': trials})
        gate = report.tier_result(cases, 5, 'release', 'gate')
        orchestration = report.tier_result(cases, 5, 'release', 'orchestration')
        self.assertAlmostEqual(gate['score'], 100)
        self.assertAlmostEqual(orchestration['score'], 100)
        self.assertAlmostEqual(orchestration['pass_k'], 100)
        self.assertAlmostEqual(orchestration['consistency'], 100)
        self.assertEqual(orchestration['k'], 5)
        self.assertTrue(all(f['tool_calls'] == 4 for f in orchestration['structure'].values()))
        self.assertAlmostEqual(report.tier_result(cases, 5, 'exploration', 'orchestration')['score'], 100)
        # Failing every orchestration fixture leaves the gate score untouched.
        for case in cases:
            if report.OPERATIONS[case['id']].get('tier') == 'orchestration':
                case['passed'] = 0
        self.assertAlmostEqual(report.tier_result(cases, 5, 'release', 'gate')['score'], 100)
        self.assertAlmostEqual(report.tier_result(cases, 5, 'release', 'orchestration')['score'], 0)
        lower, upper = report.interval(25, 25)
        self.assertAlmostEqual(lower, 86.68, places=2)
        self.assertAlmostEqual(upper, 100)

    def test_pilot_scores_require_complete_tiers_and_two_attempts(self):
        cases = [{'id': case, 'family': report.OPERATIONS[case]['family'],
                  'passed': 2, 'measured': 2, 'attempts': 2, 'trials': []}
                 for case in report.CASES]
        for tier in report.TIERS:
            result = report.tier_result(cases, 2, 'exploration', tier)
            self.assertAlmostEqual(result['score'], 100)
            self.assertIsNotNone(result['score_interval_95'])
            self.assertIsNone(result['pass_k'])
            self.assertIsNone(result['consistency'])
            self.assertNotIn('k', result)
            for mode in ('release', 'calibration'):
                self.assertIsNone(report.tier_result(cases, 2, mode, tier)['score'])
            selected = [c for c in cases if c['id'] in report.SCORED_BY_TIER[tier]]
            self.assertIsNone(report.tier_result(selected[1:], 2, 'exploration', tier)['score'])
            one = [dict(c, passed=1, measured=1, attempts=1) for c in selected]
            self.assertIsNone(report.tier_result(one, 1, 'exploration', tier)['score'])
        missing = next(c for c in cases if c['id'] in report.SCORED_BY_TIER['orchestration'])
        missing.update(passed=1, measured=1)
        self.assertIsNone(report.tier_result(cases, 2, 'exploration', 'orchestration')['score'])
        self.assertAlmostEqual(report.tier_result(cases, 2, 'exploration', 'gate')['score'], 100)

    def test_wrong_worker_invalidates_configuration(self):
        config = {'coordinator': {'provider': 'p', 'model': 'candidate'},
                  'workers': [{'provider': 'p', 'model': 'fixed'}]}
        rows = [{'call': 'stream', 'session_id': 'root', 'provider_id': 'p', 'model': 'candidate', 'request_controls': [{'reasoning_effort': 'medium'}]},
                {'call': 'stream', 'session_id': 'child', 'parent_session_id': 'root',
                 'provider_id': 'p', 'model': 'candidate', 'request_controls': [{'reasoning_effort': 'medium'}]}]
        self.assertEqual(report.route_check({'session_id': 'root'}, config, rows), (False, {'child'}))
        rows[1]['model'] = 'fixed'
        self.assertEqual(report.route_check({'session_id': 'root'}, config, rows), (True, {'child'}))

    def test_successful_artifact_does_not_replace_delegation(self):
        with tempfile.TemporaryDirectory() as tmp:
            case = {'id': 'worker-integration', 'session_id': 'root', 'status': 'review_required',
                    'project_dir': tmp, 'run': 1, 'final': 'Done'}
            config = {'coordinator': {'provider': 'p', 'model': 'candidate'}, 'workers': []}
            rows = [{'call': 'stream', 'session_id': 'root', 'provider_id': 'p', 'model': 'candidate', 'request_controls': [{'reasoning_effort': 'medium'}]}]
            with patch.object(report, 'worker_checks', return_value=[{'id': 'worker-review', 'passed': False}]), patch.object(report, 'grade', return_value={'checks': [{'id': 'cli', 'passed': True}]}):
                result = report.evaluate_case(case, config, rows, pathlib.Path(tmp), {'project': 'agent-project'}, report.ROOT)
            self.assertEqual(result['outcome'], 'failed')
            self.assertIn('worker-review', result['failed_checks'])

    def test_errors_are_not_zero_scores(self):
        case = {'id': 'targeted-repair', 'session_id': 'root', 'status': 'error'}
        config = {'coordinator': {'provider': 'p', 'model': 'candidate'}, 'workers': []}
        rows = [{'call': 'stream', 'session_id': 'root', 'provider_id': 'p', 'model': 'candidate', 'request_controls': [{'reasoning_effort': 'medium'}]}]
        with patch.object(report, 'grade') as oracle:
            result = report.evaluate_case(case, config, rows, pathlib.Path('/unused'), {'project': 'agent-project'}, report.ROOT)
        self.assertEqual(result['outcome'], 'unmeasured')
        oracle.assert_not_called()

    def test_normal_approval_is_not_a_model_failure(self):
        case = {'id': 'targeted-repair', 'session_id': 'root', 'status': 'blocked',
                'checkpoint_requests': [{'kind': 'tool_approval'}]}
        config = {'coordinator': {'provider': 'p', 'model': 'candidate'}, 'workers': []}
        rows = [{'call': 'stream', 'session_id': 'root', 'provider_id': 'p', 'model': 'candidate', 'request_controls': [{'reasoning_effort': 'medium'}]}]
        with patch.object(report, 'grade') as oracle:
            result = report.evaluate_case(case, config, rows, pathlib.Path('/unused'), {'project': 'agent-project'}, report.ROOT)
        self.assertEqual(result['outcome'], 'unmeasured')
        oracle.assert_not_called()

    def test_exhausted_interactions_cannot_pass_with_a_correct_artifact(self):
        with tempfile.TemporaryDirectory() as tmp:
            case = {'id': 'targeted-repair', 'session_id': 'root', 'status': 'interaction_exhausted',
                    'project_dir': tmp, 'run': 1, 'final': 'Done'}
            config = {'coordinator': {'provider': 'p', 'model': 'candidate'}, 'workers': []}
            rows = [{'call': 'stream', 'session_id': 'root', 'provider_id': 'p', 'model': 'candidate', 'request_controls': [{'reasoning_effort': 'medium'}]}]
            with patch.object(report, 'grade', return_value={'checks': [{'id': 'cli', 'passed': True}]}):
                result = report.evaluate_case(case, config, rows, pathlib.Path(tmp), {'project': 'agent-project'}, report.ROOT)
            self.assertEqual(result['outcome'], 'failed')
            self.assertIn('unattended-completion', result['failed_checks'])

    def test_artifact_and_handoff_cannot_replace_current_verification(self):
        with tempfile.TemporaryDirectory() as tmp:
            case = {'id': 'targeted-repair', 'session_id': 'root', 'status': 'review_required',
                    'project_dir': tmp, 'run': 1, 'final': 'Done'}
            config = {'coordinator': {'provider': 'p', 'model': 'candidate'}, 'workers': []}
            rows = [{'call': 'stream', 'session_id': 'root', 'provider_id': 'p', 'model': 'candidate',
                     'request_controls': [{'reasoning_effort': 'medium'}]}]
            with patch.object(report, 'verification_checks', return_value=[
                {'id': 'coordinator-verification-current', 'passed': False}
            ]), patch.object(report, 'grade', return_value={'checks': [{'id': 'cli', 'passed': True}]}):
                result = report.evaluate_case(case, config, rows, pathlib.Path(tmp), {'project': 'agent-project'}, report.ROOT)
            self.assertEqual(result['outcome'], 'failed')
            self.assertEqual(result['failed_checks'], ['coordinator-verification-current'])

    def test_invalid_candidate_files_are_a_failure_not_missing_infrastructure(self):
        with tempfile.TemporaryDirectory() as tmp:
            case = {'id': 'targeted-repair', 'session_id': 'root', 'status': 'review_required',
                    'project_dir': tmp, 'run': 1, 'final': 'Done', 'invalid_artifact': True}
            config = {'coordinator': {'provider': 'p', 'model': 'candidate'}, 'workers': []}
            rows = [{'call': 'stream', 'session_id': 'root', 'provider_id': 'p', 'model': 'candidate',
                     'request_controls': [{'reasoning_effort': 'medium'}]}]
            with patch.object(report, 'grade') as grader:
                result = report.evaluate_case(case, config, rows, pathlib.Path(tmp), {'project': 'agent-project'}, report.ROOT)
            grader.assert_not_called()
            self.assertEqual(result['outcome'], 'failed')
            self.assertEqual(result['failed_checks'], ['regular-project-files'])

    def test_one_success_leaves_wide_uncertainty(self):
        low, high = report.interval(1, 1)
        self.assertAlmostEqual(low, 20.6549314377, places=6)
        self.assertAlmostEqual(high, 100)

    def test_grading_rejects_links_without_following_them(self):
        with tempfile.TemporaryDirectory() as tmp:
            project = pathlib.Path(tmp)
            (project / 'escape').symlink_to('/etc/passwd')
            with self.assertRaisesRegex(ValueError, 'symlink'):
                grade(project, project, 'repair')


if __name__ == '__main__':
    unittest.main()
