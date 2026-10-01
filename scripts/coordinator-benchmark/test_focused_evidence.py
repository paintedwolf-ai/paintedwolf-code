import json
import pathlib
import tempfile
import unittest
from test_episode_support import EvidenceCase
from focused_evidence import prepared_case, sandbox_checks, returned_worker_checks


class FocusedEvidenceControls(EvidenceCase):
    def test_entry_recovery_requires_the_original_durable_receipt(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=pathlib.Path(temporary)
            session='11111111-1111-4111-8111-111111111111'
            case={'session_id':session}
            spec={'prelude':[{'id':'step'}]}
            with self.assertRaises(ValueError): prepared_case(root,case,spec)
            (root/'conversation-preparation').mkdir()
            entry={'session_id':session,'entry_at':'2026-09-07T12:00:00Z'}
            (root/'conversation-preparation'/f'{session}.entry.json').write_text(json.dumps(entry))
            self.assertEqual(prepared_case(root,case,spec),{**case,'preparation':entry})
            with self.assertRaises(ValueError):
                prepared_case(root,{**case,'preparation':{**entry,'entry_at':'changed'}},spec)

    def insert_job(self,id_,child,parent,status,merge):
        self.facts['workers'].append({'id':id_,'child_session_id':child,'parent_session_id':parent,'status':status,
                                     'merge_status':merge,'scope':{'mode':'write','paths':[]}})
        self.save()

    def snapshot(self,id_,roots,quality):
        self.facts['snapshots'][id_]={'roots_key':roots,'quality':quality}
        self.save()

    def card(self,id_,session,kind,status,payload):
        call=json.loads(payload)['tool_call_id']
        self.session['checkpoints'].append({'checkpoint_id':id_,'kind':kind,'status':status,'issued_at':'2026-09-12T00:00:00Z',
            'tool_approval':{'tool_call_id':call,'plan':{'subject':{'kind':'direct_ip'}}}})
        self.save()

    def update_jobs(self,**fields):
        for job in self.facts['workers']: job.update(fields)
        self.save()

    def change_merge(self,value,id_):
        self.job_by_id(id_)['merge_status']=value
        self.save()

    def test_denial_requires_the_exact_real_checkpoint_not_an_operator_label(self):
        case={'id':'permission-denied','session_id':'root','project_dir':str(self.root),
              'sandbox':{'kind':'deny_read','preparation_call_id':'prepared','receipt':'fallback'}}
        (self.root/'receipt.json').write_text('{"receipt":"fallback"}')
        self.card(*('checkpoint', 'root', 'tool_approval', 'rejected', '{"tool_call_id":"other"}'))
        with self.assertRaises(ValueError): sandbox_checks(self.root,case)
        self.session['checkpoints'][0]['tool_approval']['tool_call_id']='prepared';self.save()
        self.assertTrue(all(c['passed'] for c in sandbox_checks(self.root,case)))
        (self.root/'receipt.json').write_text('{"receipt":"made-up-input"}')
        self.assertFalse(all(c['passed'] for c in sandbox_checks(self.root,case)))
        self.session['checkpoints'][0]['status']='approved';self.save()
        with self.assertRaises(ValueError): sandbox_checks(self.root,case)

    def test_fresh_receipt_requires_live_observation_and_current_nonce(self):
        case={'id':'fresh-service','project_dir':str(self.root),'sandbox':{'kind':'loopback','port':1234,'observed':True,'receipt':'new'}}
        receipt={'receipt':'new','balances':[{'account':'amber','cents':100},{'account':'cedar','cents':100}]}
        (self.root/'receipt.json').write_text(json.dumps(receipt))
        (self.root/'endpoint.json').write_text('{"url":"http://127.0.0.1:1234/receipt"}')
        self.assertTrue(all(c['passed'] for c in sandbox_checks(self.root,case)))
        case['sandbox']['observed']=False
        self.assertFalse(all(c['passed'] for c in sandbox_checks(self.root,case)))
        case['sandbox']['observed']=True;receipt['receipt']='old'
        (self.root/'receipt.json').write_text(json.dumps(receipt))
        self.assertFalse(all(c['passed'] for c in sandbox_checks(self.root,case)))

    def test_wanted_complete_return_requires_promotion(self):
        spec={'setup':{'overlays':[{'label':'Change','verify':'checks','verification_verdict':'passed','result_status':'complete'}]}}
        case={'session_id':'root','prepared_overlays':{'overlays':[{'label':'Change','job_id':'job','child_session_id':'child',
              'baseline_sha256':'base','result_status':'complete','verification':{'CheckID':'check','Verdict':'passed','SourceRevision':'revision','SourceRootDigest':'roots'}}]}}
        self.snapshot(*('revision', 'roots', 'exact'))
        self.insert_job(*('job', 'child', 'root', 'complete', 'pending'))
        self.assertFalse(all(c['passed'] for c in returned_worker_checks(self.root,case,spec)))
        # A complete return requires a merged job.
        self.update_jobs(merge_status="rejected")
        self.assertFalse(all(c['passed'] for c in returned_worker_checks(self.root,case,spec)))
        self.update_jobs(merge_status="merged")
        self.assertTrue(all(c['passed'] for c in returned_worker_checks(self.root,case,spec)))
        self.update_jobs(status="running")
        self.assertFalse(all(c['passed'] for c in returned_worker_checks(self.root,case,spec)))
        self.update_jobs(status="complete")
        # An explicit disposition overrides the default merged outcome.
        self.update_jobs(status="complete",merge_status="rejected")
        spec['setup']['overlays'][0]['label']='Pagination'
        case['prepared_overlays']['overlays'][0]['label']='Pagination'
        self.assertTrue(all(c['passed'] for c in returned_worker_checks(self.root,case,spec,{'Pagination':'rejected'})))
        self.assertFalse(all(c['passed'] for c in returned_worker_checks(self.root,case,spec,{'Pagination':'merged'})))
        self.facts['snapshots']['revision']['quality']='partial';self.save()
        with self.assertRaises(ValueError): returned_worker_checks(self.root,case,spec)

    def test_dispatch_only_fixtures_have_no_prepared_baseline_to_validate(self):
        spec={'setup':{'policy':'scripted','overlays':[],'dispatches':[{'label':'App','mode':'write','paths':['a'],'stages':[]}]}}
        case={'session_id':'root','prepared_overlays':{'overlays':[]}}
        self.insert_job(*('job', 'child', 'root', 'complete', 'merged'))
        checks=returned_worker_checks(self.root,case,spec)
        self.assertEqual([c['id'] for c in checks],['resolved-worker-obligations'])
        self.assertTrue(checks[0]['passed'])

    def test_partial_return_requires_continuation_on_the_existing_child(self):
        spec={'setup':{'overlays':[{'label':'Change','verify':'checks','verification_verdict':'passed','result_status':'partial'}]}}
        case={'session_id':'root','prepared_overlays':{'overlays':[{'label':'Change','job_id':'job','child_session_id':'child',
              'baseline_sha256':'base','result_status':'partial','verification':{'CheckID':'check','Verdict':'passed','SourceRevision':'revision','SourceRootDigest':'roots'}}]}}
        self.snapshot(*('revision', 'roots', 'exact'))
        self.insert_job(*('job', 'child', 'root', 'complete', 'merged'))
        self.assertFalse(all(c['passed'] for c in returned_worker_checks(self.root,case,spec)))
        self.insert_job(*('followup', 'different-child', 'root', 'complete', 'merged'))
        self.assertFalse(all(c['passed'] for c in returned_worker_checks(self.root,case,spec)))
        self.job_by_id('followup')['child_session_id']='child';self.save()
        self.assertTrue(all(c['passed'] for c in returned_worker_checks(self.root,case,spec)))

    def test_denied_service_requires_fallback_no_observation_and_exact_endpoint(self):
        case={'id':'fresh-service-denied','session_id':'root','project_dir':str(self.root),'sandbox':{'kind':'deny_loopback','port':1234,'observed':False,'receipt':'cached','preparation_call_id':'prepared'}}
        self.card(*('checkpoint', 'root', 'tool_approval', 'rejected', '{"tool_call_id":"prepared"}'))
        (self.root/'receipt.json').write_text(json.dumps({'receipt':'cached','balances':[{'account':'birch','cents':120},{'account':'cedar','cents':100}]}))
        endpoint='{"url":"http://127.0.0.1:1234/receipt"}'
        (self.root/'endpoint.json').write_text(endpoint)
        self.assertTrue(all(c['passed'] for c in sandbox_checks(self.root,case)))
        case['sandbox']['observed']=True
        self.assertFalse(all(c['passed'] for c in sandbox_checks(self.root,case)))
        case['sandbox']['observed']=False
        (self.root/'endpoint.json').write_text(endpoint+'\n')
        self.assertFalse(all(c['passed'] for c in sandbox_checks(self.root,case)))

    def test_denied_service_counts_its_receipt_once_and_requires_the_denial(self):
        case={'id':'fresh-service-denied','session_id':'root','project_dir':str(self.root),
              'sandbox':{'kind':'deny_loopback','port':1234,'observed':False,'receipt':'cached','preparation_call_id':'prepared'}}
        self.card(*('checkpoint', 'root', 'tool_approval', 'rejected', '{"tool_call_id":"prepared"}'))
        (self.root/'endpoint.json').write_text('{"url":"http://127.0.0.1:1234/receipt"}')
        checks=sandbox_checks(self.root,case)
        self.assertEqual([c['id'] for c in checks if not c['passed']],['receipt-handoff'])
        self.assertEqual(len(checks),len({c['id'] for c in checks}))
        (self.root/'receipt.json').write_text(json.dumps({'receipt':'cached','balances':[{'account':'birch','cents':120},{'account':'cedar','cents':100}]}))
        self.assertTrue(all(c['passed'] for c in sandbox_checks(self.root,case)))
        self.session['checkpoints'][0]['status']='approved';self.save()
        with self.assertRaises(ValueError): sandbox_checks(self.root,case)

    def worker_fixture(self, overlays):
        self.facts['workers'].clear();self.save()
        self.facts['snapshots'].clear();self.save()
        self.snapshot(*('revision', 'roots', 'exact'))
        prepared = []
        for index, seed in enumerate(overlays):
            job, child = f'job-{index}', f'child-{index}'
            self.insert_job(*(job, child, 'root', 'complete', 'merged'))
            prepared.append({'label': seed['label'], 'job_id': job, 'child_session_id': child,
                'baseline_sha256': 'base', 'result_status': seed.get('result_status', 'complete'),
                'verification': {'CheckID': 'check', 'Verdict': seed['verification_verdict'],
                                 'SourceRevision': 'revision', 'SourceRootDigest': 'roots'}})
            if seed.get('result_status') == 'partial':
                self.insert_job(*(f'resumed-{index}', child, 'root', 'complete', 'merged'))
        return {'session_id': 'root', 'prepared_overlays': {'overlays': prepared}}

    def test_every_declared_worker_has_an_independent_check(self):
        root = pathlib.Path(__file__).resolve().parents[2]
        suite = json.loads((root/'lycaon/test/fixtures/eval/coordinator-benchmark.json').read_text())
        manifest = json.loads(pathlib.Path(__file__).with_name('benchmark.json').read_text())
        operations = {o['id']: o for o in manifest['operations']}
        for spec in suite['cases']:
            overlays = (spec.get('setup') or {}).get('overlays') or []
            if not overlays:
                continue
            with self.subTest(case=spec['id']):
                case = self.worker_fixture(overlays)
                outcomes = operations[spec['id']].get('overlay_outcomes') or {}
                for index, seed in enumerate(overlays):
                    self.change_merge(*(outcomes.get(seed['label'], 'merged'), f'job-{index}'))
                case['prepared_overlays']['overlays'].reverse()
                checks = returned_worker_checks(self.root, case, spec, outcomes)
                self.assertTrue(all(c['passed'] for c in checks))
                self.assertEqual(len(checks), len(overlays)+1)
                self.assertEqual(len(checks), len({c['id'] for c in checks}))
                for index, seed in enumerate(overlays):
                    partial = seed.get('result_status') == 'partial'
                    job = f'resumed-{index}' if partial else f'job-{index}'
                    wanted = outcomes.get(seed['label'], 'merged')
                    wrong = 'merged' if wanted == 'rejected' else 'rejected'
                    self.change_merge(*(wrong, job))
                    failed = [c['id'] for c in returned_worker_checks(self.root, case, spec, outcomes) if not c['passed']]
                    self.assertEqual(len(failed), 1)
                    self.assertTrue(failed[0].endswith('-'+seed['label']))
                    self.change_merge(*(wanted, job))

    def test_two_partial_workers_cannot_share_a_continuation(self):
        overlays = [{'label': label, 'verification_verdict': 'passed', 'result_status': 'partial'} for label in ('First', 'Second')]
        case = self.worker_fixture(overlays)
        spec = {'setup': {'overlays': overlays}}
        self.facts['workers']=[j for j in self.facts['workers'] if j['id']!='resumed-1'];self.save()
        checks = returned_worker_checks(self.root, case, spec)
        self.assertEqual([c['id'] for c in checks if not c['passed']], ['resumed-existing-worker-Second'])
        case['prepared_overlays']['overlays'][1]['child_session_id'] = 'child-0'
        with self.assertRaisesRegex(ValueError, 'cardinality'):
            returned_worker_checks(self.root, case, spec)

    def test_worker_labels_are_bound_to_the_declared_fixture(self):
        overlays = [{'label': label, 'verification_verdict': 'passed'} for label in ('First', 'Second')]
        case = self.worker_fixture(overlays)
        spec = {'setup': {'overlays': overlays}}
        case['prepared_overlays']['overlays'][1]['label'] = 'First'
        with self.assertRaisesRegex(ValueError, 'labels differ'):
            returned_worker_checks(self.root, case, spec)
        overlays[1]['label'] = 'First'
        with self.assertRaisesRegex(ValueError, 'unique declared labels'):
            returned_worker_checks(self.root, case, spec)
