from copy import deepcopy
import json
import pathlib
import tempfile
import unittest
from unittest.mock import Mock
from runtime_checks import project_scans, project_baseline, requested_full_pass
from smoke import Application


class RuntimeChecks(unittest.TestCase):
    def setUp(self):
        self.full_pass = {'assessment_id': 'assessment', 'members': [
            {'scanner_id': name, 'phase': 'waiting_for_scanner'} for name in ('sast', 'secrets')]}
        self.overview = {'project_id': 'project', 'enabled': True, 'running': self.full_pass,
                         'scanners': [{'id': name, 'available': True} for name in ('sast', 'secrets')]}
        self.response = {'passes': [self.full_pass], 'overview': self.overview}
        self.requested = requested_full_pass(self.response, 'project')

    def scan(self, index, status):
        member = self.full_pass['members'][index]
        member.update(phase='started', scan={'id': 'scan-' + str(index), 'scanner_id': member['scanner_id'],
                      'assessment_id': 'assessment', 'status': status})

    def complete(self):
        self.scan(0, 'complete')
        self.scan(1, 'complete')
        self.full_pass['completed_at'] = '2026-09-11T12:00:00Z'
        self.overview['last_full'] = self.overview.pop('running')

    def test_waits_for_requested_pass_and_every_member(self):
        self.assertIsNone(project_scans(self.overview, self.requested))
        self.full_pass['members'][1]['phase'] = 'waiting_for_pass'
        self.scan(0, 'complete')
        self.assertIsNone(project_scans(self.overview, self.requested))
        self.scan(1, 'running')
        self.assertIsNone(project_scans(self.overview, self.requested))
        self.scan(1, 'complete')
        self.assertIsNone(project_scans(self.overview, self.requested))
        self.complete()
        self.assertEqual(project_scans(self.overview, self.requested), self.requested)

    def test_request_rejects_missing_ambiguous_or_wrong_project_passes(self):
        for mutate in [lambda r: r.update(passes=[]), lambda r: r['passes'].append(r['passes'][0]),
                       lambda r: r['passes'][0].pop('assessment_id'),
                       lambda r: r['overview'].update(project_id='other'),
                       lambda r: r['overview'].update(enabled=False),
                       lambda r: r['overview']['scanners'][0].update(available=False),
                       lambda r: r['overview']['scanners'].append(r['overview']['scanners'][0])]:
            response = deepcopy(self.response)
            mutate(response)
            with self.assertRaises(ValueError): requested_full_pass(response, 'project')

    def test_terminal_failure_never_becomes_readiness(self):
        self.complete()
        for status in ('failed', 'timed_out', 'cancelled', 'superseded'):
            self.scan(0, status)
            with self.subTest(status=status), self.assertRaises(ValueError):
                project_scans(self.overview, self.requested)
        for phase in ('not_started', 'unknown'):
            self.full_pass['members'][0]['phase'] = phase
            with self.subTest(phase=phase), self.assertRaises(ValueError):
                project_scans(self.overview, self.requested)

    def test_diagnostics_do_not_override_structured_status(self):
        self.complete()
        self.full_pass['members'][0]['scan']['error'] = 'contains the word failed'
        self.assertEqual(project_scans(self.overview, self.requested), self.requested)

    def test_membership_and_scan_identity_cannot_change(self):
        self.complete()
        for mutate in [lambda p: p['members'].pop(), lambda p: p['members'].append(p['members'][0]),
                       lambda p: p['members'][0].update(scanner_id='unknown'),
                       lambda p: p['members'][0]['scan'].update(assessment_id='other'),
                       lambda p: p['members'][0]['scan'].update(scanner_id='other'),
                       lambda p: p['members'][0]['scan'].update(id='scan-1')]:
            overview = deepcopy(self.overview)
            mutate(overview['last_full'])
            with self.assertRaises(ValueError): project_scans(overview, self.requested)

    def test_unrelated_completed_pass_cannot_satisfy_readiness(self):
        self.complete()
        for change in ({'assessment_id': 'other'}, {'project_id': 'other'}):
            with self.assertRaises(ValueError): project_scans(self.overview, {**self.requested, **change})
        self.overview['running'] = self.full_pass
        with self.assertRaises(ValueError): project_scans(self.overview, self.requested)

    def test_application_tracks_the_returned_pass_over_http(self):
        pending = deepcopy(self.overview)
        self.complete()
        app = Mock()
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        app.directory = pathlib.Path(temporary.name)
        app.session.return_value = ('session', pathlib.Path('/fixture'))
        app.request.side_effect = [{'project_id': 'project'}, self.response, pending, self.overview]
        def wait(observe, *args, **kwargs):
            self.assertIsNone(observe())
            return observe()
        app.wait_for.side_effect = wait
        self.assertEqual(Application.verify_scanner_runtime(app, {'id': 'fixture'}), self.requested)
        self.assertEqual(json.loads((app.directory / 'scanner-request.json').read_text()), self.response)
        self.assertEqual(json.loads((app.directory / 'scanner-status.json').read_text()), self.overview)
        self.assertEqual([call.args[0] for call in app.request.call_args_list],
                         ['/v1/sessions/session', '/v1/projects/project/scans',
                          '/v1/projects/project/security', '/v1/projects/project/security'])

    def test_baseline_readiness_does_not_require_a_full_scan(self):
        overview={'project_id':'project','enabled':True,'scanners':[{'id':'sast','available':True}]}
        self.assertIsNone(project_baseline(overview))
        overview['baseline']={'snapshot_id':'snapshot','unobserved_directories':0}
        self.assertEqual(project_baseline(overview),{'project_id':'project','snapshot_id':'snapshot','scanners':['sast']})
        for extra in [{'running':{'assessment_id':'full'}},{'last_full':{'assessment_id':'full'}},{'enabled':False},{'baseline':{'snapshot_id':'snapshot','unobserved_directories':1}}]:
            with self.subTest(extra=extra), self.assertRaises(ValueError): project_baseline({**overview,**extra})
