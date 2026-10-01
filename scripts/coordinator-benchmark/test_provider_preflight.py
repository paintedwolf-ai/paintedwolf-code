import json
import pathlib
import tempfile
import threading
import unittest
from unittest.mock import patch, Mock
from provider_preflight import validate_receipt, probe, Admission, STAGES
from progress import Progress, save
from snapshot import identity


def admit(root, private, plan, progress, cancelled):
    admission = Admission(root, private, plan, cancelled)
    for item in list(progress.pending()):
        failure = admission.check(item['model'])
        if failure:
            progress.started(item)
            progress.finished(item, failure)


class ProviderPreflightTests(unittest.TestCase):
    def setUp(self):
        self.model = {'id':'candidate','provider':'cloud','model':'small','configuration':{'coordinator':{'provider':'cloud','model':'small'}}}
        self.receipt = {'provider':'cloud','model':'small','accepted':True,
                        'stages':[{'stage':s,'accepted':True} for s in STAGES]}

    def test_transport_acceptance_does_not_grade_answer_text(self):
        self.assertEqual(validate_receipt(self.receipt,self.model),self.receipt)
        self.receipt['stages'].pop()
        with self.assertRaisesRegex(ValueError,'every conversation'):
            validate_receipt(self.receipt,self.model)

    def test_probe_configuration_cleanup_requires_confirmed_shutdown(self):
        for state in ['exited', 'worker_failed', 'execution_unknown']:
            with self.subTest(state=state), tempfile.TemporaryDirectory() as temporary:
                root = pathlib.Path(temporary)
                config = root / 'provider-preflight/candidate/attempt-0000/probe/application/config'

                def execute(attempt, *unused):
                    config.mkdir(parents=True)
                    (config / '.credential-vault-development-identity').write_text('copied identity')
                    (config.parent / 'store.db').write_text('retained evidence')
                    if state == 'exited':
                        save(attempt / 'probe/result.json', self.receipt)
                    return {'kind': state, 'returncode': 0 if state == 'exited' else -9}

                with patch('provider_preflight.execute', side_effect=execute):
                    probe(root, root, self.model, threading.Event())
                self.assertEqual((config / '.credential-vault-development-identity').exists(), state == 'execution_unknown')
                self.assertEqual((config.parent / 'store.db').read_text(), 'retained evidence')

    def test_uncertain_or_missing_probe_result_never_replays_paid_calls(self):
        for kind, retained in [('execution_unknown', False), ('execution_unknown', True),
                               ('worker_failed', False), ('exited', False)]:
            with self.subTest(kind=kind, retained=retained), tempfile.TemporaryDirectory() as tmp:
                root = pathlib.Path(tmp)
                def execute(directory, command, cwd, cancel):
                    if retained:
                        destination = directory / 'probe'
                        destination.mkdir(parents=True)
                        save(destination / 'result.json', self.receipt)
                    return {'kind': kind, 'returncode': 0 if kind == 'exited' else None}
                with patch('provider_preflight.execute', side_effect=execute) as launch:
                    result = probe(root, root, self.model, threading.Event())
                    self.assertFalse(result['accepted'])
                    self.assertFalse(result['retryable'])
                    self.assertEqual(result['failure_kind'], 'harness')
                    recovered = probe(root, root, self.model, threading.Event())
                    self.assertEqual(recovered['code'], result['code'])
                    self.assertFalse(recovered['accepted'])
                    launch.assert_called_once()

    def test_utility_uses_text_transport_and_cannot_reuse_a_coordinator_receipt(self):
        utility={**self.model,'role':'lite'}
        receipt={**self.receipt,'role':'lite','stages':[{'stage':'utility_text','accepted':True}]}
        self.assertEqual(validate_receipt(receipt,utility),receipt)
        with self.assertRaisesRegex(ValueError,'another application role'):
            validate_receipt(self.receipt,utility)

    def test_wrong_model_is_not_accepted(self):
        self.receipt['model']='other'
        with self.assertRaisesRegex(ValueError,'another model'):
            validate_receipt(self.receipt,self.model)

    def test_resume_reuses_bound_acceptance_without_spending(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=pathlib.Path(tmp)
            path=root/'provider-preflight/candidate'
            path.mkdir(parents=True)
            save(path/'result.json',{**self.receipt,'configuration_sha256':identity(self.model['configuration'])})
            with patch('provider_preflight.execute') as execute:
                self.assertTrue(probe(root,root,self.model,threading.Event())['accepted'])
                execute.assert_not_called()
                self.model['configuration']['engine']='changed'
                with self.assertRaisesRegex(ValueError,'configuration changed'):
                    probe(root,root,self.model,threading.Event())

    def test_permanent_failure_blocks_only_affected_candidate_without_scoring(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=pathlib.Path(tmp)
            other={**self.model,'id':'other','model':'other','configuration':{'coordinator':{'provider':'cloud','model':'other'}}}
            plan={'models':[self.model,other],'execution':{},'episodes':[
                {'index':0,'model':'candidate','case':'one'}, {'index':1,'model':'other','case':'one'}]}
            progress=Progress(root,plan)
            rejected={**self.receipt,'accepted':False,'retryable':False,'code':'provider_request_rejected'}
            with patch('provider_preflight.probe',side_effect=[rejected,self.receipt]):
                admit(root,root,plan,progress,threading.Event())
            self.assertEqual(progress.records,{})
            self.assertEqual([i['index'] for i in progress.pending()],[1])
            self.assertEqual(progress.blocked['0']['failure']['code'],'provider_request_rejected')

    def test_unclassified_failure_is_not_invented_as_provider_rejection(self):
        with self.assertRaisesRegex(ValueError,'structured disposition'):
            validate_receipt({**self.receipt,'accepted':False},self.model)

    def test_transient_failure_backs_off_and_retries_without_a_model_verdict(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=pathlib.Path(tmp)
            cancelled=Mock()
            cancelled.is_set.return_value=False
            cancelled.wait.return_value=False
            calls=[]
            def execute(directory, command, cwd, cancel):
                calls.append(directory)
                destination=directory/'probe'
                destination.mkdir(parents=True)
                receipt=self.receipt if len(calls)>1 else {**self.receipt,'accepted':False,'retryable':True,'code':'provider_rate_limited'}
                save(destination/'result.json',receipt)
                return {'kind':'exited','returncode':0}
            with patch('provider_preflight.execute',side_effect=execute):
                self.assertTrue(probe(root,root,self.model,cancelled)['accepted'])
            self.assertEqual(len(calls),2)
            self.assertEqual(cancelled.wait.call_count, 1)
            self.assertAlmostEqual(cancelled.wait.call_args.args[0], 5, delta=0.1)

    def test_unavailable_support_model_blocks_its_dependents(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=pathlib.Path(tmp)
            self.model['configuration']['lite']={'provider':'other-cloud','model':'utility'}
            plan={'models':[self.model],'execution':{},'episodes':[{'index':0,'model':'candidate','case':'one'}]}
            progress=Progress(root,plan)
            rejected={'provider':'other-cloud','model':'utility','accepted':False,'retryable':False,'code':'provider_not_configured','stages':[]}
            with patch('provider_preflight.probe',side_effect=[self.receipt,rejected]) as check:
                admit(root,root,plan,progress,threading.Event())
            self.assertEqual(check.call_count,2)
            self.assertEqual(progress.pending(),[])
            self.assertEqual(progress.records,{})

    def test_repeated_provider_failure_stops_probing_and_retains_faults(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            cancelled = Mock()
            cancelled.is_set.return_value = False
            cancelled.wait.return_value = False
            failure = {**self.receipt, 'accepted':False, 'retryable':True, 'code':'provider_empty_completion'}
            def execute(directory, *args):
                destination = directory / 'probe'
                destination.mkdir(parents=True, exist_ok=True)
                save(destination / 'result.json', failure)
                return {'kind':'exited', 'returncode':0}
            with patch('recovery.MAX_INFRASTRUCTURE_ATTEMPTS', 3), patch('provider_preflight.execute', side_effect=execute) as launch:
                result = probe(root, root, self.model, cancelled)
                self.assertFalse(result['accepted'])
                self.assertFalse(result['retryable'])
                self.assertEqual(result['code'], 'infrastructure_recovery_exhausted')
                self.assertEqual(result['last_failure'], failure)
                self.assertEqual(len(result['recovery']['attempts']), 3)
                self.assertEqual(probe(root, root, self.model, cancelled)['code'], result['code'])
                self.assertEqual(launch.call_count, 3)

    def test_roles_of_one_model_require_independent_probes(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            reference = {'provider':'cloud', 'model':'small'}
            self.model['configuration'].update({'lite':reference, 'workers':[reference]})
            plan = {'models':[self.model], 'execution':{}, 'episodes':[{'index':0,'model':'candidate','case':'one'}]}
            progress = Progress(root, plan)
            roles = []
            def check(run, private, model, cancelled):
                role = model.get('role', 'coordinator')
                roles.append(role)
                if role == 'lite':
                    return {'provider':'cloud','model':'small','role':role,'accepted':False,
                            'retryable':False,'code':'provider_request_rejected','stages':[]}
                return {**self.receipt,'role':role}
            with patch('provider_preflight.probe', side_effect=check):
                admit(root, root, plan, progress, threading.Event())
            self.assertEqual(roles, ['coordinator','agent_pool','lite'])
            self.assertEqual(progress.pending(), [])
            self.assertEqual(progress.blocked['0']['preflight_failures'][0]['role'], 'lite')

    def test_shared_support_acceptance_survives_first_candidate_completion(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            support = {'provider':'cloud', 'model':'support', 'configured_settings':{'max_tokens':4096}}
            models = []
            for name in ['first', 'second']:
                coordinator = {'provider':'cloud', 'model':name}
                config = {'coordinator':coordinator, 'workers':[support], 'lite':support,
                          'engine_sha256':'a'*64, 'catalog_sha256':'b'*64}
                config['configuration_sha256'] = identity(config)
                models.append({'id':name, **coordinator, 'configuration':config})
            items = [{'index':index, 'model':model['id'], 'case':'one'} for index,model in enumerate(models)]
            plan = {'models':models, 'episodes':items, 'execution':{}}
            progress = Progress(root, plan)
            def execute(directory, command, *unused):
                role = command[command.index('--role') + 1]
                model = command[command.index('--model') + 1]
                destination = directory / 'probe'
                destination.mkdir(parents=True)
                stages = ['utility_text'] if role == 'lite' else STAGES
                save(destination / 'result.json', {'provider':'cloud', 'model':model, 'role':role,
                     'accepted':True, 'stages':[{'stage':stage,'accepted':True} for stage in stages]})
                return {'kind':'exited','returncode':0}
            with patch('provider_preflight.execute', side_effect=execute) as launch:
                admit(root, root, plan, progress, threading.Event())
                self.assertEqual(launch.call_count, 4)
                support_receipts = {path:path.read_bytes() for path in (root / 'provider-preflight').glob('support-*/result.json')}
                self.assertEqual(len(support_receipts), 2)
                progress.started(items[0])
                progress.finished(items[0], {'report':'retained'})
                resumed = Progress(root, plan)
                admit(root, root, plan, resumed, threading.Event())
                self.assertEqual(launch.call_count, 4)
                self.assertEqual(resumed.pending(), [items[1]])
                self.assertEqual({path:path.read_bytes() for path in support_receipts}, support_receipts)

    def test_interrupted_probes_wait_until_explicit_cancellation(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            cancelled = Mock()
            cancelled.is_set.return_value = False
            cancelled.wait.side_effect = [False] * 11 + [True]
            def execute(directory, *args):
                directory.mkdir(parents=True, exist_ok=True)
                return {'kind':'interrupted','returncode':None}
            with patch('recovery.MAX_INFRASTRUCTURE_ATTEMPTS', 3), \
                 patch('provider_preflight.execute', side_effect=execute) as launch:
                with self.assertRaises(InterruptedError):
                    probe(root, root, self.model, cancelled)
                self.assertEqual(launch.call_count, 12)
            self.assertFalse((root/'provider-preflight/candidate/result.json').exists())

    def test_unavailable_provider_does_not_hold_independent_model_preflight(self):
        from concurrent.futures import ThreadPoolExecutor
        waiting, release = threading.Event(), threading.Event()
        with tempfile.TemporaryDirectory() as directory:
            root=pathlib.Path(directory)
            other={'id':'other','provider':'other-cloud','model':'other',
                   'configuration':{'coordinator':{'provider':'other-cloud','model':'other'}}}
            def check(run,private,model,cancelled):
                if model['id']=='candidate':
                    waiting.set()
                    if not release.wait(5):raise TimeoutError('test release missing')
                return {**self.receipt,'provider':model['provider'],'model':model['model']}
            with patch('provider_preflight.probe',side_effect=check), ThreadPoolExecutor(max_workers=2) as pool:
                admission=Admission(root,root,{'models':[self.model,other]},threading.Event())
                first=pool.submit(admission.check,'candidate')
                try:
                    self.assertTrue(waiting.wait(2))
                    self.assertIsNone(pool.submit(admission.check,'other').result(timeout=2))
                finally:release.set()
                self.assertIsNone(first.result(timeout=2))

    def test_preflight_pause_preserves_deadline_without_another_request(self):
        with tempfile.TemporaryDirectory() as directory:
            root=pathlib.Path(directory)
            cancelled=Mock(is_set=Mock(return_value=False),wait=Mock(return_value=True))
            failure={**self.receipt,'accepted':False,'retryable':True,'code':'provider_rate_limited'}
            def execute(directory,*args):
                (directory/'probe').mkdir(parents=True)
                save(directory/'probe/result.json',failure)
                return {'kind':'exited','returncode':0}
            with patch('provider_preflight.time.time',return_value=1000), patch('provider_preflight.execute',side_effect=execute) as calls:
                with self.assertRaises(InterruptedError):probe(root,root,self.model,cancelled)
                deadline=root/'provider-preflight/candidate/attempt-0000/disposition.json'
                retained=deadline.read_bytes()
                with self.assertRaises(InterruptedError):probe(root,root,self.model,cancelled)
                self.assertEqual(calls.call_count,1)
                self.assertEqual(deadline.read_bytes(),retained)
                self.assertEqual(cancelled.wait.call_args.args[0],5)
